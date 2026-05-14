package aliyun_to_115

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	//"strconv"
	"strings"
	"encoding/json"
	"time"
	"database/sql"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"sort"
	"bytes"
	"path"
	"path/filepath"
	_ "github.com/mattn/go-sqlite3"

	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	"github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_share2open"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
)

type sync115Client struct {
	p115 *_115.Pan115
}

func newSync115Client(cookie string) (*sync115Client, error) {
	p115 := &_115.Pan115{}
	p115.Addition.Cookie = cookie
	if err := p115.Init(context.Background()); err != nil {
		return nil, err
	}
	return &sync115Client{p115: p115}, nil
}

func (c *sync115Client) uploadTo115(ctx context.Context, stream model.FileStreamer, dstDirID string) (model.Obj, error) {
	dstDir := &model.Object{ID: dstDirID}
	up := func(progress float64) {}
	return c.p115.Put(ctx, dstDir, stream, up)
}

func (c *sync115Client) removeFrom115(ctx context.Context, file model.Obj) error {
	return c.p115.Remove(ctx, file)
}

func (c *sync115Client) Drop() {
	if c.p115 != nil {
		c.p115.Drop(context.Background())
	}
}

func (d *AliyunTo115) doSyncLoop() {
	interval := time.Duration(d.SyncInterval) * time.Second
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		d.doSync()
	}
}

var (
	currentStats   *syncStats
	currentStatsMu sync.Mutex
)

var providerWhiteList = []string{
	"AliyundriveOpen",
	"AliyundriveShare2Open",
	"115 Share",
	"115 Cloud",
	"unknown", // 比如驱动MountPath是"/每日更新/XXX"， "/每日更新"找不到驱动，返回unknown
	"Alias",
}

type syncStats struct {
	total   int64
	skipped int64
	noLink  int64
	failed  int64
	synced  int64
	rapid   int64
	normal  int64
}

func initDBBreakpoint(db *sql.DB) {
	createTableSQL := `CREATE TABLE IF NOT EXISTS sync_state (
		key TEXT PRIMARY KEY,
		value TEXT
	);`
	_, err := db.Exec(createTableSQL)
	if err != nil {
		fmt.Printf("创建断点数据表失败: %v", err)
	}
}

func getBreakpoint(db *sql.DB) string {
	var val string
	err := db.QueryRow("SELECT value FROM sync_state WHERE key = 'breakpoint'").Scan(&val)
	if err != nil {
		if err != sql.ErrNoRows {
			fmt.Printf("读取断点失败: %v", err)
		}
		return ""
	}
	return val
}

func setBreakpoint(db *sql.DB, path string) {
	_, err := db.Exec("REPLACE INTO sync_state (key, value) VALUES ('breakpoint', ?)", path)
	if err != nil {
		fmt.Printf("更新断点失败 [%s]: %v", path, err)
	}
}

func clearBreakpoint(db *sql.DB) {
	_, err := db.Exec("DELETE FROM sync_state WHERE key = 'breakpoint'")
	if err != nil {
		fmt.Printf("清空断点失败: %v", err)
	}
}

func selfTerminate() {
	p, _ := os.FindProcess(os.Getpid())
	p.Signal(syscall.SIGTERM)
}

