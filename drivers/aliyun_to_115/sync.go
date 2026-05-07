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

// getOrCreateDir checks if a directory exists under parentID; if not, creates it.
func (c *sync115Client) getOrCreateDir(ctx context.Context, parentID string, dirName string) (string, error) {
	// 1. List current directory to find if folder already exists
	objs, err := c.p115.List(ctx, &model.Object{ID: parentID}, model.ListArgs{})
	if err == nil {
		for _, obj := range objs {
			if obj.IsDir() && obj.GetName() == dirName {
				return obj.GetID(), nil
			}
		}
	}

	// 2. Not found, create it
	newDir, err := c.p115.MakeDir(ctx, &model.Object{ID: parentID}, dirName)
	if err != nil {
		return "", fmt.Errorf("failed to create 115 dir [%s]: %w", dirName, err)
	}
	return newDir.GetID(), nil
}

// uploadTo115 uploads stream to a specific 115 directory ID.
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

	// Initial 115 Root Folder from user config
	targetRootID := d.RootFolderID
	if targetRootID == "" {
		targetRootID = "0"
	}

	fmt.Printf("[aliyun_to_115] ===== 同步开始，目标115根ID: %s =====\n", targetRootID)

	for _, aliyun := range aliyunStorages {
		mountPath := "/"
		if s := aliyun.GetStorage(); s != nil {
			mountPath = s.MountPath + "/"
		}
		aliRootID := aliyun.GetRootId()

		err := d.walkAndSync(ctx, aliyun, mountPath, aliRootID, targetRootID, stats)
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
			// 1. Maintain directory structure: get or create folder on 115
			subP115ID, err := d.p115Client.getOrCreateDir(ctx, p115ParentID, f.GetName())
			if err != nil {
				fmt.Printf("[aliyun_to_115] Skip Dir: %s, error: %v\n", f.GetName(), err)
				continue
			}
			// 2. Recurse
			subPath := currentPath + f.GetName() + "/"
			_ = d.walkAndSync(ctx, aliyun, subPath, f.GetID(), subP115ID, stats)
		} else {
			// 3. Process File
			stats.total++
			fullPath := currentPath + f.GetName()
			d.processSingleFile(ctx, aliyun, f, fullPath, p115ParentID, stats)
		}
	}
	return nil
}

func (d *AliyunTo115) processSingleFile(ctx context.Context, aliyun aliyunStorage, f model.Obj, fullPath string, p115DirID string, stats *syncStats) {
	cacheKey := f.GetID()
	hashInfo := f.GetHash()
	sha1Str := hashInfo.GetHash(utils.SHA1)
	if sha1Str != "" {
		cacheKey = sha1Str
	}

	d.syncLoopMu.Lock()
	if d.syncedCache[cacheKey] {
		d.syncLoopMu.Unlock()
		stats.skipped++
		return
	}
	d.syncLoopMu.Unlock()

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
	// Upload to the mapped 115 directory
	result, uploadErr := d.p115Client.uploadTo115(ctx, stream, p115DirID)
	elapsed := time.Since(start)

	if uploadErr != nil || result == nil {
		fmt.Printf("[aliyun_to_115] upload failed: %s : %v\n", fullPath, uploadErr)
		stats.failed++
		return
	}

	if stream.rapidUpload {
		fmt.Printf("[aliyun_to_115] ⚡ 秒传成功: %s -> 115(%s) [%v]\n", fullPath, p115DirID, elapsed)
		stats.rapid++
	} else {
		fmt.Printf("[aliyun_to_115] 📤 正常上传: %s -> 115(%s) [%v]\n", fullPath, p115DirID, elapsed)
		stats.normal++
	}

	// Important: If you want to KEEP files in 115, comment out the line below. 
	// Currently it deletes the file after upload (usually to just 'register' the SHA1).
	//_ = d.p115Client.removeFrom115(ctx, result)

	d.syncLoopMu.Lock()
	d.syncedCache[cacheKey] = true
	d.syncLoopMu.Unlock()
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
	suffix := "BKMGTPE"[exp : exp+1]
	div := 1.0
	for i := 0; i < exp; i++ {
		div *= unit
	}
	return fmt.Sprintf("%.1f %sB", float64(size)/div, suffix)
}