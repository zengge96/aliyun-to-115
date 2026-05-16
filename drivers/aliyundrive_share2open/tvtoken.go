package aliyundrive_share2open

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	log "github.com/sirupsen/logrus"
)

// getTvToken 通过 TV 扫码登录协议获取 RefreshToken 和 AccessToken
// 当 d.AlipanType == "alipanTV" 时调用此函数
func (d *AliyundriveShare2Open) getTvToken(refreshToken string) (string, string, error) {
	body := map[string]interface{}{
		"refresh_token": refreshToken,
	}

	requestInfo, err := generateRequestInfo("/v4/token", body)
	if err != nil {
		return "", "", fmt.Errorf("[ali_tv] 生成请求信息失败: %w", err)
	}

	headers := requestInfo["headers"].(map[string]string)
	bodyMap := requestInfo["body"].(map[string]interface{})

	resp, err := base.RestyClient.R().
		SetHeaders(headers).
		SetBody(bodyMap).
		Post("https://api.extscreen.com/aliyundrive/v4/token")

	if err != nil || resp.StatusCode() != 200 {
		return "", "", fmt.Errorf("[ali_tv] 请求 token 失败: %s", resp.String())
	}

	var tokenData map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &tokenData); err != nil {
		return "", "", fmt.Errorf("[ali_tv] 解析响应失败: %w", err)
	}

	data, ok := tokenData["data"].(map[string]interface{})
	if !ok {
		text := ""
		if t, ok := tokenData["text"].(string); ok {
			text = t
		}
		return "", "", fmt.Errorf("[ali_tv] token 响应格式错误: %s", text)
	}

	ciphertext, ok := data["ciphertext"].(string)
	if !ok {
		return "", "", fmt.Errorf("[ali_tv] 响应缺少 ciphertext")
	}
	iv, ok := data["iv"].(string)
	if !ok {
		return "", "", fmt.Errorf("[ali_tv] 响应缺少 iv")
	}

	plain, err := decrypt(ciphertext, iv, requestInfo["key"].(string))
	if err != nil {
		return "", "", fmt.Errorf("[ali_tv] 解密响应失败: %w", err)
	}

	type tvTokenResp struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
	}
	var token tvTokenResp
	if err := json.Unmarshal([]byte(plain), &token); err != nil {
		return "", "", fmt.Errorf("[ali_tv] 解析 token JSON 失败: %w", err)
	}

	rt := token.RefreshToken
	at := token.AccessToken
	if rt == "" || at == "" {
		return "", "", fmt.Errorf("[ali_tv] token 为空 rt=%s at=%s", rt, at)
	}

	log.Infof("[ali_tv] token 获取成功: %s -> %s", refreshToken[:min(8, len(refreshToken))], rt[:min(8, len(rt))])
	return rt, at, nil
}

// -------------------- 以下为 TV 协议所需的加密工具 --------------------

func randomString(length int) string {
	if length <= 0 {
		length = 32
	}
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := range result {
		result[i] = charset[r.Intn(len(charset))]
	}
	return string(result)
}

func h(charArray []rune, modifier interface{}) string {
	// 去重
	uniqueMap := make(map[rune]bool)
	var uniqueChars []rune
	for _, c := range charArray {
		if !uniqueMap[c] {
			uniqueMap[c] = true
			uniqueChars = append(uniqueChars, c)
		}
	}

	// 处理 modifier，截取字符串后部分转换成数字
	modStr := fmt.Sprintf("%v", modifier)
	if len(modStr) < 7 {
		panic("modifier 字符串长度不足7")
	}
	numPart := modStr[7:]
	numericModifier, err := strconv.Atoi(numPart)
	if err != nil {
		panic(err)
	}

	var builder strings.Builder
	for _, char := range uniqueChars {
		charCode := int(char)
		newCharCode := charCode - (numericModifier % 127) - 1
		if newCharCode < 0 {
			newCharCode = -newCharCode
		}
		if newCharCode < 33 {
			newCharCode += 33
		}
		builder.WriteRune(rune(newCharCode))
	}

	return builder.String()
}

