package aliyun_to_115

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	//"strconv"
	"strings"
	"time"
	"os"
	"path"
	"path/filepath"

	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_share2open"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
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

	// ========== strm.txt 模式检测 ==========
	cwd, _ := os.Getwd()
	strmFile := filepath.Join(cwd, "strm.txt")
	strmWorkFile := filepath.Join(cwd, "strm_work.txt")
	strmSuccessFile := filepath.Join(cwd, "strm_success.txt")
	if _, err := os.Stat(strmFile); err == nil {
		// strm.txt 存在，切换为文件同步模式

		// 第1步：准备 strm_work.txt（首次运行从 strm.txt 复制）
		if _, err := os.Stat(strmWorkFile); os.IsNotExist(err) {
			data, err := os.ReadFile(strmFile)
			if err != nil {
				fmt.Printf("[aliyun_to_115] 读取 strm.txt 失败: %v\n", err)
				return
			}
			fmt.Printf("[aliyun_to_115] strm.txt 首次运行，复制为 strm_work.txt\n")
			os.WriteFile(strmWorkFile, data, 0644)
		}

		// 第2步：如果 strm_success.txt 存在，从 strm_work.txt 中过滤掉已成功的行
		if successData, err := os.ReadFile(strmSuccessFile); err == nil {
			successSet := make(map[string]bool)
			for _, line := range strings.Split(strings.TrimSpace(string(successData)), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					successSet[line] = true
				}
			}

			// 读取 strm_work.txt，过滤掉已成功的行
			workData, err := os.ReadFile(strmWorkFile)
			if err == nil {
				var filteredLines []string
				for _, line := range strings.Split(strings.TrimSpace(string(workData)), "\n") {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					if !successSet[line] {
						filteredLines = append(filteredLines, line)
					}
				}
				newWork := strings.Join(filteredLines, "\n") + "\n"
				os.WriteFile(strmWorkFile, []byte(newWork), 0644)
			}

			// 删除 strm_success.txt
			os.Remove(strmSuccessFile)
			fmt.Printf("[aliyun_to_115] 过滤完成，已从 strm_work.txt 移除 %d 条已成功记录\n", len(successSet))
		}

		// 第3步：读取 strm_work.txt 并逐行处理
		workData, err := os.ReadFile(strmWorkFile)
		if err != nil {
			fmt.Printf("[aliyun_to_115] 读取 strm_work.txt 失败: %v\n", err)
			return
		}

		lines := strings.Split(strings.TrimSpace(string(workData)), "\n")
		_ = len(lines) // totalLines
		processedCount := 0

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "#", 2)
			if len(parts) != 2 {
				continue
			}
			dstRaw := strings.TrimSpace(parts[0])
			dstRaw = "/" + strings.TrimPrefix(dstRaw, "/")
			srcRaw := strings.TrimSpace(parts[1])

			// 解析出真实 srcPath：如果是 HTTP URL 则提取路径部分并做 URL decode
			srcPath := srcRaw
			if strings.HasPrefix(srcRaw, "http://") || strings.HasPrefix(srcRaw, "https://") {
				if u, err := url.Parse(srcRaw); err == nil {
					srcPath, _ = url.QueryUnescape(u.Path)
					srcPath = strings.TrimPrefix(srcPath, "/d")
				}
			}

			dstPath := dstRaw
			// 扩展名替换
			srcExt := filepath.Ext(srcPath)
			if srcExt != "" {
				ext := filepath.Ext(dstRaw)
				if ext != "" {
					dstPath = strings.TrimSuffix(dstRaw, ext) + srcExt
				}
			}

			// 处理这一行
			if err := d.processSingleFile(ctx, srcPath, dstPath, stats); err == nil {
				// 成功后追加到 strm_success.txt
				os.WriteFile(strmSuccessFile, []byte(line+"\n"), 0644)
			}
		}

		// 第4步：同步完成，删除 strm_work.txt（strm_success.txt 已为空文件/已删）
		if processedCount > 0 {
			os.Remove(strmWorkFile)
			fmt.Printf("[aliyun_to_115] strm_work.txt 已清理\n")
		}

		fmt.Printf("[aliyun_to_115] ===== strm模式同步完成: 发现%v / 跳过%v / 秒传%v / 正常%v / 失败%v =====\n",
			stats.total, stats.skipped, stats.rapid, stats.normal, stats.failed)
		return
	}

	// ========== 原生遍历驱动模式 ==========
	for _, aliyun := range aliyunStorages {
		storage := aliyun.GetStorage()
		mountPath := "/"
		if storage != nil {
			mountPath = storage.MountPath
		}
		
		fmt.Printf("[aliyun_to_115] 正在处理阿里存储: %s\n", mountPath)

		aliRootID := aliyun.GetRootId()
		err := d.walkAndSync(ctx, aliyun, mountPath + "/", aliRootID, stats)
		if err != nil {
			fmt.Printf("[aliyun_to_115] walk error for %s: %v\n", mountPath, err)
		}
	}

	fmt.Printf("[aliyun_to_115] ===== 同步完成: 发现%v / 跳过%v / 秒传%v / 正常%v / 失败%v =====\n",
		stats.total, stats.skipped, stats.rapid, stats.normal, stats.failed)
}