func (d *AliyunTo115) doSync() {
	d.syncLoopMu.Lock()
	if d.syncRunning {
		d.syncLoopMu.Unlock()
		return
	}
	d.syncRunning = true
	d.userInt = true
	d.syncLoopMu.Unlock()

	defer func() {
		d.syncLoopMu.Lock()
		d.syncRunning = false
		d.syncLoopMu.Unlock()
	}()

	ctx := context.Background()
	stats := &syncStats{}
	currentStatsMu.Lock()
	currentStats = stats
	currentStatsMu.Unlock()

	// 注册信号处理，Ctrl+C 时打印进度
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done() // 等待信号触发
		currentStatsMu.Lock()
		defer currentStatsMu.Unlock()
		if currentStats != nil && d.userInt {
			fmt.Printf("\n[aliyun_to_115] ===== 用户中断: 跳过%v / 秒传%v / 正常%v / 失败%v =====\n",
				currentStats.skipped, currentStats.rapid, currentStats.normal, currentStats.failed)
		}
	}()

	// 用户配置的目标 115 根目录
	configRootID := d.RootFolderID
	if configRootID == "" {
		configRootID = "0"
	}

	strmDBFile := filepath.Join(d.basePath, "data", "work.db")
	db2, err := sql.Open("sqlite3", strmDBFile)
	if err != nil {
		fmt.Printf("[aliyun_to_115] 打开work.db失败: %v\n", err)
		return
	}
	defer db2.Close()

	// ========== strm.txt 模式检测（SQLite） ==========
	strmFile := filepath.Join(d.basePath, "strm.txt")
	if _, err := os.Stat(strmFile); err == nil {
		// strm.txt 存在，切换为文件同步模式

		// 初始化 SQLite
		if err := os.MkdirAll(filepath.Join(d.basePath, "data"), 0755); err != nil {
			fmt.Printf("[aliyun_to_115] 创建data目录失败: %v\n", err)
			return
		}

		// 建表
		if _, err := db2.Exec(`CREATE TABLE IF NOT EXISTS strm_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			line TEXT NOT NULL UNIQUE
		)`); err != nil {
			fmt.Printf("[aliyun_to_115] 建表失败: %v\n", err)
			return
		}

		// 检查是否需要从 strm.txt 初始化
		var count int
		row := db2.QueryRow("SELECT COUNT(*) FROM strm_tasks")
		if row.Scan(&count); count == 0 {
			// strm.txt -> 写入数据库
			data, err := os.ReadFile(strmFile)
			if err != nil {
				fmt.Printf("[aliyun_to_115] 读取strm.txt失败: %v\n", err)
				return
			}
			tx, _ := db2.Begin()
			for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				l = strings.TrimSpace(l)
				if l == "" || strings.HasPrefix(l, "#") {
					continue
				}
				tx.Exec("INSERT INTO strm_tasks(line) VALUES (?)", l)
			}
			tx.Commit()
			fmt.Printf("[aliyun_to_115] 从strm.txt加载任务到数据库\n")
		}

		// 逐条处理：SELECT -> sync -> DELETE
		for {
			var recID int64
			var line string
			err := db2.QueryRow("SELECT id, line FROM strm_tasks ORDER BY id ASC LIMIT 1").Scan(&recID, &line)
			if err == sql.ErrNoRows {
				break // 全部完成
			}
			if err != nil {
				fmt.Printf("[aliyun_to_115] 查询失败: %v\n", err)
				break
			}

			line = strings.TrimSpace(line)
			if line == "" {
				db2.Exec("DELETE FROM strm_tasks WHERE id = ?", recID)
				continue
			}
			parts := strings.SplitN(line, "#", 2)
			var srcRaw, dstRaw string
			if len(parts) == 2 {
				srcRaw = strings.TrimSpace(parts[1])
				dstRaw = strings.TrimSpace(parts[0])
			} else if len(parts) == 1 {
				srcRaw = strings.TrimSpace(parts[0])
				dstRaw = strings.TrimSpace(parts[0])
			} else {
				db2.Exec("DELETE FROM strm_tasks WHERE id = ?", recID)
				continue
			}

			if strings.HasPrefix(srcRaw, "http://xiaoya.host") || strings.HasPrefix(srcRaw, "https://xiaoya.host") {
				if u, err := url.Parse(srcRaw); err == nil {
					srcRaw, _ = url.QueryUnescape(u.Path)
					srcRaw = strings.TrimPrefix(srcRaw, "/d")
				}
			}

			dstPath := "/" + strings.TrimPrefix(dstRaw, "/")
			srcPath := srcRaw

			srcExt := filepath.Ext(srcPath)
			if srcExt != "" {
				ext := filepath.Ext(dstPath)
				if ext == ".strm" {
					dstPath = strings.TrimSuffix(dstPath, ext) + srcExt
				}
			}

			failed := true
			if strings.HasPrefix(srcPath, "http://") || strings.HasPrefix(srcPath, "https://") {
				if err := d.processSingleFile_http(ctx, srcPath, dstPath, stats); err == nil {
					failed = false
				}
			} else if strings.HasPrefix(srcPath, "file://") {
				if err := d.processSingleFile_file(ctx, srcPath, dstPath, stats); err == nil {
					failed = false
				}
			} else {
				if err := d.processSingleFile(ctx, srcPath, dstPath, stats); err == nil {
					failed = false
				}
			}
			
			if failed {
				failedLine := fmt.Sprintf("%s#%s\n", srcPath, dstPath)
				if f, err := os.OpenFile("./failed.txt", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
					f.WriteString(failedLine)
					f.Close()
				}		
			}
			db2.Exec("DELETE FROM strm_tasks WHERE id = ?", recID)
		}

		fmt.Printf("[aliyun_to_115] ===== 同步完成: 跳过%v / 秒传%v / 正常%v / 失败%v =====\n",
			stats.skipped, stats.rapid, stats.normal, stats.failed)
		d.userInt = false
		if d.RunOnce {
			selfTerminate()
		}
		return
	}

	// ========== 驱动遍历模式 ==========
	d.discoverAliyunStorages()
	initDBBreakpoint(db2)

	breakpointPath := getBreakpoint(db2)
	fullScan := false
	if breakpointPath == "" {
		fmt.Println("[aliyun_to_115] 未发现断点记录，开始全新全量扫描...")
		fullScan = true
	} else {
		fmt.Printf("[aliyun_to_115] 读取到断点: %s，准备恢复扫描...\n", breakpointPath)
	}

	// 使用 fs.List 遍历所有文件，按 provider 白名单过滤
	fmt.Println("[aliyun_to_115] 开始通过fs.List遍历文件...")
	d.fsWalkAndSync(ctx, "/每日更新/动漫/国漫", stats, breakpointPath, &fullScan, db2)

	clearBreakpoint(db2)

	fmt.Printf("[aliyun_to_115] ===== 同步完成: 跳过%v / 秒传%v / 正常%v / 失败%v =====\n",
		stats.skipped, stats.rapid, stats.normal, stats.failed)

	if d.RunOnce {
		selfTerminate()
	}
	return
}

func getPair(spath string) (string, string) {
	if name, spath, ok := strings.Cut(spath, ":"); ok && !strings.Contains(name, "/") {
		return name, spath
	}
	return path.Base(spath), spath
}

