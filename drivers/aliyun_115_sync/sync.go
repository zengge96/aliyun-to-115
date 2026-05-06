package aliyun_115_sync

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// sync115Client wraps _115.Pan115 for Aliyun-115 sync.
type sync115Client struct {
	p115 *_115.Pan115
}

// NewSync115Client creates a sync115Client from cookie.
func NewSync115Client(cookie string) (*sync115Client, error) {
	p115 := &_115.Pan115{}
	p115.Addition.Cookie = cookie
	if err := p115.Init(context.Background()); err != nil {
		return nil, err
	}
	return &sync115Client{p115: p115}, nil
}

// uploadTo115 uploads stream to 115 using _115.Pan115.Put().
func (c *sync115Client) uploadTo115(ctx context.Context, stream model.FileStreamer, fullHash string) (model.Obj, error) {
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

// syncState tracks processed files within a sync cycle.
type syncState struct {
	processed map[string]bool
	mu        sync.Mutex
}

func newSyncState() *syncState {
	return &syncState{processed: make(map[string]bool)}
}

func (s *syncState) has(sha1 string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processed[sha1]
}

func (s *syncState) add(sha1 string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processed[sha1] = true
}

// walkFilesRecursively recursively lists all files under parentID using AliyundriveOpen.List.
// Uses a visited set to detect and skip cycles.
func (d *Aliyun115Sync) walkFilesRecursively(ctx context.Context, parentID string) ([]*fileWithPath, error) {
	visited := make(map[string]bool)
	var walk func(parentPath, parentID string) ([]*fileWithPath, error)
	walk = func(parentPath, parent string) ([]*fileWithPath, error) {
		if visited[parent] {
			return nil, nil
		}
		visited[parent] = true

		files, err := d.AliyundriveOpen.List(ctx, &model.Object{ID: parent}, model.ListArgs{})
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
	return walk("", parentID)
}

// fileWithPath wraps a model.Obj with its computed full path on disk.
type fileWithPath struct {
	model.Obj
	fullPath string
}

func (f *fileWithPath) GetPath() string { return f.fullPath }

// urlFileStreamer implements model.FileStreamer for a URL download.
type urlFileStreamer struct {
	name        string
	path        string
	size        int64
	sha1Str     string
	url         string
	rapidUpload bool // true=秒传 false=正常上传，由115驱动回调写入
	// Runtime state for Read() method
	reader      io.Reader
	readerClose func() error
}

func (f *urlFileStreamer) GetID() string             { return "" }
func (f *urlFileStreamer) GetName() string {
	if f.path != "" {
		return f.path
	}
	return f.name
}
func (f *urlFileStreamer) SetPath(path string) { f.path = path }

func (f *urlFileStreamer) SetRapidUpload(b bool) { f.rapidUpload = b }
func (f *urlFileStreamer) GetSize() int64            { return f.size }
func (f *urlFileStreamer) ModTime() time.Time        { return time.Time{} }
func (f *urlFileStreamer) CreateTime() time.Time     { return time.Time{} }
func (f *urlFileStreamer) IsDir() bool               { return false }
func (f *urlFileStreamer) Hash() (string, any)       { return f.sha1Str, nil }
func (f *urlFileStreamer) GetHash() utils.HashInfo   { return utils.NewHashInfo(utils.SHA1, f.sha1Str) }
func (f *urlFileStreamer) GetPath() string           { return f.path }
func (f *urlFileStreamer) GetMimetype() string       { return "application/octet-stream" }
func (f *urlFileStreamer) NeedStore() bool           { return true }
func (f *urlFileStreamer) IsForceStreamUpload() bool { return false }
func (f *urlFileStreamer) GetExist() model.Obj       { return nil }
func (f *urlFileStreamer) SetExist(model.Obj)        {}
func (f *urlFileStreamer) Add(io.Closer)             {}
func (f *urlFileStreamer) AddIfCloser(any)           {}

func newUrlFileStreamer(name string, size int64, sha1Str, url string) *urlFileStreamer {
	return &urlFileStreamer{name: name, size: size, sha1Str: sha1Str, url: url}
}

// Read downloads file content from URL on demand (supports OSS upload path)
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
	log.Printf("[DEBUG] CacheFullAndWriter: temp file=%s", tmpF.Name())
	var tmpFile model.File = tmpF
	defer func() {
		if tmpFile != nil {
			log.Printf("[DEBUG] CacheFullAndWriter: closing temp file (success)")
			// Don't remove - caller will use it
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
	log.Printf("[DEBUG] CacheFullAndWriter: downloaded, seeking to start")
	tmpF.Seek(0, io.SeekStart)
	tmpFile = nil // prevent deferred remove
	return tmpF, nil
}

func (f *urlFileStreamer) GetFile() model.File { return nil }
