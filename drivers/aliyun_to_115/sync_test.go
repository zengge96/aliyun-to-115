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
// Test: CDN Real Upload — 5 chunks should get 5 different ETags
// =============================================================================
func TestVirtualFile_CDNRealUpload_DifferentETags(t *testing.T) {
	// 1. Read proxy URL from ~/.openclaw/workspace/url.txt
	urlData, err := os.ReadFile(os.ExpandEnv("$HOME/.openclaw/workspace/url.txt"))
	if err != nil {
		t.Skipf("skip: no url.txt: %v", err)
	}
	proxyURL := strings.TrimSpace(string(urlData))
	if proxyURL == "" {
		t.Skip("skip: empty url.txt")
	}

	// 2. Follow 302 to get CDN URL
	client302 := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client302.Get(proxyURL)
	if err != nil {
		t.Fatalf("proxy URL request failed: %v", err)
	}
	resp.Body.Close()
	cdnURL := resp.Header.Get("Location")
	if cdnURL == "" {
		t.Fatalf("no Location header in 302 response")
	}
	t.Logf("CDN URL: %s", cdnURL)

	// 3. Get 115 cookie and init sync115Client (for VirtualFile reading)
	cookie := skipWithoutCookie(t, "/root/.openclaw/115_cookie.txt")
	syncClient, err := newSync115Client(cookie)
	if err != nil {
		t.Skipf("skip: newSync115Client failed: %v", err)
	}
	defer syncClient.Drop()

	// 4. Create urlFileStreamer → VirtualFile
	const fileSize = int64(5 * 1024 * 1024)
	name := "cdn_etag_test_5mb.bin"
	stream := newUrlFileStreamer(name, fileSize, "", cdnURL)
	vf, err := stream.CacheFullAndWriter(nil, nil)
	if err != nil {
		t.Fatalf("CacheFullAndWriter failed: %v", err)
	}

	// 5. Verify 5 different chunks (SHA1)
	const chunkSize = int64(1024 * 1024)
	chunkHashes := make([]string, 5)
	for i := 0; i < 5; i++ {
		offset := int64(i) * chunkSize
		buf := make([]byte, chunkSize)
		n, err := vf.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			t.Fatalf("ReadAt offset=%d failed: %v", offset, err)
		}
		h := sha1.New()
		h.Write(buf[:n])
		chunkHashes[i] = strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
		t.Logf("chunk[%d] offset=%d size=%d sha1=%s", i, offset, n, chunkHashes[i])
	}
	uniqueHashes := make(map[string]bool)
	for _, h := range chunkHashes {
		uniqueHashes[h] = true
	}
	if len(uniqueHashes) != 5 {
		t.Errorf("chunks do NOT have unique content: got %d unique, want 5", len(uniqueHashes))
	}

	// 6. Get bucket via rapidUpload using driver115.Pan115Client
	// Parse cookie string "UID=xxx;CID=xxx;SEID=xxx;KID=xxx" into map
	cookieMap := make(map[string]string)
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if idx := strings.IndexByte(part, '='); idx > 0 {
			cookieMap[strings.TrimSpace(part[:idx])] = strings.TrimSpace(part[idx+1:])
		}
	}
	driverClient := driver115.New()
	driverClient.ImportCookies(cookieMap, ".115.com")

	// Create small temp file to get UploadOSSParams (bucket, callback)
	tmpFile, err := os.CreateTemp("", "115bucket_*.bin")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	defer tmpFile.Close()

	randBuf := make([]byte, 512)
	io.ReadFull(crand.Reader, randBuf)
	tmpFile.Write(randBuf)
	tmpFile.Sync()
	tmpFile.Close()

	tmpF, err := os.Open(tmpPath)
	if err != nil {
		t.Fatalf("Open temp file failed: %v", err)
	}
	defer tmpF.Close()
	tmpInfo, _ := tmpF.Stat()

	h := sha1.New()
	io.Copy(h, tmpF)
	fullHash := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
	tmpF.Seek(0, 0)

	rapidResp, err := driverClient.RapidUpload(tmpInfo.Size(), "test_bucket.bin", "0", fullHash, fullHash, tmpF)
	if err != nil {
		t.Fatalf("RapidUpload to get bucket failed: %v", err)
	}
	bucketName := rapidResp.Bucket
	t.Logf("Got bucket: %s, callback: %s", bucketName, rapidResp.Callback)

	ossParams := &driver115.UploadOSSParams{
		Bucket:   rapidResp.Bucket,
		Callback: rapidResp.Callback,
		Object:   rapidResp.Object,
	}

	// 7. Get OSS token and create OSS client
	ossToken, err := driverClient.GetOSSToken()
	if err != nil {
		t.Fatalf("GetOSSToken failed: %v", err)
	}
	ossClient, err := netutil.NewOSSClient(driver115.OSSEndpoint, ossToken.AccessKeyID, ossToken.AccessKeySecret,
		oss.EnableMD5(true), oss.EnableCRC(true))
	if err != nil {
		t.Fatalf("NewOSSClient failed: %v", err)
	}
	bucket, err := ossClient.Bucket(bucketName)
	if err != nil {
		t.Fatalf("Bucket(%s) failed: %v", bucketName, err)
	}

	// 8. InitiateMultipartUpload
	objectKey := fmt.Sprintf("test_etag_%d.bin", time.Now().UnixNano())
	imur, err := bucket.InitiateMultipartUpload(objectKey,
		oss.SetHeader(driver115.OssSecurityTokenHeaderName, ossToken.SecurityToken),
		oss.UserAgentHeader(driver115.OSSUserAgent),
		oss.EnableSha1(), oss.Sequential(),
	)
	if err != nil {
		t.Fatalf("InitiateMultipartUpload failed: %v", err)
	}
	t.Logf("uploadID=%s object=%s", imur.UploadID, objectKey)

	// 9. Upload 5 parts and collect ETags
	etags := make([]string, 5)
	for i := 0; i < 5; i++ {
		offset := int64(i) * chunkSize
		buf := make([]byte, chunkSize)
		n, err := vf.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			t.Fatalf("ReadAt offset=%d failed: %v", offset, err)
		}
		part, err := bucket.UploadPart(imur, bytes.NewReader(buf[:n]), int64(n), i+1,
			driver115.OssOption(ossParams, ossToken)...)
		if err != nil {
			t.Fatalf("UploadPart chunk[%d] failed: %v", i, err)
		}
		etags[i] = part.ETag
		t.Logf("part[%d] offset=%d etag=%s", i+1, offset, etags[i])
	}

	// 10. Verify 5 different ETags
	uniqueETags := make(map[string]bool)
	for _, e := range etags {
		uniqueETags[e] = true
	}
	if len(uniqueETags) != 5 {
		t.Errorf("ETags NOT all different: got %v (unique=%d)", etags, len(uniqueETags))
	} else {
		t.Logf("✅ all 5 chunks have different ETags: %v", etags)
	}
}
