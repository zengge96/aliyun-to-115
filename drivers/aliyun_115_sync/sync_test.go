package aliyun_115_sync

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_open"
	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

var (
	aliyunRefreshToken  string
	aliyunAPIAddress    = "https://api.oplist.org/alicloud/renewapi"
)

func init() {
	tokenData, err := os.ReadFile("/root/.openclaw/aliyun_refresh_token.txt")
	if err != nil {
		panic("read aliyun refresh token file failed: " + err.Error())
	}
	aliyunRefreshToken = strings.TrimSpace(string(tokenData))
}

// =============================================================================
// Shared test fixtures
// =============================================================================

func init() {
	conf.Conf = conf.DefaultConfig("data")
	base.InitClient()
}


// fileStreamerForTest implements model.FileStreamer backed by a real file path
type fileStreamerForTest struct {
	name     string
	size     int64
	sha1Str  string
	filePath string
	file     *os.File
}

func (f *fileStreamerForTest) GetID() string             { return "" }
func (f *fileStreamerForTest) GetName() string           { return f.name }
func (f *fileStreamerForTest) GetSize() int64            { return f.size }
func (f *fileStreamerForTest) ModTime() time.Time        { return time.Time{} }
func (f *fileStreamerForTest) CreateTime() time.Time     { return time.Time{} }
func (f *fileStreamerForTest) IsDir() bool               { return false }
func (f *fileStreamerForTest) Hash() (string, any)       { return f.sha1Str, nil }
func (f *fileStreamerForTest) GetHash() utils.HashInfo   { return utils.NewHashInfo(utils.SHA1, f.sha1Str) }
func (f *fileStreamerForTest) GetPath() string           { return "" }
func (f *fileStreamerForTest) GetMimetype() string       { return "application/octet-stream" }
func (f *fileStreamerForTest) NeedStore() bool           { return true }
func (f *fileStreamerForTest) IsForceStreamUpload() bool { return false }
func (f *fileStreamerForTest) GetExist() model.Obj       { return nil }
func (f *fileStreamerForTest) SetExist(model.Obj)        {}

func (f *fileStreamerForTest) ensureFile() error {
	if f.file == nil {
		var err error
		f.file, err = os.Open(f.filePath)
		return err
	}
	return nil
}

func (f *fileStreamerForTest) Read(p []byte) (int, error) {
	if err := f.ensureFile(); err != nil {
		return 0, err
	}
	return f.file.Read(p)
}

func (f *fileStreamerForTest) Close() error {
	if f.file != nil {
		return f.file.Close()
	}
	return nil
}

func (f *fileStreamerForTest) Add(io.Closer)   {}
func (f *fileStreamerForTest) AddIfCloser(any) {}

func (f *fileStreamerForTest) RangeRead(ra http_range.Range) (io.Reader, error) {
	if err := f.ensureFile(); err != nil {
		return nil, err
	}
	f.file.Seek(int64(ra.Start), 0)
	return &io.LimitedReader{R: f.file, N: ra.Length}, nil
}

func (f *fileStreamerForTest) CacheFullAndWriter(up *model.UpdateProgress, w io.Writer) (model.File, error) {
	if err := f.ensureFile(); err != nil {
		return nil, err
	}
	return &testCacheFile{file: f.file, size: f.size}, nil
}

func (f *fileStreamerForTest) GetFile() model.File { return nil }

// testCacheFile implements model.File for the cached file
type testCacheFile struct {
	file *os.File
	size int64
}

func (f *testCacheFile) GetSize() int64              { return f.size }
func (f *testCacheFile) ModTime() time.Time           { return time.Time{} }
func (f *testCacheFile) CreateTime() time.Time       { return time.Time{} }
func (f *testCacheFile) IsDir() bool                 { return false }
func (f *testCacheFile) Hash() (string, any)        { return "", nil }
func (f *testCacheFile) GetHash() utils.HashInfo     { return utils.HashInfo{} }
func (f *testCacheFile) GetPath() string             { return f.file.Name() }
func (f *testCacheFile) GetMimetype() string         { return "" }
func (f *testCacheFile) NeedStore() bool             { return false }
func (f *testCacheFile) GetID() string               { return "" }
func (f *testCacheFile) GetName() string             { return "" }
func (f *testCacheFile) IsForceStreamUpload() bool   { return false }
func (f *testCacheFile) GetExist() model.Obj         { return nil }
func (f *testCacheFile) SetExist(model.Obj)          {}

