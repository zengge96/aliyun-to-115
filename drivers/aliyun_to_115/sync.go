package aliyun_to_115

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_share2open"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type sync115Client struct {
	p115 *_115.Pan115
}

func newSync115Client(cookie string) (*sync115Client, error) {
	p115 := &_115.Pan115{}
	p115.Addition.Cookie = cookie
	if err := p115.Init(context.Background()); err != nil {
		return nil, err
	}
	return &sync115Client{p115: p115}, nil
}

// getOrCreateDir 检查文件夹是否存在，不存在则创建，返回文件夹 ID
func (c *sync115Client) getOrCreateDir(ctx context.Context, parentID string, dirName string) (string, error) {
	if dirName == "" || dirName == "/" {
		return parentID, nil
	}
	// 1. 列出当前目录检查是否已存在
	objs, err := c.p115.List(ctx, &model.Object{ID: parentID}, model.ListArgs{})
	if err == nil {
		for _, obj := range objs {
			if obj.IsDir() && obj.GetName() == dirName {
				return obj.GetID(), nil
			}
		}
	}
	// 2. 创建新目录
	newDir, err := c.p115.MakeDir(ctx, &model.Object{ID: parentID}, dirName)
	if err != nil {
		return "", fmt.Errorf("failed to create dir [%s] in [%s]: %w", dirName, parentID, err)
	}
	return newDir.GetID(), nil
}

func (c *sync115Client) uploadTo115(ctx context.Context, stream model.FileStreamer, dstDirID string) (model.Obj, error) {
	dstDir := &model.Object{ID: dstDirID}
	up := func(progress float64) {}
	return c.p115.Put(ctx, dstDir, stream, up)
}

func (c *sync115Client) removeFrom115(ctx context.Context, file model.Obj) error {
	return c.p115.Remove(ctx, file)
}

func (c *sync115Client) Drop() {
	if c.p115 != nil {
		c.p115.Drop(context.Background())
	}
}

func (d *AliyunTo115) doSyncLoop() {
	interval := time.Duration(d.SyncInterval) * time.Second
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		d.doSync()
	}
}

type syncStats struct {
	total   int64
	skipped int64
	noLink  int64
	failed  int64
	synced  int64
	rapid   int64
	normal  int64
}

func (d *AliyunTo115) doSync() {
	d.syncLoopMu.Lock()
	if d.syncRunning {
		d.syncLoopMu.Unlock()
		return
	}
	d.syncRunning = true
	d.syncLoopMu.Unlock()

	defer func() {
		d.syncLoopMu.Lock()
		d.syncRunning = false
		d.syncLoopMu.Unlock()
	}()

	ctx := context.Background()
	aliyunStorages := d.discoverAliyunStorages()
	stats := &syncStats{}

	// 用户配置的目标 115 根目录
	configRootID := d.RootFolderID
	if configRootID == "" {
		configRootID = "0"
	}

	for _, aliyun := range aliyunStorages {
		storage := aliyun.GetStorage()
		mountPath := "/"
		if storage != nil {
			mountPath = storage.MountPath
		}
		
		fmt.Printf("[aliyun_to_115] 正在处理阿里存储: %s\n", mountPath)

		// 1. 在 115 上根据 MountPath 创建层级
		// 例如 /aliyun/drive 会在 115 根目录下依次创建 aliyun 和 drive 文件夹
		segments := strings.Split(strings.Trim(mountPath, "/"), "/")
		current115ParentID := configRootID
		var err error
		for _, seg := range segments {
			if seg == "" { continue }
			current115ParentID, err = d.p115Client.getOrCreateDir(ctx, current115ParentID, seg)
			if err != nil {
				fmt.Printf("[aliyun_to_115] 创建挂载路径映射失败: %v\n", err)
				break
			}
		}
		if err != nil { continue }

		// 2. 开始递归遍历阿里盘并同步
		aliRootID := aliyun.GetRootId()
		err = d.walkAndSync(ctx, aliyun, mountPath+"/", aliRootID, current115ParentID, stats)
		if err != nil {
			fmt.Printf("[aliyun_to_115] walk error for %s: %v\n", mountPath, err)
		}
	}

	fmt.Printf("[aliyun_to_115] ===== 同步完成: 发现%v / 跳过%v / 秒传%v / 正常%v / 失败%v =====\n",
		stats.total, stats.skipped, stats.rapid, stats.normal, stats.failed)
}

