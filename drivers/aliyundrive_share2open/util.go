package aliyundrive_share2open

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	log "github.com/sirupsen/logrus"
)

var (
	limiterOther = rateLimit(600, 1)
)

const API_URL = "https://openapi.alipan.com"

func rateLimit(max int, periodSec int) *syncRateLimiter {
	return &syncRateLimiter{max: max, period: time.Duration(periodSec) * time.Second}
}

type syncRateLimiter struct {
	mu      sync.Mutex
	max     int
	period  time.Duration
	count   int
	last    time.Time
}

func (l *syncRateLimiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.last) > l.period {
		l.count = 0
		l.last = now
	}
	if l.count >= l.max {
		timer := time.NewTimer(time.Until(l.last.Add(l.period)))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			l.count = 0
			l.last = time.Now()
		}
	}
	l.count++
	return nil
}

func (d *AliyundriveShare2Open) wait(ctx context.Context, limiter *syncRateLimiter) error {
	return limiter.Wait(ctx)
}

func getSub(token string) (string, error) {
	parts := utils.Json.Get([]byte(token), "sub").ToString()
	if parts == "" {
		return "", errors.New("failed to get sub from token")
	}
	return parts, nil
}

func (d *AliyundriveShare2Open) refreshToken() error {
	url := "https://auth.aliyundrive.com/v2/account/token"
	var resp base.TokenResp
	var e ErrorResp
	_, err := base.RestyClient.R().
		SetBody(base.Json{"refresh_token": d.RefreshToken, "grant_type": "refresh_token"}).
		SetResult(&resp).
		SetError(&e).
		Post(url)
	if err != nil {
		return err
	}
	if e.Code != "" {
		return fmt.Errorf("failed to refresh token: %s", e.Message)
	}
	d.RefreshToken, d.AccessToken = resp.RefreshToken, resp.AccessToken
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *AliyundriveShare2Open) getShareToken() error {
	data := base.Json{
		"share_id": d.ShareId,
	}
	if d.SharePwd != "" {
		data["share_pwd"] = d.SharePwd
	}
	var e ErrorResp
	var resp ShareTokenResp
	_, err := base.RestyClient.R().
		SetResult(&resp).SetError(&e).SetBody(data).
		Post("https://api.aliyundrive.com/v2/share_link/get_share_token")
	if err != nil {
		return err
	}
	if e.Code != "" {
		return errors.New(e.Message)
	}
	d.ShareToken = resp.ShareToken
	return nil
}

func (d *AliyundriveShare2Open) request(url, method string, callback base.ReqCallback) ([]byte, error) {
	var e ErrorResp
	req := base.RestyClient.R().
		SetError(&e).
		SetHeader("content-type", "application/json").
		SetHeader("Authorization", "Bearer\t"+d.AccessToken).
		SetHeader("x-share-token", d.ShareToken)
	if callback != nil {
		callback(req)
	} else {
		req.SetBody("{}")
	}
	resp, err := req.Execute(method, url)
	if err != nil {
		return nil, err
	}
	if e.Code != "" {
		fmt.Println(e.Code, ": ", e.Message)
		if utils.SliceContains([]string{"AccessTokenInvalid", "AccessTokenExpired", "I400JD"}, e.Code) || e.Code == "ShareLinkTokenInvalid" {
			if utils.SliceContains([]string{"AccessTokenInvalid", "AccessTokenExpired", "I400JD"}, e.Code) {
				err = d.refreshToken()
			} else {
				err = d.getShareToken()
			}
			if err != nil {
				return nil, err
			}
			return d.request(url, method, callback)
		} else {
			return nil, errors.New(e.Code + ": " + e.Message)
		}
	}
	return resp.Body(), nil
}

func (d *AliyundriveShare2Open) getFiles(fileId string) ([]File, error) {
	files := make([]File, 0)
	data := base.Json{
		"limit":                   200,
		"order_by":                d.OrderBy,
		"order_direction":         d.OrderDirection,
		"parent_file_id":          fileId,
		"share_id":                d.ShareId,
		"marker":                  "first",
	}
	for data["marker"] != "" {
		if data["marker"] == "first" {
			data["marker"] = ""
		}
		var e ErrorResp
		var resp ListResp
		_, err := base.RestyClient.R().
			SetHeader("x-share-token", d.ShareToken).
			SetResult(&resp).SetError(&e).SetBody(data).
			Post("https://api.aliyundrive.com/adrive/v3/file/list")
		if err != nil {
			return nil, err
		}
		//fmt.Printf("aliyundrive share get files: %s\n", res.String())
		if e.Code != "" {
			if utils.SliceContains([]string{"AccessTokenInvalid", "AccessTokenExpired", "I400JD"}, e.Code) || e.Code == "ShareLinkTokenInvalid" {
				err = d.getShareToken()
				if err != nil {
					return nil, err
				}
				return d.getFiles(fileId)
			}
			return nil, errors.New(e.Message)
		}
		data["marker"] = resp.NextMarker
		files = append(files, resp.Items...)
	}
	if len(files) > 0 && d.MyAliDriveId == "" {
		d.MyAliDriveId = files[0].DriveId
	}
	return files, nil
}