func (f *testCacheFile) Read(p []byte) (int, error) {
	return f.file.Read(p)
}

func (f *testCacheFile) ReadAt(p []byte, off int64) (int, error) {
	return f.file.ReadAt(p, off)
}

func (f *testCacheFile) Seek(offset int64, whence int) (int64, error) {
	return f.file.Seek(offset, whence)
}

func (f *testCacheFile) Close() error {
	return f.file.Close()
}

// =============================================================================
// Test 1: Basic upload (1MB, in-memory data)
// =============================================================================

func TestSync115ClientUploadsViaPut(t *testing.T) {
	cookieData, err := os.ReadFile("/root/.openclaw/115_cookie.txt")
	if err != nil {
		t.Skipf("skip: no cookie file: %v", err)
	}
	cookie := strings.TrimSpace(string(cookieData))

	client, err := NewSync115Client(cookie)
	if err != nil {
		t.Fatalf("NewSync115Client failed: %v", err)
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
		name:    "tdd_1mb.bin",
		size:    size,
		sha1Str: sha1Str,
		data:    content,
	}

	result, err := client.uploadTo115(context.Background(), stream, sha1Str)
	if err != nil {
		t.Fatalf("uploadTo115 failed: %v", err)
	}
	if result == nil {
		t.Fatal("uploadTo115 returned nil")
	}

	t.Logf("✅ uploadTo115 success: %s", result.GetName())
	client.removeFrom115(context.Background(), result)
}

// memFileStreamer: in-memory backed streamer (for small test data)
type memFileStreamer struct {
	name    string
	size    int64
	sha1Str string
	data    []byte
	pos     int64
}

func (f *memFileStreamer) GetID() string             { return "" }
func (f *memFileStreamer) GetName() string           { return f.name }
func (f *memFileStreamer) GetSize() int64            { return f.size }
func (f *memFileStreamer) ModTime() time.Time        { return time.Time{} }
func (f *memFileStreamer) CreateTime() time.Time     { return time.Time{} }
func (f *memFileStreamer) IsDir() bool               { return false }
func (f *memFileStreamer) Hash() (string, any)       { return f.sha1Str, nil }
func (f *memFileStreamer) GetHash() utils.HashInfo   { return utils.NewHashInfo(utils.SHA1, f.sha1Str) }
func (f *memFileStreamer) GetPath() string           { return "" }
func (f *memFileStreamer) GetMimetype() string       { return "application/octet-stream" }
func (f *memFileStreamer) NeedStore() bool           { return true }
func (f *memFileStreamer) IsForceStreamUpload() bool { return false }
func (f *memFileStreamer) GetExist() model.Obj       { return nil }
func (f *memFileStreamer) SetExist(model.Obj)        {}
func (f *memFileStreamer) Read(p []byte) (int, error) {
	if f.pos >= f.size {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.pos:])
	f.pos += int64(n)
	return n, nil
}
func (f *memFileStreamer) Close() error { return nil }
func (f *memFileStreamer) Add(closer io.Closer) {}
func (f *memFileStreamer) AddIfCloser(any)      {}
func (f *memFileStreamer) RangeRead(ra http_range.Range) (io.Reader, error) {
	start := ra.Start
	if start >= f.size {
		return io.NopCloser(strings.NewReader("")), nil
	}
	end := start + ra.Length
	if end > f.size {
		end = f.size
	}
	return io.NopCloser(strings.NewReader(string(f.data[start:end]))), nil
}
func (f *memFileStreamer) CacheFullAndWriter(up *model.UpdateProgress, w io.Writer) (model.File, error) { return nil, nil }
func (f *memFileStreamer) GetFile() model.File { return nil }