func (d *AliyunTo115) walkAndSync(ctx context.Context, aliyun aliyunStorage, currentPath, aliParentID, p115ParentID string, stats *syncStats) error {
	files, err := aliyun.List(ctx, &model.Object{ID: aliParentID}, model.ListArgs{})
	if err != nil {
		return err
	}

	for _, f := range files {
		if f.IsDir() {
			// 同步创建目录
			subP115ID, err := d.p115Client.getOrCreateDir(ctx, p115ParentID, f.GetName())
			if err != nil {
				fmt.Printf("[aliyun_to_115] 无法在 115 创建目录: %s, 错误: %v\n", f.GetName(), err)
				continue
			}
			subPath := currentPath + f.GetName() + "/"
			_ = d.walkAndSync(ctx, aliyun, subPath, f.GetID(), subP115ID, stats)
		} else {
			stats.total++
			fullPath := currentPath + f.GetName()
			d.processSingleFile(ctx, aliyun, f, fullPath, p115ParentID, stats)
		}
	}
	return nil
}

func (d *AliyunTo115) processSingleFile(ctx context.Context, aliyun aliyunStorage, f model.Obj, fullPath string, p115DirID string, stats *syncStats) {
	mountPath := d.GetStorage().MountPath
	cacheKey := mountPath + "/" + f.GetID()
	hashInfo := f.GetHash()
	sha1Str := hashInfo.GetHash(utils.SHA1)
	if sha1Str != "" {
		cacheKey = mountPath + "/" + sha1Str
	}

	d.syncLoopMu.Lock()
	if d.syncedCache[cacheKey] {
		d.syncLoopMu.Unlock()
		stats.skipped++
		return
	}
	d.syncLoopMu.Unlock()

	// 115 share 风控严重，等待 1 秒
	if _, ok := aliyun.(*aliyundrive_share2open.AliyundriveShare2Open); ok {
		time.Sleep(1 * time.Second)
	}

	link, err := aliyun.Link(ctx, f, model.LinkArgs{})
	if driver, ok := aliyun.(*aliyundrive_share2open.AliyundriveShare2Open); ok {
		sha1Str = driver.GetHash(ctx, f, model.LinkArgs{})
	}

	if err != nil || link == nil || link.URL == "" {
		stats.noLink++
		return
	}

	stream := newUrlFileStreamer(f.GetName(), f.GetSize(), sha1Str, link.URL)
	start := time.Now()
	// 上传到对应的 115 目录 ID
	result, uploadErr := d.p115Client.uploadTo115(ctx, stream, p115DirID)
	elapsed := time.Since(start)

	if uploadErr != nil || result == nil {
		fmt.Printf("[aliyun_to_115] 上传失败: %s : %v\n", fullPath, uploadErr)
		stats.failed++
		return
	}

	if stream.rapidUpload {
		fmt.Printf("[aliyun_to_115] ⚡ 秒传成功: %s -> 115目录(%s) [%v]\n", fullPath, p115DirID, elapsed)
		stats.rapid++
	} else {
		fmt.Printf("[aliyun_to_115] 📤 正常上传: %s -> 115目录(%s) [%v]\n", fullPath, p115DirID, elapsed)
		stats.normal++
	}

	// 如果你希望在 115 中物理保留文件，请注释掉下面这一行
	if d.DeleteAfterSync {
		_ = d.p115Client.removeFrom115(ctx, result)
	}

	d.syncLoopMu.Lock()
	d.syncedCache[cacheKey] = true
	d.syncLoopMu.Unlock()

	d.saveSyncedCache(cacheKey)

	stats.synced++
}

type urlFileStreamer struct {
	name     string
	path     string
	size     int64
	sha1Str  string
	url      string
	rapidUpload bool
	reader      io.Reader
	readerClose func() error
	file      model.File   // 缓存虚拟文件，避免重复创建
}

