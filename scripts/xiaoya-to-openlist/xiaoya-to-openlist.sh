#!/bin/bash

# ================= 配置区 =================
DB_PATH="./data/data.db"
OPENLIST_BIN="./openlist"
INPUT_SQL="xiaoya.sql"
INIT_WAIT_TIME=12

# 默认参数，可以在config.txt中覆盖定义，拷贝到config.txt中修改
CONST_REFRESH_TOKEN_OPEN="<REFRESH_TOKEN_OPEN>" # 获取方式与openlist官方AliyundriveOpen驱动完全一致
CONST_REFRESH_TOKEN="<REFRESH_TOKEN>"
CONST_115_COOKIE="<115_COOKIE>"
CONST_115_SYNC_ROOT_ID="root"
CONST_TEMP_TRANSFER_FOLDER_ID="root"
CONST_ALIPAN_TYPE="alipan" # alipan - 对应opentoken, alipanTV - 对应tvtoken，配置与openlist官方AliyundriveOpen驱动完全一致
CONST_ADMIN_PASS="12345"
MOUNT_PATHS=() # ()表示全部挂载，("/每日更新" "/整理中")等表示按需挂载

# ================= 辅助函数 =================
# SQL 转义，防止单引号注入破坏 SQL 语句
escape_sql() { echo "${1//\'/''}"; }

check_and_install_sqlite() {
    # 定义颜色输出，方便阅读
    local RED='\033[0;31m'
    local GREEN='\033[0;32m'
    local YELLOW='\033[1;33m'
    local NC='\033[0m' # No Color

    # 1. 检查是否已经安装了 sqlite3
    if command -v sqlite3 >/dev/null 2>&1; then
        local version=$(sqlite3 --version | awk '{print $1}')
        echo -e "${GREEN}✅ SQLite 已安装! 当前版本: ${version}${NC}"
        return 0
    fi

    echo -e "${YELLOW}⚠️ 未检测到 SQLite，准备开始安装...${NC}"

    # 2. 检查 root 权限与 sudo
    local SUDO_CMD=""
    if [ "$(id -u)" -ne 0 ]; then
        if command -v sudo >/dev/null 2>&1; then
            SUDO_CMD="sudo"
        else
            echo -e "${RED}❌ 错误: 当前不是 root 用户，且未找到 sudo 命令。请以 root 身份运行此脚本。${NC}"
            return 1
        fi
    fi

    # 3. 探测包管理器并执行安装
    if command -v apt-get >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 Debian/Ubuntu 系统 (apt-get)...${NC}"
        $SUDO_CMD apt-get update -y
        $SUDO_CMD apt-get install -y sqlite3
    
    elif command -v dnf >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 Fedora/RHEL 8+ 系统 (dnf)...${NC}"
        $SUDO_CMD dnf install -y sqlite
    
    elif command -v yum >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 CentOS/RHEL 7 系统 (yum)...${NC}"
        $SUDO_CMD yum install -y sqlite
    
    elif command -v pacman >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 Arch Linux 系统 (pacman)...${NC}"
        $SUDO_CMD pacman -Sy --noconfirm sqlite
    
    elif command -v zypper >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 openSUSE 系统 (zypper)...${NC}"
        $SUDO_CMD zypper install -y sqlite3
    
    elif command -v apk >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 Alpine Linux 系统 (apk)...${NC}"
        $SUDO_CMD apk add sqlite
    
    elif command -v brew >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 macOS (Homebrew)...${NC}"
        # 注意：Homebrew 极其不建议甚至禁止用 sudo 运行，因此这里不加 $SUDO_CMD
        brew install sqlite
    
    else
        echo -e "${RED}❌ 错误: 无法识别当前系统的包管理器，请手动安装 SQLite。${NC}"
        return 1
    fi

    # 4. 再次验证是否安装成功
    if command -v sqlite3 >/dev/null 2>&1; then
        local version=$(sqlite3 --version | awk '{print $1}')
        echo -e "${GREEN}🎉 SQLite 安装成功! 当前版本: ${version}${NC}"
        return 0
    else
        echo -e "${RED}❌ 错误: SQLite 安装失败，请检查网络或源配置。${NC}"
        return 1
    fi
}

check_and_install_sqlite

# 加载自定义配置
load_external_config() {
    local config_path="./config.txt"
    if [ ! -f "$config_path" ]; then return; fi
    echo ">>> [0/6] 加载自定义配置..."
    eval "$(cat "$config_path")"
}

# 初始化数据库
init_db() {
    mkdir -p "$(dirname "$DB_PATH")"
    
    if [ ! -f "$DB_PATH" ]; then
        echo ">>> [1/6] 正在通过 $OPENLIST_BIN 初始化数据库..."
        if [ -f "$OPENLIST_BIN" ]; then chmod 0755 "$OPENLIST_BIN"; fi
        
        "$OPENLIST_BIN" server >/dev/null 2>&1 &
        "$OPENLIST_BIN" admin set "$CONST_ADMIN_PASS" >/dev/null 2>&1 &
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

echo ">>> [2/6] 清理旧数据并准备环境..."
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

echo ">>> [3/6] 解析与转换 SQL 数据..."

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

echo ">>> [4/6] 插入 AliyunTo115 驱动..."
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
    echo ">>> [5/6] 同步完成！总挂载数: $STORAGE_COUNT"
else
    echo "!!! 数据库同步过程中出错，请检查。"
fi

echo ">>> [6/6] 启动openlist同步服务..."
echo

"$OPENLIST_BIN" server