// =============================================================================
// Test 2: Upload → Delete → Rapid Reupload (small file, file-backed)
// =============================================================================

func TestSync115ClientRapidUpload(t *testing.T) {
	cookieData, err := os.ReadFile("/root/.openclaw/115_cookie.txt")
	if err != nil {
		t.Skipf("skip: no cookie file: %v", err)
	}
	cookie := strings.TrimSpace(string(cookieData))

	client, err := NewSync115Client(cookie)
	if err != nil {
		t.Fatalf("NewSync115Client failed: %v", err)
	}
	defer client.Drop()

	srcFile := "/etc/hostname"
	fileInfo, err := os.Stat(srcFile)
	if err != nil {
		t.Fatal("stat file failed:", err)
	}

	f, err := os.Open(srcFile)
	if err != nil {
		t.Fatal("open file failed:", err)
	}
	h := sha1.New()
	io.Copy(h, f)
	f.Close()
	sha1Str := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
	t.Logf("Using file: %s, size: %d, SHA1: %s", srcFile, fileInfo.Size(), sha1Str)

	stream := &fileStreamerForTest{
		name:     "rapid_test_small.txt",
		size:     fileInfo.Size(),
		sha1Str:  sha1Str,
		filePath: srcFile,
	}

	// Step 1: First upload
	t.Log("=== Step 1: First upload ===")
	start1 := time.Now()
	result1, err := client.uploadTo115(context.Background(), stream, sha1Str)
	if err != nil {
		t.Fatal("first uploadTo115 failed:", err)
	}
	if result1 == nil {
		t.Fatal("first uploadTo115 returned nil")
	}
	t.Logf("✅ First upload success: %s (ID: %s) in %v", result1.GetName(), result1.GetID(), time.Since(start1))
	id1 := result1.GetID()

	// Step 2: Delete
	t.Log("=== Step 2: Delete from 115 ===")
	err = client.removeFrom115(context.Background(), result1)
	if err != nil {
		t.Fatal("removeFrom115 failed:", err)
	}
	t.Logf("✅ Delete success: %s", result1.GetName())

	// Step 3: Re-upload (should be rapid)
	t.Log("=== Step 3: Re-upload (should use rapid upload) ===")
	start2 := time.Now()
	result2, err := client.uploadTo115(context.Background(), stream, sha1Str)
	if err != nil {
		t.Fatal("re-upload failed:", err)
	}
	if result2 == nil {
		t.Fatal("re-upload returned nil")
	}
	duration2 := time.Since(start2)
	t.Logf("✅ Re-upload success: %s (ID: %s) in %v", result2.GetName(), result2.GetID(), duration2)

	if id1 == result2.GetID() {
		t.Logf("🎯 Rapid upload confirmed - same file ID: %s", id1)
	} else {
		t.Logf("ℹ️  Different file ID: first=%s, second=%s", id1, result2.GetID())
	}

	if duration2 < 2*time.Second {
		t.Logf("🎯 Rapid upload confirmed by timing - %v << first upload", duration2)
	} else {
		t.Logf("⚠️  Re-upload took %v (expected rapid upload to be much faster)", duration2)
	}

	client.removeFrom115(context.Background(), result2)
}

// =============================================================================


// =============================================================================
// Aliyun tests — use real Aliyun115Sync
// =============================================================================

