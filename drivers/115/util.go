package _115

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	netutil "github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	cipher "github.com/xiaoyaliu00/115driver/pkg/crypto/ec115"
	driver115 "github.com/xiaoyaliu00/115driver/pkg/driver"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/pkg/errors"
)

// RapidUploader lets a stream reporter whether 115 matched the rapid-upload hash.
// Implement this to receive the result without changing Put's signature.
type RapidUploader interface {
	SetRapidUpload(bool)
}

// var UserAgent = driver115.UA115Browser
func (d *Pan115) login() error {
	var err error
	opts := []driver115.Option{
		driver115.UA(d.getUA()),
		func(c *driver115.Pan115Client) {
			c.Client.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: conf.Conf.TlsInsecureSkipVerify})
		},
	}
	d.client = driver115.New(opts...)
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

func (d *Pan115) getFiles(fileId string) ([]FileObj, error) {
	res := make([]FileObj, 0)
	if d.PageSize <= 0 {
		d.PageSize = driver115.FileListLimit
	}
	files, err := d.client.ListWithLimit(fileId, d.PageSize, driver115.WithMultiUrls())
	if err != nil {
		return nil, err
	}
	for _, file := range *files {
		res = append(res, FileObj{File: file})
	}
	return res, nil
}

func (d *Pan115) getNewFile(fileId string) (*FileObj, error) {
	file, err := d.client.GetFile(fileId)
	if err != nil {
		return nil, err
	}
	return &FileObj{File: *file}, nil
}

func (d *Pan115) getNewFileByPickCode(pickCode string) (*FileObj, error) {
	result := driver115.GetFileInfoResponse{}
	req := d.client.NewRequest().
		SetQueryParam("pick_code", pickCode).
		ForceContentType("application/json;charset=UTF-8").
		SetResult(&result)
	resp, err := req.Get(driver115.ApiFileInfo)
	if err := driver115.CheckErr(err, &result, resp); err != nil {
		return nil, err
	}
	if len(result.Files) == 0 {
		return nil, errors.New("not get file info")
	}
	fileInfo := result.Files[0]

	f := &FileObj{}
	f.From(fileInfo)
	return f, nil
}

func (d *Pan115) getUA() string {
	return fmt.Sprintf("Mozilla/5.0 115Browser/%s", appVer)
}

func (c *Pan115) GenerateToken(fileID, preID, timeStamp, fileSize, signKey, signVal string) string {
	userID := strconv.FormatInt(c.client.UserID, 10)
	userIDMd5 := md5.Sum([]byte(userID))
	tokenMd5 := md5.Sum([]byte(md5Salt + fileID + fileSize + signKey + signVal + userID + timeStamp + hex.EncodeToString(userIDMd5[:]) + appVer))
	return hex.EncodeToString(tokenMd5[:])
}

func (c *Pan115) GenerateSignature(fileID, target string) string {
	userIDStr := strconv.FormatInt(c.client.UserID, 10)
	sh1hash := sha1.Sum([]byte(userIDStr + fileID + target + "0"))
	sigStr := c.client.Userkey + hex.EncodeToString(sh1hash[:]) + "000000"
	sh1Sig := sha1.Sum([]byte(sigStr))
	return strings.ToUpper(hex.EncodeToString(sh1Sig[:]))
}

