# xiaoya → OpenList 数据转换

将 [xiaoya](https://github.com/xiaoyaDev/data) 的 `update.sql` 转换为 OpenList `storages` 表格式并导入。

## 内容

- `convert_xiaoya.py` - 核心转换逻辑（Python）
- `xiaoya-to-openlist.sh` - 自动下载 + 转换 + 导入

## 使用方法

```bash
# 自动模式（下载 → 转换 → 导入，全部自动完成）
./xiaoya-to-openlist.sh --auto

# 交互模式
./xiaoya-to-openlist.sh
```

## 转换说明

| xiaoya 驱动 | OpenList 驱动 |
|---|---|
| AliyundriveShare | AliyundriveShare2Open |
| 115 Share | 115Share |
| Alias | Local |
| AList V2/V3, QuarkShare, PikPakShare | 跳过（不支持） |

### Alias（本地路径）转换
- 路径中的 `本地:` 前缀会被移除
- `\\n` 分隔符会转为真正的换行符

### 阿里云分享转换
- `share_pwd` 字段重命名为 `share_password`

## 注意

- **115 Share 的 cookie 是占位符**，转换后需要手动替换为真实 cookie