func TestSyncAliyunList(t *testing.T) {
	d := &Aliyun115Sync{
		AliyundriveOpen: &aliyundrive_open.AliyundriveOpen{},
	}
	cookieData, err := os.ReadFile("/root/.openclaw/115_cookie.txt")
	if err != nil {
		t.Skipf("skip: no cookie file: %v", err)
	}
	d.Open115Cookie = strings.TrimSpace(string(cookieData))
	d.Addition.SyncInterval = 0
	d.AliyundriveOpen.Addition.RefreshToken = aliyunRefreshToken
	d.AliyundriveOpen.Addition.APIAddress = aliyunAPIAddress
	d.AliyundriveOpen.Addition.RootFolderID = "root"
	d.AliyundriveOpen.Addition.UseOnlineAPI = true

	// Manually refresh token (like original aliyunDriverForSync did) to avoid Init → MustSaveDriverStorage
	var tokenResp struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
		ErrorMessage string `json:"text"`
	}
	_, err = base.RestyClient.R().
		SetResult(&tokenResp).
		SetQueryParams(map[string]string{
			"refresh_ui":  d.AliyundriveOpen.Addition.RefreshToken,
			"server_use":  "true",
			"driver_txt":  "alicloud_qr",
		}).
		Get(aliyunAPIAddress)
	if err != nil {
		t.Fatalf("token refresh failed: %v", err)
	}
	if tokenResp.AccessToken == "" {
		t.Fatalf("token refresh returned empty access token: %s", tokenResp.ErrorMessage)
	}
	d.AliyundriveOpen.Addition.AccessToken = tokenResp.AccessToken

	// Initialize the limiter (required for List/Link operations)
	d.AliyundriveOpen.InitLimiter()

	// Get drive info to find DriveId
	var driveResp struct {
		ResourceDriveID string `json:"resource_drive_id"`
		DefaultDriveID  string `json:"default_drive_id"`
		UserID          string `json:"user_id"`
	}
	_, err = base.RestyClient.R().
		SetHeader("Authorization", "Bearer "+tokenResp.AccessToken).
		SetResult(&driveResp).
		SetBody(map[string]string{"resource_drive_id": "", "default_drive_id": ""}).
		Post("https://openapi.alipan.com/adrive/v1.0/user/getDriveInfo")
	if err != nil {
		t.Fatalf("getDriveInfo failed: %v", err)
	}
	if driveResp.ResourceDriveID == "" && driveResp.DefaultDriveID == "" {
		t.Fatalf("getDriveInfo returned empty drive ID")
	}
	d.AliyundriveOpen.DriveId = driveResp.ResourceDriveID
	if d.AliyundriveOpen.DriveId == "" {
		d.AliyundriveOpen.DriveId = driveResp.DefaultDriveID
	}
	t.Logf("DriveId: %s", d.AliyundriveOpen.DriveId)

	// Initialize p115Client (needed for uploadTo115)
	p115Client, err := NewSync115Client(d.Open115Cookie)
	if err != nil {
		t.Fatalf("NewSync115Client failed: %v", err)
	}
	d.p115Client = p115Client
	defer d.p115Client.Drop()

	files, err := d.walkFilesRecursively(context.Background(), d.AliyundriveOpen.Addition.RootFolderID)
	if err != nil {
		t.Fatalf("walkFilesRecursively failed: %v", err)
	}
	t.Logf("Total files found: %d", len(files))
	for _, f := range files {
		if !f.IsDir() {
			t.Logf("  File: %s (%d bytes, sha1=%s)", f.GetName(), f.GetSize(), f.GetHash().GetHash(utils.SHA1))
		}
	}
	if len(files) == 0 {
		t.Fatal("expected at least one file")
	}
}

