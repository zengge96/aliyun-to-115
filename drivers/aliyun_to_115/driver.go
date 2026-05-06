package aliyun_to_115

import (
	"context"
	"errors"
	"fmt"
	"sync"

	aliyundrive_open "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_open"
	aliyundrive_share2open "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_share2open"
	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// aliyunStorage unifies AliyundriveOpen and AliyundriveShare2Open for sync.
type aliyunStorage interface {
	List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error)
	Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error)
	GetRootId() string
	GetStorage() *model.Storage
}

type AliyunTo115 struct {
	// p115 provides List/Put/Link/etc. (Pan115 embeds model.Storage → satisfies SetStorage/GetStorage)
	p115 _115.Pan115
	// aliased addition lives as a named field, not embedded, to avoid shadowing p115.Addition
	Addition
	p115Client  *sync115Client
	syncLoopMu  sync.Mutex
	syncRunning bool
	syncedCache map[string]bool // SHA1 → true, persistent across sync cycles
}

func (d *AliyunTo115) Config() driver.Config { return config }
func (d *AliyunTo115) GetAddition() driver.Additional { return &d.Addition }

func (d *AliyunTo115) Init(ctx context.Context) error {
	// 1. Copy Addition params to p115, then init it for normal List/Put/etc.
	d.p115.Addition.Cookie = d.Open115Cookie
	d.p115.Addition.QRCodeToken = d.QRCodeToken
	d.p115.Addition.QRCodeSource = d.QRCodeSource
	d.p115.Addition.PageSize = d.PageSize
	d.p115.Addition.LimitRate = d.LimitRate
	d.p115.Addition.RootFolderID = d.RootFolderID
	if err := d.p115.Init(ctx); err != nil {
		return err
	}

	// 2. Init 115 upload client for sync task
	if d.Open115Cookie == "" {
		return errors.New("open115_cookie is required")
	}
	p115Client, err := newSync115Client(d.Open115Cookie)
	if err != nil {
		return err
	}
	d.p115Client = p115Client

	// 3. Init synced cache
	d.syncedCache = make(map[string]bool)

	// 4. Start background sync loop
	go d.doSyncLoop()
	return nil
}

func (d *AliyunTo115) Drop(ctx context.Context) error {
	if d.p115Client != nil {
		d.p115Client.Drop()
	}
	return d.p115.Drop(ctx)
}

// discoverAliyunStorages finds all AliyundriveOpen and AliyundriveShare2Open storages.
func (d *AliyunTo115) discoverAliyunStorages() []aliyunStorage {
	var storages []aliyunStorage
	allStorages := op.GetAllStorages()
	fmt.Printf("[aliyun_to_115] discoverAliyunStorages: 共注册%v个存储\n", len(allStorages))
	for i, s := range allStorages {
		mountPath := ""
		if s2 := s.GetStorage(); s2 != nil {
			mountPath = s2.MountPath
		}
		fmt.Printf("[aliyun_to_115] discoverAliyunStorages [%d]: 类型=%T mount=%s\n", i+1, s, mountPath)
		switch v := s.(type) {
		case *aliyundrive_open.AliyundriveOpen:
			fmt.Printf("[aliyun_to_115] discoverAliyunStorages [%d]: ✅ 匹配AliyundriveOpen\n", i+1)
			storages = append(storages, v)
		case *aliyundrive_share2open.AliyundriveShare2Open:
			fmt.Printf("[aliyun_to_115] discoverAliyunStorages [%d]: ✅ 匹配AliyundriveShare2Open\n", i+1)
			storages = append(storages, v)
		default:
			fmt.Printf("[aliyun_to_115] discoverAliyunStorages [%d]: ❌ 不匹配，跳过\n", i+1)
		}
	}
	fmt.Printf("[aliyun_to_115] discoverAliyunStorages: 最终匹配到%v个阿里云存储\n", len(storages))
	return storages
}

func (d *AliyunTo115) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return d.p115.List(ctx, dir, args)
}

// GetDetails delegates to p115.
func (d *AliyunTo115) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	return d.p115.GetDetails(ctx)
}

// Link delegates to p115.
func (d *AliyunTo115) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return d.p115.Link(ctx, file, args)
}

// Put delegates to p115 (user upload to 115).
func (d *AliyunTo115) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	return d.p115.Put(ctx, dstDir, stream, up)
}

// MakeDir delegates to p115.
func (d *AliyunTo115) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	return d.p115.MakeDir(ctx, parentDir, dirName)
}

// Move delegates to p115.
func (d *AliyunTo115) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return d.p115.Move(ctx, srcObj, dstDir)
}

// Rename delegates to p115.
func (d *AliyunTo115) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	return d.p115.Rename(ctx, srcObj, newName)
}

// Copy delegates to p115.
func (d *AliyunTo115) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return d.p115.Copy(ctx, srcObj, dstDir)
}

// Remove delegates to p115.
func (d *AliyunTo115) Remove(ctx context.Context, obj model.Obj) error {
	return d.p115.Remove(ctx, obj)
}

// Other exposes manual sync trigger via action "sync".
func (d *AliyunTo115) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	switch args.Method {
	case "sync":
		go d.doSync()
		return "sync started", nil
	}
	return nil, nil
}

// GetStorage implements driver.Driver (delegates to embedded p115).
func (d *AliyunTo115) GetStorage() *model.Storage {
	return d.p115.GetStorage()
}

// SetStorage implements driver.Driver (delegates to embedded p115).
func (d *AliyunTo115) SetStorage(s model.Storage) {
	d.p115.SetStorage(s)
}

var _ driver.Driver = (*AliyunTo115)(nil)
