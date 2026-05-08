package _115_share

import (
	"context"
	"fmt"
	"time"

	driver115 "github.com/xiaoyaliu00/115driver/pkg/driver"
	//driver115 "github.com/xiaoyaliu00/115driver/pkg/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"golang.org/x/time/rate"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/drivers/base"
)

type Pan115Share struct {
	model.Storage
	Addition
	client  *driver115.Pan115Client
	limiter *rate.Limiter
	pan115LoginStatus   bool
	DownloadUrl_dict map[string]string
	cron        *cron.Cron
}

func (d *Pan115Share) Config() driver.Config {
	return config
}

func (d *Pan115Share) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Pan115Share) Init(ctx context.Context) error {
	if d.LimitRate > 0 {
		d.limiter = rate.NewLimiter(rate.Limit(d.LimitRate), 1)
	}
	var downloadurlmap map[string]string
	downloadurlmap = make(map[string]string)
	d.DownloadUrl_dict = downloadurlmap

    d.cron = cron.NewCron(time.Second * 10)
    d.cron.Do(func() {
                //fmt.Println(time.Now().Format("01-02-2006 15:04:05")," 清空115缓存下载链接: ", d.MountPath) //d.ShareId) //d.MyAliDriveId)
                d.DownloadUrl_dict = make(map[string]string)
    })

	d.pan115LoginStatus = false
	//return d.login()
	return nil
}

func (d *Pan115Share) WaitLimit(ctx context.Context) error {
	if d.limiter != nil {
		return d.limiter.Wait(ctx)
	}
	return nil
}

func (d *Pan115Share) Drop(ctx context.Context) error {
	return nil
}

func (d *Pan115Share) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if !d.pan115LoginStatus {
        	if err:= d.login(); err == nil {
            		d.pan115LoginStatus = true
        	}
    	}

	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}

	files := make([]driver115.ShareFile, 0)
	fileResp, err := d.client.GetShareSnap(d.ShareCode, d.ReceiveCode, dir.GetID(), driver115.QueryLimit(int(conf.Conf.Page_Size)))
	if err != nil {
        	if errs.IsObjectNotFound(err) {
            		return nil, nil
        	}
		return nil, err
	}
	files = append(files, fileResp.Data.List...)
	total := fileResp.Data.Count
	count := len(fileResp.Data.List)
	for total > count {
		time.Sleep(500 * time.Millisecond)
		fileResp, err := d.client.GetShareSnap(
			d.ShareCode, d.ReceiveCode, dir.GetID(),
			driver115.QueryLimit(int(conf.Conf.Page_Size)), driver115.QueryOffset(count),
		)
		if len(fileResp.Data.List) == 0 {
			break
		}
		if err != nil {
			if errs.IsObjectNotFound(err) {
           		return nil, nil
       		}
			return nil, err
		}
		files = append(files, fileResp.Data.List...)
		count += len(fileResp.Data.List)
	}
	return utils.SliceConvert(files, transFunc)
}

func (d *Pan115Share) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	base.V115novip = 0 // 解除非会员下载次数限制
	if !d.pan115LoginStatus {
		if err := d.login(); err == nil {
			d.pan115LoginStatus = true
		}
	}

	file_id := file.GetID()
	file_name := file.GetName()
    existed_download_url, ok := d.DownloadUrl_dict[file_id]
    if ok {
        fmt.Println(time.Now().Format("01-02-2006 15:04:05"),"115下载链接已存在: ",file_name)
		return &model.Link{URL: existed_download_url}, nil
    }

	if err := d.WaitLimit(ctx); err != nil {
		fmt.Println(time.Now().Format("01-02-2006 15:04:05"),file_name,"获取115直链超时: ",err)
		return nil, err
	}

	var err_msg = "非115会员24小时内不能下载超过%d次，已超出"
	if time.Since(base.V115lasttime) > base.V115countwindow {
        base.V115count = 0
        base.V115lasttime = time.Now()
    }
	
	if base.V115count >= conf.Conf.Max_donwload_count {
			if base.V115novip == 1 {
				fmt.Printf("非115会员24小时内不能下载超过%d次，已超出\n", conf.Conf.Max_donwload_count)
				err := fmt.Errorf(err_msg, conf.Conf.Max_donwload_count)
				return nil, err
			}
	} else {
            if base.V115novip == 1 {
				remaincount := conf.Conf.Max_donwload_count - base.V115count
				fmt.Printf("你非115会员，本日还剩%d次下载\n", remaincount)
			}
	}	
	
	base.V115count++

	downloadInfo, err := d.client.DownloadByShareCode(d.ShareCode, d.ReceiveCode, file.GetID())
	if err != nil {
		time.Sleep(500 * time.Millisecond)
		downloadInfo2, err := d.client.DownloadByShareCode(d.ShareCode, d.ReceiveCode, file.GetID())
		if err != nil {
			fmt.Println(time.Now().Format("01-02-2006 15:04:05"),file_name,"获取115直链发生错误: ",err)
			return nil, err
		} else {
			downloadInfo = downloadInfo2
		}
	}

/*
	// 使用自己帐号获取直链
	var userAgent = args.Header.Get("User-Agent")
	downloadInfo2, err := d.client.DownloadWithUA(downloadInfo.FileID, userAgent)		

    if err == nil {
		d.DownloadUrl_dict[file_id] = downloadInfo2.Url.Url
		fmt.Println(time.Now().Format("01-02-2006 15:04:05"),file_name,"获取115 分享直链: ",downloadInfo2.Url.Url)
        link := &model.Link{
            URL:    downloadInfo2.Url.Url,
            Header: downloadInfo2.Header,
        }
        return link, nil
    }
*/
	d.DownloadUrl_dict[file_id] = downloadInfo.URL.URL	
	fmt.Println(time.Now().Format("01-02-2006 15:04:05"),file_name,"获取115分享直链: ",downloadInfo.URL.URL)
	return &model.Link{URL: downloadInfo.URL.URL}, nil
}

func (d *Pan115Share) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	return errs.NotSupport
}

func (d *Pan115Share) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errs.NotSupport
}

func (d *Pan115Share) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	return errs.NotSupport
}

func (d *Pan115Share) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errs.NotSupport
}

func (d *Pan115Share) Remove(ctx context.Context, obj model.Obj) error {
	return errs.NotSupport
}

func (d *Pan115Share) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	return errs.NotSupport
}

var _ driver.Driver = (*Pan115Share)(nil)