func TestSyncAliyunLink(t *testing.T) {
	d := &Aliyun115Sync{
		AliyundriveOpen: &aliyundrive_open.AliyundriveOpen{},
	}
	d.Addition.SyncInterval = 0
	cookieData, err := os.ReadFile("/root/.openclaw/115_cookie.txt")
	if err != nil {
		t.Skipf("skip: no cookie file: %v", err)
	}
	d.Open115Cookie = strings.TrimSpace(string(cookieData))
	d.AliyundriveOpen.Addition.RefreshToken = aliyunRefreshToken
	d.AliyundriveOpen.Addition.APIAddress = aliyunAPIAddress
	d.AliyundriveOpen.Addition.RootFolderID = "root"
	d.AliyundriveOpen.Addition.UseOnlineAPI = true

	// Manually refresh token to avoid MustSaveDriverStorage
	var tokenResp struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
		ErrorMessage string `json:"text"`
	}
	_, err = base.RestyClient.R().
		SetResult(&tokenResp).
		SetQueryParams(map[string]string{
			"refresh_ui":  d.AliyundriveOpen.Addition.RefreshToken,
			"server_use":  "true",
			"driver_txt":  "alicloud_qr",
		}).
		Get(aliyunAPIAddress)
	if err != nil {
		t.Fatalf("token refresh failed: %v", err)
	}
	if tokenResp.AccessToken == "" {
		t.Fatalf("token refresh returned empty access token: %s", tokenResp.ErrorMessage)
	}
	d.AliyundriveOpen.Addition.AccessToken = tokenResp.AccessToken
	d.AliyundriveOpen.InitLimiter()

	// Get drive info
	var driveResp struct {
		ResourceDriveID string `json:"resource_drive_id"`
		DefaultDriveID  string `json:"default_drive_id"`
	}
	_, err = base.RestyClient.R().
		SetHeader("Authorization", "Bearer "+tokenResp.AccessToken).
		SetResult(&driveResp).
		SetBody(map[string]string{"resource_drive_id": "", "default_drive_id": ""}).
		Post("https://openapi.alipan.com/adrive/v1.0/user/getDriveInfo")
	if err != nil {
		t.Fatalf("getDriveInfo failed: %v", err)
	}
	d.AliyundriveOpen.DriveId = driveResp.ResourceDriveID
	if d.AliyundriveOpen.DriveId == "" {
		d.AliyundriveOpen.DriveId = driveResp.DefaultDriveID
	}
	t.Logf("DriveId: %s", d.AliyundriveOpen.DriveId)

	// Initialize p115Client (needed for uploadTo115)
	p115Client, err := NewSync115Client(d.Open115Cookie)
	if err != nil {
		t.Fatalf("NewSync115Client failed: %v", err)
	}
	d.p115Client = p115Client
	defer d.p115Client.Drop()

	files, err := d.walkFilesRecursively(context.Background(), d.AliyundriveOpen.Addition.RootFolderID)
	if err != nil {
		t.Fatalf("walkFilesRecursively failed: %v", err)
	}

	// Test Link() on a small file
	for _, f := range files {
		if f.IsDir() || f.GetSize() > 100*1024*1024 {
			continue
		}
		link, err := d.AliyundriveOpen.Link(context.Background(), f, model.LinkArgs{})
		if err != nil {
			t.Fatalf("Link failed for %s: %v", f.GetName(), err)
		}
		if link == nil || link.URL == "" {
			t.Fatalf("Link returned empty URL for %s", f.GetName())
		}
		t.Logf("✅ Link for %s: %s", f.GetName(), link.URL)
		break
	}
}

// uploadFileToAliyun generates a local temp file of given size (MB), computes SHA1,
// uploads to Aliyun root folder via Put, returns the uploaded object.
// Caller is responsible for deleting the Aliyun file.
func uploadFileToAliyun(t *testing.T, d *Aliyun115Sync, sizeMB int) model.Obj {
	data := make([]byte, sizeMB*1024*1024)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand.Read failed: %v", err)
	}
	sha1Bytes := sha1.Sum(data)
	sha1Str := hex.EncodeToString(sha1Bytes[:])

	// Write to temp file
	tmpFile, err := os.CreateTemp("", "sync_test_*.bin")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	if _, err := tmpFile.Write(data); err != nil {
		t.Fatalf("Write to temp file failed: %v", err)
	}
	tmpFile.Close()

	fi, _ := os.Stat(tmpFile.Name())
	name := fmt.Sprintf("sync_test_%dmb_%d.bin", sizeMB, time.Now().UnixNano())
	stream := &fileStreamerForTest{
		name:     name,
		size:     fi.Size(),
		sha1Str:  sha1Str,
		filePath: tmpFile.Name(),
	}

	t.Logf("Uploading %dMB file to Aliyun: %s, SHA1: %s", sizeMB, name, sha1Str)
	result, err := d.AliyundriveOpen.Put(context.Background(), &model.Object{ID: "root"}, stream, func(f float64) {})
	if err != nil {
		t.Fatalf("Aliyun Put failed: %v", err)
	}
	t.Logf("✅ Aliyun upload success: %s (id=%s, sha1=%s)", result.GetName(), result.GetID(), result.GetHash().GetHash(utils.SHA1))
	return result
}