func (d *AliyundriveShare2Open) _refreshTokenOpen(ctx context.Context) (string, string, error) {
	if d.UseOnlineAPI && d.APIAddress != "" {
		u := d.APIAddress
		var resp struct {
			RefreshToken string `json:"refresh_token"`
			AccessToken  string `json:"access_token"`
			ErrorMessage string `json:"text"`
		}

		// 根据AlipanType选项设置driver_txt
		driverTxt := "alicloud_qr"
		if d.AlipanType == "alipanTV" {
			driverTxt = "alicloud_tv"
		}
		err := d.wait(ctx, limiterOther)
		if err != nil {
			return "", "", err
		}
		_, err = base.RestyClient.R().
			SetResult(&resp).
			SetQueryParams(map[string]string{
				"refresh_ui": d.RefreshTokenOpen,
				"server_use": "true",
				"driver_txt": driverTxt,
			}).
			Get(u)
		if err != nil {
			return "", "", err
		}
		if resp.RefreshToken == "" || resp.AccessToken == "" {
			if resp.ErrorMessage != "" {
				return "", "", fmt.Errorf("failed to refresh token: %s", resp.ErrorMessage)
			}
			return "", "", fmt.Errorf("empty token returned from proxy API, a wrong refresh token may have been used")
		}
		return resp.RefreshToken, resp.AccessToken, nil
	}
	// 本地刷新逻辑，必须要求 client_id 和 client_secret
	if d.ClientID == "" || d.ClientSecret == "" {
		return "", "", fmt.Errorf("empty ClientID or ClientSecret")
	}
	err := d.wait(ctx, limiterOther)
	if err != nil {
		return "", "", err
	}
	url := API_URL + "/oauth/access_token"
	//var resp base.TokenResp
	var e ErrorResp
	res, err := base.RestyClient.R().
		//ForceContentType("application/json").
		SetBody(base.Json{
			"client_id":     d.ClientID,
			"client_secret": d.ClientSecret,
			"grant_type":    "refresh_token",
			"refresh_token": d.RefreshTokenOpen,
		}).
		//SetResult(&resp).
		SetError(&e).
		Post(url)
	if err != nil {
		return "", "", err
	}
	log.Debugf("[ali_open] refresh token response: %s", res.String())
	if e.Code != "" {
		return "", "", fmt.Errorf("failed to refresh token: %s", e.Message)
	}
	refresh, access := utils.Json.Get(res.Body(), "refresh_token").ToString(), utils.Json.Get(res.Body(), "access_token").ToString()
	if refresh == "" {
		return "", "", fmt.Errorf("failed to refresh token: refresh token is empty, resp: %s", res.String())
	}
	curSub, err := getSub(d.RefreshToken)
	if err != nil {
		return "", "", err
	}
	newSub, err := getSub(refresh)
	if err != nil {
		return "", "", err
	}
	if curSub != newSub {
		return "", "", errors.New("failed to refresh token: sub not match")
	}
	return refresh, access, nil
}

func (d *AliyundriveShare2Open) refreshTokenOpen(ctx context.Context) error {
	refresh, access, err := d._refreshTokenOpen(ctx)
	for i := 0; i < 3; i++ {
		if err == nil {
			break
		} else {
			log.Errorf("[ali_open] failed to refresh token: %s", err)
		}
		refresh, access, err = d._refreshTokenOpen(ctx)
	}
	if err != nil {
		return err
	}
	log.Infof("[ali_open] token exchange: %s -> %s", d.RefreshToken, refresh)
	d.RefreshTokenOpen, d.AccessTokenOpen = refresh, access
	tokenMutex.Lock()
	AliOpenRefreshToken, AliOpenAccessToken = refresh, access
	tokenMutex.Lock()
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *AliyundriveShare2Open) requestOpen(ctx context.Context, uri, method string, callback base.ReqCallback, retry ...bool) ([]byte, error) {
	// 优先使用全局共享 token（其他实例刷新的），其次用实例自己的
	tokenToUse := AliOpenAccessToken
	if tokenToUse == "" {
		tokenToUse = d.AccessTokenOpen
	}
	req := base.RestyClient.R()
	req.SetHeader("Authorization", "Bearer " + tokenToUse)
	if method == http.MethodPost {
		req.SetHeader("Content-Type", "application/json")
	}
	if callback != nil {
		callback(req)
	}
	var e ErrorResp
	req.SetError(&e)
	res, err := req.Execute(method, API_URL+uri)
	if err != nil {
		return nil, err
	}
	isRetry := len(retry) > 0 && retry[0]
	if e.Code != "" {
		if !isRetry && utils.SliceContains([]string{"AccessTokenInvalid", "AccessTokenExpired", "I400JD"}, e.Code) {
			oldToken := tokenToUse
			tokenMutex.Lock()
			if AliOpenAccessToken != "" && AliOpenAccessToken != oldToken {
				tokenMutex.Unlock()
				return d.requestOpen(ctx, uri, method, callback, true)
			}
			err = d.refreshTokenOpen(ctx)
			tokenMutex.Unlock()
			if err != nil {
				return nil, err
			}
			return d.requestOpen(ctx, uri, method, callback, true)
		}
		return nil, fmt.Errorf("%s:%s", e.Code, e.Message)
	}
	return res.Body(), nil
}
