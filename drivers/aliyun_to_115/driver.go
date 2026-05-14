package aliyun_to_115

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	_115_share "github.com/OpenListTeam/OpenList/v4/drivers/115_share"
	aliyundrive_open "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_open"
	aliyundrive_share2open "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_share2open"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// aliyunStorage unifies AliyundriveOpen, AliyundriveShare2Open and 115Share for sync.
type aliyunStorage interface {
	List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error)
	Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error)
	GetRootId() string
	GetStorage() *model.Storage
}

type AliyunTo115 struct {
	p115 _115.Pan115
	Addition
	p115Client  *sync115Client
	syncLoopMu  sync.Mutex
	syncRunning bool
	userInt bool
	syncCacheDB *sql.DB
	syncedCache  map[string]bool // SHA1 → true
	basePath     string
}

func (d *AliyunTo115) Config() driver.Config { return config }
func (d *AliyunTo115) GetAddition() driver.Additional { return &d.Addition }

func (d *AliyunTo115) Init(ctx context.Context) error {
	if d.Open115Cookie == "" {
		return errors.New("open115_cookie is required")
	}

	// 初始化内部驱动参数
	d.p115.Addition.Cookie = d.Open115Cookie
	d.p115.Addition.QRCodeToken = d.QRCodeToken
	d.p115.Addition.QRCodeSource = d.QRCodeSource
	d.p115.Addition.PageSize = d.PageSize
	d.p115.Addition.LimitRate = d.LimitRate
	d.p115.Addition.RootFolderID = d.RootFolderID
	if err := d.p115.Init(ctx); err != nil {
		return err
	}

	// 1. RootFolderID == "auto" 逻辑优化
	if d.RootFolderID == "auto" {
		const syncFolderName = "小雅同步"
		objs, err := d.p115.List(ctx, &model.Object{ID: "0"}, model.ListArgs{})
		if err != nil {
			return fmt.Errorf("list root folder failed: %w", err)
		}

		var targetID string
		for _, obj := range objs {
			if obj.IsDir() && obj.GetName() == syncFolderName {
				targetID = obj.GetID()
				break
			}
		}

		if targetID == "" {
			newDir, err := d.p115.MakeDir(ctx, &model.Object{ID: "0"}, syncFolderName)
			if err != nil {
				return fmt.Errorf("auto create sync folder failed: %w", err)
			}
			targetID = newDir.GetID()
			fmt.Printf("[aliyun_to_115] auto created sync folder: %s (%s)\n", syncFolderName, targetID)
		} else {
			fmt.Printf("[aliyun_to_115] auto sync folder found: %s (%s)\n", syncFolderName, targetID)
		}

		d.RootFolderID = targetID
		d.p115.Addition.RootFolderID = targetID
		op.MustSaveDriverStorage(d)
	}

	// 2. 初始化同步客户端
	p115Client, err := newSync115Client(d.Open115Cookie)
	if err != nil {
		return err
	}
	d.p115Client = p115Client
	d.syncedCache = make(map[string]bool)

	// 打开 work.db（SQLite），与 strm_tasks 表同一库
	d.basePath, _ = os.Getwd()
	workDBPath := filepath.Join(d.basePath, "data", "work.db")
	if err := os.MkdirAll(filepath.Join(d.basePath, "data"), 0755); err != nil {
		return fmt.Errorf("create data dir failed: %w", err)
	}
	d.syncCacheDB, err = sql.Open("sqlite3", workDBPath)
	if err != nil {
		return fmt.Errorf("open work.db failed: %w", err)
	}
	if _, err := d.syncCacheDB.Exec(`CREATE TABLE IF NOT EXISTS aliyun_sync_cache (cache_key TEXT PRIMARY KEY, synced_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return fmt.Errorf("create cache table failed: %w", err)
	}

	rows, err := d.syncCacheDB.Query("SELECT cache_key FROM aliyun_sync_cache")
	if err != nil {
		fmt.Printf("[aliyun_to_115] load cache error: %v\n", err)
	} else {
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil {
				d.syncedCache[k] = true
			}
		}
		rows.Close()
	}
	if len(d.syncedCache) > 0 {
		fmt.Printf("[aliyun_to_115] 从work.db加载 %d 条同步记录\n", len(d.syncedCache))
	}

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
	// Wait for all storages in DB to finish initialization (up to 5 minutes)
	start := time.Now()
	for {
		dbStorages, _, err := db.GetStorages(1, 9999)
		if err == nil && len(op.GetAllStorages()) >= len(dbStorages) {
			fmt.Print("\r[aliyun_to_115] Waiting storages init: all ready\n")
			break
		}
		if time.Since(start) > 5*time.Minute {
			fmt.Printf("\r[aliyun_to_115] Waiting storages init timeout: %d/%d\n",
				len(op.GetAllStorages()), len(dbStorages))
			break
		}
		fmt.Printf("\r[aliyun_to_115] Waiting storages init: %d/%d ready...",
				len(op.GetAllStorages()), len(dbStorages))
		time.Sleep(2 * time.Second)
	}

	var storages []aliyunStorage
	allStorages := op.GetAllStorages()
	for _, s := range allStorages {
		switch v := s.(type) {
		case *aliyundrive_open.AliyundriveOpen:
			storages = append(storages, v)
		case *aliyundrive_share2open.AliyundriveShare2Open:
			storages = append(storages, v)
		case *_115_share.Pan115Share:
			storages = append(storages, v)
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

// saveSyncedCache 持久化 cache key 到数据库
func (d *AliyunTo115) saveSyncedCache(cacheKey string) {
	_, err := d.syncCacheDB.Exec("INSERT OR IGNORE INTO aliyun_sync_cache(cache_key, synced_at) VALUES (?, ?)", cacheKey, time.Now())
	if err != nil {
		fmt.Printf("[aliyun_to_115] 持久化 cache key 失败 [%s]: %v\n", cacheKey, err)
	}
}

var _ driver.Driver = (*AliyunTo115)(nil)