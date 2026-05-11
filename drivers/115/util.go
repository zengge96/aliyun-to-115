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

	tmpF, err := s.CacheFullAndWriter(&up, nil)
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
	// 写线程数：因为 OSS 强制要求 Sequential() 按顺序上传，请务必保持 writeThreadsNum 为 1。
	writeThreadsNum := 1
	// 队列最大长度：控制内存占用。采用保序滑动窗口后，内存占用被严格限制在 queueMaxLen * chunkSize 以内
	queueMaxLen := 10
	// ============================================================

	if ossToken, err = d.client.GetOSSToken(); err != nil {
		return nil, err
	}

	if ossClient, err = netutil.NewOSSClient(driver115.OSSEndpoint, ossToken.AccessKeyID, ossToken.AccessKeySecret, oss.EnableMD5(true), oss.EnableCRC(true)); err != nil {
		return nil, err
	}

	if bucket, err = ossClient.Bucket(params.Bucket); err != nil {
		return nil, err
	}

	ticker := time.NewTicker(options.TokenRefreshTime)
	defer ticker.Stop()
	timeout := time.NewTimer(options.Timeout)
	defer timeout.Stop()

	if chunks, err = SplitFile(fileSize); err != nil {
		return nil, err
	}

	if imur, err = bucket.InitiateMultipartUpload(params.Object,
		oss.SetHeader(driver115.OssSecurityTokenHeaderName, ossToken.SecurityToken),
		oss.UserAgentHeader(driver115.OSSUserAgent),
		oss.EnableSha1(), oss.Sequential(),
	); err != nil {
		return nil, err
	}

	wg := sync.WaitGroup{}
	wg.Add(len(chunks))

	// 给 error 增加缓冲，避免多协程同时报错时产生阻塞泄漏
	errCh := make(chan error, readThreadsNum+writeThreadsNum+2)
	UploadedPartsCh := make(chan oss.UploadPart)
	quit := make(chan struct{})

	// 监听所有分片处理完成
	go func() {
		wg.Wait()
		quit <- struct{}{}
	}()

	// ==================== 新增：保序结构与通道 ====================
	type Job struct {
		Index int
		Chunk oss.FileChunk
	}
	type ChunkData struct {
		Chunk oss.FileChunk
		Data  []byte
	}

	jobsCh := make(chan Job) // 派发读任务
	// 每个分片一个专属的等待通道，防止乱序
	chunkWaiters := make([]chan ChunkData, len(chunks))
	for i := range chunkWaiters {
		chunkWaiters[i] = make(chan ChunkData, 1)
	}
	// 重新排好序后，塞给写线程的单行道
	orderedDataQueue := make(chan ChunkData)
	// 内存控制令牌桶：最大允许在内存中同时处理（或排队）的分片数
	activeChunksLimit := make(chan struct{}, queueMaxLen)

	// ==================== 0. 任务分配与滑动内存控制 ====================
	go func() {
		defer close(jobsCh)
		for i, chunk := range chunks {
			// 先获取内存令牌（若积压达到 queueMaxLen 则会阻塞，直至写线程成功上传释放令牌）
			select {
			case <-ctx.Done():
				return
			case activeChunksLimit <- struct{}{}:
			}

			select {
			case <-ctx.Done():
				return
			case jobsCh <- Job{Index: i, Chunk: chunk}:
			}
		}
	}()

	// ==================== 1. 读线程池 ====================
	var readWg sync.WaitGroup
	for i := 0; i < readThreadsNum; i++ {
		readWg.Add(1)
		go func() {
			defer readWg.Done()
			for job := range jobsCh {
				chunk := job.Chunk
				buf := make([]byte, chunk.Size)
				var readErr error
				maxReadRetries := 3

				// 读取重试机制
				for retry := 0; retry < maxReadRetries; retry++ {
					select {
					case <-ctx.Done():
						return
					default:
					}

					_, readErr = tmpF.ReadAt(buf, chunk.Offset)
					if readErr == nil || errors.Is(readErr, io.EOF) {
						readErr = nil
						break
					}

					if retry < maxReadRetries-1 {
						select {
						case <-ctx.Done():
							return
						case <-time.After(100 * time.Millisecond):
						}
					}
				}

				if readErr != nil {
					errCh <- errors.Wrap(readErr, fmt.Sprintf("读取 %s 的第%d个分片失败(已重试%d次)", s.GetName(), chunk.Number, maxReadRetries))
					return
				}

				// 将读取完毕的数据放入它专属序号的等待通道
				chunkWaiters[job.Index] <- ChunkData{Chunk: chunk, Data: buf}
			}
		}()
	}

	// ==================== 1.5 严格保序调度器 ====================
	// 它按 0, 1, 2, 3... 的死顺序收集，发现缺失就坚决等待，保证送给写线程的永远是顺序结构
	go func() {
		defer close(orderedDataQueue)
		for i := 0; i < len(chunks); i++ {
			select {
			case <-ctx.Done():
				return
			case cd := <-chunkWaiters[i]: // 阻塞等待当前顺位编号的 chunk 完成
				select {
				case <-ctx.Done():
					return
				case orderedDataQueue <- cd:
				}
			}
		}
	}()

	// ==================== 2. 写（上传）线程池 ====================
	completedNum := atomic.Int32{}
	var tokenMutex sync.RWMutex

	for i := 0; i < writeThreadsNum; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errCh <- fmt.Errorf("recovered in %v", r)
				}
			}()

			// 拿到的必然是严格按序到达的数据
			for cd := range orderedDataQueue {
				chunk := cd.Chunk
				buf := cd.Data
				var part oss.UploadPart
				var uploadErr error

				// 上传重试机制
				for retry := 0; retry < 3; retry++ {
					select {
					case <-ctx.Done():
						return 
					default:
					}

					tokenMutex.RLock()
					currentToken := ossToken
					tokenMutex.RUnlock()

					part, uploadErr = bucket.UploadPart(imur, driver.NewLimitedUploadStream(ctx, bytes.NewReader(buf)),
						chunk.Size, chunk.Number, driver115.OssOption(params, currentToken)...)
					if uploadErr == nil {
						break 
					}
				}

				if uploadErr != nil {
					errCh <- errors.Wrap(uploadErr, fmt.Sprintf("上传 %s 的第%d个分片时出现错误：%v", s.GetName(), chunk.Number, uploadErr))
					return 
				}

				num := completedNum.Add(1)
				up(float64(num) * 100.0 / float64(len(chunks)))

				select {
				case <-ctx.Done():
					return
				case UploadedPartsCh <- part:
				}

				// 【重点】上传成功并登记完毕后，释放一个内存槽位令牌！此时读派发器才能分配新一轮的任务
				<-activeChunksLimit
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
			newToken, tErr := d.client.GetOSSToken()
			if tErr != nil {
				return nil, errors.Wrap(tErr, "定时刷新token时出现错误")
			}
			tokenMutex.Lock()
			ossToken = newToken
			tokenMutex.Unlock()
		case <-quit:
			break LOOP
		case e := <-errCh:
			return nil, e
		case <-timeout.C:
			return nil, fmt.Errorf("time out")
		}
	}

	if _, err := bucket.CompleteMultipartUpload(imur, parts, append(
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

func chunksProducer(ch chan oss.FileChunk, chunks []oss.FileChunk) {
	for _, chunk := range chunks {
		ch <- chunk
	}
}

func SplitFile(fileSize int64) (chunks []oss.FileChunk, err error) {
	for i := int64(1); i < 10; i++ {
		if fileSize < i*utils.GB { // 文件大小小于iGB时分为i*200片
			if chunks, err = SplitFileByPartNum(fileSize, int(i*200)); err != nil {
				return
			}
			break
		}
	}
	if fileSize > 9*utils.GB { // 文件大小大于9GB时分为2000片
		if chunks, err = SplitFileByPartNum(fileSize, 2000); err != nil {
			return
		}
	}
	// 单个分片大小不能小于1MB
	if chunks[0].Size < 1000*utils.KB {
		if chunks, err = SplitFileByPartSize(fileSize, 1000*utils.KB); err != nil {
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