func getRealProvider(ctx context.Context, itemPath string) string {
	drv, err := fs.GetStorage(itemPath, &fs.GetStoragesArgs{})
	if err != nil || drv == nil || drv.GetStorage() == nil {
		return "unknown"
	}

	s := drv.GetStorage()
	// 如果当前已经是具体的存储驱动（非Alias），直接返回
	if s.Driver != "Alias" {
		return s.Driver
	}

	// 1. 获取并解析 Addition 配置
	type AliasAddition struct {
		Paths string `json:"paths"`
	}
	var addition AliasAddition
	if err := json.Unmarshal([]byte(s.Addition), &addition); err != nil {
		return "Alias"
	}

	// 2. 模拟 Init 逻辑：构建映射表
	// 逻辑对齐：将 Paths 解析为 pathMap 和 rootOrder
	pathMap := make(map[string][]string)
	var rootOrder []string
	
	lines := strings.Split(addition.Paths, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v := getPair(line)
		if _, ok := pathMap[k]; !ok {
			rootOrder = append(rootOrder, k)
		}
		pathMap[k] = append(pathMap[k], v)
	}

	// 3. 计算相对路径
	relPath := strings.TrimPrefix(itemPath, s.MountPath)
	if !strings.HasPrefix(relPath, "/") {
		relPath = "/" + relPath
	}

	if relPath == "/" {
		return "Alias"
	}

	// 4. 根据 rootOrder 的长度执行不同的查找逻辑 (对齐 Init 的 switch 逻辑)
	switch len(rootOrder) {
	case 0:
		return "Alias"
		
	case 1:
		// case 1: 单一根节点合并模式 (Union)
		// 所有路径都映射到了同一个根别名下
		targets := pathMap[rootOrder[0]]
		for _, target := range targets {
			// 在合并模式下，relPath 是相对于 Alias 根的
			realPath := strings.TrimSuffix(target, "/") + relPath
			_, err := fs.Get(ctx, realPath, &fs.GetArgs{NoLog: true})
			if err == nil {
				// 递归探测，确保钻取到最底层
				return getRealProvider(ctx, realPath)
			}
		}

	default:
		// default: 多根节点模式
		// 必须匹配前缀才能定位到对应的物理路径
		for _, prefix := range rootOrder {
			if strings.HasPrefix(relPath, prefix) {
				targets := pathMap[prefix]
				subPath := strings.TrimPrefix(relPath, prefix)
				for _, target := range targets {
					realPath := strings.TrimSuffix(target, "/") + "/" + strings.TrimPrefix(subPath, "/")
					_, err := fs.Get(ctx, realPath, &fs.GetArgs{NoLog: true})
					if err == nil {
						return getRealProvider(ctx, realPath)
					}
				}
			}
		}
	}

	return "Alias"
}

// fsWalkAndSync 使用 fs.List 遍历所有文件，通过 provider 白名单过滤
func (d *AliyunTo115) fsWalkAndSync(ctx context.Context, currentPath string, stats *syncStats, breakpointPath string, fullScan *bool, db *sql.DB) error {
	if !strings.HasSuffix(currentPath, "/") {
		currentPath += "/"
	}

	files, err := fs.List(ctx, currentPath, &fs.ListArgs{NoLog: true})
	if err != nil {
		return err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].GetName() < files[j].GetName()
	})

	for _, f := range files {
		provider := getRealProvider(ctx, filepath.Join(currentPath, f.GetName()))
		fmt.Printf("provider:%s, path:%s\n", provider, filepath.Join(currentPath, f.GetName()))
		inWhiteList := false
		for _, p := range providerWhiteList {
			if provider == p {
				inWhiteList = true
				break
			}
		}
		if !inWhiteList {
			continue
		}

		if f.IsDir() {
			subPath := currentPath + f.GetName() + "/"
			if !strings.HasPrefix(subPath, "/每日更新/动漫/国漫/所有/") {
				continue
			}
			if !(*fullScan) {
				if !strings.HasPrefix(breakpointPath, subPath) {
					continue
				}
			}

			d.fsWalkAndSync(ctx, subPath, stats, breakpointPath, fullScan, db)
		} else {
			fullPath := currentPath + f.GetName()

			if !(*fullScan) {
				if fullPath == breakpointPath {
					fmt.Printf("\n>>> 精确匹配到断点文件: %s <<<\n", fullPath)
					fmt.Println(">>> 状态已切换，开始从此处恢复同步任务...")
					time.Sleep(1 * time.Second)
					*fullScan = true
					clearBreakpoint(db)
				} else {
					continue
				}
			}

			if *fullScan {
				setBreakpoint(db, fullPath) 
				stats.total++
				if err := d.processSingleFile(ctx, fullPath, fullPath, stats); err != nil {
					failedLine := fmt.Sprintf("%s#%s\n", fullPath, fullPath)
					if f, err := os.OpenFile("./failed.txt", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
						f.WriteString(failedLine)
						f.Close()
					}	
				}
			}
		}
	}

	return nil
}

