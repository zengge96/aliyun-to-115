package aliyun_to_115

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// Test: Aliyun CDN Range request test (production simulation)
// Reads URL from url.txt (Alist URL that returns 302 to Aliyun CDN).
// Follows redirect, then sends concurrent Range requests to CDN.
// This simulates what UploadByMultipart does: concurrent ReadAt calls on VirtualFile.
// =============================================================================

func TestAliyunCDN_RangeSupport(t *testing.T) {
	// Read URL from url.txt (in workspace root)
	urlPath := "/root/.openclaw/workspace/url.txt"
	data, err := os.ReadFile(urlPath)
	if err != nil {
		t.Skipf("skip: no url file at %s: %v", urlPath, err)
	}
	alistURL := strings.TrimSpace(string(data))
	t.Logf("alistURL: %s", alistURL)

	// Fresh client that follows redirects
	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil // follow all redirects
		},
	}

	// Step 1: Follow redirect to get CDN URL
	reqInit, _ := http.NewRequest(http.MethodGet, alistURL, nil)
	respInit, err := httpClient.Do(reqInit)
	if err != nil {
		t.Fatalf("GET alistURL failed: %v", err)
	}
	respInit.Body.Close()
	cdnURL := respInit.Request.URL.String()
	t.Logf("cdnURL (after redirect): %s", cdnURL)

	// Step 2: Get file size from Content-Range header of a Range response
	reqSize, _ := http.NewRequest(http.MethodGet, cdnURL, nil)
	reqSize.Header.Set("Range", "bytes=0-0")
	respSize, err := httpClient.Do(reqSize)
	if err != nil {
		t.Fatalf("Range[0-0] failed: %v", err)
	}
	contentRange := respSize.Header.Get("Content-Range")
	actualSize := respSize.ContentLength // this is range length, not total
	respSize.Body.Close()

	// Parse total file size from Content-Range: bytes 0-0/2585682857
	var fileSize int64
	if _, err := fmt.Sscanf(contentRange, "bytes 0-0/%d", &fileSize); err != nil {
		t.Fatalf("failed to parse Content-Range %q: %v", contentRange, err)
	}
	t.Logf("Content-Range: %s, total fileSize: %d (range returned %d bytes)", contentRange, fileSize, actualSize)

	// Step 3: Simulate what UploadByMultipart does — concurrent ReadAt calls
	// Use same chunk size as production: 734565 bytes (from log)
	chunkSize := int64(1024 * 100) // 100KB for faster test
	numChunks := 7
	offsets := make([]int64, numChunks)
	for i := 0; i < numChunks; i++ {
		offsets[i] = int64(i) * chunkSize
	}

	type chunkResult struct {
		idx          int
		offset       int64
		size         int
		data         []byte
		etag         string
		status       int
		contentRange string
		err          error
	}

	t.Logf("\n=== Sending %d concurrent Range requests ===", numChunks)

	results := make(chan chunkResult, numChunks)
	var wg sync.WaitGroup
	for i, off := range offsets {
		wg.Add(1)
		go func(idx int, offset int64) {
			defer wg.Done()

			buf := make([]byte, chunkSize)
			endPos := offset + chunkSize - 1
			if endPos >= fileSize {
				endPos = fileSize - 1
			}

			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, cdnURL, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, endPos))

			resp, err := httpClient.Do(req)
			if err != nil {
				results <- chunkResult{idx: idx, offset: offset, err: err}
				return
			}
			defer resp.Body.Close()

			n, err := io.ReadFull(resp.Body, buf)
			if err != nil && err != io.ErrUnexpectedEOF {
				results <- chunkResult{idx: idx, offset: offset, status: resp.StatusCode, err: err}
				return
			}

			results <- chunkResult{
				idx:          idx,
				offset:       offset,
				size:         n,
				data:         bytes.Clone(buf[:n]),
				etag:         resp.Header.Get("ETag"),
				status:       resp.StatusCode,
				contentRange: resp.Header.Get("Content-Range"),
			}
		}(i, off)
	}
	wg.Wait()
	close(results)

	// Step 4: Analyze
	t.Logf("\n=== Results ===")
	uniqueETags := make(map[string]int)   // etag -> count
	uniqueData := make(map[string]bool)   // first 20 bytes as string
	all206 := true
	var sha1Results []string

	for r := range results {
		if r.err != nil {
			t.Logf("  chunk[%d] offset=%d ERROR: %v", r.idx, r.offset, r.err)
			continue
		}
		uniqueETags[r.etag]++
		if len(r.data) > 0 {
			preview := string(r.data[:min(20, len(r.data))])
			uniqueData[preview] = true
			h := sha1.New()
			h.Write(r.data)
			sha1Results = append(sha1Results, fmt.Sprintf("chunk[%d] offset=%d etag=%s sha1=%s preview=%x",
				r.idx, r.offset, r.etag,
				strings.ToUpper(hex.EncodeToString(h.Sum(nil))),
				preview))
		}
		t.Logf("  chunk[%d] offset=%d status=%d etag=%s size=%d preview=%x",
			r.idx, r.offset, r.status, r.etag, r.size, r.data[:min(20, len(r.data))])
		if r.status != 206 {
			all206 = false
		}
	}

	// Step 5: Check SHA1 diversity (different offsets should have different content)
	t.Logf("\n=== SHA1 of each chunk ===")
	for _, s := range sha1Results {
		t.Logf("  %s", s)
	}

	etagCount := len(uniqueETags)
	t.Logf("\n=== Analysis ===")
	t.Logf("  Total chunks: %d", numChunks)
	t.Logf("  Unique ETags: %d (out of %d chunks)", etagCount, numChunks)
	t.Logf("  Unique content previews: %d", len(uniqueData))

	// Assertions
	if !all206 {
		t.Errorf("❌ NOT all 206 — CDN does NOT support Range properly")
	} else {
		t.Logf("✅ All responses are 206 Partial Content")
	}

	if etagCount < numChunks/2 {
		t.Errorf("❌ Only %d unique ETag(s) out of %d — CDN is CACHING and ignoring Range!", etagCount, numChunks)
	} else {
		t.Logf("✅ Good ETag diversity (%d/%d) — CDN Range appears to work", etagCount, numChunks)
	}

	if len(uniqueData) < numChunks/2 {
		t.Errorf("❌ Only %d unique content patterns — chunks are identical despite different offsets (CACHING BUG)", len(uniqueData))
	} else {
		t.Logf("✅ Content at different offsets is different — CDN Range works correctly")
	}
}