func (f *urlFileStreamer) GetID() string            { return "" }
func (f *urlFileStreamer) GetName() string          { return f.name }
func (f *urlFileStreamer) SetPath(path string)       { f.path = path }
func (f *urlFileStreamer) SetRapidUpload(b bool)     { f.rapidUpload = b }
func (f *urlFileStreamer) GetSize() int64            { return f.size }
func (f *urlFileStreamer) ModTime() time.Time        { return time.Time{} }
func (f *urlFileStreamer) CreateTime() time.Time     { return time.Time{} }
func (f *urlFileStreamer) IsDir() bool               { return false }
func (f *urlFileStreamer) GetHash() utils.HashInfo { return utils.NewHashInfo(utils.SHA1, f.sha1Str) }
func (f *urlFileStreamer) GetPath() string           { return f.path }
func (f *urlFileStreamer) GetMimetype() string       { return "application/octet-stream" }
func (f *urlFileStreamer) NeedStore() bool           { return true }
func (f *urlFileStreamer) IsForceStreamUpload() bool { return false }
func (f *urlFileStreamer) GetExist() model.Obj        { return nil }
func (f *urlFileStreamer) SetExist(model.Obj)        {}
func (f *urlFileStreamer) Add(io.Closer)             {}
func (f *urlFileStreamer) AddIfCloser(any)           {}

func newUrlFileStreamer(name string, size int64, sha1Str, url string) *urlFileStreamer {
	return &urlFileStreamer{name: name, size: size, sha1Str: sha1Str, url: url}
}

func (f *urlFileStreamer) Read(p []byte) (n int, err error) {
	if f.reader == nil {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		f.reader = resp.Body
		f.readerClose = resp.Body.Close
	}
	return f.reader.Read(p)
}

func (f *urlFileStreamer) Close() error {
	if f.readerClose != nil {
		f.readerClose()
		f.readerClose = nil
	}
	return nil
}

func (f *urlFileStreamer) RangeRead(ra http_range.Range) (io.Reader, error) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.url, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", ra.Start, ra.Start+ra.Length-1))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (f *urlFileStreamer) GetFile() model.File {
	if f.file != nil {
		f.file.Seek(0, io.SeekStart)
	}
	return f.file
}

// VirtualFile 按需发 HTTP Range 请求，不落盘
type VirtualFile struct {
	url        string
	client     *http.Client
	size       int64
	currOffset int64
	ctx        context.Context
}

func (v *VirtualFile) Read(p []byte) (n int, err error) {
	n, err = v.ReadAt(p, v.currOffset)
	if n > 0 {
		v.currOffset += int64(n) // 必须推进偏移量
	}
	return n, err
}

func (v *VirtualFile) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= v.size && v.size > 0 {
		return 0, io.EOF
	}
	endPos := off + int64(len(p)) - 1
	if v.size > 0 && endPos >= v.size {
		endPos = v.size - 1
	}
	req, err := http.NewRequestWithContext(v.ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, endPos))
	resp, err := v.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("http error: %d", resp.StatusCode)
	}
	n, err = io.ReadFull(resp.Body, p)
	if err == io.ErrUnexpectedEOF {
		err = nil
	}
	return n, err
}

func (v *VirtualFile) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = v.currOffset + offset
	case io.SeekEnd:
		newOffset = v.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if newOffset < 0 {
		return 0, errors.New("seek position out of range")
	}
	v.currOffset = newOffset
	return v.currOffset, nil
}

func (v *VirtualFile) Close() error { return nil }

func (f *urlFileStreamer) CacheFullAndWriter(up *model.UpdateProgress, w io.Writer) (model.File, error) {
	if f.file != nil {
		f.file.Seek(0, io.SeekStart)
		return f.file, nil
	}

	// HEAD 获取文件大小
	var fileSize int64
	if resp, err := http.DefaultClient.Head(f.url); err == nil {
		fileSize, _ = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		resp.Body.Close()
	}

	// Use a dedicated client with DisableKeepAlives to avoid HTTP/1.1 connection reuse
	// race conditions when multiple goroutines call ReadAt concurrently on the same VirtualFile
	httpClient := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
		Timeout: 60 * time.Second,
	}

	vf := &VirtualFile{
		url:    f.url,
		client: httpClient,
		size:   fileSize,
		ctx:    context.Background(),
	}
	f.file = vf

	if w != nil {
		go func() {
			r, _ := http.Get(f.url)
			if r != nil {
				defer r.Body.Close()
				io.Copy(w, r.Body)
			}
		}()
	}

	return vf, nil
}

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	var exp int
	for floatSize := float64(size); floatSize >= unit; floatSize /= unit {
		exp++
	}
	suffix := "BKMGTPE"[exp : exp+1]
	div := 1.0
	for i := 0; i < exp; i++ {
		div *= unit
	}
	return fmt.Sprintf("%.1f %sB", float64(size)/div, suffix)
}