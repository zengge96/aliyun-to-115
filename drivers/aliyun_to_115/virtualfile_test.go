package aliyun_to_115

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// Test: VirtualFile concurrent ReadAt against local mock server
// Simulates: production flow of VirtualFile.ReadAt being called concurrently
// from multiple goroutines, each reading a different offset.
// =============================================================================

func TestVirtualFile_ConcurrentReadAt_LocalMock(t *testing.T) {
	// Constants
	const (
		fileSize  = int64(5*1024*1024 + 123) // 5MB+123, NOT multiple of 256
		chunkSize = int64(1024 * 100)       // 100KB chunks
	)

	// Pre-generate random file content once
	fileContent := make([]byte, fileSize)
	if _, err := rand.Read(fileContent); err != nil {
		t.Fatalf("failed to generate random file content: %v", err)
	}

	// --- Mock CDN server that respects Range headers ---
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		log.Printf("[MOCK] Range: %q URL: %s", rangeHeader, r.URL)

		var start, end int64
		n, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end)
		if n != 2 || err != nil {
			log.Printf("[MOCK] failed to parse Range header: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if start < 0 || end < start || end >= fileSize {
			log.Printf("[MOCK] invalid range %d-%d (fileSize=%d)", start, end, fileSize)
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		sliceSize := end - start + 1
		content := fileContent[start : start+sliceSize]
		log.Printf("[MOCK] returning bytes %d-%d/%d (size=%d) preview=%x",
			start, end, fileSize, sliceSize, content[:min(10, len(content))])

		w.Header().Set("ETag", fmt.Sprintf("mock-etag-%d", fileSize))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", sliceSize))
		w.WriteHeader(http.StatusPartialContent)
		io.Copy(w, bytes.NewReader(content))
	}))
	defer server.Close()

	// --- Create VirtualFile pointing to mock server ---
	// Use DisableKeepAlives to prevent connection reuse across goroutines
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        0,
			MaxIdleConnsPerHost: 0,
			DisableKeepAlives:   true,
		},
		Timeout: 30 * time.Second,
	}
	vf := &VirtualFile{
		url:    server.URL,
		client: client,
		size:   fileSize,
		ctx:    context.Background(),
	}

	// --- Concurrent ReadAt test ---
	type chunkRes struct {
		idx    int
		offset int64
		size   int64
		data   []byte
		err    error
	}

	offsets := []int64{0, 1, 102400, 204800, 307200, 409600, 500000}
	results := make(chan chunkRes, len(offsets))
	var wg sync.WaitGroup

	t.Logf("=== %d concurrent VirtualFile.ReadAt calls ===", len(offsets))
	for i, off := range offsets {
		wg.Add(1)
		go func(idx int, offset int64) {
			defer wg.Done()
			thisChunkSize := chunkSize
			if offset == 0 {
				thisChunkSize = 1024 // small chunk for offset 0
			}
			if offset+int64(thisChunkSize) > fileSize {
				thisChunkSize = fileSize - offset
			}
			buf := make([]byte, thisChunkSize)
			n, err := vf.ReadAt(buf, offset)
			if err != nil {
				results <- chunkRes{idx: idx, offset: offset, size: thisChunkSize, err: err}
				return
			}
			results <- chunkRes{idx: idx, offset: offset, size: int64(n), data: bytes.Clone(buf[:n])}
		}(i, off)
	}
	wg.Wait()
	close(results)

	// --- Verify ---
	t.Logf("\n=== Results ===")
	uniqueSha1 := make(map[string]bool)
	allOk := true

	// Collect results for second pass
	var allResults []chunkRes
	for r := range results {
		allResults = append(allResults, r)
		if r.err != nil {
			t.Logf("  chunk[%d] offset=%d ERROR: %v", r.idx, r.offset, r.err)
			allOk = false
			continue
		}
		h := sha1.New()
		h.Write(r.data)
		s := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
		uniqueSha1[s] = true
		// Verify content matches expected from fileContent
		expected := fileContent[r.offset : r.offset+int64(len(r.data))]
		if !bytes.Equal(r.data, expected) {
			t.Errorf("  ❌ chunk[%d] offset=%d: content mismatch!\n    expected: %x\n    got:      %x",
				r.idx, r.offset, expected[:min(20, len(expected))], r.data[:min(20, len(r.data))])
			allOk = false
		}
		t.Logf("  chunk[%d] offset=%d size=%d sha1=%s ✅", r.idx, r.offset, len(r.data), s)
	}

	t.Logf("\n=== Analysis ===")
	t.Logf("  Total chunks: %d", len(offsets))
	t.Logf("  Unique SHA1s: %d", len(uniqueSha1))

	if len(uniqueSha1) < len(offsets) {
		t.Errorf("❌ Only %d/%d unique SHA1s — VirtualFile.ReadAt returns wrong data",
			len(uniqueSha1), len(offsets))
		allOk = false
	} else {
		t.Logf("✅ All SHA1s unique — VirtualFile.ReadAt concurrent reads work correctly")
	}

	_ = allOk // suppress unused warning
}