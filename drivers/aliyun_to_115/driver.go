package aliyun_to_115

import (
	"context"
	"errors"
	"sync"

	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	aliyundrive_open "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_open"
	aliyundrive_share2open "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_share2open"
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
	// p115 provides List/Put/Link/etc.
	p115 _115.Pan115
	// Addition lives as a named field
	Addition
	p115Client  *sync115Client
	syncLoopMu  sync.Mutex
	syncRunning bool
	syncedCache map[string]bool // SHA1 → true
}

func (d *AliyunTo115) Config() driver.Config { return config }
func (d *AliyunTo115) GetAddition() driver.Additional { return &d.Addition }

func (d *AliyunTo115) Init(ctx context.Context) error {
	// 1. Copy Addition params to p115
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

func (d *AliyunTo115) discoverAliyunStorages() []aliyunStorage {
	var storages []aliyunStorage
	allStorages := op.GetAllStorages()
	for _, s := range allStorages {
		switch v := s.(type) {
		case *aliyundrive_open.AliyundriveOpen:
			storages = append(storages, v)
		case *aliyundrive_share2open.AliyundriveShare2Open:
			storages = append(storages, v)
		default:
		}
	}
	return storages
}

func (d *AliyunTo115) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return d.p115.List(ctx, dir, args)
}

func (d *AliyunTo115) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	return d.p115.GetDetails(ctx)
}

func (d *AliyunTo115) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return d.p115.Link(ctx, file, args)
}

func (d *AliyunTo115) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	return d.p115.Put(ctx, dstDir, stream, up)
}

func (d *AliyunTo115) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	return d.p115.MakeDir(ctx, parentDir, dirName)
}

func (d *AliyunTo115) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return d.p115.Move(ctx, srcObj, dstDir)
}

func (d *AliyunTo115) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	return d.p115.Rename(ctx, srcObj, newName)
}

func (d *AliyunTo115) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return d.p115.Copy(ctx, srcObj, dstDir)
}

func (d *AliyunTo115) Remove(ctx context.Context, obj model.Obj) error {
	return d.p115.Remove(ctx, obj)
}

func (d *AliyunTo115) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	switch args.Method {
	case "sync":
		go d.doSync()
		return "sync started", nil
	}
	return nil, nil
}

func (d *AliyunTo115) GetStorage() *model.Storage {
	return d.p115.GetStorage()
}

func (d *AliyunTo115) SetStorage(s model.Storage) {
	d.p115.SetStorage(s)
}

var _ driver.Driver = (*AliyunTo115)(nil)