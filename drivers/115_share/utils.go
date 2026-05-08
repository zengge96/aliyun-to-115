package _115_share

import (
	"fmt"
	"strconv"
	"time"

	driver115 "github.com/xiaoyaliu00/115driver/pkg/driver"
	//driver115 "github.com/xiaoyaliu00/115driver/pkg/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
    "github.com/OpenListTeam/OpenList/v4/drivers/base"
)

var _ model.Obj = (*FileObj)(nil)

type FileObj struct {
	Size     int64
	Sha1     string
	Utm      time.Time
	FileName string
	isDir    bool
	FileID   string
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

func transFunc(sf driver115.ShareFile) (model.Obj, error) {
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
	}, nil
}

var UserAgent = driver115.UADefalut

func (d *Pan115Share) login() error {
	var err error
	opts := []driver115.Option{
		driver115.UA(UserAgent),
	}
	d.client = driver115.New(opts...)
	if _, err := d.client.GetShareSnap(d.ShareCode, d.ReceiveCode, ""); err != nil {
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
		if userInfo, err := d.client.GetUser(); err == nil {
			fmt.Println("115用户等级是: ",userInfo.Vip, "用户名是: ", userInfo.UserName)
	    	if userInfo.Vip == 0 {
				base.V115novip = 1
			}
		}
	} else {
		return errors.New("missing cookie or qrcode account")
	}

	return d.client.LoginCheck()
}
