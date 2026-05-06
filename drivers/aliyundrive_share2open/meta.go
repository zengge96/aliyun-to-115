package aliyundrive_share2open

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	RefreshToken        string `json:"RefreshToken" required:"true"`
	RefreshTokenOpen    string `json:"RefreshTokenOpen" required:"true"`
	TempTransferFolderID string `json:"TempTransferFolderID" default:"root"`
	Autoremove          bool   `json:"autoremove"`
	ShareId             string `json:"share_id" required:"true"`
	SharePwd            string `json:"share_pwd"`
	driver.RootID
	OrderBy             string `json:"order_by" type:"select" options:"name,size,updated_at,created_at"`
	OrderDirection      string `json:"order_direction" type:"select" options:"ASC,DESC"`
	UseOnlineAPI       bool   `json:"use_online_api" default:"true"`
	AlipanType          string `json:"alipan_type" type:"select" options:"alipan,alipanTV"`
	APIAddress          string `json:"api_url_address" default:"https://api.oplist.org/alicloud/renewapi"`
	ClientID            string `json:"client_id" required:"false" help:"Keep it empty if you don't have one"`
	ClientSecret        string `json:"client_secret" required:"false" help:"Keep it empty if you don't have one"`
}

var config = driver.Config{
	Name:        "AliyundriveShare2Open",
	LocalSort:   false,
	OnlyProxy:   false,
	NoUpload:    true,
	DefaultRoot: "root",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &AliyundriveShare2Open{
			base: "https://openapi.alipan.com",
		}
	})
}
