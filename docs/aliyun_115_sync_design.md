# Aliyun115Sync 驱动开发方案

## 目标

基于 `aliyundrive_open` 制作新驱动 `aliyun_115_sync`，让阿里云盘中的文件在 115 网盘上也能秒传。

核心思路：115 的秒传依赖 SHA1 索引。如果一个文件的 SHA1 已经在 115 上注册过，后续相同 SHA1 的文件就能秒传成功。通过将阿里云盘的文件内容上传到 115（随后删除），即可预注册 SHA1，供其他来源秒传使用。

## 需求方（黑哥）确认的决策

1. 遍历方式：全量扫描，每次 SyncInterval 秒扫描所有文件
2. 并发控制：每次只处理 1 个文件，不限速
3. 下载 URL 有效期：一般不过期；如果过期了重新获取 `Link()`
4. 115 秒传上限：**无上限**
5. preHash（前128KB的SHA1）：**可以随便填**，不影响
6. 测试 Cookie：可以硬编码，不用环境变量

## 技术方案

### 驱动结构

```
drivers/aliyun_115_sync/
  driver.go      # 驱动主体，继承 aliyundrive_open
  meta.go        # 配置项定义
  sync.go        # 115 同步逻辑（TDD 开发）
  sync_test.go   # 115 同步测试
```

### 配置项

| 字段 | 类型 | 说明 |
|------|------|------|
| `Open115Cookie` | string | 115 Cookie（必填） |
| `SyncInterval` | int | 扫描间隔秒数（默认60） |
| 其余字段 | | 继承自 aliyundrive_open |

### 同步流程

```
Init():
    if Open115Cookie != "":
        启动 syncLoop() 后台协程

syncLoop():
    每 SyncInterval 秒执行一次 doSync()

doSync():
    1. 获取阿里云盘根目录
    2. 递归遍历所有文件（深度优先，队列实现）
    3. 对每个文件（互斥锁，每次只处理1个）：
        a. sha1 = file.GetHash().GetHash(utils.SHA1)
        b. if sha1 == "": continue  # 跳過无 SHA1 的文件
        c. if sha1 已记录 in synced: continue  # 已同步过
        d. if checkInstantTransfer(sha1) == false:
               # 115 已有该 SHA1
               记录 sha1，continue
        e. # 需要上传真实内容
        f. downloadUrl = aliyun.Link(file).URL  # 阿里下载链接
        g. 边下载阿里内容、边计算完整 SHA1、边分片上传到 115 OSS
        h. 115根目录找到该文件并删除（保留 SHA1 索引）
        i. 记录 sha1 in synced
```

### 流式分片上传实现

- 不走 115 驱动的 `UploadByMultipart`（那个会缓存整个文件到磁盘）
- 自己实现流式 OSS 分片上传：
  1. 调用 `client.GetOSSToken()` 获取 OSS 凭证
  2. 下载流同时做 SHA1 哈希计算
  3. 按固定大小分片（待定，大概用 10MB），每片读满就调一次 OSS `UploadPart`
  4. 最后 `CompleteMultipartUpload`

### preHash 处理

填全 0 字符串的 SHA1（`0000000000000000000000000000000000000000`），不影响秒传结果。

### 断点续扫

内存维护 `synced map[string]bool`，同步过的 SHA1 记录下来，服务重启后不重复处理。

### SHA1 来源

从阿里云盘文件元数据直接取 `file.GetHash().GetHash(utils.SHA1)`，不需要下载后本地计算。

## TDD 开发要求

- `sync.go` 里的 115 同步逻辑用 TDD 方式开发
- `sync_test.go` 写测试，硬编码 115 Cookie（私有库无安全问题）
- RED-GREEN-REFACTOR 循环：测试 → 失败 → 最小代码 → 通过 → 重构

### 需要覆盖的测试场景

1. `checkInstantTransfer` — SHA1 不在 115 时返回 true
2. `checkInstantTransfer` — SHA1 已在 115 时返回 false
3. `uploadAndDelete` — 完整流程：上传 → 秒传验证 → 删除 → SHA1 保留
4. 遍历文件列表，跳过无 SHA1 的文件
5. 递归遍历所有子目录文件
6. 已同步记录不重复处理

## 参考资料

- 115 driver: `drivers/115/` — 登录、文件列表、秒传、上传、删除 API
- aliyundrive_open driver: `drivers/aliyundrive_open/` — 继承对象
- 115driver 库: `github.com/SheltonZhu/115driver` — 核心 115 操作封装

## 已废弃的残留代码

之前的 `aliyun_115_sync` 残留代码（`/tmp` 里克隆的旧版本）存在以下问题，已废弃：

1. 只扫描根目录，不递归子目录
2. `uploadAndDelete` 用 `make([]byte, fileSize)` 填 0 假内容，SHA1 和真实文件不匹配
3. 预检和上传用的不是同一个 SHA1

新的实现将全部从头按照本方案执行。