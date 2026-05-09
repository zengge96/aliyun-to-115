package aliyun_to_115

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	driver115 "github.com/xiaoyaliu00/115driver/pkg/driver"
	netutil "github.com/OpenListTeam/OpenList/v4/internal/net"
)

func init() {
	conf.Conf = conf.DefaultConfig("data")
	base.InitClient()
}

func skipWithoutCookie(t *testing.T, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("skip: no cookie file at %s: %v", path, err)
	}
	return strings.TrimSpace(string(data))
}

// =============================================================================
// Test 1: formatSize — pure function, table-driven
// =============================================================================
func TestFormatSize(t *testing.T) {
	tests := []struct {
		size int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{int64(1024*1024) * 500, "500.0 MB"},
		{int64(1024*1024*1024), "1.0 GB"},
		{int64(1024*1024*1024) * 2, "2.0 GB"},
	}
	for _, tc := range tests {
		got := formatSize(tc.size)
		if got != tc.want {
			t.Errorf("formatSize(%d) = %q, want %q", tc.size, got, tc.want)
		}
	}
}

// =============================================================================
// Test 2: memFileStreamer — implements FileStreamer
// =============================================================================

// memFileStreamer is an in-memory backed FileStreamer for testing.
type memFileStreamer struct {
	*utils.Closers // satisfies utils.ClosersIF
	name    string
	path    string
	size    int64
	sha1Str string
	data    []byte
	pos     int64
}

func (m *memFileStreamer) GetID() string             { return "" }
func (m *memFileStreamer) GetName() string           { return m.name }
func (m *memFileStreamer) GetSize() int64            { return m.size }
func (m *memFileStreamer) GetPath() string           { return m.path }
func (m *memFileStreamer) SetPath(path string)       { m.path = path }
func (m *memFileStreamer) ModTime() time.Time        { return time.Time{} }
func (m *memFileStreamer) CreateTime() time.Time     { return time.Time{} }
func (m *memFileStreamer) IsDir() bool               { return false }
func (m *memFileStreamer) GetHash() utils.HashInfo   { return utils.NewHashInfo(utils.SHA1, m.sha1Str) }
func (m *memFileStreamer) GetMimetype() string       { return "application/octet-stream" }
func (m *memFileStreamer) NeedStore() bool          { return true }
func (m *memFileStreamer) IsForceStreamUpload() bool { return false }
func (m *memFileStreamer) GetExist() model.Obj       { return nil }
func (m *memFileStreamer) SetExist(model.Obj)        {}

func (m *memFileStreamer) Read(p []byte) (int, error) {
	if m.pos >= m.size {
		return 0, io.EOF
	}
	n := copy(p, m.data[m.pos:])
	m.pos += int64(n)
	return n, nil
}

func (m *memFileStreamer) RangeRead(ra http_range.Range) (io.Reader, error) {
	start := ra.Start
	if start >= m.size {
		return io.NopCloser(strings.NewReader("")), nil
	}
	end := start + ra.Length
	if end > m.size {
		end = m.size
	}
	return io.NopCloser(strings.NewReader(string(m.data[start:end]))), nil
}

func (m *memFileStreamer) CacheFullAndWriter(up *model.UpdateProgress, w io.Writer) (model.File, error) {
	r := bytes.NewReader(m.data)
	if w != nil {
		r.WriteTo(w) // write full content to w if needed
	}
	return r, nil
}

func (m *memFileStreamer) GetFile() model.File { return bytes.NewReader(m.data) }

func TestMemFileStreamer_ImplementsFileStreamer(t *testing.T) {
	content := []byte("hello world")
	h := sha1.New()
	h.Write(content)
	sha1Str := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))

	s := &memFileStreamer{
		Closers: &utils.Closers{},
		name:    "test.txt",
		size:    int64(len(content)),
		sha1Str: sha1Str,
		data:    content,
	}

	// Verify it satisfies model.FileStreamer
	var _ model.FileStreamer = s

	// Verify Obj methods
	if s.GetName() != "test.txt" {
		t.Errorf("GetName() = %q, want %q", s.GetName(), "test.txt")
	}
	if s.GetSize() != int64(len(content)) {
		t.Errorf("GetSize() = %d, want %d", s.GetSize(), len(content))
	}
	if s.IsDir() {
		t.Error("IsDir() = true, want false")
	}

	// Verify GetHash
	hashInfo := s.GetHash()
	if got := hashInfo.GetHash(utils.SHA1); got != sha1Str {
		t.Errorf("GetHash().GetHash(SHA1) = %q, want %q", got, sha1Str)
	}
}

