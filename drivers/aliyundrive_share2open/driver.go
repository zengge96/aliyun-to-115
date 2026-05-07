package aliyundrive_share2open

import (
	"context"
	"net/http"
	"time"
	"fmt"
	"encoding/json"
	"github.com/tidwall/gjson"
	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type AliyundriveShare2Open struct {
	model.Storage
	Addition
	AccessToken string
	ShareToken  string
//	DriveId     string
	cron        *cron.Cron
	cron1	    *cron.Cron
	cron2	    *cron.Cron
	cron3	    *cron.Cron	
	base             string
    	MyAliDriveId     string
	backup_drive_id	 string
	resource_drive_id	string
	AccessTokenOpen  string
	CopyFiles        map[string]string
	DownloadUrl_dict map[string]string
	Hash_dict map[string]string
	FileID_Link		 map[string]string
}

func (d *AliyundriveShare2Open) Config() driver.Config {
	return config
}

func (d *AliyundriveShare2Open) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *AliyundriveShare2Open) Init(ctx context.Context) error {
	err := d.refreshToken()
	if err != nil {
		return err
	}
	err2 := d.getShareToken()
	if err2 != nil {
		return err2
	}
	err = d.refreshTokenOpen(ctx)
	if err != nil {
		return err
	}
	var siteMap map[string]string
    var downloadurlmap map[string]string
	var fileid_link map[string]string
    downloadurlmap = make(map[string]string)
	fileid_link = make(map[string]string)
	siteMap = make(map[string]string)
	d.CopyFiles = siteMap
	d.DownloadUrl_dict = downloadurlmap
	d.Hash_dict = make(map[string]string)
	d.FileID_Link = fileid_link

	//res, err := d.request("https://api.aliyundrive.com/v2/user/get", http.MethodPost, nil)
	res, err := d.requestOpen(ctx, "/adrive/v1.0/user/getDriveInfo", http.MethodPost, func(req *resty.Request){})
	if err != nil {
		return err
	}
	d.MyAliDriveId = utils.Json.Get(res, "default_drive_id").ToString()
	d.backup_drive_id = utils.Json.Get(res, "backup_drive_id").ToString()
	d.resource_drive_id = utils.Json.Get(res, "resource_drive_id").ToString()
	if d.resource_drive_id != "" {
		d.MyAliDriveId = d.resource_drive_id 
		//fmt.Println("资源库ID:", d.resource_drive_id)
	}

	d.cron = cron.NewCron(time.Hour * 2)
	d.cron.Do(func() {
		err := d.refreshToken()
		if err != nil {
			log.Errorf("%+v", err)
		}
	})

	d.cron3 = cron.NewCron(time.Hour * time.Duration(24))
	d.cron3.Do(func() {
		if d.Autoremove {
			var siteMap map[string]string
			siteMap = make(map[string]string)
			d.CopyFiles = siteMap
			err := d.Purge_temp_folder(ctx)
			if err != nil {
				fmt.Printf("\033[0;32m%s%s%s\033[0m\n",time.Now().Format("01-02-2006 15:04:05"),"获取转存文件夹错误",err)
			}
		}
	})

    d.cron2 = cron.NewCron(time.Minute * 13)
    d.cron2.Do(func() {
	if len(d.FileID_Link) > 0 {
		fmt.Println(time.Now().Format("01-02-2006 15:04:05")," 清空缓存下载链接: ", d.MountPath) //d.ShareId) //d.MyAliDriveId)
		d.DownloadUrl_dict = make(map[string]string)
		d.Hash_dict = make(map[string]string)
		d.FileID_Link = make(map[string]string)
		d.CopyFiles = make(map[string]string)
	}
    })
	return nil
}

func (d *AliyundriveShare2Open) Drop(ctx context.Context) error {
	if d.cron != nil { d.cron.Stop() }	
	if d.cron2 != nil { d.cron2.Stop() }		
	if d.cron3 != nil { d.cron3.Stop() }		
	d.MyAliDriveId = ""
	return nil
}