// setupSyncDriver initializes Aliyun driver, p115Client, and returns both.
// Call deferred d.p115Client.Drop() after use.
func setupSyncDriver(t *testing.T) *Aliyun115Sync {
	d := &Aliyun115Sync{
		AliyundriveOpen: &aliyundrive_open.AliyundriveOpen{},
	}
	cookieData, err := os.ReadFile("/root/.openclaw/115_cookie.txt")
	if err != nil {
		t.Skipf("skip: no cookie file: %v", err)
	}
	d.Open115Cookie = strings.TrimSpace(string(cookieData))
	d.Addition.SyncInterval = 0
	d.AliyundriveOpen.Addition.RefreshToken = aliyunRefreshToken
	d.AliyundriveOpen.Addition.APIAddress = aliyunAPIAddress
	d.AliyundriveOpen.Addition.RootFolderID = "root"
	d.AliyundriveOpen.Addition.UseOnlineAPI = true

	var tokenResp struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
		ErrorMessage string `json:"text"`
	}
	_, err = base.RestyClient.R().
		SetResult(&tokenResp).
		SetQueryParams(map[string]string{
			"refresh_ui":  d.AliyundriveOpen.Addition.RefreshToken,
			"server_use":  "true",
			"driver_txt":  "alicloud_qr",
		}).
		Get(aliyunAPIAddress)
	if err != nil {
		t.Fatalf("token refresh failed: %v", err)
	}
	d.AliyundriveOpen.Addition.AccessToken = tokenResp.AccessToken
	d.AliyundriveOpen.InitLimiter()

	var driveResp struct {
		ResourceDriveID string `json:"resource_drive_id"`
		DefaultDriveID  string `json:"default_drive_id"`
	}
	_, err = base.RestyClient.R().
		SetHeader("Authorization", "Bearer "+tokenResp.AccessToken).
		SetResult(&driveResp).
		SetBody(map[string]string{"resource_drive_id": "", "default_drive_id": ""}).
		Post("https://openapi.alipan.com/adrive/v1.0/user/getDriveInfo")
	if err != nil {
		t.Fatalf("getDriveInfo failed: %v", err)
	}
	d.AliyundriveOpen.DriveId = driveResp.ResourceDriveID
	if d.AliyundriveOpen.DriveId == "" {
		d.AliyundriveOpen.DriveId = driveResp.DefaultDriveID
	}
	t.Logf("DriveId: %s", d.AliyundriveOpen.DriveId)

	p115Client, err := NewSync115Client(d.Open115Cookie)
	if err != nil {
		t.Fatalf("NewSync115Client failed: %v", err)
	}
	d.p115Client = p115Client

	return d
}