func (d *AliyunTo115) walkAndSync(ctx context.Context, aliyun aliyunStorage, currentPath, aliParentID string, stats *syncStats, breakpointPath string, fullScan *bool, db *sql.DB) error {
	if !strings.HasSuffix(currentPath, "/") {
		currentPath += "/"
	}

	files, err := aliyun.List(ctx, &model.Object{ID: aliParentID}, model.ListArgs{})
	if err != nil {
		return err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].GetName() < files[j].GetName()
	})

	for _, f := range files {
		if f.IsDir() {
			subPath := currentPath + f.GetName() + "/"

			if !(*fullScan) {
				if !strings.HasPrefix(breakpointPath, subPath) {
					continue 
				}
			}

			_ = d.walkAndSync(ctx, aliyun, subPath, f.GetID(), stats, breakpointPath, fullScan, db)
			
		} else {
			fullPath := currentPath + f.GetName()

			if !(*fullScan) {
				if fullPath == breakpointPath {
					fmt.Printf("\n>>> 精确匹配到断点文件: %s <<<\n", fullPath)
					fmt.Println(">>> 状态已切换，开始从此处恢复同步任务...")
					time.Sleep(1 * time.Second)
					*fullScan = true
				} else {
					continue 
				}
			}

			if *fullScan {
				setBreakpoint(db, fullPath) 
				stats.total++

				if err := d.processSingleFile(ctx, fullPath, fullPath, stats); err != nil {
					failedLine := fmt.Sprintf("%s#%s\n", fullPath, fullPath)
					if f, err := os.OpenFile("./failed.txt", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
						f.WriteString(failedLine)
						f.Close()
					}	
				}
			}
		}
	}
	return nil
}

func getFirstDirPurePath(p string) string {
	p = path.Clean(p)
	if p == "/" || p == "." {
		return ""
	}
	for {
		parent := path.Dir(p)
		if parent == "." || parent == "/" {
			return strings.TrimPrefix(p, "/")
		}
		p = parent
	}
}

func (d *AliyunTo115) getOrCreateDirID(ctx context.Context, fullPath string) (string, error) {
	fullPath = path.Clean(fullPath)
	
	if fullPath == "/" || fullPath == "." || fullPath == "" {
		dirObj, err := fs.Get(ctx, "/", &fs.GetArgs{NoLog: true})
		if err != nil {
			return "", fmt.Errorf("获取根目录信息失败: %w", err)
		}
		return dirObj.GetID(), nil
	}

	dirObj, err := fs.Get(ctx, fullPath, &fs.GetArgs{NoLog: true})
	if err == nil {
		if dirObj.IsDir() {
			return dirObj.GetID(), nil
		}
		return "", fmt.Errorf("路径冲突：目标是文件而非文件夹: %s", fullPath)
	}
	
	parentPath := path.Dir(fullPath)
	if parentPath != fullPath {
		_, err = d.getOrCreateDirID(ctx, parentPath)
		if err != nil {
			return "", fmt.Errorf("创建父目录失败 [%s]: %w", parentPath, err)
		}
	}

	storage, actualPath, err := op.GetStorageAndActualPath(fullPath)
	if err != nil {
		return "", fmt.Errorf("解析存储路径失败: %w", err)
	}

	err = op.MakeDir(ctx, storage, actualPath)
	if err != nil {
		return "", fmt.Errorf("创建目录失败 [%s]: 错误: %w", fullPath, err)
	}

	time.Sleep(500 * time.Millisecond)
	fs.List(ctx, parentPath, &fs.ListArgs{
			NoLog: true,
			Refresh: true,
		})

	dirObj, err = fs.Get(ctx, fullPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		return "", fmt.Errorf("获取新建目录 ID 失败 [%s]: %w", fullPath, err)
	}
	
	return dirObj.GetID(), nil
}

