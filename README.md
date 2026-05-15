# AliyunTo115 Driver

阿里云盘 / 115分享 → 115 文件 SHA1 预注册同步驱动。

## 功能

- **自动扫描所有阿里云盘存储**：遍历 OpenList 中所有 `AliyundriveOpen`、`AliyundriveShare2Open`、`115Share` 类型的存储，递归同步文件到 115
- **SHA1 预注册**：文件上传到 115 后立即删除，只保留 SHA1 记录，使其他 115 用户可以享受秒传
- **跨驱动全局去重**：同一文件在多个阿里云盘存储中出现，只上传一次
- **定时同步**：后台 Goroutine 定期扫描，可配置扫描间隔
- **同步记录持久化**：重启后 dedup cache 不丢失（SQLite 持久化）
- **strm.txt 批量同步**：支持从 `strm.txt` 读取任务列表，数据库队列化处理
- **HTTP/File 源支持**：strm 模式支持 `http://`、`https://`、`file://` 协议直接下载

## 同步模式

### 驱动遍历模式（默认）

扫描 OpenList 中所有阿里云盘 / 115分享 存储，递归遍历文件并同步到 115。

### strm.txt 批量模式

在驱动目录下放置 `strm.txt`，每行格式：

```
[目标路径] #[源路径]
```

示例：

```
/电影/你好，李焕英.mkv #/小雅/你好，李焕英.mkv
/音乐/周杰伦 - 晴天.flac #file:///mnt/sda1/music/周杰伦 - 晴天.flac
/剧集/绝命毒师 S01E01.mp4 #https://example.com/tv/breaking-bad-s01e01.mp4
```

**xiaoya.host 特殊路径**（自动解析 `/d` 前缀）：
```
/影视/电影.mkv #http://xiaoya.host/d/电影.mkv
```

**strm 文件扩展名处理**：若目标路径以 `.strm` 结尾，程序自动将源文件扩展名附加到目标路径：
```
# 源：/d/电影.mp4 → 目标：/影视/电影.mkv（自动去掉 .strm，加 .mp4）
/影视/电影.strm #http://xiaoya.host/d/电影.mp4
```

**文件夹格式支持**：若源路径（`#` 后）是目录，程序会递归遍历该目录并保持目录结构同步到目标路径：
```
# 源：小雅 /小雅/源/路径/电影 目录 → 目标：/115/目标/路径/影视/电影 目录（保持目录结构）
/115/目标/路径/影视/电影 #/小雅/源/路径/电影
```

strm 任务写入 `data/work.db` 的 `strm_tasks` 表，程序崩溃重启后可从断点恢复。

## 机制说明

```
源驱动文件 → 下载链接 → 上传到115(秒传或普通) → 删除115文件 → SHA1保留在115系统
```

- **秒传成功**：SHA1 已在 115 注册 → 快速完成 → 删除文件
- **普通上传**：SHA1 未注册 → 完整上传 → 删除文件 → SHA1 注册完成

### 各源驱动 SHA1 获取方式

| 驱动 | SHA1 来源 | 说明 |
|------|-----------|------|
| `AliyundriveOpen` | listing 直接返回 | 无需额外处理 |
| `AliyundriveShare2Open` | 需通过 `Copy2Myali` → `GetmyLink` 获取 | 首次同步后缓存到内存 Hash_dict |
| `115Share` | listing 直接返回 | 无需额外处理 |
| `http://` / `https://` | 下载内容后计算 | 仅支持 ≤100MB |
| `file://` | 读取本地文件计算 | 仅支持 ≤100MB |

## 驱动行为

- **对外表现**：完整的 115 驱动，用户可以正常浏览、上传、下载 115 文件
- **后台任务**：自动扫描所有阿里云盘存储，同步文件 SHA1 到 115

## 配置项

