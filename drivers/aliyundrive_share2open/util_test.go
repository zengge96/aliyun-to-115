package aliyundrive_share2open

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func uniqueTempDir(t *testing.T) string {
	tmpDir := fmt.Sprintf("/tmp/test-openlist-aliyun-%d", time.Now().UnixNano())
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	return tmpDir
}

func testSetup(t *testing.T) {
	tmpDir := uniqueTempDir(t)
	conf.Conf = conf.DefaultConfig(tmpDir)
	sqlDB, err := gorm.Open(sqlite.Open(tmpDir+"/data.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	db.Init(sqlDB)
	base.InitClient()
}

// readTestTokens reads aliyundrive credentials from local files.
// - RefreshToken (32-char): ~/.openclaw/aliyun_share_refresh_token.txt
// - RefreshTokenOpen (JWT):   ~/.openclaw/aliyun_refresh_token.txt
func readTestTokens(t *testing.T) (shareRefreshToken, openRefreshToken string) {
	data, err := os.ReadFile("/root/.openclaw/aliyun_share_refresh_token.txt")
	if err != nil {
		t.Fatalf("failed to read aliyun_share_refresh_token.txt: %v", err)
	}
	shareRefreshToken = string(data)

	data, err = os.ReadFile("/root/.openclaw/aliyun_refresh_token.txt")
	if err != nil {
		t.Fatalf("failed to read aliyun_refresh_token.txt: %v", err)
	}
	openRefreshToken = string(data)
	return
}

type testObj struct {
	id      string
	name    string
	size    int64
	isDir   bool
	modTime time.Time
}

func (t *testObj) GetID() string           { return t.id }
func (t *testObj) GetName() string         { return t.name }
func (t *testObj) GetSize() int64          { return t.size }
func (t *testObj) ModTime() time.Time      { return t.modTime }
func (t *testObj) CreateTime() time.Time   { return t.modTime }
func (t *testObj) IsDir() bool             { return t.isDir }
func (t *testObj) GetHash() utils.HashInfo { return utils.HashInfo{} }
func (t *testObj) GetPath() string         { return "" }

// TestInitAndList calls d.Init() then d.List() via the real driver.
// Requires network access to aliyundrive API and the proxy server.
func TestInitAndList(t *testing.T) {
	testSetup(t)
	ctx := context.Background()
	d := &AliyundriveShare2Open{}
	d.MountPath = "/test"
	shareToken, openToken := readTestTokens(t)
	d.RefreshToken = shareToken
	d.RefreshTokenOpen = openToken
	d.ShareId = "bqso6QJEA3f"
	d.UseOnlineAPI = true
	d.APIAddress = "https://api.oplist.org/alicloud/renewapi"

	err := d.Init(ctx)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Logf("Init OK — AccessToken: %s..., ShareToken: %s...", d.AccessToken, d.ShareToken)

	items, err := d.List(ctx, &testObj{id: "root", name: ""}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(items) == 0 {
		t.Error("expected at least 1 item")
	}
	for _, item := range items {
		t.Logf("  - %s (isDir=%v)", item.GetName(), item.IsDir())
	}
}

// findFile does BFS to find the first file under parent.
// Returns nil if no file found (only directories).
func findFile(d *AliyundriveShare2Open, ctx context.Context, parent model.Obj) model.Obj {
	queue := []model.Obj{parent}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		items, err := d.List(ctx, current, model.ListArgs{})
		if err != nil || len(items) == 0 {
			continue
		}
		for _, item := range items {
			if !item.IsDir() {
				return item
			}
			queue = append(queue, item)
		}
	}
	return nil
}

// TestLink calls d.Init(), finds a file via List, then calls d.Link().
// Requires network access to aliyundrive API and the proxy server.
func TestLink(t *testing.T) {
	testSetup(t)
	ctx := context.Background()
	d := &AliyundriveShare2Open{}
	d.MountPath = "/test"
	shareToken, openToken := readTestTokens(t)
	d.RefreshToken = shareToken
	d.RefreshTokenOpen = openToken
	d.ShareId = "bqso6QJEA3f"
	d.UseOnlineAPI = true
	d.APIAddress = "https://api.oplist.org/alicloud/renewapi"

	err := d.Init(ctx)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Logf("Init OK — MyAliDriveId=%s", d.MyAliDriveId)

	rootItems, err := d.List(ctx, &testObj{id: "root", name: ""}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(rootItems) == 0 {
		t.Fatal("expected at least 1 item from root List")
	}

	file := findFile(d, ctx, rootItems[0])
	if file == nil {
		t.Fatal("no file found in share (only directories)")
	}
	t.Logf("Linking: %s (id=%s, isDir=%v)", file.GetName(), file.GetID(), file.IsDir())
	d.TempTransferFolderID = "67ef8744bea8e8f2f7cf4156a107ae03a2745c30"

	link, err := d.Link(ctx, file, model.LinkArgs{})
	if err != nil {
		t.Fatalf("Link failed: %v", err)
	}
	t.Logf("Link URL: %s", link.URL)
	if link.URL == "" {
		t.Error("expected non-empty URL")
	}
	// Note: Copy2Myali requires user token with write permission.
	// The share-link token is read-only, so abnormal.png is expected
	// when the file hasn't been transferred yet.
	t.Logf("Link test done (Copy2Myali needs user token, not share token)")
}

// TestRefreshTokenOpen calls d.refreshTokenOpen() via the real driver.
// Requires network access to the proxy server.
func TestRefreshTokenOpen(t *testing.T) {
	testSetup(t)
	ctx := context.Background()
	d := &AliyundriveShare2Open{}
	d.MountPath = "/test"
	_, openToken := readTestTokens(t)
	d.RefreshTokenOpen = openToken
	d.UseOnlineAPI = true
	d.APIAddress = "https://api.oplist.org/alicloud/renewapi"

	err := d.refreshTokenOpen(ctx)
	if err != nil {
		t.Fatalf("refreshTokenOpen failed: %v", err)
	}
	t.Logf("refreshTokenOpen OK")
}