func (d *Pan115) rapidUpload(fileSize int64, fileName, dirID, preID, fileID string, stream model.FileStreamer) (*driver115.UploadInitResp, error) {
	var (
		ecdhCipher   *cipher.EcdhCipher
		encrypted    []byte
		decrypted    []byte
		encodedToken string
		err          error
		target       = "U_1_" + dirID
		bodyBytes    []byte
		result       = driver115.UploadInitResp{}
		fileSizeStr  = strconv.FormatInt(fileSize, 10)
	)
	if ecdhCipher, err = cipher.NewEcdhCipher(); err != nil {
		return nil, err
	}

	if ok, err := d.client.UploadAvailable(); !ok || err != nil {
		return nil, err
	}

	userID := strconv.FormatInt(d.client.UserID, 10)
	form := url.Values{}
	form.Set("appid", "0")
	form.Set("appversion", appVer)
	form.Set("userid", userID)
	form.Set("filename", fileName)
	form.Set("filesize", fileSizeStr)
	form.Set("fileid", fileID)
	form.Set("target", target)
	form.Set("sig", d.GenerateSignature(fileID, target))

	signKey, signVal := "", ""
	for retry := true; retry; {
		t := driver115.NowMilli()


		if encodedToken, err = ecdhCipher.EncodeToken(t.ToInt64()); err != nil {
			return nil, err
		}

		params := map[string]string{
			"k_ec": encodedToken,
		}

		form.Set("t", t.String())
		form.Set("token", d.GenerateToken(fileID, preID, t.String(), fileSizeStr, signKey, signVal))
		if signKey != "" && signVal != "" {
			form.Set("sign_key", signKey)
			form.Set("sign_val", signVal)
		}
		if encrypted, err = ecdhCipher.Encrypt([]byte(form.Encode())); err != nil {
			return nil, err
		}

		req := d.client.NewRequest().
			SetQueryParams(params).
			SetBody(encrypted).
			SetHeaderVerbatim("Content-Type", "application/x-www-form-urlencoded").
			SetDoNotParseResponse(true)
		resp, err := req.Post(driver115.ApiUploadInit)
		if err != nil {
			return nil, err
		}
		data := resp.RawBody()
		defer data.Close()
		if bodyBytes, err = io.ReadAll(data); err != nil {
			return nil, err
		}
		if decrypted, err = ecdhCipher.Decrypt(bodyBytes); err != nil {
			return nil, err
		}
		if err = driver115.CheckErr(json.Unmarshal(decrypted, &result), &result, resp); err != nil {
			return nil, err
		}
		if result.Status == 7 {
			// Update signKey & signVal
			signKey = result.SignKey
			signVal, err = UploadDigestRange(stream, result.SignCheck)
			if err != nil {
				return nil, err
			}
		} else {
			retry = false
		}
		result.SHA1 = fileID
	}

	return &result, nil
}

func (d *Pan115) RapidUploadCheck(ctx context.Context, fileSize int64, fileName, dirID, preID, fileID string, stream model.FileStreamer) (*driver115.UploadInitResp, error) {
	var (
		ecdhCipher   *cipher.EcdhCipher
		encrypted    []byte
		decrypted    []byte
		encodedToken string
		err          error
		target       = "U_1_" + dirID
		bodyBytes    []byte
		result       = driver115.UploadInitResp{}
		fileSizeStr  = strconv.FormatInt(fileSize, 10)
	)
	if ecdhCipher, err = cipher.NewEcdhCipher(); err != nil {
		return nil, err
	}

	if ok, err := d.client.UploadAvailable(); !ok || err != nil {
		return nil, err
	}

	userID := strconv.FormatInt(d.client.UserID, 10)
	form := url.Values{}
	form.Set("appid", "0")
	form.Set("appversion", appVer)
	form.Set("userid", userID)
	form.Set("filename", fileName)
	form.Set("filesize", fileSizeStr)
	form.Set("fileid", fileID)
	form.Set("target", target)
	form.Set("sig", d.GenerateSignature(fileID, target))

	signKey, signVal := "", ""
	for retry := true; retry; {
		t := driver115.NowMilli()

		if encodedToken, err = ecdhCipher.EncodeToken(t.ToInt64()); err != nil {
			return nil, err
		}

		params := map[string]string{
			"k_ec": encodedToken,
		}

		form.Set("t", t.String())
		form.Set("token", d.GenerateToken(fileID, preID, t.String(), fileSizeStr, signKey, signVal))
		if signKey != "" && signVal != "" {
			form.Set("sign_key", signKey)
			form.Set("sign_val", signVal)
		}
		if encrypted, err = ecdhCipher.Encrypt([]byte(form.Encode())); err != nil {
			return nil, err
		}

		req := d.client.NewRequest().
			SetQueryParams(params).
			SetBody(encrypted).
			SetHeaderVerbatim("Content-Type", "application/x-www-form-urlencoded").
			SetDoNotParseResponse(true)
		resp, err := req.Post(driver115.ApiUploadInit)
		if err != nil {
			return nil, err
		}
		data := resp.RawBody()
		defer data.Close()
		if bodyBytes, err = io.ReadAll(data); err != nil {
			return nil, err
		}
		if decrypted, err = ecdhCipher.Decrypt(bodyBytes); err != nil {
			return nil, err
		}
		if err = driver115.CheckErr(json.Unmarshal(decrypted, &result), &result, resp); err != nil {
			return nil, err
		}
		if result.Status == 7 {
			signKey = result.SignKey
			signVal, err = UploadDigestRange(stream, result.SignCheck)
			if err != nil {
				return nil, err
			}
		} else {
			retry = false
		}
		result.SHA1 = fileID
	}

	return &result, nil
}