func (d *AliyunTo115) walkAndSync(ctx context.Context, aliyun aliyunStorage, currentPath, aliParentID string, stats *syncStats) error {
	files, err := aliyun.List(ctx, &model.Object{ID: aliParentID}, model.ListArgs{})
	if err != nil {
		return err
	}

	for _, f := range files {
		if f.IsDir() {
			subPath := currentPath + f.GetName() + "/"
			_ = d.walkAndSync(ctx, aliyun, subPath, f.GetID(), stats)
		} else {
			stats.total++
			fullPath := currentPath + f.GetName()
			d.processSingleFile(ctx, fullPath, fullPath, stats)
		}
	}
	return nil
}

func getFirstDirPurePath(p string) string {
	p = path.Clean(p)
	if p == "/" || p == "." {
		return ""
	}
	for {
		parent := path.Dir(p)
		if parent == "." || parent == "/" {
			return strings.TrimPrefix(p, "/")
		}
		p = parent
	}
}

func (d *AliyunTo115) getOrCreateDirID(ctx context.Context, fullPath string) (string, error) {
	fullPath = path.Clean(fullPath)
	
	if fullPath == "/" || fullPath == "." || fullPath == "" {
		dirObj, err := fs.Get(ctx, "/", &fs.GetArgs{NoLog: true})
		if err != nil {
			return "", fmt.Errorf("获取根目录信息失败: %w", err)
		}
		return dirObj.GetID(), nil
	}

	dirObj, err := fs.Get(ctx, fullPath, &fs.GetArgs{NoLog: true})
	if err == nil {
		if dirObj.IsDir() {
			return dirObj.GetID(), nil
		}
		return "", fmt.Errorf("路径冲突：目标是文件而非文件夹: %s", fullPath)
	}
	
	parentPath := path.Dir(fullPath)
	if parentPath != fullPath {
		_, err = d.getOrCreateDirID(ctx, parentPath)
		if err != nil {
			return "", fmt.Errorf("创建父目录失败 [%s]: %w", parentPath, err)
		}
	}

	storage, actualPath, err := op.GetStorageAndActualPath(fullPath)
	if err != nil {
		return "", fmt.Errorf("解析存储路径失败: %w", err)
	}

	err = op.MakeDir(ctx, storage, actualPath)
	if err != nil {
		return "", fmt.Errorf("创建目录失败 [%s]: 错误: %w", fullPath, err)
	}

	time.Sleep(500 * time.Millisecond)
	fs.List(ctx, parentPath, &fs.ListArgs{
			NoLog: true,
			Refresh: true,
		})

	dirObj, err = fs.Get(ctx, fullPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		return "", fmt.Errorf("获取新建目录 ID 失败 [%s]: %w", fullPath, err)
	}
	
	return dirObj.GetID(), nil
}

