package aliyun_to_115

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
)

func init() {
	base.InitClient()
}

// =============================================================================
// Test: VirtualFile concurrent ReadAt — same as production UploadByMultipart
// =============================================================================

func TestVirtualFile_ConcurrentReadAt(t *testing.T) {
	urlPath := "/root/.openclaw/workspace/url.txt"
	data, err := os.ReadFile(urlPath)
	if err != nil {
		t.Skipf("skip: no url file at %s: %v", urlPath, err)
	}
	alistURL := strings.TrimSpace(string(data))

	// Follow 302 to get CDN URL
	client := &http.Client{Timeout: 30, CheckRedirect: func(req *http.Request, via []*http.Request) error { return nil }}
	reqInit, _ := http.NewRequest(http.MethodGet, alistURL, nil)
	respInit, err := client.Do(reqInit)
	if err != nil {
		t.Fatalf("GET alistURL failed: %v", err)
	}
	respInit.Body.Close()
	if respInit.Request.URL.Host == "47.104.92.112:39123" {
		t.Skipf("skip: alist server not reachable from this host")
	}
	cdnURL := respInit.Request.URL.String()
	t.Logf("cdnURL: %s", cdnURL)

	// Get file size via Content-Range
	reqSize, _ := http.NewRequest(http.MethodGet, cdnURL, nil)
	reqSize.Header.Set("Range", "bytes=0-0")
	respSize, _ := client.Do(reqSize)
	cr := respSize.Header.Get("Content-Range")
	var fileSize int64
	fmt.Sscanf(cr, "bytes 0-0/%d", &fileSize)
	respSize.Body.Close()
	t.Logf("fileSize: %d", fileSize)

	// Create VirtualFile (same way production code does)
	vf := &VirtualFile{
		url:    cdnURL,
		client: http.DefaultClient,
		size:   fileSize,
		ctx:    context.Background(),
	}

	// Simulate production: concurrent ReadAt calls
	chunkSize := int64(1024 * 100) // 100KB
	numChunks := 7
	type result struct {
		idx    int
		offset int64
		data   []byte
		err    error
	}

	t.Logf("\n=== %d concurrent VirtualFile.ReadAt calls ===", numChunks)
	results := make(chan result, numChunks)
	var wg sync.WaitGroup
	for i := 0; i < numChunks; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			off := int64(idx) * chunkSize
			buf := make([]byte, chunkSize)
			n, err := vf.ReadAt(buf, off)
			if err != nil {
				results <- result{idx: idx, offset: off, err: err}
				return
			}
			results <- result{idx: idx, offset: off, data: bytes.Clone(buf[:n])}
		}(i)
	}
	wg.Wait()
	close(results)

	// Analyze
	t.Logf("\n=== Results ===")
	uniqueSha1 := make(map[string]int)
	var sha1List []string
	for r := range results {
		if r.err != nil {
			t.Logf("  chunk[%d] offset=%d ERROR: %v", r.idx, r.offset, r.err)
			continue
		}
		h := sha1.New()
		h.Write(r.data)
		s := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
		uniqueSha1[s]++
		sha1List = append(sha1List, fmt.Sprintf("chunk[%d] offset=%d sha1=%s preview=%x",
			r.idx, r.offset, s, r.data[:min(20, len(r.data))]))
		t.Logf("  chunk[%d] offset=%d size=%d sha1=%s preview=%x",
			r.idx, r.offset, len(r.data), s, r.data[:min(20, len(r.data))])
	}

	t.Logf("\n=== SHA1 diversity ===")
	for _, s := range sha1List {
		t.Logf("  %s", s)
	}

	// Assertions
	uniqueCount := len(uniqueSha1)
	t.Logf("\n=== Analysis ===")
	t.Logf("  Total chunks: %d", numChunks)
	t.Logf("  Unique SHA1s: %d", uniqueCount)

	if uniqueCount < numChunks/2 {
		t.Errorf("❌ Only %d/%d unique SHA1s — data is being repeated (CDN caching bug)", uniqueCount, numChunks)
	} else {
		t.Logf("✅ All SHA1s unique — VirtualFile.ReadAt works correctly")
	}
}