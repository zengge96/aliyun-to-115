package aliyun_to_115

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_share2open"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// sync115Client wraps _115.Pan115 for Aliyun-115 sync uploads.
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

// uploadTo115 uploads stream to 115 root directory (ID "0") and returns the uploaded file.
func (c *sync115Client) uploadTo115(ctx context.Context, stream model.FileStreamer) (model.Obj, error) {
	dstDir := &model.Object{ID: "0"}
	up := func(progress float64) {}
	return c.p115.Put(ctx, dstDir, stream, up)
}

// removeFrom115 deletes a file from 115 by its ID.
func (c *sync115Client) removeFrom115(ctx context.Context, file model.Obj) error {
	return c.p115.Remove(ctx, file)
}

// Drop cleans up resources.
func (c *sync115Client) Drop() {
	if c.p115 != nil {
		c.p115.Drop(context.Background())
	}
}

// doSyncLoop runs periodic sync scans.
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

// syncStats 用于统计同步结果
type syncStats struct {
	total   int64
	skipped int64
	noLink  int64
	failed  int64
	synced  int64
	rapid   int64
	normal  int64
}

// doSync 现在会一边遍历一边执行同步
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

	fmt.Printf("[aliyun_to_115] ===== 本轮同步开始，共%v个阿里云存储 =====\n", len(aliyunStorages))

	for _, aliyun := range aliyunStorages {
		mountPath := "/"
		if s := aliyun.GetStorage(); s != nil {
			mountPath = s.MountPath + "/"
		}
		rootID := aliyun.GetRootId()

		// 开始边走边同步
		err := d.walkAndSync(ctx, aliyun, mountPath, rootID, stats)
		if err != nil {
			fmt.Printf("[aliyun_to_115] walk error for %s: %v\n", mountPath, err)
		}
	}

	fmt.Printf("[aliyun_to_115] ===== 本轮完成: 发现%v个 / 跳过%v个 / 秒传%v个 / 正常%v个 / 失败%v个 / 无链接%v个 =====\n",
		stats.total, stats.skipped, stats.rapid, stats.normal, stats.failed, stats.noLink)
}

// walkAndSync 递归遍历并直接处理文件
func (d *AliyunTo115) walkAndSync(ctx context.Context, aliyun aliyunStorage, rootPath, rootID string, stats *syncStats) error {
	visited := make(map[string]bool)

	var walk func(parentPath, parentID string) error
	walk = func(parentPath, parentID string) error {
		if visited[parentID] {
			return nil
		}
		visited[parentID] = true

		files, err := aliyun.List(ctx, &model.Object{ID: parentID}, model.ListArgs{})
		if err != nil {
			return err
		}

		for _, f := range files {
			if f.IsDir() {
				// 递归进入子目录
				subPath := parentPath + f.GetName() + string(os.PathSeparator)
				_ = walk(subPath, f.GetID())
			} else {
				// 发现文件，立即同步
				stats.total++
				fullPath := parentPath + f.GetName()
				d.processSingleFile(ctx, aliyun, f, fullPath, stats)
			}
		}
		return nil
	}

	return walk(rootPath, rootID)
}

// processSingleFile 封装了单个文件的同步逻辑
func (d *AliyunTo115) processSingleFile(ctx context.Context, aliyun aliyunStorage, f model.Obj, fullPath string, stats *syncStats) {
	// 1. 确定 Cache Key (优先使用 SHA1)
	cacheKey := f.GetID()
	hashInfo := f.GetHash()
	sha1Str := hashInfo.GetHash(utils.SHA1)
	if sha1Str != "" {
		cacheKey = sha1Str
	}

	// 2. 检查缓存
	d.syncLoopMu.Lock()
	if d.syncedCache[cacheKey] {
		d.syncLoopMu.Unlock()
		stats.skipped++
		return
	}
	d.syncLoopMu.Unlock()

	// 3. 获取下载链接
	link, err := aliyun.Link(ctx, f, model.LinkArgs{})
	
	// 特殊驱动处理 (Share2Open)
	if driver, ok := aliyun.(*aliyundrive_share2open.AliyundriveShare2Open); ok {
		sha1Str = driver.GetHash(ctx, f, model.LinkArgs{})
	}

	if err != nil || link == nil || link.URL == "" {
		fmt.Printf("[aliyun_to_115] no download link: %s (sha1=%s): %v\n", fullPath, sha1Str, err)
		stats.noLink++
		return
	}

	// 4. 执行上传到 115
	stream := newUrlFileStreamer(f.GetName(), f.GetSize(), sha1Str, link.URL)
	start := time.Now()
	result, uploadErr := d.p115Client.uploadTo115(ctx, stream)
	elapsed := time.Since(start)

	if uploadErr != nil || result == nil {
		fmt.Printf("[aliyun_to_115] upload failed: %s : %v\n", fullPath, uploadErr)
		stats.failed++
		return
	}

	// 5. 打印结果
	if stream.rapidUpload {
		fmt.Printf("[aliyun_to_115] ⚡ 秒传成功: %s (%s, %v)\n", fullPath, formatSize(f.GetSize()), elapsed)
		stats.rapid++
	} else {
		fmt.Printf("[aliyun_to_115] 📤 正常上传: %s (%s, %v)\n", fullPath, formatSize(f.GetSize()), elapsed)
		stats.normal++
	}

	// 6. 从 115 删除文件（仅保留 SHA1 预注册）
	_ = d.p115Client.removeFrom115(ctx, result)

	// 7. 更新缓存
	d.syncLoopMu.Lock()
	d.syncedCache[cacheKey] = true
	d.syncLoopMu.Unlock()
	stats.synced++
}

// fileWithPath wraps a model.Obj with its computed full path.
type fileWithPath struct {
	model.Obj
	fullPath string
}

func (f *fileWithPath) GetPath() string { return f.fullPath }

// urlFileStreamer implements model.FileStreamer for a URL download.
type urlFileStreamer struct {
	name     string
	path     string
	size     int64
	sha1Str  string
	url      string
	rapidUpload bool
	reader      io.Reader
	readerClose func() error
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

func (f *urlFileStreamer) CacheFullAndWriter(up *model.UpdateProgress, w io.Writer) (model.File, error) {
	log.Printf("[DEBUG] CacheFullAndWriter called: size=%d", f.size)
	tmpF, err := os.CreateTemp(os.TempDir(), "urlstream-*")
	if err != nil {
		return nil, fmt.Errorf("CreateTemp failed: %w", err)
	}
	tmpFileName := tmpF.Name()
	defer func() {
		if err != nil {
			tmpF.Close()
			os.Remove(tmpFileName)
		}
	}()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download URL failed: %w", err)
	}
	defer resp.Body.Close()

	if w != nil {
		_, err = io.Copy(io.MultiWriter(tmpF, w), resp.Body)
	} else {
		_, err = io.Copy(tmpF, resp.Body)
	}
	if err != nil {
		return nil, fmt.Errorf("download to temp file failed: %w", err)
	}
	tmpF.Seek(0, io.SeekStart)
	fc := &model.FileCloser{File: tmpF, Closer: tmpF}
	err = nil // clear error so defer doesn't remove the file
	return fc, nil
}

func (f *urlFileStreamer) GetFile() model.File { return nil }

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	var exp int
	for floatSize := float64(size); floatSize >= unit; floatSize /= unit {
		exp++
	}
	div := float64(unit)
	for i := 0; i < exp-1; i++ {
		div *= unit
	}
	suffix := "BKMGTPE"[exp : exp+1]
	return fmt.Sprintf("%.1f %sB", float64(size)/div, suffix)
}