func TestMemFileStreamer_RangeRead(t *testing.T) {
	content := []byte("0123456789") // 10 bytes
	s := &memFileStreamer{
		Closers: &utils.Closers{},
		name:    "test.bin",
		size:    int64(len(content)),
		data:    content,
	}

	// Read bytes [3, 6) => "345"
	r, err := s.RangeRead(http_range.Range{Start: 3, Length: 3})
	if err != nil {
		t.Fatalf("RangeRead failed: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(data) != "345" {
		t.Errorf("RangeRead(3, 3) = %q, want %q", string(data), "345")
	}
}

func TestMemFileStreamer_CacheFullAndWriter(t *testing.T) {
	content := []byte("test content")
	s := &memFileStreamer{
		Closers: &utils.Closers{},
		name:    "test.bin",
		size:    int64(len(content)),
		data:    content,
	}

	var buf strings.Builder
	f, err := s.CacheFullAndWriter(nil, &buf)
	if err != nil {
		t.Fatalf("CacheFullAndWriter failed: %v", err)
	}
	if f != nil {
		t.Logf("CacheFullAndWriter returned file (expected nil for mem streamer)")
	}
}

// =============================================================================
// Test 3: urlFileStreamer — unit test for GetHash
// =============================================================================
func TestURLFileStreamer_GetHash(t *testing.T) {
	f := newUrlFileStreamer("test.txt", 1024, "DA39A3EE5E6B4B0D3255BFEF95601890AFD80709", "https://example.com/file")
	hash := f.GetHash()
	got := hash.GetHash(utils.SHA1)
	want := "DA39A3EE5E6B4B0D3255BFEF95601890AFD80709"
	if got != want {
		t.Errorf("GetHash() = %q, want %q", got, want)
	}
}

func TestURLFileStreamer_RangeRead(t *testing.T) {
	// Use a public URL that supports range requests
	f := newUrlFileStreamer("range_test", 100, "abc123", "https://httpbin.org/bytes/100")
	ra := http_range.Range{Start: 0, Length: 10}
	r, err := f.RangeRead(ra)
	if err != nil {
		t.Fatalf("RangeRead failed: %v", err)
	}
	data := make([]byte, 10)
	n, err := r.Read(data)
	if err != nil && err != io.EOF {
		t.Fatalf("Read failed: %v", err)
	}
	if n != 10 {
		t.Errorf("read %d bytes, want 10", n)
	}
	t.Logf("RangeRead returned: %q", string(data))
}

// =============================================================================
// Test 4: sync115Client — integration test (requires cookie)
// =============================================================================
func TestSync115Client_UploadTo115(t *testing.T) {
	cookie := skipWithoutCookie(t, "/root/.openclaw/115_cookie.txt")

	// Create Pan115 via newSync115Client (may panic if 115 API unreachable — test will crash)
	// Wrap in recover to prevent panic from crashing the test process
	var client *sync115Client
	var initErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				initErr = fmt.Errorf("115 init panicked: %v", r)
			}
		}()
		client, initErr = newSync115Client(cookie)
	}()
	if initErr != nil {
		t.Skipf("skip: %v", initErr)
	}
	defer client.Drop()

	// Create 1MB test content
	size := int64(1024 * 1024)
	content := make([]byte, size)
	rand.Read(content)

	h := sha1.New()
	h.Write(content)
	sha1Str := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))

	stream := &memFileStreamer{
		Closers: &utils.Closers{},
		name:    "tdd_1mb.bin",
		size:    size,
		sha1Str: sha1Str,
		data:    content,
	}

	result, err := client.uploadTo115(context.Background(), stream, "0")
	if err != nil {
		t.Fatalf("uploadTo115 failed: %v", err)
	}
	if result == nil {
		t.Fatal("uploadTo115 returned nil")
	}
	t.Logf("✅ uploadTo115 success: %s (size=%d)", result.GetName(), result.GetSize())

	// Clean up: remove the uploaded file
	client.removeFrom115(context.Background(), result)
}

// =============================================================================
// Test 5: Upload 11MB large file — exercises OSS multipart + in-memory stream
// ================================================================================

