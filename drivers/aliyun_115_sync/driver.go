package aliyun_115_sync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	aliyundrive_open "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_open"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type Aliyun115Sync struct {
	AliyundriveOpen *aliyundrive_open.AliyundriveOpen // explicit — not embed, to avoid Init ordering issues
	Addition                                                             // own: Open115Cookie, SyncInterval
	p115Client   *sync115Client
	syncMu       sync.Mutex
	syncRunning  bool
	syncedCache  map[string]bool // persistent record of already-uploaded SHA1s across sync cycles
}

func (d *Aliyun115Sync) Config() driver.Config  { return config }
func (d *Aliyun115Sync) GetAddition() driver.Additional { return &d.Addition }

func (d *Aliyun115Sync) Init(ctx context.Context) error {
	// Lazy-init AliyundriveOpen if called before SetStorage
	if d.AliyundriveOpen == nil {
		d.AliyundriveOpen = &aliyundrive_open.AliyundriveOpen{}
	}

	// Sync Addition fields → AliyundriveOpen.Addition so Init sees the right config
	d.AliyundriveOpen.Addition.DriveType = d.DriveType
	d.AliyundriveOpen.Addition.RootFolderID = d.RootFolderID
	d.AliyundriveOpen.Addition.RefreshToken = d.RefreshToken
	d.AliyundriveOpen.Addition.OrderBy = d.OrderBy
	d.AliyundriveOpen.Addition.OrderDirection = d.OrderDirection
	d.AliyundriveOpen.Addition.UseOnlineAPI = d.UseOnlineAPI
	d.AliyundriveOpen.Addition.AlipanType = d.AlipanType
	d.AliyundriveOpen.Addition.APIAddress = d.APIAddress
	d.AliyundriveOpen.Addition.ClientID = d.ClientID
	d.AliyundriveOpen.Addition.ClientSecret = d.ClientSecret
	d.AliyundriveOpen.Addition.RemoveWay = d.RemoveWay
	d.AliyundriveOpen.Addition.RapidUpload = d.RapidUpload
	d.AliyundriveOpen.Addition.InternalUpload = d.InternalUpload
	d.AliyundriveOpen.Addition.LIVPDownloadFormat = d.LIVPDownloadFormat
	d.AliyundriveOpen.Addition.AccessToken = d.AccessToken

	if err := d.AliyundriveOpen.Init(ctx); err != nil {
		return err
	}

	if d.Open115Cookie == "" {
		return errors.New("open115_cookie is required")
	}
	client, err := NewSync115Client(d.Open115Cookie)
	if err != nil {
		return err
	}
	d.p115Client = client
	go d.doSyncLoop()
	return nil
}

func (d *Aliyun115Sync) Drop(ctx context.Context) error {
	if d.p115Client != nil {
		d.p115Client.Drop()
	}
	return d.AliyundriveOpen.Drop(ctx)
}

// P115Client exposes the 115 client for testing.
func (d *Aliyun115Sync) P115Client() *sync115Client {
	return d.p115Client
}

// RegisterFile uploads a file to 115 then deletes it — used to pre-register SHA1.
func (d *Aliyun115Sync) RegisterFile(ctx context.Context, file model.Obj) (err error) {
	link, err := d.AliyundriveOpen.Link(ctx, file, model.LinkArgs{})
	if err != nil || link == nil || link.URL == "" {
		return err
	}
	hashInfo := file.GetHash()
	sha1Str := hashInfo.GetHash(utils.SHA1)
	stream := newUrlFileStreamer(file.GetName(), file.GetSize(), sha1Str, link.URL)
	result, uploadErr := d.p115Client.uploadTo115(ctx, stream, sha1Str)
	if uploadErr != nil || result == nil {
		return uploadErr
	}
	return d.p115Client.removeFrom115(ctx, result)
}

// GetStorage implements driver.Driver — forwarded to AliyundriveOpen
func (d *Aliyun115Sync) GetStorage() *model.Storage {
	return d.AliyundriveOpen.GetStorage()
}

// SetStorage implements driver.Driver — AliyundriveOpen may be nil if called before Init
func (d *Aliyun115Sync) SetStorage(storage model.Storage) {
	if d.AliyundriveOpen == nil {
		d.AliyundriveOpen = &aliyundrive_open.AliyundriveOpen{}
	}
	d.AliyundriveOpen.SetStorage(storage)
}

// GetRoot implements driver.Reader
func (d *Aliyun115Sync) GetRoot(ctx context.Context) (model.Obj, error) {
	return d.AliyundriveOpen.GetRoot(ctx)
}

// List implements driver.Reader
func (d *Aliyun115Sync) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return d.AliyundriveOpen.List(ctx, dir, args)
}

// MakeDir implements driver.Reader
func (d *Aliyun115Sync) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	return d.AliyundriveOpen.MakeDir(ctx, parentDir, dirName)
}

