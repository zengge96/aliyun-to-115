# AliyunTo115 Driver

阿里云盘 → 115 文件 SHA1 预注册同步驱动。

## 功能

- **自动扫描所有阿里云盘存储**：遍历 OpenList 中所有 `AliyundriveOpen` 类型的存储，递归同步文件到 115
- **SHA1 预注册**：文件上传到 115 后立即删除，只保留 SHA1 记录，使其他 115 用户可以享受秒传
- **跨驱动全局去重**：同一文件在多个阿里云盘存储中出现，只上传一次
- **定时同步**：后台 Goroutine 定期扫描，可配置扫描间隔
- **手动触发**：通过 `Other` action `sync` 手动触发同步

## 机制说明

```
阿里云盘文件 → 下载链接 → 上传到115(秒传或普通) → 删除115文件 → SHA1保留在115系统
```

- **秒传成功**：SHA1 已在 115 注册 → 快速完成 → 删除文件
- **普通上传**：SHA1 未注册 → 完整上传 → 删除文件 → SHA1 注册完成

## 驱动行为

- **对外表现**：完整的 115 驱动，用户可以正常浏览、上传、下载 115 文件
- **后台任务**：自动扫描所有阿里云盘存储，同步文件 SHA1 到 115

## 配置项

| 配置项 | 类型 | 必填 | 默认值 | 说明 |
|--------|------|------|--------|------|
| `open115_cookie` | string | 是 | - | 115 登录 Cookie |
| `sync_interval` | number | 否 | 600 | 同步间隔（秒），默认 10 分钟 |

## 使用方式

1. 在 OpenList 添加存储，选择类型 **AliyunTo115**
2. 填写 115 Cookie 和同步间隔
3. 挂载该存储后，可以正常作为 115 驱动使用
4. 后台自动扫描所有阿里云盘存储，同步文件到 115

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
  ├─ op.GetAllStorages() 遍历所有存储
  ├─ 筛选 Driver=="AliyundriveOpen" 的存储
  ├─ 初始化 Pan115（用于 List/Put 等）
  └─ 启动 doSyncLoop()

doSyncLoop() [定时]
  └─ doSync()
      └─ 遍历所有阿里云盘存储
          └─ walkFilesRecursively() 递归列出文件
              └─ 对每个文件:
                  ├─ SHA1 去重检查
                  ├─ Aliyun.Link() 获取下载链接
                  ├─ p115Client.uploadTo115() 上传
                  ├─ p115Client.removeFrom115() 删除115文件
                  └─ 标记 syncedCache[sha1]=true
```

## 与 aliyun_115_sync 的区别

| 特性 | aliyun_115_sync | AliyunTo115 |
|------|----------------|-------------|
| 扫描范围 | 单个阿里云盘存储 | 所有阿里云盘存储 |
| List 行为 | 透传给 AliyundriveOpen | 返回 115 根目录 |
| 驱动基础 | AliyundriveOpen | 115 |
| 存储绑定 | 绑定到特定阿里云盘 MountPath | 独立 MountPath，作为正常 115 驱动使用 |

## 许可证

MIT