func (d *AliyunTo115) processSingleFile(ctx context.Context, srcPath string, dstPath string, stats *syncStats) error {
	aliyun, _, err := op.GetStorageAndActualPath(srcPath)
	if err != nil {
		fmt.Printf("[aliyun_to_115] 获取驱动失败， fullPath=%s : %v\n", srcPath, err)
		return err
	}

	f, err := fs.Get(ctx, srcPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		fmt.Printf("[aliyun_to_115] 获取文件对象失败， fullPath=%s : %v\n", srcPath, err)
		return err
	}
	if f.IsDir() {
		return errors.New("is directory")
	}

	p115DirStr := d.GetStorage().MountPath + path.Dir(dstPath)
	p115DirID, err := d.getOrCreateDirID(ctx, p115DirStr)
	if err != nil {
		fmt.Printf("[aliyun_to_115] 准备115目录失败 [%s]: %v\n", p115DirStr, err)
		return err
	}

	// 缓存逻辑
	aliyunMountPath := aliyun.GetStorage().MountPath
	cacheKey := aliyunMountPath + "/" + f.GetID()
	hashInfo := f.GetHash()
	sha1Str := hashInfo.GetHash(utils.SHA1)
	if sha1Str != "" {
		cacheKey = aliyunMountPath + "/" + sha1Str
	}

	d.syncLoopMu.Lock()
	if d.syncedCache[cacheKey] {
		d.syncLoopMu.Unlock()
		stats.skipped++
		return nil
	}
	d.syncLoopMu.Unlock()

	// 115 share 风控严重，等待 1 秒
	if _, ok := aliyun.(*aliyundrive_share2open.AliyundriveShare2Open); ok {
		time.Sleep(1 * time.Second)
	}

	link, err := aliyun.Link(ctx, f, model.LinkArgs{})
	// 兼容某些驱动可能重新获取一次 Hash
	if driver, ok := aliyun.(*aliyundrive_share2open.AliyundriveShare2Open); ok {
		sha1Str = driver.GetHash(ctx, f, model.LinkArgs{})
	}

	if err != nil || link == nil || link.URL == "" {
		stats.noLink++
		return errors.New("no link")
	}

	stream := newUrlFileStreamer(path.Base(dstPath), f.GetSize(), sha1Str, link.URL)
	start := time.Now()
	
	// 使用动态获取到的 p115DirID 上传
	result, uploadErr := d.p115Client.uploadTo115(ctx, stream, p115DirID)
	elapsed := time.Since(start)

	if uploadErr != nil || result == nil {
		fmt.Printf("[aliyun_to_115] 上传失败: %s : %v\n", srcPath, uploadErr)
		stats.failed++
		return uploadErr
	}

	if stream.rapidUpload {
		fmt.Printf("[aliyun_to_115] ⚡ 秒传成功: %s -> 115目录ID(%s) [%v]\n", srcPath, p115DirID, elapsed)
		stats.rapid++
	} else {
		fmt.Printf("[aliyun_to_115] 📤 正常上传: %s -> 115目录ID(%s) [%v]\n", srcPath, p115DirID, elapsed)
		stats.normal++
	}

	if d.DeleteAfterSync {
		_ = d.p115Client.removeFrom115(ctx, result)
	}

	d.syncLoopMu.Lock()
	d.syncedCache[cacheKey] = true
	d.syncLoopMu.Unlock()
	d.saveSyncedCache(cacheKey)
	stats.synced++
	return nil
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
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		// Server returned fewer bytes than requested (e.g., CDN range limit)
		// n contains actual bytes read
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
	// var fileSize int64
	// if resp, err := http.DefaultClient.Head(f.url); err == nil {
	// 	fileSize, _ = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	// 	resp.Body.Close()
	// }

	httpClient := &http.Client{}

	vf := &VirtualFile{
		url:    f.url,
		client: httpClient,
		//size:   fileSize,
		size:   f.size,
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