// Move implements driver.Reader
func (d *Aliyun115Sync) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return d.AliyundriveOpen.Move(ctx, srcObj, dstDir)
}

// Rename implements driver.Reader
func (d *Aliyun115Sync) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	return d.AliyundriveOpen.Rename(ctx, srcObj, newName)
}

// Copy implements driver.Reader
func (d *Aliyun115Sync) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return d.AliyundriveOpen.Copy(ctx, srcObj, dstDir)
}

// Remove implements driver.Reader
func (d *Aliyun115Sync) Remove(ctx context.Context, obj model.Obj) error {
	return d.AliyundriveOpen.Remove(ctx, obj)
}

// Put implements driver.Reader
func (d *Aliyun115Sync) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	return d.AliyundriveOpen.Put(ctx, dstDir, stream, up)
}

// GetDetails implements driver.Reader
func (d *Aliyun115Sync) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	return d.AliyundriveOpen.GetDetails(ctx)
}

// Link implements driver.Reader
func (d *Aliyun115Sync) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return d.AliyundriveOpen.Link(ctx, file, args)
}

// Other implements driver.Other
func (d *Aliyun115Sync) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	return d.AliyundriveOpen.Other(ctx, args)
}

func (d *Aliyun115Sync) doSyncLoop() {
	interval := time.Duration(d.SyncInterval) * time.Second
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		d.doSync()
	}
}

func (d *Aliyun115Sync) doSync() {
	d.syncMu.Lock()
	if d.syncRunning {
		d.syncMu.Unlock()
		return
	}
	d.syncRunning = true
	d.syncMu.Unlock()

	defer func() {
		d.syncMu.Lock()
		d.syncRunning = false
		d.syncMu.Unlock()
	}()

	ctx := context.Background()

	// Lazy-init persistent cache
	if d.syncedCache == nil {
		d.syncedCache = make(map[string]bool)
	}

	// Stats for this cycle
	var total, skipped, noLink, failed, synced, rapid, normal int64

	// Walk all Aliyun files recursively using walkFilesRecursively
	allFiles, err := d.walkFilesRecursively(ctx, d.AliyundriveOpen.RootFolderID)
	if err != nil {
		return
	}
	total = int64(len(allFiles))

	for _, file := range allFiles {
		hashInfo := file.GetHash()
		sha1Str := hashInfo.GetHash(utils.SHA1)
		if sha1Str == "" {
			continue
		}
		d.syncMu.Lock()
		if d.syncedCache[sha1Str] {
			d.syncMu.Unlock()
			skipped++
			continue
		}
		d.syncMu.Unlock()

		// Get download link via AliyundriveOpen's Link method
		link, err := d.AliyundriveOpen.Link(ctx, file, model.LinkArgs{})
		if err != nil || link == nil || link.URL == "" {
			fmt.Printf("[sync] ⚠️ no download link: %s (sha1=%s): %v\n", file.GetPath(), sha1Str, err)
			noLink++
			continue // no cache — will retry next cycle
		}

		// Create stream from URL
		stream := &urlFileStreamer{
			name:    file.GetName(),
			path:    file.GetPath(),
			size:    file.GetSize(),
			sha1Str: sha1Str,
			url:     link.URL,
		}

		// Upload to 115
		start := time.Now()
		result, uploadErr := d.p115Client.uploadTo115(ctx, stream, sha1Str)
		elapsed := time.Since(start)
		if uploadErr != nil || result == nil {
			fmt.Printf("[sync] ⚠️ upload failed: %s (sha1=%s): %v\n", file.GetPath(), sha1Str, uploadErr)
			failed++
			continue // no cache — will retry next cycle
		}

		if stream.rapidUpload {
			fmt.Printf("[sync] ⚡ 秒传成功: %s (sha1=%s, %s, %v)\n", file.GetPath(), sha1Str, formatSize(file.GetSize()), elapsed)
			rapid++
		} else {
			fmt.Printf("[sync] 📤 正常上传: %s (sha1=%s, %s, %v)\n", file.GetPath(), sha1Str, formatSize(file.GetSize()), elapsed)
			normal++
		}

		// Mark as synced and delete from 115
		d.syncMu.Lock()
		d.syncedCache[sha1Str] = true
		d.syncMu.Unlock()
		synced++
		_ = d.p115Client.removeFrom115(ctx, result)
	}

	fmt.Printf("[sync] ===== 本轮完成: 共%v个 / 跳过%v个 / 秒传%v个 / 正常%v个 / 失败%v个 / 无链接%v个 =====\n",
		total, skipped, rapid, normal, failed, noLink)
}

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	var exp int
	for floatSize := float64(size); floatSize >= unit; floatSize /= unit {
		exp++
	}
	div := float64(unit)
	for i := 0; i < exp-1; i++ {
		div *= unit
	}
	suffix := "BKMGTPE"[exp : exp+1]
	return fmt.Sprintf("%.1f %sB", float64(size)/div, suffix)
}
