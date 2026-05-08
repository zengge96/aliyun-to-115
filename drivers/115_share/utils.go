package _115_share

import (
	"fmt"
	"strconv"
	"time"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

var _ model.Obj = (*FileObj)(nil)

type FileObj struct {
	Size     int64
	Sha1     string
	Utm      time.Time
	FileName string
	isDir    bool
	FileID   string
	ThumbURL string
}

func (f *FileObj) CreateTime() time.Time {
	return f.Utm
}

func (f *FileObj) GetHash() utils.HashInfo {
	return utils.NewHashInfo(utils.SHA1, f.Sha1)
}

func (f *FileObj) GetSize() int64 {
	return f.Size
}

func (f *FileObj) GetName() string {
	return f.FileName
}

func (f *FileObj) ModTime() time.Time {
	return f.Utm
}

func (f *FileObj) IsDir() bool {
	return f.isDir
}

func (f *FileObj) GetID() string {
	return f.FileID
}

func (f *FileObj) GetPath() string {
	return ""
}

func (f *FileObj) Thumb() string {
	return f.ThumbURL
}

type shareFile struct {
	FileID     string                 `json:"fid"`
	UID        int                    `json:"uid"`
	CategoryID driver115.IntString    `json:"cid"`
	FileName   string                 `json:"n"`
	Type       string                 `json:"ico"`
	Sha1       string                 `json:"sha"`
	Size       driver115.StringInt64  `json:"s"`
	Labels     []*driver115.LabelInfo `json:"fl"`
	UpdateTime string                 `json:"t"`
	IsFile     int                    `json:"fc"`
	ParentID   string                 `json:"pid"`
	ThumbURL   string                 `json:"u"`
}

type shareSnapResp struct {
	driver115.BasicResp
	Data struct {
		Count int         `json:"count"`
		List  []shareFile `json:"list"`
	} `json:"data"`
}

type downloadShareResp struct {
	driver115.BasicResp
	Data driver115.SharedDownloadInfo `json:"data"`
}

func transFunc(sf shareFile) (model.Obj, error) {
	timeInt, err := strconv.ParseInt(sf.UpdateTime, 10, 64)
	if err != nil {
		return nil, err
	}
	var (
		utm    = time.Unix(timeInt, 0)
		isDir  = (sf.IsFile == 0)
		fileID = string(sf.FileID)
	)
	if isDir {
		fileID = string(sf.CategoryID)
	}
	return &FileObj{
		Size:     int64(sf.Size),
		Sha1:     sf.Sha1,
		Utm:      utm,
		FileName: string(sf.FileName),
		isDir:    isDir,
		FileID:   fileID,
		ThumbURL: sf.ThumbURL,
	}, nil
}

func buildShareReferer(shareCode, receiveCode string) string {
	return fmt.Sprintf("https://115cdn.com/s/%s?password=%s&", shareCode, receiveCode)
}

func (d *Pan115Share) getShareSnapWithUA(ua, dirID string, queries ...driver115.Query) (*shareSnapResp, error) {
	result := shareSnapResp{}
	query := map[string]string{
		"share_code":   d.ShareCode,
		"receive_code": d.ReceiveCode,
		"cid":          dirID,
		"limit":        "20",
		"asc":          "0",
		"offset":       "0",
		"format":       "json",
	}
	for _, q := range queries {
		q(&query)
	}

	req := d.client.NewRequest().
		SetQueryParams(query).
		SetHeader("referer", buildShareReferer(d.ShareCode, d.ReceiveCode)).
		ForceContentType("application/json;charset=UTF-8").
		SetResult(&result)
	if ua != "" {
		req = req.SetHeader("User-Agent", ua)
	}

	resp, err := req.Get(driver115.ApiShareSnap)
	if err := driver115.CheckErr(err, &result, resp); err != nil {
		return nil, err
	}
	return &result, nil
}

func (d *Pan115Share) downloadByShareCodeWithUA(ua, fileID string) (*driver115.SharedDownloadInfo, error) {
	result := downloadShareResp{}
	params := map[string]string{
		"share_code":   d.ShareCode,
		"receive_code": d.ReceiveCode,
		"file_id":      fileID,
		"dl":           "1",
	}

	req := d.client.NewRequest().
		SetQueryParams(params).
		ForceContentType("application/json").
		SetHeader("referer", buildShareReferer(d.ShareCode, d.ReceiveCode)).
		SetResult(&result)
	if ua != "" {
		req = req.SetHeader("User-Agent", ua)
	}

	resp, err := req.Get(driver115.ApiDownloadGetShareUrl)
	if err := driver115.CheckErr(err, &result, resp); err != nil {
		return nil, err
	}
	return &result.Data, nil
}

func (d *Pan115Share) login() error {
	var err error
	opts := []driver115.Option{
		driver115.UA(base.UserAgent),
	}
	d.client = driver115.New(opts...)
	if _, err = d.getShareSnapWithUA(base.UserAgent, ""); err != nil {
		return errors.Wrap(err, "failed to get share snap")
	}
	cr := &driver115.Credential{}
	if d.QRCodeToken != "" {
		s := &driver115.QRCodeSession{
			UID: d.QRCodeToken,
		}
		if cr, err = d.client.QRCodeLoginWithApp(s, driver115.LoginApp(d.QRCodeSource)); err != nil {
			return errors.Wrap(err, "failed to login by qrcode")
		}
		d.Cookie = fmt.Sprintf("UID=%s;CID=%s;SEID=%s;KID=%s", cr.UID, cr.CID, cr.SEID, cr.KID)
		d.QRCodeToken = ""
	} else if d.Cookie != "" {
		if err = cr.FromCookie(d.Cookie); err != nil {
			return errors.Wrap(err, "failed to login by cookies")
		}
		d.client.ImportCredential(cr)
	} else {
		return errors.New("missing cookie or qrcode account")
	}

	return d.client.LoginCheck()
}