func (d *AliyunTo115) processSingleFile_http(ctx context.Context, srcPath string, dstPath string, stats *syncStats) error {
	p115DirStr := d.GetStorage().MountPath + path.Dir(dstPath)
	p115DirID, err := d.getOrCreateDirID(ctx, p115DirStr)
	if err != nil {
		fmt.Printf("[aliyun_to_115] 准备115目录失败 [%s]: %v\n", p115DirStr, err)
		return err
	}

	// 1. HEAD 请求获取文件大小
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, srcPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("[aliyun_to_115] HEAD请求失败 [%s]: %v\n", srcPath, err)
		stats.failed++
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Printf("[aliyun_to_115] HEAD请求非200 [%s]: status=%d\n", srcPath, resp.StatusCode)
		stats.failed++
		return fmt.Errorf("HEAD status %d", resp.StatusCode)
	}
	fileSize := resp.ContentLength
	if fileSize <= 0 {
		fmt.Printf("[aliyun_to_115] 无法获取文件大小 [%s]\n", srcPath)
		stats.failed++
		return fmt.Errorf("content-length invalid")
	}

	// 2. 超过100MB跳过
	if fileSize > 100*1024*1024 {
		fmt.Printf("[aliyun_to_115] 文件超过100MB跳过 [%s]: %d\n", srcPath, fileSize)
		stats.skipped++
		return nil
	}

	// 3. 下载完整内容到内存 (修复点：必须使用 GET 方法)
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, srcPath, nil)

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		fmt.Printf("[aliyun_to_115] 下载请求失败 [%s]: %v\n", srcPath, err)
		stats.failed++
		return err
	}
	defer resp2.Body.Close()

	// 检查 GET 请求的响应码
	if resp2.StatusCode != 200 {
		fmt.Printf("[aliyun_to_115] 下载请求非200 [%s]: status=%d\n", srcPath, resp2.StatusCode)
		stats.failed++
		return fmt.Errorf("GET status %d", resp2.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp2.Body, 100*1024*1024+1))
	if err != nil {
		fmt.Printf("[aliyun_to_115] 读取内容发生异常 [%s]: %v\n", srcPath, err)
		stats.failed++
		return err
	}

	// 修复点：明确抛出长度不匹配的错误，避免返回 nil
	if int64(len(data)) != fileSize {
		errMismatch := fmt.Errorf("文件大小不匹配: 期望 %d, 实际读取 %d", fileSize, len(data))
		fmt.Printf("[aliyun_to_115] 读取内容失败 [%s]: %v\n", srcPath, errMismatch)
		stats.failed++
		return errMismatch
	}

	// 4. 计算 SHA1
	sha1Str := utils.HashData(utils.SHA1, data)

	cacheKey := srcPath + "/" + sha1Str
	d.syncLoopMu.Lock()
	if d.syncedCache[cacheKey] {
		d.syncLoopMu.Unlock()
		stats.skipped++
		return nil
	}
	d.syncLoopMu.Unlock()

	// 5. 创建内存流
	stream := newMemFileStreamer(path.Base(dstPath), fileSize, sha1Str, data)

	// 6. 上传（重试3次）
	var result model.Obj
	var uploadErr error
	start := time.Now()
	for attempt := 1; attempt <= 3; attempt++ {
		result, uploadErr = d.p115Client.uploadTo115(ctx, stream, p115DirID)
		if uploadErr == nil && result != nil {
			break
		}
		if attempt < 3 {
			time.Sleep(1 * time.Second)
		}
	}
	elapsed := time.Since(start)

	if uploadErr != nil || result == nil {
		fmt.Printf("[aliyun_to_115] 上传失败: %s : %v\n", srcPath, uploadErr)
		stats.failed++
		return uploadErr
	}

	if stream.rapidUpload {
		fmt.Printf("[aliyun_to_115] ⚡ 秒传成功: %s -> %s [%v]\n", srcPath, p115DirStr+"/"+path.Base(dstPath), elapsed)
		stats.rapid++
	} else {
		fmt.Printf("[aliyun_to_115] 📤 正常上传: %s -> %s [%v]\n", srcPath, p115DirStr+"/"+path.Base(dstPath), elapsed)
		stats.normal++
	}

	if d.DeleteAfterSync {
		_ = d.p115Client.removeFrom115(ctx, result)
	}

	d.syncLoopMu.Lock()
	d.syncedCache[cacheKey] = true
	d.syncLoopMu.Unlock()
	d.saveSyncedCache(cacheKey)
	stats.synced++
	return nil
}

func (d *AliyunTo115) processSingleFile_file(ctx context.Context, srcPath string, dstPath string, stats *syncStats) error {
	p115DirStr := d.GetStorage().MountPath + path.Dir(dstPath)
	p115DirID, err := d.getOrCreateDirID(ctx, p115DirStr)
	if err != nil {
		fmt.Printf("[aliyun_to_115] 准备115目录失败 [%s]: %v\n", p115DirStr, err)
		return err
	}

	// 去掉 file:// 前缀得到真实路径
	localPath := strings.TrimPrefix(srcPath, "file://")
	if localPath == srcPath {
		localPath = strings.TrimPrefix(srcPath, "file:")
	}

	// 打开文件用于计算 SHA1
	f, err := os.Open(localPath)
	if err != nil {
		fmt.Printf("[aliyun_to_115] 打开文件失败 [%s]: %v\n", localPath, err)
		stats.failed++
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		fmt.Printf("[aliyun_to_115] 获取文件信息失败 [%s]: %v\n", localPath, err)
		stats.failed++
		return err
	}
	fileSize := fi.Size()

	// 计算 SHA1（utils.HashFile 会自动 Seek 回开头）
	sha1Str, err := utils.HashFile(utils.SHA1, f)
	if err != nil {
		fmt.Printf("[aliyun_to_115] 计算SHA1失败 [%s]: %v\n", localPath, err)
		stats.failed++
		return err
	}

	cacheKey := srcPath + "/" + sha1Str
	d.syncLoopMu.Lock()
	if d.syncedCache[cacheKey] {
		d.syncLoopMu.Unlock()
		stats.skipped++
		return nil
	}
	d.syncLoopMu.Unlock()

	// 重新打开用于上传
	f2, err := os.Open(localPath)
	if err != nil {
		stats.failed++
		return err
	}
	defer f2.Close()

	stream := newFileStreamer(path.Base(dstPath), fileSize, sha1Str, f2)

	var result model.Obj
	var uploadErr error
	start := time.Now()
	for attempt := 1; attempt <= 3; attempt++ {
		result, uploadErr = d.p115Client.uploadTo115(ctx, stream, p115DirID)
		if uploadErr == nil && result != nil {
			break
		}
		if attempt < 3 {
			time.Sleep(1 * time.Second)
		}
	}
	elapsed := time.Since(start)

	if uploadErr != nil || result == nil {
		fmt.Printf("[aliyun_to_115] 上传失败: %s : %v\n", srcPath, uploadErr)
		stats.failed++
		return uploadErr
	}

	if stream.rapidUpload {
		fmt.Printf("[aliyun_to_115] ⚡ 秒传成功: %s -> %s [%v]\n", srcPath, p115DirStr+"/"+path.Base(dstPath), elapsed)
		stats.rapid++
	} else {
		fmt.Printf("[aliyun_to_115] 📤 正常上传: %s -> %s [%v]\n", srcPath, p115DirStr+"/"+path.Base(dstPath), elapsed)
		stats.normal++
	}

	if d.DeleteAfterSync {
		_ = d.p115Client.removeFrom115(ctx, result)
	}

	d.syncLoopMu.Lock()
	d.syncedCache[cacheKey] = true
	d.syncLoopMu.Unlock()
	d.saveSyncedCache(cacheKey)
	stats.synced++
	return nil
}

