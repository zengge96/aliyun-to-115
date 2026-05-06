//go:build ignore

package main

import (
	"fmt"
	"os"
	"strings"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

func main() {
	cookieData, err := os.ReadFile("/root/.openclaw/115_cookie.txt")
	if err != nil {
		fmt.Printf("读取cookie失败: %v\n", err)
		os.Exit(1)
	}
	cookie := strings.TrimSpace(string(cookieData))

	client := driver115.New()
	cr := &driver115.Credential{}
	if err := cr.FromCookie(cookie); err != nil {
		fmt.Printf("Cookie解析失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Cookie解析成功: UID=%s\n", cr.UID)

	client.ImportCredential(cr)
	if err := client.LoginCheck(); err != nil {
		fmt.Printf("LoginCheck失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("LoginCheck成功!")

	// 测试上传
	fmt.Println("测试上传 1MB...")
	
	// 创建 1MB 测试数据
	data := make([]byte, 1*1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	
	err = client.RapidUploadOrByOSS("0", "test_1mb.bin", int64(len(data)), strings.NewReader(string(data)))
	if err != nil {
		fmt.Printf("上传失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("上传成功!")
}