func TestSyncUploadNew1MBTo115(t *testing.T) {
	d := setupSyncDriver(t)
	defer d.p115Client.Drop()

	// Step 1: Upload new 1MB file to Aliyun
	aliyunFile := uploadFileToAliyun(t, d, 1)
	defer func() {
		if err := d.AliyundriveOpen.Remove(context.Background(), aliyunFile); err != nil {
			t.Logf("⚠️  Aliyun Remove warning: %v", err)
		} else {
			t.Logf("✅ Cleaned Aliyun file: %s", aliyunFile.GetName())
		}
	}()

	// Step 2: Get Aliyun link
	link, err := d.AliyundriveOpen.Link(context.Background(), aliyunFile, model.LinkArgs{})
	if err != nil || link == nil || link.URL == "" {
		t.Fatalf("Aliyun Link failed: %v", err)
	}
	t.Logf("✅ Aliyun link: %s", link.URL)

	// Step 3: Verify downloaded content SHA1 matches Aliyun file
	sha1Str := aliyunFile.GetHash().GetHash(utils.SHA1)
	t.Logf("[1MB] Verifying downloaded content SHA1 (expected: %s)...", sha1Str)
	tmpCheck, err := os.CreateTemp(os.TempDir(), "sync_sha1_check-*")
	if err != nil {
		t.Fatalf("CreateTemp for SHA1 check failed: %v", err)
	}
	tmpCheckName := tmpCheck.Name()
	defer os.Remove(tmpCheckName)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, link.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download for SHA1 check failed: %v", err)
	}
	_, err = io.Copy(tmpCheck, resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("copy for SHA1 check failed: %v", err)
	}
	tmpCheck.Close()

	checkFile, _ := os.Open(tmpCheckName)
	h := sha1.New()
	io.Copy(h, checkFile)
	checkFile.Close()
	downloadedSHA1 := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
	t.Logf("[1MB] Downloaded SHA1: %s", downloadedSHA1)
	if downloadedSHA1 != sha1Str {
		t.Fatalf("SHA1 mismatch! Expected %s, got %s", sha1Str, downloadedSHA1)
	}
	t.Logf("[1MB] SHA1 verification passed")

	// Step 4: Upload to 115 via urlFileStreamer
	stream := newUrlFileStreamer(aliyunFile.GetName(), aliyunFile.GetSize(), sha1Str, link.URL)

	start := time.Now()
	result, err := d.p115Client.uploadTo115(context.Background(), stream, sha1Str)
	if err != nil {
		t.Fatalf("uploadTo115 failed: %v", err)
	}
	duration := time.Since(start)
	t.Logf("✅ 115 upload success: %s (id=%s, size=%d) in %v", result.GetName(), result.GetID(), result.GetSize(), duration)

	// Step 5: Cleanup 115
	if err := d.p115Client.removeFrom115(context.Background(), result); err != nil {
		t.Logf("⚠️  115 Remove warning: %v", err)
	} else {
		t.Logf("✅ Cleaned 115 file: %s", result.GetName())
	}
}

func TestSyncUploadNew11MBTo115(t *testing.T) {
	d := setupSyncDriver(t)
	defer d.p115Client.Drop()

	// Step 1: Upload new 11MB file to Aliyun (triggers multipart upload path)
	aliyunFile := uploadFileToAliyun(t, d, 11)
	defer func() {
		if err := d.AliyundriveOpen.Remove(context.Background(), aliyunFile); err != nil {
			t.Logf("⚠️  Aliyun Remove warning: %v", err)
		} else {
			t.Logf("✅ Cleaned Aliyun file: %s", aliyunFile.GetName())
		}
	}()

	// Step 2: Get Aliyun link
	link, err := d.AliyundriveOpen.Link(context.Background(), aliyunFile, model.LinkArgs{})
	if err != nil || link == nil || link.URL == "" {
		t.Fatalf("Aliyun Link failed: %v", err)
	}
	t.Logf("✅ Aliyun link: %s", link.URL)

	// Step 3: Upload to 115 via urlFileStreamer
	sha1Str := aliyunFile.GetHash().GetHash(utils.SHA1)
	stream := newUrlFileStreamer(aliyunFile.GetName(), aliyunFile.GetSize(), sha1Str, link.URL)

	start := time.Now()
	result, err := d.p115Client.uploadTo115(context.Background(), stream, sha1Str)
	if err != nil {
		t.Fatalf("uploadTo115 failed: %v", err)
	}
	duration := time.Since(start)
	t.Logf("✅ 115 upload success: %s (id=%s, size=%d) in %v", result.GetName(), result.GetID(), result.GetSize(), duration)

	// Step 4: Cleanup 115
	if err := d.p115Client.removeFrom115(context.Background(), result); err != nil {
		t.Logf("⚠️  115 Remove warning: %v", err)
	} else {
		t.Logf("✅ Cleaned 115 file: %s", result.GetName())
	}
}

