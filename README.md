# AliyunTo115 Driver

阿里云盘 / 115分享 → 115 文件 SHA1 预注册同步驱动。

## 功能

- **自动扫描所有阿里云盘存储**：遍历 OpenList 中所有 `AliyundriveOpen`、`AliyundriveShare2Open`、`115Share` 类型的存储，递归同步文件到 115
- **SHA1 预注册**：文件上传到 115 后立即删除，只保留 SHA1 记录，使其他 115 用户可以享受秒传
- **跨驱动全局去重**：同一文件在多个阿里云盘存储中出现，只上传一次
- **定时同步**：后台 Goroutine 定期扫描，可配置扫描间隔
- **手动触发**：通过 `Other` action `sync` 手动触发同步
- **同步记录持久化**：重启后 dedup cache 不丢失（SQLite 持久化）

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

## 驱动行为

- **对外表现**：完整的 115 驱动，用户可以正常浏览、上传、下载 115 文件
- **后台任务**：自动扫描所有阿里云盘存储，同步文件 SHA1 到 115

## 配置项

| 配置项 | 类型 | 必填 | 默认值 | 说明 |
|--------|------|------|--------|------|
| `open115_cookie` | string | 是 | - | 115 登录 Cookie |
| `qrcode_token` | string | 否 | - | 115 二维码登录 Token |
| `qrcode_source` | string | 否 | linux | 二维码设备来源 |
| `page_size` | number | 否 | 100 | 分页大小 |
| `limit_rate` | float | 否 | 2 | API 限速（1/[limit_rate] 秒/请求） |
| `root_folder_id` | string | 否 | 0 | 115 根目录 ID |
| `sync_interval` | number | 否 | 600 | 同步间隔（秒），默认 10 分钟 |

## 使用方式

1. 在 OpenList 添加存储，选择类型 **AliyunTo115**
2. 填写 115 Cookie 和同步间隔
3. 挂载该存储后，可以正常作为 115 驱动使用
4. 后台自动扫描所有阿里云盘 / 115分享 存储，同步文件到 115

## 手动触发同步

通过 OpenList API 调用：

```bash
curl -X POST "http://localhost:8088/api/v3/other" \
  -H "Content-Type: application/json" \
  -d '{"action": "sync", "storage_id": "<your_storage_id>"}'
```

## 工作流程

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

## 同步源支持

| 源驱动 | 说明 |
|--------|------|
| `AliyundriveOpen` | 阿里云盘个人版 |
| `AliyundriveShare2Open` | 阿里云盘分享链接（需要先保存到自己的云盘） |
| `115Share` | 115 分享链接（收件箱） |

## 许可证

MIT