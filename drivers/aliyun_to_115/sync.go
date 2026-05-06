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

// doSync performs one sync cycle across all aliyun storages.
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

	var total, skipped, noLink, failed, synced, rapid, normal int64

	for _, aliyun := range d.aliyunStorages {
		mountPath := ""
		if s := aliyun.GetStorage(); s != nil {
			mountPath = s.MountPath
		}
		files, err := d.walkFilesRecursively(ctx, aliyun)
		if err != nil {
			fmt.Printf("[aliyun_to_115] walk error for %s: %v\n", mountPath, err)
			continue
		}
		total += int64(len(files))

		for _, file := range files {
			hashInfo := file.GetHash()
			sha1Str := hashInfo.GetHash(utils.SHA1)
			if sha1Str == "" {
				continue
			}

			// Check synced cache (global SHA1 dedup across all aliyun storages)
			d.syncLoopMu.Lock()
			if d.syncedCache[sha1Str] {
				d.syncLoopMu.Unlock()
				skipped++
				continue
			}
			d.syncLoopMu.Unlock()

			// Get download link from Aliyun
			link, err := aliyun.Link(ctx, file, model.LinkArgs{})
			if err != nil || link == nil || link.URL == "" {
				fmt.Printf("[aliyun_to_115] no download link: %s (sha1=%s): %v\n", file.GetPath(), sha1Str, err)
				noLink++
				continue
			}

			// Upload to 115
			stream := newUrlFileStreamer(file.GetName(), file.GetSize(), sha1Str, link.URL)
			start := time.Now()
			result, uploadErr := d.p115Client.uploadTo115(ctx, stream)
			elapsed := time.Since(start)
			if uploadErr != nil || result == nil {
				fmt.Printf("[aliyun_to_115] upload failed: %s (sha1=%s): %v\n", file.GetPath(), sha1Str, uploadErr)
				failed++
				continue
			}

			if stream.rapidUpload {
				fmt.Printf("[aliyun_to_115] ⚡ 秒传成功: %s (sha1=%s, %s, %v)\n", file.GetPath(), sha1Str, formatSize(file.GetSize()), elapsed)
				rapid++
			} else {
				fmt.Printf("[aliyun_to_115] 📤 正常上传: %s (sha1=%s, %s, %v)\n", file.GetPath(), sha1Str, formatSize(file.GetSize()), elapsed)
				normal++
			}

			// Delete from 115 (leaving SHA1 pre-registered)
			_ = d.p115Client.removeFrom115(ctx, result)

			// Mark as synced
			d.syncLoopMu.Lock()
			d.syncedCache[sha1Str] = true
			d.syncLoopMu.Unlock()
			synced++
		}
	}

	fmt.Printf("[aliyun_to_115] ===== 本轮完成: 共%v个 / 跳过%v个 / 秒传%v个 / 正常%v个 / 失败%v个 / 无链接%v个 =====\n",
		total, skipped, rapid, normal, failed, noLink)
}

// walkFilesRecursively recursively lists all files under an aliyun storage.
func (d *AliyunTo115) walkFilesRecursively(ctx context.Context, aliyun aliyunStorage) ([]*fileWithPath, error) {
	visited := make(map[string]bool)
	var walk func(parentPath, parentID string) ([]*fileWithPath, error)
	walk = func(parentPath, parentID string) ([]*fileWithPath, error) {
		if visited[parentID] {
			return nil, nil
		}
		visited[parentID] = true

		files, err := aliyun.List(ctx, &model.Object{ID: parentID}, model.ListArgs{})
		if err != nil {
			return nil, err
		}

		var result []*fileWithPath
		for _, f := range files {
			if f.IsDir() {
				subFiles, _ := walk(parentPath+f.GetName()+string(os.PathSeparator), f.GetID())
				result = append(result, subFiles...)
			} else {
				fw := &fileWithPath{Obj: f, fullPath: parentPath + f.GetName()}
				result = append(result, fw)
			}
		}
		return result, nil
	}
	rootID := aliyun.GetRootId()
	if _, ok := aliyun.(*aliyundrive_share2open.AliyundriveShare2Open); ok {
		rootID = ""
	}
	return walk("", rootID)
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