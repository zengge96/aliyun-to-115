package aliyun_to_115

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
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
	return nil, nil
}
func (m *memFileStreamer) GetFile() model.File { return nil }

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