func (d *AliyunTo115) processSingleFile(ctx context.Context, srcPath string, dstPath string, stats *syncStats) error {
	aliyun, _, err := op.GetStorageAndActualPath(srcPath)
	if err != nil {
		fmt.Printf("[aliyun_to_115] 获取源文件驱动失败， fullPath=%s : %v\n", srcPath, err)
		return err
		stats.failed++
	}

	f, err := fs.Get(ctx, srcPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		fmt.Printf("[aliyun_to_115] 获取源文件对象失败， fullPath=%s : %v\n", srcPath, err)
		stats.failed++
		return err
	}
	if f.IsDir() {
		stats.failed++
		return fmt.Errorf("[aliyun_to_115] 源文件对象是目录， fullPath=%s", srcPath)
	}

	p115DirStr := d.GetStorage().MountPath + path.Dir(dstPath)
	p115DirID, err := d.getOrCreateDirID(ctx, p115DirStr)
	if err != nil {
		stats.failed++
		fmt.Printf("[aliyun_to_115] 准备115目标目录失败 [%s]: %v\n", p115DirStr, err)
		return err
	}

	// 缓存逻辑
	cacheKey := srcPath + "/" + f.GetID()
	hashInfo := f.GetHash()
	sha1Str := hashInfo.GetHash(utils.SHA1)
	if sha1Str != "" {
		cacheKey = srcPath + "/" + sha1Str
	}

	d.syncLoopMu.Lock()
	if d.syncedCache[cacheKey] {
		d.syncLoopMu.Unlock()
		stats.skipped++
		return nil
	}
	d.syncLoopMu.Unlock()

	// 115 share 风控严重，等待 1 秒
	if _, ok := aliyun.(*aliyundrive_share2open.AliyundriveShare2Open); ok {
		time.Sleep(1 * time.Second)
	}

	link, err := aliyun.Link(ctx, f, model.LinkArgs{})
	// 兼容某些驱动可能重新获取一次 Hash
	if driver, ok := aliyun.(*aliyundrive_share2open.AliyundriveShare2Open); ok {
		sha1Str = driver.GetHash(ctx, f, model.LinkArgs{})
	}

	if err != nil || link == nil || link.URL == "" {
		stats.noLink++
		return errors.New("no link")
	}

	// 规避115 Share List的Size错误
	fileSize := f.GetSize()
	provider, _ := model.GetProvider(f)
	if provider == "115 Share" {
		req, _ := http.NewRequestWithContext(ctx, http.MethodHead, link.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Printf("[aliyun_to_115] 115直链HEAD请求失败 [%s]: %v\n", srcPath, err)
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			fmt.Printf("[aliyun_to_115] 115直链HEAD请求非200 [%s]: status=%d\n", srcPath, resp.StatusCode)
			return fmt.Errorf("HEAD status %d", resp.StatusCode)
		}
		fileSize = resp.ContentLength
		if fileSize <= 0 {
			fmt.Printf("[aliyun_to_115] 115直链无法获取文件大小 [%s]\n", srcPath)
			return fmt.Errorf("content-length invalid")
		}
	}

	stream := newUrlFileStreamer(path.Base(dstPath), fileSize, sha1Str, link.URL)

	fmt.Printf("fileSize %d, sha1Str %s dstPath %s", fileSize, sha1Str, dstPath)

	var result model.Obj
	var uploadErr error
	start := time.Now()
	for attempt := 1; attempt <= 3; attempt++ {
		result, uploadErr = d.p115Client.uploadTo115(ctx, stream, p115DirID)
		if uploadErr == nil && result != nil {
			break
		}
		if attempt < 3 {
			time.Sleep(1 * time.Second)
		}
	}
	elapsed := time.Since(start)

	if uploadErr != nil || result == nil {
		fmt.Printf("[aliyun_to_115] 上传失败: %s : %v\n", srcPath, uploadErr)
		stats.failed++
		return uploadErr
	}

	if stream.rapidUpload {
		fmt.Printf("[aliyun_to_115] ⚡ 秒传成功: %s -> %s [%v]\n", srcPath, p115DirStr+"/"+path.Base(dstPath), elapsed)
		stats.rapid++
	} else {
		fmt.Printf("[aliyun_to_115] 📤 正常上传: %s -> %s [%v]\n", srcPath, p115DirStr+"/"+path.Base(dstPath), elapsed)
		stats.normal++
	}

	if d.DeleteAfterSync {
		_ = d.p115Client.removeFrom115(ctx, result)
	}

	d.syncLoopMu.Lock()
	d.syncedCache[cacheKey] = true
	d.syncLoopMu.Unlock()
	d.saveSyncedCache(cacheKey)
	stats.synced++
	return nil
}