func TestSyncDoRegister(t *testing.T) {
	d := &Aliyun115Sync{
		AliyundriveOpen: &aliyundrive_open.AliyundriveOpen{},
	}
	d.Addition.SyncInterval = 0
	cookieData, err := os.ReadFile("/root/.openclaw/115_cookie.txt")
	if err != nil {
		t.Skipf("skip: no cookie file: %v", err)
	}
	d.Open115Cookie = strings.TrimSpace(string(cookieData))
	d.AliyundriveOpen.Addition.RefreshToken = aliyunRefreshToken
	d.AliyundriveOpen.Addition.APIAddress = aliyunAPIAddress
	d.AliyundriveOpen.Addition.RootFolderID = "root"
	d.AliyundriveOpen.Addition.UseOnlineAPI = true

	// Manually refresh token to avoid MustSaveDriverStorage
	var tokenResp struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
		ErrorMessage string `json:"text"`
	}
	_, err = base.RestyClient.R().
		SetResult(&tokenResp).
		SetQueryParams(map[string]string{
			"refresh_ui":  d.AliyundriveOpen.Addition.RefreshToken,
			"server_use":  "true",
			"driver_txt":  "alicloud_qr",
		}).
		Get(aliyunAPIAddress)
	if err != nil {
		t.Fatalf("token refresh failed: %v", err)
	}
	if tokenResp.AccessToken == "" {
		t.Fatalf("token refresh returned empty access token: %s", tokenResp.ErrorMessage)
	}
	d.AliyundriveOpen.Addition.AccessToken = tokenResp.AccessToken
	d.AliyundriveOpen.InitLimiter()

	// Get drive info
	var driveResp struct {
		ResourceDriveID string `json:"resource_drive_id"`
		DefaultDriveID  string `json:"default_drive_id"`
	}
	_, err = base.RestyClient.R().
		SetHeader("Authorization", "Bearer "+tokenResp.AccessToken).
		SetResult(&driveResp).
		SetBody(map[string]string{"resource_drive_id": "", "default_drive_id": ""}).
		Post("https://openapi.alipan.com/adrive/v1.0/user/getDriveInfo")
	if err != nil {
		t.Fatalf("getDriveInfo failed: %v", err)
	}
	d.AliyundriveOpen.DriveId = driveResp.ResourceDriveID
	if d.AliyundriveOpen.DriveId == "" {
		d.AliyundriveOpen.DriveId = driveResp.DefaultDriveID
	}
	t.Logf("DriveId: %s", d.AliyundriveOpen.DriveId)

	// Initialize p115Client (needed for uploadTo115)
	p115Client, err := NewSync115Client(d.Open115Cookie)
	if err != nil {
		t.Fatalf("NewSync115Client failed: %v", err)
	}
	d.p115Client = p115Client
	defer d.p115Client.Drop()

	files, err := d.walkFilesRecursively(context.Background(), d.AliyundriveOpen.Addition.RootFolderID)
	if err != nil {
		t.Fatalf("walkFilesRecursively failed: %v", err)
	}

	// Pick a file to test the full register flow
	var smallFile model.Obj
	for _, f := range files {
		if !f.IsDir() && f.GetSize() > 0 {
			smallFile = f
			break
		}
	}
	if smallFile == nil {
		t.Fatal("no file found for upload test")
	}
	t.Logf("Selected file: %s (%d bytes, sha1=%s)", smallFile.GetName(), smallFile.GetSize(), smallFile.GetHash().GetHash(utils.SHA1))

	// Full flow: get link, create streamer, upload to 115, delete
	link, err := d.AliyundriveOpen.Link(context.Background(), smallFile, model.LinkArgs{})
	if err != nil || link == nil || link.URL == "" {
		t.Fatalf("Link failed: %v", err)
	}
	sha1Str := smallFile.GetHash().GetHash(utils.SHA1)
	stream := newUrlFileStreamer(smallFile.GetName(), smallFile.GetSize(), sha1Str, link.URL)

	result, err := d.p115Client.uploadTo115(context.Background(), stream, sha1Str)
	if err != nil {
		t.Fatalf("uploadTo115 failed: %v", err)
	}
	t.Logf("✅ Uploaded: %s (file_id=%s)", result.GetName(), result.GetID())

	delErr := d.p115Client.removeFrom115(context.Background(), result)
	if delErr != nil {
		t.Logf("⚠️  removeFrom115 warning: %v", delErr)
	} else {
		t.Logf("✅ Deleted: %s", result.GetName())
	}
}