func TestSync115Client_UploadLargeFile(t *testing.T) {
	cookie := skipWithoutCookie(t, "/root/.openclaw/115_cookie.txt")

	var client *sync115Client
	var initErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				initErr = fmt.Errorf("115 init panicked: %v", r)
			}
		}()
		client, initErr = newSync115Client(cookie)
	}()
	if initErr != nil {
		t.Skipf("skip: %v", initErr)
	}
	defer client.Drop()

	// Create 11MB test content
	size := int64(11 * 1024 * 1024)
	content := make([]byte, size)
	rand.Read(content)

	h := sha1.New()
	h.Write(content)
	sha1Str := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))

	stream := &memFileStreamer{
		Closers: &utils.Closers{},
		name:    "tdd_11mb.bin",
		size:    size,
		sha1Str: sha1Str,
		data:    content,
	}

	result, err := client.uploadTo115(context.Background(), stream, "0")
	if err != nil {
		t.Fatalf("uploadTo115 failed: %v", err)
	}
	if result == nil {
		t.Fatal("uploadTo115 returned nil")
	}
	t.Logf("✅ uploadTo115 large file success: %s (size=%d)", result.GetName(), result.GetSize())

	client.removeFrom115(context.Background(), result)
}

// =============================================================================
// Test 7: Upload via urlFileStreamer (HTTP) — URL → VirtualFile → HTTP Range → 115
// =============================================================================

func TestSync115Client_UploadViaUrlFileStreamer(t *testing.T) {
	cookie := skipWithoutCookie(t, "/root/.openclaw/115_cookie.txt")

	var client *sync115Client
	var initErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				initErr = fmt.Errorf("115 init panicked: %v", r)
			}
		}()
		client, initErr = newSync115Client(cookie)
	}()
	if initErr != nil {
		t.Skipf("skip: %v", initErr)
	}
	defer client.Drop()

	// 1. Create local temp file (5MB)
	const fileSize = int64(11 * 1024 * 1024)
	content := make([]byte, fileSize)
	rand.Read(content)

	tmpFile, err := os.CreateTemp("", "urlstreamertest_*.bin")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(content); err != nil {
		os.Remove(tmpPath)
		t.Fatalf("Write temp file failed: %v", err)
	}
	tmpFile.Close()

	// 2. Compute SHA1 of content for urlFileStreamer
	h := sha1.New()
	h.Write(content)
	sha1Str := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))

	// 3. Start local HTTP server serving the file
	httpFile, err := os.Open(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		t.Fatalf("Open tmpPath failed: %v", err)
	}
	defer httpFile.Close()

	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, "test.bin", time.Now(), httpFile)
		}),
	}

	// Find an available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Remove(tmpPath)
		t.Fatalf("Listen failed: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// Start server in goroutine
	go httpServer.Serve(listener)

	url := fmt.Sprintf("http://127.0.0.1:%d/test.bin", port)

	// 4. Create urlFileStreamer pointing to local HTTP server
	stream := newUrlFileStreamer("tdd_url_11mb.bin", fileSize, sha1Str, url)

	// 5. Upload via HTTP URL → VirtualFile → 115
	result, err := client.uploadTo115(context.Background(), stream, "0")
	if err != nil {
		// Clean up server before fatal
		httpServer.Close()
		os.Remove(tmpPath)
		t.Fatalf("uploadTo115 via URL failed: %v", err)
	}
	if result == nil {
		httpServer.Close()
		os.Remove(tmpPath)
		t.Fatal("uploadTo115 returned nil")
	}
	t.Logf("✅ uploadTo115 via URL success: %s (size=%d)", result.GetName(), result.GetSize())

	// 6. Cleanup: remove from 115, stop server, remove temp file
	client.removeFrom115(context.Background(), result)
	httpServer.Close()
	os.Remove(tmpPath)
}

// =============================================================================
// Test 5: Dedup cache — empty aliyunStorages should leave cache unchanged
// =============================================================================
func TestSyncDedupCache(t *testing.T) {
	d := &AliyunTo115{
		syncRunning: false,
		syncedCache: map[string]bool{
			"already_synced_sha1": true,
		},
		// syncLoopMu is zero-value (unlocked)
	}

	d.doSync()

	// Cache should be unchanged
	if !d.syncedCache["already_synced_sha1"] {
		t.Error("cache entry was lost after doSync")
	}
	if len(d.syncedCache) != 1 {
		t.Errorf("cache size = %d, want 1", len(d.syncedCache))
	}
}