func UploadDigestRange(stream model.FileStreamer, rangeSpec string) (result string, err error) {
	var start, end int64
	if _, err = fmt.Sscanf(rangeSpec, "%d-%d", &start, &end); err != nil {
		return
	}

	length := end - start + 1
	reader, err := stream.RangeRead(http_range.Range{Start: start, Length: length})
	if err != nil {
		return "", err
	}
	hashStr, err := utils.HashReader(utils.SHA1, reader)
	if err != nil {
		return "", err
	}
	result = strings.ToUpper(hashStr)
	return
}

// UploadByOSS use aliyun sdk to upload
func (c *Pan115) UploadByOSS(ctx context.Context, params *driver115.UploadOSSParams, s model.FileStreamer, dirID string, up driver.UpdateProgress) (*UploadResult, error) {
	ossToken, err := c.client.GetOSSToken()
	if err != nil {
		return nil, err
	}
	ossClient, err := netutil.NewOSSClient(driver115.OSSEndpoint, ossToken.AccessKeyID, ossToken.AccessKeySecret)
	if err != nil {
		return nil, err
	}
	bucket, err := ossClient.Bucket(params.Bucket)
	if err != nil {
		return nil, err
	}

	var bodyBytes []byte
	r := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
		Reader:         s,
		UpdateProgress: up,
	})
	if err = bucket.PutObject(params.Object, r, append(
		driver115.OssOption(params, ossToken),
		oss.CallbackResult(&bodyBytes),
	)...); err != nil {
		return nil, err
	}

	var uploadResult UploadResult
	if err = json.Unmarshal(bodyBytes, &uploadResult); err != nil {
		return nil, err
	}
	return &uploadResult, uploadResult.Err(string(bodyBytes))
}