| 配置项 | 类型 | 必填 | 默认值 | 说明 |
|--------|------|------|--------|------|
| `open115_cookie` | string | 是 | - | 115 登录 Cookie |
| `sync_interval` | number | 否 | 600 | 同步间隔（秒），默认 10 分钟 |
| `qrcode_token` | string | 否 | - | 115 二维码登录 Token |
| `qrcode_source` | string | 否 | linux | 二维码设备来源 |
| `page_size` | number | 否 | 1000 | 分页大小 |
| `limit_rate` | float | 否 | 2 | API 限速（1/[limit_rate] 秒/请求） |
| `root_folder_id` | string | 否 | 0 | 115 根目录 ID |
| `delete_after_sync` | bool | 否 | false | 同步完成后是否删除 115 上的文件（SHA1 秒传记录仍会保留） |

## 使用方式

### 驱动遍历模式

1. 在 OpenList 添加存储，选择类型 **AliyunTo115**
2. 填写 115 Cookie 和同步间隔
3. 挂载该存储后，可以正常作为 115 驱动使用
4. 后台自动扫描所有阿里云盘 / 115分享 存储，同步文件到 115

### strm.txt 批量模式

1. 在 OpenList 添加存储，选择类型 **AliyunTo115**
2. 在该存储的本地目录下放置 `strm.txt`（与 `data` 目录同级）
3. 程序启动时检测到 `strm.txt` 后自动切换为 strm 模式，从数据库队列读取任务逐个处理

## 清理同步缓存

如果需要重新同步所有文件（例如更换了源存储内容），可以清除同步缓存记录：

```bash
openlist cache clear
```

清除数据库中的所有同步记录（`aliyun_sync_cache` 表），重启后内存 cache 也会清空，所有文件会被重新同步。

## 工作流程

### 驱动遍历模式

```
Init()
  ├─ 初始化 Pan115 客户端
  ├─ SQLite 建表（不存在则创建 aliyun_sync_cache）
  ├─ 从数据库预热 syncedCache
  └─ 启动 doSyncLoop()

doSyncLoop() [定时]
  └─ doSync()
      ├─ discoverAliyunStorages() 遍历所有存储
      │     筛选：AliyundriveOpen | AliyundriveShare2Open | 115Share
      ├─ 根据 MountPath 在 115 创建目录层级
      └─ walkAndSync() 递归遍历源文件
          └─ processSingleFile()
              ├─ dedup 检查（syncedCache）
              ├─ Link() 获取下载链接
              ├─ uploadTo115() 上传到 115
              ├─ removeFrom115() 删除115文件
              ├─ syncedCache[sha1]=true（内存）
              └─ saveSyncedCache() 持久化到 SQLite
```

### strm 批量模式

```
Init()
  ├─ 检测 strm.txt 是否存在
  ├─ 若存在：加载 strm.txt 到 strm_tasks 表
  └─ 启动 doSyncLoop_strm()

doSyncLoop_strm()
  └─ 循环从 strm_tasks 读取任务
      ├─ 解析 dstRaw#srcRaw
      ├─ xiaoya.host 路径处理（/d 前缀）
      ├─ strm 扩展名处理
      └─ 根据协议分发
          ├─ http:// / https:// → processSingleFile_http()
          │     HEAD 查大小 → 下载到内存 → 计算SHA1 → memFileStreamer 上传
          ├─ file:// → processSingleFile_file()
          │     打开本地文件 → 计算SHA1 → fileStreamer 上传
          └─ 其他 → processSingleFile()
              （原驱动遍历逻辑不变）
```

## 同步源支持

| 源驱动 / 协议 | 说明 |
|---------------|------|
| `AliyundriveOpen` | 阿里云盘个人版 |
| `AliyundriveShare2Open` | 阿里云盘分享链接（需要先保存到自己的云盘） |
| `115Share` | 115 分享链接（收件箱），listing 直接返回 SHA1 |
| `http://` / `https://` | 直接从 HTTP URL 下载（≤100MB） |
| `file://` | 从本地文件系统读取（≤100MB） |
| `xiaoya.host` | 小雅挂载路径，自动解析 `/d` 前缀 |

## 许可证

MIT