// =============================================================================
// Test 8: Upload via urlFileStreamer (HTTP) — URL → VirtualFile → HTTP Range → 115
// =============================================================================

func TestSync115Client_UploadViaUrlFileStreamer(t *testing.T) {
	cookie := skipWithoutCookie(t, "/root/.openclaw/115_cookie.txt")

	var syncClient *sync115Client
	var initErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				initErr = fmt.Errorf("115 init panicked: %v", r)
			}
		}()
		syncClient, initErr = newSync115Client(cookie)
	}()
	if initErr != nil {
		t.Skipf("skip: %v", initErr)
	}
	defer syncClient.Drop()

	urlPath := "/root/.openclaw/workspace/url.txt"
	data, err := os.ReadFile(urlPath)
	if err != nil {
		t.Skipf("skip: no url file at %s: %v", urlPath, err)
	}
	alistURL := strings.TrimSpace(string(data))

	// Follow 302 to get CDN URL
	httpClient := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return nil },
	}
	reqInit, _ := http.NewRequest(http.MethodGet, alistURL, nil)
	respInit, err := httpClient.Do(reqInit)
	if err != nil {
		t.Fatalf("GET alistURL failed: %v", err)
	}
	respInit.Body.Close()
	if respInit.Request.URL.Host == "47.104.92.112:39123" {
		t.Skipf("skip: alist server not reachable from this host")
	}

	cdnURL := respInit.Request.URL.String()
	t.Logf("cdnURL: %s", cdnURL)

	// 使用 HEAD 请求动态获取远程文件的大小 (fileSize)
	reqHead, _ := http.NewRequest(http.MethodHead, cdnURL, nil)
	respHead, err := httpClient.Do(reqHead)
	if err != nil {
		t.Fatalf("HEAD cdnURL failed: %v", err)
	}
	respHead.Body.Close()
	fileSize := respHead.ContentLength
	if fileSize <= 0 {
		t.Fatalf("Invalid fileSize from CDN: %d", fileSize)
	}

	// 构造一个合法的假 SHA1 (40个字符的十六进制字符串，已改为大写)
	sha1Str := "DA39A3EE5E6B4B0D3255BFEF95601890AFD80709"

	// 4. Create urlFileStreamer pointing to local HTTP server
	stream := newUrlFileStreamer("cdn.bin", fileSize, sha1Str, cdnURL)

	// 加入 30s 整体上传超时 Context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 劫持标准输出(Stdout)和标准错误(Stderr)，以便捕获屏幕打印记录
	oldStdout, oldStderr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr

	// 使用 Goroutine 防止管道缓冲区满导致死锁
	var outBuf bytes.Buffer
	outputDone := make(chan struct{})
	go func() {
		io.Copy(&outBuf, rOut)
		io.Copy(&outBuf, rErr)
		close(outputDone)
	}()

	// 5. Upload via HTTP URL → VirtualFile → 115
	result, err := syncClient.uploadTo115(ctx, stream, "0")

	// 恢复标准输出
	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	<-outputDone // 等待读取完毕
	capturedOutput := outBuf.String()

	if err != nil {
		t.Fatalf("uploadTo115 via URL failed: %v\nOutput: %s", err, capturedOutput)
	}
	if result == nil {
		t.Fatal("uploadTo115 returned nil")
	}

	// 解析屏幕打印，检查 etag
	// 匹配形如 etag="xxxxx" 或 ETag="xxxxx" 或 Etag=xxxxx 的内容
	re := regexp.MustCompile(`(?i)etag=["']?([a-zA-Z0-9_-]+)["']?`)
	matches := re.FindAllStringSubmatch(capturedOutput, -1)

	uniqueEtags := make(map[string]bool)
	for _, m := range matches {
		if len(m) > 1 {
			uniqueEtags[m[1]] = true
		}
	}

	t.Logf("Captured %d unique ETags.", len(uniqueEtags))
	if len(uniqueEtags) < 5 {
		t.Errorf("Test Failed: expected at least 5 unique etags, but got %d.\nCaptured Output:\n%s", len(uniqueEtags), capturedOutput)
	} else {
		t.Logf("✅ ETag validation passed! Unique etags: %v", uniqueEtags)
	}

	t.Logf("✅ uploadTo115 via URL success: %s (size=%d)", result.GetName(), result.GetSize())

	// 6. Cleanup: remove from 115
	syncClient.removeFrom115(context.Background(), result)
}