func (d *AliyundriveShare2Open) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
    count := 0
	for {
		files, err := d.getFiles(dir.GetID())
		if err != nil {
			if count > 3 {
				fmt.Println("获取目录列表失败，结束重试",d.MountPath,": ",dir.GetName())
				return nil, err
			}
			count += 1
			fmt.Println("获取目录列表失败，重试第",count,"次 ",d.MountPath,": ",dir.GetName())
			time.Sleep(2 * time.Second)
		}	

		if err == nil {
			return utils.SliceConvert(files, func(src File) (model.Obj, error) {
				obj := fileToObj(src);
				return obj, nil
			})
		}	
	}
}

func (d *AliyundriveShare2Open) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	file_id :=  file.GetID()
	file_name := file.GetName()

	DownloadUrl, ok := d.FileID_Link[file_id]
	if ok {
		return &model.Link{
			Header: http.Header{
				"Referer": []string{"https://www.aliyundrive.com/"},
			},
			URL: DownloadUrl,
		}, nil
	}
	new_file_id, error := d.Copy2Myali( ctx , d.MyAliDriveId, file_id, file_name)
	if error != nil || new_file_id == "" {
		return &model.Link{
			Header: http.Header{
				"Referer": []string{"https://www.aliyundrive.com/"},
			},
			URL: "http://img.xiaoya.pro/abnormal.png",
		}, nil
	} 

	time.Sleep(2 * 1000 * time.Millisecond)
	DownloadUrl, err := d.GetmyLink(ctx, new_file_id, file_id, file_name)
	if err != nil {
		fmt.Println(time.Now().Format("01-02-2006 15:04:05"),"获取转存后的直链失败！！！",err)
	}
	if err == nil {
		d.FileID_Link[file_id] = DownloadUrl
	}	
	return &model.Link{
		Header: http.Header{
			"Referer": []string{"https://www.aliyundrive.com/"},
		},
		URL: DownloadUrl,
	}, nil
}

func (d *AliyundriveShare2Open) GetHash(ctx context.Context, file model.Obj, args model.LinkArgs) string {
    fileId := file.GetID() 
    d.Link(ctx, file, args) 
    if d.Hash_dict != nil {
        if existedHash, ok := d.Hash_dict[fileId]; ok {
            return existedHash
        }
    }
    return ""
}

func (d *AliyundriveShare2Open) Copy2Myali(ctx context.Context, src_driveid string, file_id string, file_name string) (string, error) {

	Newfile_id, ok := d.CopyFiles[file_id]  // 如果键不存在，ok 的值为 false，v2 的值为该类型的零值
	if ok {
		return Newfile_id, nil
	}
    targetUrl := "https://api.aliyundrive.com/adrive/v2/batch"
	jsonData := map[string]interface{}{
		"resource": "file",
		"requests": []interface{}{
			map[string]interface{}{
				"method": "POST",
				"url": "/file/copy",
				"id": "0",
				"headers": map[string]interface{}{"Content-Type": "application/json"},
				"body": map[string]interface{}{
				"file_id": file_id,
				"share_id": d.ShareId,
				"auto_rename": true,
				"to_parent_file_id": d.TempTransferFolderID,
				"to_drive_id": d.MyAliDriveId,
				},
			},},
	}
	r, err := d.request(targetUrl, http.MethodPost, func(req *resty.Request) {
		req.SetBody(jsonData)
	})
	if err != nil {
		fmt.Println("转存失败: ",string(r),err)
		return "", err
	}
	var responses map[string]interface{}
	json.Unmarshal([]byte(r), &responses)

	respon := responses["responses"].([]interface{})[0]
	Newfile_id, _ = respon.(map[string]interface{})["body"].(map[string]interface{})["file_id"].(string)

	if Newfile_id != "" {
		d.CopyFiles[file_id] = Newfile_id
	}
	if Newfile_id == "" {
		NNewfile_id := utils.Json.Get(r, "file_id").ToString()
		if NNewfile_id != "" {
			d.CopyFiles[file_id] = NNewfile_id
		}
		if NNewfile_id == "" {
            r, err := d.request(targetUrl, http.MethodPost, func(req *resty.Request) {req.SetBody(jsonData)})
            if err != nil {
                fmt.Println("转存失败: ",string(r),err)
                return "", err
            }
            var responses map[string]interface{}
            json.Unmarshal([]byte(r), &responses)
            respon := responses["responses"].([]interface{})[0]
            Newfile_id, _ = respon.(map[string]interface{})["body"].(map[string]interface{})["file_id"].(string)
            if Newfile_id != "" {
                d.CopyFiles[file_id] = Newfile_id
			}
            if Newfile_id == "" {
				fmt.Println(time.Now().Format("01-02-2006 15:04:05"),"获取新file id失败: ",err)
				return "", err
			}
		}
	}
	return Newfile_id, nil

}

