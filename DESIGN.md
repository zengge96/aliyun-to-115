# AliyunTo115 驱动设计文档

## 目标

创建一个 OpenList 驱动，自动扫描所有阿里云盘存储的文件，同步到 115 并删除，只保留 SHA1 预注册记录。

## 核心价值

- 用户上传文件到阿里云盘后，文件会被"秒传预注册"到 115
- 任何人之后往 115 上传相同文件时，直接秒传成功
- 不占用 115 存储空间（文件上传后立即删除）

## 架构

```
AliyunTo115 (新驱动)
├── Pan115 (嵌入)                    ← List 返回115根目录，用户正常操作
├── sync115Client                   ← 后台上传+删115文件
├── aliyunStorages []*Wrapper        ← Init时收集的所有阿里云盘驱动
└── syncedCache map[string]bool      ← 全局SHA1去重
```

## Init 流程

1. `op.GetAllStorages()` 获取所有已初始化的 storage（自己不在其中）
2. 过滤出 `Driver == "AliyundriveOpen"` 的实例
3. 初始化内嵌的 `Pan115`（用于 `List`/`Put`/`Link` 等正常操作）
4. 初始化 `sync115Client`（与 Pan115 使用同一账号 Cookie）
5. 启动 `doSyncLoop()` 定时同步线程

## Sync 流程

```
doSyncLoop (定时)
  └→ doSync()
      └→ 遍历 aliyunStorages
          └→ walkFilesRecursively(rootFolderID)
              └→ 对每个文件:
                  ├─ SHA1 去重检查
                  ├─ Link() 获取阿里云盘下载链接
                  ├─ uploadTo115() 上传到115
                  ├─ removeFrom115() 删除115文件（只留SHA1）
                  └─ syncedCache[sha1]=true
```

## 用户访问 MountPath

- `List` → `Pan115.List` 返回 115 根目录
- `Put`/`Link` 等 → 透传给 Pan115

## 与 aliyun_115_sync 的区别

| 特性 | aliyun_115_sync | AliyunTo115 |
|------|----------------|-------------|
| 扫描范围 | 单个阿里云盘存储 | 所有阿里云盘存储 |
| List 行为 | 透传给 AliyundriveOpen | 返回 115 根目录 |
| 驱动基础 | AliyundriveOpen | 115 |
| 存储绑定 | 绑定到特定阿里云盘 MountPath | 独立 MountPath |

## 已确认的约束

- 不扫描自己（Init 时还未加入 storagesMap）
- 不排除任何 storage（全部扫描）
- 115 upload client 和 Pan115 用同一账号
- 只删 115 文件，不动阿里云盘文件
- SHA1 去重是全局的（跨所有阿里云盘存储）