// UploadByMultipart upload by mutipart blocks
func (d *Pan115) UploadByMultipart(ctx context.Context, params *driver115.UploadOSSParams, fileSize int64, s model.FileStreamer,
	dirID string, up driver.UpdateProgress, opts ...driver115.UploadMultipartOption,
) (*UploadResult, error) {
	// 创建派生 context，确保由于错误退出函数时，所有后台 goroutine 都能收到关闭信号，防止泄漏
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		chunks    []oss.FileChunk
		parts     []oss.UploadPart
		imur      oss.InitiateMultipartUploadResult
		ossClient *oss.Client
		bucket    *oss.Bucket
		ossToken  *driver115.UploadOSSTokenResp
		bodyBytes []byte
		err       error
	)

	//fmt.Printf("[DEBUG Multi] START fileName=%s fileSize=%d dirID=%s\n", s.GetName(), fileSize, dirID)

	tmpF, err := s.CacheFullAndWriter(&up, nil)
	//fmt.Printf("[DEBUG Multi] CacheFullAndWriter tmpF=%T err=%v\n", tmpF, err)
	if err != nil {
		return nil, err
	}

	options := driver115.DefalutUploadMultipartOptions()
	if len(opts) > 0 {
		for _, f := range opts {
			f(options)
		}
	}

	// ==================== 自定义读写分离参数 ====================
	// 读线程数：负责从本地或缓存将数据读进内存队列
	readThreadsNum := 2
	// 写线程数：如果 OSS 强制要求 Sequential() 按顺序上传，请务必保持 writeThreadsNum 为 1。
	// 若 OSS 实际上允许无序并行分片，可将其改大以提升上传速度。
	writeThreadsNum := 1
	// 队列最大长度：控制内存占用。如每个分片10MB，队列10则最多占用 100MB 内存缓冲
	queueMaxLen := 10
	// ============================================================

	if ossToken, err = d.client.GetOSSToken(); err != nil {
		return nil, err
	}
	ak := ossToken.AccessKeyID
	if len(ak) > 8 {
		ak = ak[:8] + "..."
	}
	//fmt.Printf("[DEBUG Multi] GetOSSToken ok AccessKeyID=%s SecurityToken_len=%d\n", ak, len(ossToken.SecurityToken))

	if ossClient, err = netutil.NewOSSClient(driver115.OSSEndpoint, ossToken.AccessKeyID, ossToken.AccessKeySecret, oss.EnableMD5(true), oss.EnableCRC(true)); err != nil {
		return nil, err
	}

	if bucket, err = ossClient.Bucket(params.Bucket); err != nil {
		return nil, err
	}

	// ossToken一小时后就会失效，所以每50分钟重新获取一次
	ticker := time.NewTicker(options.TokenRefreshTime)
	defer ticker.Stop()
	// 设置超时
	timeout := time.NewTimer(options.Timeout)
	defer timeout.Stop()

	if chunks, err = SplitFile(fileSize); err != nil {
		return nil, err
	}
	//fmt.Printf("[DEBUG Multi] SplitFile done chunks=%d totalSize=%d\n", len(chunks), fileSize)

	if imur, err = bucket.InitiateMultipartUpload(params.Object,
		oss.SetHeader(driver115.OssSecurityTokenHeaderName, ossToken.SecurityToken),
		oss.UserAgentHeader(driver115.OSSUserAgent),
		oss.EnableSha1(), oss.Sequential(),
	); err != nil {
		return nil, err
	}
	//fmt.Printf("[DEBUG Multi] InitiateMultipartUpload ok uploadID=%s object=%s\n", imur.UploadID, params.Object)

	wg := sync.WaitGroup{}
	wg.Add(len(chunks))

	chunksCh := make(chan oss.FileChunk)
	errCh := make(chan error)
	UploadedPartsCh := make(chan oss.UploadPart)
	quit := make(chan struct{})

	// 定义带数据的结构体，用于在读写线程间传递
	type ChunkData struct {
		Chunk oss.FileChunk
		Data  []byte
	}
	// 读写分离的有界队列
	dataQueue := make(chan ChunkData, queueMaxLen)

	// chunk 生产者 (生成分片元数据)
	go chunksProducer(chunksCh, chunks)
	
	// 监听所有分片处理完成
	go func() {
		wg.Wait()
		quit <- struct{}{}
	}()

	// ==================== 1. 读线程池 ====================
	var readWg sync.WaitGroup
	for i := 0; i < readThreadsNum; i++ {
		readWg.Add(1)
		go func() {
			defer readWg.Done()
			for chunk := range chunksCh {
				buf := make([]byte, chunk.Size)
				if _, err := tmpF.ReadAt(buf, chunk.Offset); err != nil && !errors.Is(err, io.EOF) {
					errCh <- errors.Wrap(err, fmt.Sprintf("读取 %s 的第%d个分片失败", s.GetName(), chunk.Number))
					return // 读取发生致命错误，退出当前读线程
				}

				// 将读取的数据放入队列，若队列满则阻塞，若 ctx canceled 则退出
				select {
				case <-ctx.Done():
					return
				case dataQueue <- ChunkData{Chunk: chunk, Data: buf}:
				}
			}
		}()
	}

	// 监控所有的读线程，全部结束后关闭数据队列
	go func() {
		readWg.Wait()
		close(dataQueue)
	}()

	// ==================== 2. 写（上传）线程池 ====================
	completedNum := atomic.Int32{}
	var tokenMutex sync.RWMutex // 保护 ossToken 并发读写的锁

	for i := 0; i < writeThreadsNum; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errCh <- fmt.Errorf("recovered in %v", r)
				}
			}()

			// 不断从队列获取带有数据的 chunk
			for cd := range dataQueue {
				chunk := cd.Chunk
				buf := cd.Data
				var part oss.UploadPart
				var uploadErr error

				// 出现错误就继续尝试，共尝试3次
				for retry := 0; retry < 3; retry++ {
					select {
					case <-ctx.Done():
						return // 退出写线程
					default:
					}

					// 安全地读取当前 Token
					tokenMutex.RLock()
					currentToken := ossToken
					tokenMutex.RUnlock()

					// 执行上传网络请求
					part, uploadErr = bucket.UploadPart(imur, driver.NewLimitedUploadStream(ctx, bytes.NewReader(buf)),
						chunk.Size, chunk.Number, driver115.OssOption(params, currentToken)...)
					if uploadErr == nil {
						break // 上传成功，跳出重试循环
					}
				}

				if uploadErr != nil {
					errCh <- errors.Wrap(uploadErr, fmt.Sprintf("上传 %s 的第%d个分片时出现错误：%v", s.GetName(), chunk.Number, uploadErr))
					return // 退出当前写线程
				}

				// 上传成功，更新进度条
				num := completedNum.Add(1)
				up(float64(num) * 100.0 / float64(len(chunks)))
				
				// 将成功的分片信息塞给收集器
				select {
				case <-ctx.Done():
					return
				case UploadedPartsCh <- part:
				}
			}
		}()
	}

	// ==================== 3. 结果收集与主循环 ====================
	go func() {
		for part := range UploadedPartsCh {
			parts = append(parts, part)
			wg.Done()
		}
	}()