func generateKey(t interface{}) string {
	params := tvGetParams(t)

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var concatenatedParams strings.Builder
	for _, k := range keys {
		if k != "t" {
			concatenatedParams.WriteString(params[k])
		}
	}

	keyArray := []rune(concatenatedParams.String())
	hashedKeyString := h(keyArray, t)

	md5Sum := md5.Sum([]byte(hashedKeyString))
	return hex.EncodeToString(md5Sum[:])
}

func tvGetParams(t interface{}) map[string]string {
	return map[string]string{
		"akv":     "2.8.1496",
		"apv":     "1.4.1",
		"b":       "vivo",
		"d":       "2c7d30cd7ae5e8017384988393f397c6",
		"m":       "V2329A",
		"n":       "V2329A",
		"mac":     "",
		"wifiMac": "00db00200063",
		"nonce":   "",
		"t":       fmt.Sprintf("%v", t),
	}
}

func getSign(apiPath string, t string) string {
	params := tvGetParams(t)
	key := generateKey(t)

	data := fmt.Sprintf("POST-/api%v-%v-%v-%v", apiPath, t, params["d"], key)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

func getTimestamp() string {
	type timestampResp struct {
		Code int    `json:"code"`
		Data struct {
			Timestamp float64 `json:"timestamp"`
		} `json:"data"`
	}

	var resp timestampResp
	_, err := base.RestyClient.R().
		SetResult(&resp).
		Get("https://api.extscreen.com/timestamp")
	if err != nil || resp.Code != 200 {
		return strconv.FormatInt(time.Now().Unix(), 10)
	}
	return strconv.FormatInt(int64(resp.Data.Timestamp), 10)
}

func generateRequestInfo(apiPath string, body map[string]interface{}) (map[string]interface{}, error) {
	t := getTimestamp()
	keyStr := generateKey(t)
	headers := tvGetParams(t)

	bodyJsonBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("JSON 编码失败: %w", err)
	}

	iv := randomString(16)
	encrypted, err := encrypt(string(bodyJsonBytes), iv, keyStr)
	if err != nil {
		return nil, fmt.Errorf("AES 加密失败: %w", err)
	}

	encryptedBody := map[string]interface{}{
		"ciphertext": encrypted,
		"iv":         iv,
	}

	headers["Content-Type"] = "application/json"
	headers["sign"] = getSign(apiPath, t)

	return map[string]interface{}{
		"headers": headers,
		"body":    encryptedBody,
		"key":     keyStr,
	}, nil
}

func encrypt(plaintextStr, ivHex, keyStr string) (string, error) {
	key := []byte(keyStr)
	if len(key) != 32 {
		return "", errors.New("key 长度必须为 32 字节（AES-256）")
	}

	iv := []byte(ivHex)
	if len(iv) != aes.BlockSize {
		return "", errors.New("IV 长度必须为 16 字节（128 位）")
	}

	plaintext := []byte(plaintextStr)
	plaintext = pkcs7Pad(plaintext, aes.BlockSize)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("创建 AES cipher 失败: %w", err)
	}

	ciphertext := make([]byte, len(plaintext))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, plaintext)

	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decrypt(ciphertextB64, ivHex, keyStr string) (string, error) {
	key := []byte(keyStr)
	if len(key) != 32 {
		return "", errors.New("key 长度必须为 32 字节（AES-256）")
	}

	iv, err := hex.DecodeString(ivHex)
	if err != nil {
		return "", fmt.Errorf("iv 解码失败: %w", err)
	}
	if len(iv) != aes.BlockSize {
		return "", errors.New("IV 长度必须为 16 字节（128 位）")
	}

	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", fmt.Errorf("密文 base64 解码失败: %w", err)
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return "", errors.New("密文长度不是块大小的倍数")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("创建 AES cipher 失败: %w", err)
	}

	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	plaintext, err = pkcs7Unpad(plaintext, aes.BlockSize)
	if err != nil {
		return "", fmt.Errorf("去 padding 失败: %w", err)
	}

	return string(plaintext), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padText := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padText...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("无效的数据长度")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > blockSize {
		return nil, errors.New("无效的 padding 长度")
	}
	for i := len(data) - padLen; i < len(data); i++ {
		if data[i] != byte(padLen) {
			return nil, errors.New("padding 内容不合法")
		}
	}
	return data[:len(data)-padLen], nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}