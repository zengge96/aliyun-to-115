#!/bin/bash

# ================= 配置区 =================
DB_PATH="./data/data.db"
OPENLIST_BIN="./openlist"
INPUT_SQL="xiaoya.sql"
INIT_WAIT_TIME=12

# 默认参数
CONST_REFRESH_TOKEN_OPEN="<REFRESH_TOKEN_OPEN>"
CONST_REFRESH_TOKEN="<REFRESH_TOKEN>"
CONST_115_COOKIE="<115_COOKIE>"
CONST_115_SYNC_ROOT_ID="root"
CONST_TEMP_TRANSFER_FOLDER_ID="root"
CONST_ALIPAN_TYPE="alipan"
MOUNT_PATHS=()

# ================= 辅助函数 =================
# SQL 转义，防止单引号注入破坏 SQL 语句
escape_sql() { echo "${1//\'/''}"; }

# 加载自定义配置
load_external_config() {
    local config_path="./config.txt"
    if [ ! -f "$config_path" ]; then return; fi
    echo ">>> [0/5] 加载自定义配置..."
    eval "$(cat "$config_path")"
}

# 初始化数据库
init_db() {
    mkdir -p "$(dirname "$DB_PATH")"
    
    if [ ! -f "$DB_PATH" ]; then
        echo ">>> [1/5] 正在通过 $OPENLIST_BIN 初始化数据库..."
        if [ -f "$OPENLIST_BIN" ]; then chmod 0755 "$OPENLIST_BIN"; fi
        
        "$OPENLIST_BIN" server >/dev/null 2>&1 &
        local pid=$!
        sleep "$INIT_WAIT_TIME"
        kill "$pid" 2>/dev/null
        wait "$pid" 2>/dev/null
    fi
    
    if [ -f "$DB_PATH" ]; then
        local timestamp
        timestamp=$(date +"%H%M%S")
        cp "$DB_PATH" "${DB_PATH}.${timestamp}.bak"
    else
        echo "!!! 错误: $DB_PATH 未生成，请检查 OpenList 是否正常启动。"
        exit 1
    fi
}

# ================= 主流程 =================

load_external_config

if [ ! -f "$INPUT_SQL" ]; then
    echo "!!! 错误: 找不到文件 $INPUT_SQL"
    exit 1
fi

init_db

echo ">>> [2/5] 清理旧数据并准备环境..."
ESC_TOKEN_OPEN=$(escape_sql "$CONST_REFRESH_TOKEN_OPEN")
ESC_TOKEN=$(escape_sql "$CONST_REFRESH_TOKEN")
ESC_115_COOKIE=$(escape_sql "$CONST_115_COOKIE")
ESC_SYNC_ROOT_ID=$(escape_sql "$CONST_115_SYNC_ROOT_ID")
ESC_TEMP_ID=$(escape_sql "$CONST_TEMP_TRANSFER_FOLDER_ID")
ESC_ALIPAN_TYPE=$(escape_sql "$CONST_ALIPAN_TYPE")

TMP_SQL="process_tmp_$$.sql"
cat <<EOF > "$TMP_SQL"
BEGIN TRANSACTION;

-- 清理 3 个表
DELETE FROM x_storages;
DELETE FROM x_meta;
DELETE FROM x_setting_items;

-- 创建用于承接原始数据的临时表 (严格定义16列，对应 xiaoya.sql 里的 16 个值)
CREATE TEMP TABLE temp_storages (
    c0, c1, c2, c3, c4, c5, c6, c7, c8, c9, 
    c10, c11, c12, c13, c14, c15
);
EOF

echo ">>> [3/5] 解析与转换 SQL 数据..."

# 将 x_storages 的数据重定向插入到临时表进行处理
grep -i "^INSERT INTO x_storages" "$INPUT_SQL" | sed 's/INSERT INTO x_storages/INSERT INTO temp_storages/i' >> "$TMP_SQL"

# 追加 SQL 转换与清洗逻辑
cat <<EOF >> "$TMP_SQL"
-- 过滤不需要的驱动
DELETE FROM temp_storages WHERE c3 IN ('PikPakShare', 'QuarkShare', 'AList V2', 'AList V3', 'UCShare');
EOF

if [ ${#MOUNT_PATHS[@]} -gt 0 ]; then
    cond=""
    for p in "${MOUNT_PATHS[@]}"; do
        cond="$cond c1 NOT LIKE '${p}%' AND"
    done
    cond="${cond%AND}"
    echo "DELETE FROM temp_storages WHERE $cond;" >> "$TMP_SQL"
fi

cat <<EOF >> "$TMP_SQL"
-- 转换 AliyundriveShare 驱动
UPDATE temp_storages 
SET 
    c3 = 'AliyundriveShare2Open',
    c6 = json_set(c6, 
        '$.refresh_token', '$ESC_TOKEN',
        '$.RefreshToken', '$ESC_TOKEN',
        '$.RefreshTokenOpen', '$ESC_TOKEN_OPEN',
        '$.TempTransferFolderID', '$ESC_TEMP_ID',
        '$.use_online_api', json('true'),
        '$.alipan_type', '$ESC_ALIPAN_TYPE',
        '$.api_url_address', 'https://api.oplist.org/alicloud/renewapi'
    )
WHERE c3 = 'AliyundriveShare' AND json_valid(c6) = 1;

-- 转换 115 Share
UPDATE temp_storages 
SET c6 = json_set(c6, '$.cookie', '$ESC_115_COOKIE')
WHERE c3 = '115 Share' AND json_valid(c6) = 1;

-- 转换 Alias
UPDATE temp_storages 
SET c6 = json_set(c6, '$.paths', replace(json_extract(c6, '$.paths'), '本地:', ''))
WHERE c3 = 'Alias' AND json_valid(c6) = 1 AND json_extract(c6, '$.paths') IS NOT NULL;

-- 映射并插入 OpenList 所需的最终 21 列表结构 (跳过原数组的第7位和多余位，精准对应 Python)
INSERT INTO x_storages 
SELECT 
    c0, c1, c2, c3, c4, 
    '', c5, c6, '', c8, c9, 
    '0', '0', c10, c11, c12, c13, c14, 
    '0', c15, '0'
FROM temp_storages;

EOF

echo ">>> [4/5] 插入 AliyunTo115 驱动..."
cat <<EOF >> "$TMP_SQL"
INSERT INTO x_storages VALUES (
    NULL, '/115sync', 0, 'AliyunTo115', 1, '', 'work',
    json('{"open115_cookie":"$ESC_115_COOKIE","sync_interval":20,"root_folder_id":"$ESC_SYNC_ROOT_ID","qrcode_token":"","qrcode_source":"","page_size":0,"limit_rate":0,"delete_after_sync":false}'),
    '', datetime('now', 'localtime'), 0,
    0, 0, '', '', '', 0, '302_redirect', 0, '', 0
);

COMMIT;
EOF

# 第一步：执行核心存储转换（有严格事务保护）
sqlite3 "$DB_PATH" < "$TMP_SQL"
SQL_RET=$?

# 第二步：处理 Setting 配置，完全模拟原版 Python 的 try...except: pass （使用 2>/dev/null 屏蔽因列数不同的报错）
grep -i "^INSERT INTO x_setting_items" "$INPUT_SQL" | sqlite3 "$DB_PATH" 2>/dev/null

rm -f "$TMP_SQL"

if [ $SQL_RET -eq 0 ]; then
    STORAGE_COUNT=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM x_storages;")
    echo ">>> [5/5] 同步完成！总挂载数: $STORAGE_COUNT"
else
    echo "!!! 数据库同步过程中出错，请检查。"
fi