LOOP:
	for {
		select {
		case <-ticker.C:
			// 到时重新获取 ossToken，加写锁以防止写线程在此期间读取
			newToken, tErr := d.client.GetOSSToken()
			if tErr != nil {
				return nil, errors.Wrap(tErr, "定时刷新token时出现错误")
			}
			tokenMutex.Lock()
			ossToken = newToken
			tokenMutex.Unlock()
		case <-quit:
			break LOOP
		case e := <-errCh:  // 修正：捕获具体的 errCh 发送的 error
			return nil, e
		case <-timeout.C:
			return nil, fmt.Errorf("time out")
		}
	}

	// 不知道啥原因，oss那边分片上传不计算sha1，导致115服务器校验错误
	// params.Callback.Callback = strings.ReplaceAll(params.Callback.Callback, "${sha1}", params.SHA1)
	//fmt.Printf("[DEBUG Multi] CompleteMultipartUpload partsCount=%d\n", len(parts))
	//for i, p := range parts {
	//fmt.Printf("[DEBUG Multi]   part[%d] ETag=%s PartNumber=%d\n", i, p.ETag, p.PartNumber)
	//}
	
	if _, err := bucket.CompleteMultipartUpload(imur, parts, append(
		driver115.OssOption(params, ossToken),
		oss.CallbackResult(&bodyBytes),
	)...); err != nil {
		return nil, err
	}
	//fmt.Printf("[DEBUG Multi] CompleteMultipartUpload raw_response=%s\n", string(bodyBytes))

	var uploadResult UploadResult
	if err = json.Unmarshal(bodyBytes, &uploadResult); err != nil {
		return nil, err
	}
	//fmt.Printf("[DEBUG Multi] uploadResult=%+v\n", uploadResult)
	return &uploadResult, uploadResult.Err(string(bodyBytes))
}

func chunksProducer(ch chan oss.FileChunk, chunks []oss.FileChunk) {
	for _, chunk := range chunks {
		ch <- chunk
	}
}

func SplitFile(fileSize int64) (chunks []oss.FileChunk, err error) {
	for i := int64(1); i < 10; i++ {
		if fileSize < i*utils.GB { // 文件大小小于iGB时分为i*1000片
			if chunks, err = SplitFileByPartNum(fileSize, int(i*1000)); err != nil {
				return
			}
			break
		}
	}
	if fileSize > 9*utils.GB { // 文件大小大于9GB时分为10000片
		if chunks, err = SplitFileByPartNum(fileSize, 10000); err != nil {
			return
		}
	}
	// 单个分片大小不能小于100KB
	if chunks[0].Size < 100*utils.KB {
		if chunks, err = SplitFileByPartSize(fileSize, 100*utils.KB); err != nil {
			return
		}
	}
	return
}

// SplitFileByPartNum splits big file into parts by the num of parts.
// Split the file with specified parts count, returns the split result when error is nil.
func SplitFileByPartNum(fileSize int64, chunkNum int) ([]oss.FileChunk, error) {
	if chunkNum <= 0 || chunkNum > 10000 {
		return nil, errors.New("chunkNum invalid")
	}

	if int64(chunkNum) > fileSize {
		return nil, errors.New("oss: chunkNum invalid")
	}

	var chunks []oss.FileChunk
	chunk := oss.FileChunk{}
	chunkN := (int64)(chunkNum)
	for i := int64(0); i < chunkN; i++ {
		chunk.Number = int(i + 1)
		chunk.Offset = i * (fileSize / chunkN)
		if i == chunkN-1 {
			chunk.Size = fileSize/chunkN + fileSize%chunkN
		} else {
			chunk.Size = fileSize / chunkN
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// SplitFileByPartSize splits big file into parts by the size of parts.
// Splits the file by the part size. Returns the FileChunk when error is nil.
func SplitFileByPartSize(fileSize int64, chunkSize int64) ([]oss.FileChunk, error) {
	if chunkSize <= 0 {
		return nil, errors.New("chunkSize invalid")
	}

	chunkN := fileSize / chunkSize
	if chunkN >= 10000 {
		return nil, errors.New("Too many parts, please increase part size")
	}

	var chunks []oss.FileChunk
	chunk := oss.FileChunk{}
	for i := int64(0); i < chunkN; i++ {
		chunk.Number = int(i + 1)
		chunk.Offset = i * chunkSize
		chunk.Size = chunkSize
		chunks = append(chunks, chunk)
	}

	if fileSize%chunkSize > 0 {
		chunk.Number = len(chunks) + 1
		chunk.Offset = int64(len(chunks)) * chunkSize
		chunk.Size = fileSize % chunkSize
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}
