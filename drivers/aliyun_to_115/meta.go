package aliyun_to_115

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type aliyunTo115Addition struct {
	Open115Cookie  string       `json:"open115_cookie" required:"true" help:"115 cookie for sync upload"`
	SyncInterval   int64        `json:"sync_interval" type:"number" default:"600" help:"sync interval in seconds, default 600 (10 minutes)"`
	QRCodeToken    string       `json:"qrcode_token" type:"text" help:"115 QRCode token"`
	QRCodeSource   string       `json:"qrcode_source" type:"select" options:"web,android,ios,tv,alipaymini,wechatmini,qandroid" default:"linux" help:"select the QR code device"`
	PageSize       int64        `json:"page_size" type:"number" default:"1000" help:"list api per page size"`
	LimitRate      float64      `json:"limit_rate" type:"float" default:"2" help:"limit all api request rate ([limit]r/1s)"`
	DeleteAfterSync bool        `json:"delete_after_sync" type:"bool" default:"false" help:"delete file from 115 after sync (SHA1 will still be registered)"`
	driver.RootID
}

type Addition = aliyunTo115Addition

var config = driver.Config{
	Name:              "AliyunTo115",
	DefaultRoot:       "root",
	NoOverwriteUpload: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &AliyunTo115{}
	})
}