type memFileReader struct {
	*bytes.Reader
}

func (m *memFileReader) Close() error {
	return nil
}

type memFileStreamer struct {
	name        string
	size        int64
	sha1Str     string
	data        []byte
	offset      int64
	rapidUpload bool
}

func newMemFileStreamer(name string, size int64, sha1Str string, data []byte) *memFileStreamer {
	return &memFileStreamer{name: name, size: size, sha1Str: sha1Str, data: data}
}

func (f *memFileStreamer) GetID() string         { return "" }
func (f *memFileStreamer) GetName() string       { return f.name }
func (f *memFileStreamer) SetPath(path string)   { _ = path }
func (f *memFileStreamer) SetRapidUpload(b bool) { f.rapidUpload = b }
func (f *memFileStreamer) GetSize() int64        { return f.size }
func (f *memFileStreamer) ModTime() time.Time    { return time.Time{} }
func (f *memFileStreamer) CreateTime() time.Time { return time.Time{} }
func (f *memFileStreamer) IsDir() bool           { return false }
func (f *memFileStreamer) GetHash() utils.HashInfo { return utils.NewHashInfo(utils.SHA1, f.sha1Str) }
func (f *memFileStreamer) GetPath() string       { return "" }
func (f *memFileStreamer) GetMimetype() string   { return "application/octet-stream" }
func (f *memFileStreamer) NeedStore() bool       { return true }
func (f *memFileStreamer) IsForceStreamUpload() bool { return false }
func (f *memFileStreamer) GetExist() model.Obj   { return nil }
func (f *memFileStreamer) SetExist(model.Obj)    {}
func (f *memFileStreamer) Add(io.Closer)         {}
func (f *memFileStreamer) AddIfCloser(any)       {}

func (f *memFileStreamer) CacheFullAndWriter(up *model.UpdateProgress, w io.Writer) (model.File, error) {
	if w != nil {
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	return f.GetFile(), nil
}

func (f *memFileStreamer) GetFile() model.File {
	return &memFileReader{Reader: bytes.NewReader(f.data)}
}

func (f *memFileStreamer) Read(p []byte) (n int, err error) {
	if f.offset >= f.size {
		return 0, io.EOF
	}
	remaining := f.size - f.offset
	toRead := int64(len(p))
	if toRead > remaining {
		toRead = remaining
	}
	copy(p, f.data[f.offset:f.offset+toRead])
	f.offset += toRead
	return int(toRead), nil
}

func (f *memFileStreamer) Close() error { 
	return nil 
}

func (f *memFileStreamer) RangeRead(ra http_range.Range) (io.Reader, error) {
	start := ra.Start
	if start >= f.size {
		return bytes.NewReader(nil), nil
	}
	
	end := start + ra.Length
	if end > f.size {
		end = f.size
	}
	
	return bytes.NewReader(f.data[start:end]), nil
}

// fileStreamer 读取本地文件进行上传
type fileStreamer struct {
	name        string
	size        int64
	sha1Str     string
	file        *os.File
	rapidUpload bool
}

func newFileStreamer(name string, size int64, sha1Str string, file *os.File) *fileStreamer {
	return &fileStreamer{name: name, size: size, sha1Str: sha1Str, file: file}
}

func (f *fileStreamer) GetID() string         { return "" }
func (f *fileStreamer) GetName() string       { return f.name }
func (f *fileStreamer) SetPath(path string)   { _ = path }
func (f *fileStreamer) SetRapidUpload(b bool) { f.rapidUpload = b }
func (f *fileStreamer) GetSize() int64        { return f.size }
func (f *fileStreamer) ModTime() time.Time    { return time.Time{} }
func (f *fileStreamer) CreateTime() time.Time { return time.Time{} }
func (f *fileStreamer) IsDir() bool           { return false }
func (f *fileStreamer) GetHash() utils.HashInfo { return utils.NewHashInfo(utils.SHA1, f.sha1Str) }
func (f *fileStreamer) GetPath() string       { return "" }
func (f *fileStreamer) GetMimetype() string   { return "application/octet-stream" }
func (f *fileStreamer) NeedStore() bool       { return true }
func (f *fileStreamer) IsForceStreamUpload() bool { return false }
func (f *fileStreamer) GetExist() model.Obj   { return nil }
func (f *fileStreamer) SetExist(model.Obj)    {}
func (f *fileStreamer) Add(io.Closer)         {}
func (f *fileStreamer) AddIfCloser(any)       {}

func (f *fileStreamer) CacheFullAndWriter(up *model.UpdateProgress, w io.Writer) (model.File, error) {
	_, err := f.file.Seek(0, io.SeekStart)
	if err != nil {
		return nil, err
	}
	if w != nil {
		_, err = io.Copy(w, f.file)
		if err != nil {
			return nil, err
		}
		f.file.Seek(0, io.SeekStart)
	}
	return f.file, nil
}

func (f *fileStreamer) GetFile() model.File { 
	f.file.Seek(0, io.SeekStart)
	return f.file 
}

func (f *fileStreamer) Read(p []byte) (n int, err error) {
	return f.file.Read(p)
}

func (f *fileStreamer) Close() error { 
	return f.file.Close() 
}

// 重点修复部位：支持高并发分片读取
func (f *fileStreamer) RangeRead(ra http_range.Range) (io.Reader, error) {
	if f.file == nil {
		return nil, os.ErrClosed
	}
	// 使用 io.NewSectionReader，它依赖 ReadAt 进行无状态读取，不会互相干扰
	return io.NewSectionReader(f.file, ra.Start, ra.Length), nil
}

type urlFileStreamer struct {
	name     string
	path     string
	size     int64
	sha1Str  string
	url      string
	rapidUpload bool
	reader      io.Reader
	readerClose func() error
	file      model.File   // 缓存虚拟文件，避免重复创建
}

func (f *urlFileStreamer) GetID() string            { return "" }
func (f *urlFileStreamer) GetName() string          { return f.name }
func (f *urlFileStreamer) SetPath(path string)       { f.path = path }
func (f *urlFileStreamer) SetRapidUpload(b bool)     { f.rapidUpload = b }
func (f *urlFileStreamer) GetSize() int64            { return f.size }
func (f *urlFileStreamer) ModTime() time.Time        { return time.Time{} }
func (f *urlFileStreamer) CreateTime() time.Time     { return time.Time{} }
func (f *urlFileStreamer) IsDir() bool               { return false }
func (f *urlFileStreamer) GetHash() utils.HashInfo { return utils.NewHashInfo(utils.SHA1, f.sha1Str) }
func (f *urlFileStreamer) GetPath() string           { return f.path }
func (f *urlFileStreamer) GetMimetype() string       { return "application/octet-stream" }
func (f *urlFileStreamer) NeedStore() bool           { return true }
func (f *urlFileStreamer) IsForceStreamUpload() bool { return false }
func (f *urlFileStreamer) GetExist() model.Obj        { return nil }
func (f *urlFileStreamer) SetExist(model.Obj)        {}
func (f *urlFileStreamer) Add(io.Closer)             {}
func (f *urlFileStreamer) AddIfCloser(any)           {}

func newUrlFileStreamer(name string, size int64, sha1Str, url string) *urlFileStreamer {
	return &urlFileStreamer{name: name, size: size, sha1Str: sha1Str, url: url}
}

func (f *urlFileStreamer) Read(p []byte) (n int, err error) {
	if f.reader == nil {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		f.reader = resp.Body
		f.readerClose = resp.Body.Close
	}
	return f.reader.Read(p)
}

func (f *urlFileStreamer) Close() error {
	if f.readerClose != nil {
		f.readerClose()
		f.readerClose = nil
	}
	return nil
}

func (f *urlFileStreamer) RangeRead(ra http_range.Range) (io.Reader, error) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.url, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", ra.Start, ra.Start+ra.Length-1))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (f *urlFileStreamer) GetFile() model.File {
	if f.file != nil {
		f.file.Seek(0, io.SeekStart)
	}
	return f.file
}