func (d *AliyundriveShare2Open) GetmyLink(ctx context.Context, file_id string, old_file_id string, file_name string) (string, error) {
	existed_download_url, ok := d.DownloadUrl_dict[file_id]
	if ok {
		return existed_download_url, nil
	}

    count := 1
    for {
        res, err := d.requestOpen(ctx, "/adrive/v1.0/openFile/getDownloadUrl", http.MethodPost, func(req *resty.Request) {
			req.SetBody(base.Json{
					"drive_id":   d.MyAliDriveId,
					"file_id":    file_id,
					"expire_sec": 14300,
			})
		})
        if err != nil {
            if count > 2 {
                  return "http://img.xiaoya.pro/abnormal.png", err
            }
			fmt.Println("获取下载链接失败第",count,"次 ",file_name)
            count += 1
            time.Sleep(1 * 1000 * time.Millisecond)
        }

        if err == nil {
            d.DownloadUrl_dict[file_id] = utils.Json.Get(res, "url").ToString()
			d.Hash_dict[old_file_id] = utils.Json.Get(res, "content_hash").ToString()
			return d.DownloadUrl_dict[file_id], nil
		}	
    }	
}

func (d *AliyundriveShare2Open) Remove(ctx context.Context, file_id string) error {
	_, err := d.requestOpen(ctx, "/adrive/v1.0/openFile/delete", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"file_id": file_id,
			"drive_id":  d.MyAliDriveId,
		})
	})
	return err
}

func (d *AliyundriveShare2Open) Purge_temp_folder(ctx context.Context) error {
	res, err := d.requestOpen(ctx, "/adrive/v1.0/openFile/list", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"parent_file_id": d.TempTransferFolderID,
			"drive_id":  d.MyAliDriveId,
		})
	})
	if err != nil {	return err }

	delete_files := gjson.GetBytes(res, "items.#.file_id")
	if len(delete_files.Array()) > 0 {
		fmt.Println(delete_files.Array())
		for _,b := range delete_files.Array() {
			err := d.Remove(ctx, b.String()) 
			if err != nil {
				//fmt.Printf("\033[0;32m%s%s%s\033[0m\n",time.Now().Format("01-02-2006 15:04:05"),"转存文件删除失败：",err)
			}	
			if err == nil {
				fmt.Printf("\033[0;32m%s%s%s\033[0m\n",time.Now().Format("01-02-2006 15:04:05"),"转存文件成功删除：",b.String())
			}
		}
	}
	return nil
}

func (d *AliyundriveShare2Open) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	var resp base.Json
	var uri string
	new_file_id, _ := d.Copy2Myali(ctx , d.MyAliDriveId, args.Obj.GetID(), args.Obj.GetName())
	data := base.Json{
		"drive_id": d.MyAliDriveId,
		"share_id": d.ShareId,
		"file_id":  new_file_id,
	}
	switch args.Method {
	case "video_preview":
		uri = "/adrive/v1.0/openFile/getVideoPreviewPlayInfo"
		data["category"] = "live_transcoding"
		data["url_expire_sec"] = 14400
	default:
		return nil, errs.NotSupport
	}
	_, err := d.requestOpen(ctx, uri, http.MethodPost, func(req *resty.Request) {
		req.SetBody(data).SetResult(&resp)
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

var _ driver.Driver = (*AliyundriveShare2Open)(nil)