// VirtualFile 按需发 HTTP Range 请求，不落盘
type VirtualFile struct {
	url        string
	client     *http.Client
	size       int64
	currOffset int64
	ctx        context.Context
}

func (v *VirtualFile) Read(p []byte) (n int, err error) {
	n, err = v.ReadAt(p, v.currOffset)
	if n > 0 {
		v.currOffset += int64(n) // 必须推进偏移量
	}
	return n, err
}

func (v *VirtualFile) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= v.size && v.size > 0 {
		return 0, io.EOF
	}
	endPos := off + int64(len(p)) - 1
	if v.size > 0 && endPos >= v.size {
		endPos = v.size - 1
	}
	req, err := http.NewRequestWithContext(v.ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, endPos))
	resp, err := v.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("http error: %d", resp.StatusCode)
	}

	n, err = io.ReadFull(resp.Body, p)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		// Server returned fewer bytes than requested (e.g., CDN range limit)
		// n contains actual bytes read
		err = nil
	}
	return n, err
}

func (v *VirtualFile) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = v.currOffset + offset
	case io.SeekEnd:
		newOffset = v.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if newOffset < 0 {
		return 0, errors.New("seek position out of range")
	}
	v.currOffset = newOffset
	return v.currOffset, nil
}

func (v *VirtualFile) Close() error { return nil }

func (f *urlFileStreamer) CacheFullAndWriter(up *model.UpdateProgress, w io.Writer) (model.File, error) {
	if f.file != nil {
		f.file.Seek(0, io.SeekStart)
		return f.file, nil
	}

	// HEAD 获取文件大小
	// var fileSize int64
	// if resp, err := http.DefaultClient.Head(f.url); err == nil {
	// 	fileSize, _ = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	// 	resp.Body.Close()
	// }

	httpClient := &http.Client{}

	vf := &VirtualFile{
		url:    f.url,
		client: httpClient,
		//size:   fileSize,
		size:   f.size,
		ctx:    context.Background(),
	}
	f.file = vf

	if w != nil {
		go func() {
			r, _ := http.Get(f.url)
			if r != nil {
				defer r.Body.Close()
				io.Copy(w, r.Body)
			}
		}()
	}

	return vf, nil
}

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	var exp int
	for floatSize := float64(size); floatSize >= unit; floatSize /= unit {
		exp++
	}
	suffix := "BKMGTPE"[exp : exp+1]
	div := 1.0
	for i := 0; i < exp; i++ {
		div *= unit
	}
	return fmt.Sprintf("%.1f %sB", float64(size)/div, suffix)
}
