#!/bin/bash

# ================= 配置区 =================
DB_PATH="./data/data.db"
OPENLIST_BIN="./openlist"
INPUT_SQL="./xiaoya.sql"
INPUT_STRM="./strm.txt"
INPUT_115_SHARE_LIST="./115share_internal.txt"
INIT_WAIT_TIME=12
CONST_XIAOYA_URL="https://github.com/xiaoyaDev/data/raw/refs/heads/main/update.zip"
CONST_XIAOYA_115_SHARE_URL="https://github.com/xiaoyaDev/data/raw/refs/heads/main/115share_list.txt"

# 默认参数，可以在config.txt中覆盖定义，拷贝到config.txt中修改
CONST_ALIPAN_TYPE="alipan" # alipan - 对应opentoken, alipanTV - 对应tvtoken，配置与openlist官方AliyundriveOpen驱动完全一致
CONST_REFRESH_TOKEN_OPEN="<REFRESH_TOKEN_OPEN>" # 获取方式与openlist官方AliyundriveOpen驱动完全一致
CONST_REFRESH_TOKEN="<REFRESH_TOKEN>"
CONST_115_COOKIE="<115_COOKIE>"
CONST_115_SYNC_ROOT_ID="auto" # auto会自动在115根目录下创建"小雅同步"目录
CONST_TEMP_TRANSFER_FOLDER_ID="root"
CONST_ADMIN_PASS="12345"
MOUNT_PATHS=() # ()表示全部挂载，("/每日更新" "/整理中")表示按需挂载，以具体配置为准

# ================= 辅助函数 =================
# SQL 转义，防止单引号注入破坏 SQL 语句
escape_sql() { echo "${1//\'/''}"; }

check_and_install_deps() {
    # 定义颜色输出
    local RED='\033[0;31m'
    local GREEN='\033[0;32m'
    local YELLOW='\033[1;33m'
    local NC='\033[0m' # No Color

    local MISSING_DEPS=()

    # 1. 检查 sqlite3 是否安装
    if ! command -v sqlite3 >/dev/null 2>&1; then
        MISSING_DEPS+=("sqlite3")
    fi

    # 2. 检查 curl 是否安装
    if ! command -v curl >/dev/null 2>&1; then
        MISSING_DEPS+=("curl")
    fi

    # 3. 检查 unzip 是否安装
    if ! command -v unzip >/dev/null 2>&1; then
        MISSING_DEPS+=("unzip")
    fi

    # 如果都不缺少，直接返回
    if [ ${#MISSING_DEPS[@]} -eq 0 ]; then
        echo -e "${GREEN}✅ 所有依赖 (SQLite, Curl, Unzip) 已安装!${NC}"
        return 0
    fi

    echo -e "${YELLOW}⚠️ 检测到缺少依赖: ${MISSING_DEPS[*]}，准备开始安装...${NC}"

    # 4. 检查 root 权限
    local SUDO_CMD=""
    if [ "$(id -u)" -ne 0 ]; then
        if command -v sudo >/dev/null 2>&1; then
            SUDO_CMD="sudo"
        fi
    fi

    # 5. 探测包管理器并执行安装
    # 注意：某些系统中包名可能略有不同（sqlite3 vs sqlite）
    if command -v apt-get >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 Debian/Ubuntu 系统 (apt-get)...${NC}"
        $SUDO_CMD apt-get update -y
        $SUDO_CMD apt-get install -y sqlite3 curl unzip || { echo -e "${RED}❌ 安装失败${NC}"; exit 1; }

    elif command -v dnf >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 Fedora/RHEL 8+ 系统 (dnf)...${NC}"
        $SUDO_CMD dnf install -y sqlite curl unzip || { echo -e "${RED}❌ 安装失败${NC}"; exit 1; }

    elif command -v yum >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 CentOS/RHEL 7 系统 (yum)...${NC}"
        $SUDO_CMD yum install -y sqlite curl unzip || { echo -e "${RED}❌ 安装失败${NC}"; exit 1; }

    elif command -v pacman >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 Arch Linux 系统 (pacman)...${NC}"
        $SUDO_CMD pacman -Sy --noconfirm sqlite curl unzip || { echo -e "${RED}❌ 安装失败${NC}"; exit 1; }

    elif command -v zypper >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 openSUSE 系统 (zypper)...${NC}"
        $SUDO_CMD zypper install -y sqlite3 curl unzip || { echo -e "${RED}❌ 安装失败${NC}"; exit 1; }

    elif command -v apk >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 Alpine Linux 系统 (apk)...${NC}"
        $SUDO_CMD apk add sqlite curl unzip || { echo -e "${RED}❌ 安装失败${NC}"; exit 1; }

    elif command -v brew >/dev/null 2>&1; then
        echo -e "${YELLOW}🔍 检测到 macOS (Homebrew)...${NC}"
        brew install sqlite curl unzip || { echo -e "${RED}❌ 安装失败${NC}"; exit 1; }

    else
        echo -e "${RED}❌ 错误: 无法识别当前系统的包管理器，请手动安装 SQLite, Curl 和 Unzip。${NC}"
        exit 1
    fi

    # 6. 最终验证
    local FINAL_CHECK=0
    command -v sqlite3 >/dev/null 2>&1 || FINAL_CHECK=1
    command -v curl >/dev/null 2>&1 || FINAL_CHECK=1
    command -v unzip >/dev/null 2>&1 || FINAL_CHECK=1

    if [ $FINAL_CHECK -eq 0 ]; then
        echo -e "${GREEN}🎉 所有依赖 (SQLite, Curl, Unzip) 安装成功!${NC}"
    else
        echo -e "${RED}❌ 错误: 依赖安装后仍有组件无法调用，请检查环境路径。${NC}"
        exit 1
    fi
}

download_and_extract_sql() {
    if [ "$CONST_XIAOYA_URL"x = x ]; then
        echo "未定义小雅资源包下载地址，使用本地文件！"
        return 0
    fi

    # 定义颜色
    local RED='\033[0;31m'
    local GREEN='\033[0;32m'
    local YELLOW='\033[1;33m'
    local NC='\033[0m'

    local TEMP_ZIP="./update_data.zip"

    local TARGET_DIR=$(dirname "$INPUT_SQL")
    mkdir -p "$TARGET_DIR"

    echo -e ">>> 正在下载小雅更新包...${NC}"
    curl -sL -f "$CONST_XIAOYA_URL" -o "$TEMP_ZIP" || {
        echo -e "${RED}❌ 错误: 下载失败。${NC}"
        exit 1
    }

    unzip -p "$TEMP_ZIP" "*.sql" > "$INPUT_SQL" || {
        echo -e "${RED}❌ 错误: 解压失败或压缩包内无 .sql 文件。${NC}"
        rm -f "$TEMP_ZIP"
        exit 1
    }

    rm -f "$TEMP_ZIP"

    if [ ! -s "$INPUT_SQL" ]; then
        echo -e "${RED}❌ 错误: 提取后的文件为空，请检查压缩包内容。${NC}"
        exit 1
    fi
}

check_configs() {
    local has_error=0
    # 定义颜色输出以提高警示效果
    local RED='\033[0;31m'
    local YELLOW='\033[1;33m'
    local NC='\033[0m' # 恢复默认颜色

    # 检查 CONST_REFRESH_TOKEN_OPEN
    if [[ -z "$CONST_REFRESH_TOKEN_OPEN" || "$CONST_REFRESH_TOKEN_OPEN" == "<REFRESH_TOKEN_OPEN>" ]]; then
        echo -e "${RED}[错误] 未配置 CONST_REFRESH_TOKEN_OPEN${NC}"
        echo -e "${YELLOW} -> 提示: 获取方式与openlist官方AliyundriveOpen驱动完全一致${NC}"
        has_error=1
    fi

    # 检查 CONST_REFRESH_TOKEN
    if [[ -z "$CONST_REFRESH_TOKEN" || "$CONST_REFRESH_TOKEN" == "<REFRESH_TOKEN>" ]]; then
        echo -e "${RED}[错误] 未配置 CONST_REFRESH_TOKEN${NC}"
        has_error=1
    fi

    # 检查 CONST_115_COOKIE
    if [[ -z "$CONST_115_COOKIE" || "$CONST_115_COOKIE" == "<115_COOKIE>" ]]; then
        echo -e "${RED}[错误] 未配置 CONST_115_COOKIE${NC}"
        has_error=1
    fi

    # 如果存在任何一个未配置的项，则退出脚本
    if [[ $has_error -eq 1 ]]; then
        echo -e "\n${RED}请先在脚本中填写上述必须的基础配置，然后再重新运行！程序退出。${NC}"
        exit 1
    fi
}

# 加载自定义配置
load_external_config() {
    local config_path="./config.txt"
    if [ ! -f "$config_path" ]; then return; fi
    echo ">>> 加载自定义配置..."
    eval "$(cat "$config_path")"
}

# 初始化数据库
init_db() {
    
    if [ -f "$OPENLIST_BIN" ]; then
        chmod 0755 "$OPENLIST_BIN"
    else
        echo "请先下载openlist同步项目：https://github.com/zengge96/aliyun-to-115"
        exit 1
    fi

    mkdir -p "$(dirname "$DB_PATH")"
    
    if [ ! -f "$DB_PATH" ]; then
        echo ">>> 正在通过 $OPENLIST_BIN 初始化数据库..."
        "$OPENLIST_BIN" server >/dev/null 2>&1 &
        local pid=$!
        sleep "$INIT_WAIT_TIME"
        kill "$pid" 2>/dev/null
        wait "$pid" 2>/dev/null
        "$OPENLIST_BIN" admin set "$CONST_ADMIN_PASS" >/dev/null 2>&1 &
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

download_and_import_115_share_list() {
    echo -e ">>> 正在下载115分享列表..."
    local LIST_FILE="$INPUT_115_SHARE_LIST" 

    if [ -n "$CONST_XIAOYA_115_SHARE_URL" ]; then
        curl -sL -f "$CONST_XIAOYA_115_SHARE_URL" -o "$LIST_FILE"
    fi

    if [ -s "$LIST_FILE" ]; then
        while read -r line || [ -n "$line" ]; do
            [[ -z "$line" || "$line" =~ ^# ]] && continue
            
            mount_path=$(echo "$line" | awk '{print $1}')
            
            # 根据 MOUNT_PATHS 进行过滤
            if [ ${#MOUNT_PATHS[@]} -gt 0 ]; then
                local keep=0
                local full_path="/🏷️我的115分享/$mount_path"
                for p in "${MOUNT_PATHS[@]}"; do
                    # 匹配逻辑：完整的挂载路径是否以配置的过滤路径开头
                    if [[ "$full_path" == "$p"* ]]; then
                        keep=1
                        break
                    fi
                done
                # 如果没有匹配任何一个设定挂载路径，则跳过
                if [ $keep -eq 0 ]; then
                    continue
                fi
            fi

            share_id=$(echo "$line" | awk '{print $2}')
            root_folder_id=$(echo "$line" | awk '{print $3}')
            share_pwd=$(echo "$line" | awk '{print $4}')

            [ "$root_folder_id" == "root" ] && root_folder_id=""

            sqlite3 "$DB_PATH" <<EOF
INSERT INTO x_storages VALUES(NULL,'/🏷️我的115分享/$mount_path',0,'115 Share',86400,'','work','{"cookie":"$ESC_115_COOKIE","root_folder_id":"$root_folder_id","qrcode_token":"","qrcode_source":"linux","page_size":300,"limit_rate":2,"share_code":"$share_id","receive_code":"$share_pwd"}','','2022-09-29 20:14:52.313982364+00:00',0,0,0,'name','ASC','front',0,'302_redirect',0,'',0);
EOF
        done < "$LIST_FILE"
    fi
}

function get_xiaoya_updates() {
    # 1. 检查依赖项
    for cmd in curl jq unzip awk; do
        if ! command -v $cmd &> /dev/null; then
            echo "错误: 缺少必要的命令 '$cmd'，请先安装。" >&2
            return 1
        fi
    done

    echo "=> 正在计算时间 (1个月前)..." >&2
    # 兼容 Linux (GNU date) 和 macOS (BSD date) 的时间计算
    if date --version >/dev/null 2>&1; then
        ONE_MONTH_AGO=$(date -d "1 month ago" --utc +%Y-%m-%dT%H:%M:%SZ)
    else
        ONE_MONTH_AGO=$(date -v-1m -u +%Y-%m-%dT%H:%M:%SZ)
    fi

    local REPO="xiaoyaDev/data"
    local FILE_PATH="index.zip"
    local API_URL="https://api.github.com/repos/${REPO}/commits?path=${FILE_PATH}"

    echo "=> 正在查询 GitHub 获取小雅索引数据 Commit 记录..." >&2
    
    # 获取最新版本的 commit SHA
    local LATEST_SHA=$(curl -s "${API_URL}&per_page=1" | jq -r '.[0].sha')
    if [ "$LATEST_SHA" == "null" ] || [ -z "$LATEST_SHA" ]; then
        echo "错误: 无法获取最新 commit，可能是触发了 GitHub API 限制。" >&2
        return 1
    fi

    # 利用 until 参数获取距今大于1个月的第一个版本的 commit SHA
    local OLD_SHA=$(curl -s "${API_URL}&until=${ONE_MONTH_AGO}&per_page=1" | jq -r '.[0].sha')
    if [ "$OLD_SHA" == "null" ] || [ -z "$OLD_SHA" ]; then
        echo "错误: 无法获取1个月前的 commit。" >&2
        return 1
    fi

    echo "=> 最新 Commit: ${LATEST_SHA:0:7}" >&2
    echo "=> 往期 Commit: ${OLD_SHA:0:7}" >&2

    if [ "$LATEST_SHA" == "$OLD_SHA" ]; then
        echo "=> 过去一个月内该文件没有任何更新。" >&2
        return 0
    fi

    # 创建临时工作目录
    local TMP_DIR=$(mktemp -d)
    
    echo "=> 正在下载新旧版本的压缩包..." >&2
    local RAW_BASE="https://raw.githubusercontent.com/${REPO}"
    curl -sL "${RAW_BASE}/${LATEST_SHA}/${FILE_PATH}" -o "${TMP_DIR}/latest.zip"
    curl -sL "${RAW_BASE}/${OLD_SHA}/${FILE_PATH}" -o "${TMP_DIR}/old.zip"

    echo "=> 正在解压 index.daily.txt..." >&2
    unzip -p "${TMP_DIR}/latest.zip" index.daily.txt > "${TMP_DIR}/latest.txt"
    unzip -p "${TMP_DIR}/old.zip" index.daily.txt > "${TMP_DIR}/old.txt"

    echo "=> 提取新增的内容完成" >&2
    
    # 使用 awk 高效对比两个文件，输出 latest.txt 中存在但 old.txt 中不存在的行 (且保持原本顺序)
    awk 'NR==FNR{seen[$0]=1; next} !seen[$0]' "${TMP_DIR}/old.txt" "${TMP_DIR}/latest.txt" | sed 's/^\.//' | awk -F'#' '{printf("%s#%s\n",$1,$1)}' > strm.txt

    # 清理临时文件
    rm -rf "${TMP_DIR}"
}

# ================= 主流程 =================
main() {
    check_and_install_deps
    load_external_config
    check_configs

    download_and_extract_sql

    if [ ! -f "$INPUT_SQL" ]; then
        echo "!!! 错误: 找不到文件 $INPUT_SQL"
        exit 1
    fi

    init_db

    echo ">>> 清理旧数据并准备环境..."
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

    echo ">>> 解析与转换 SQL 数据..."

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
SET 
    c6 = json_set(c6, 
        '$.provider_pass_through', json('true')
    )
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

    echo ">>> 插入 AliyunTo115 驱动..."
    cat <<EOF >> "$TMP_SQL"
INSERT INTO x_storages VALUES (
    NULL, '/115sync', 0, 'AliyunTo115', 30, '', 'work',
    json('{"open115_cookie":"$ESC_115_COOKIE","sync_interval":20,"root_folder_id":"$ESC_SYNC_ROOT_ID","qrcode_token":"","qrcode_source":"","page_size":0,"limit_rate":2,"delete_after_sync":false,"run_once":true}'),
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

    download_and_import_115_share_list

    if [ $SQL_RET -eq 0 ]; then
        STORAGE_COUNT=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM x_storages;")
        echo ">>> 同步完成！总挂载数: $STORAGE_COUNT"
    else
        echo "!!! 数据库同步过程中出错，请检查。"
    fi

    echo ">>> 数据全部准备就绪，并自动运行openlist同步服务。"
    echo
}

# 定义清理函数，当脚本接收到 SIGINT (Ctrl+C) 时调用
cleanup() {
    # 终止当前正在运行的后台子进程（如果有）
    # 如果 openlist 在前台运行，这步操作可以确保脚本彻底结束
    exit 0
}

# 捕获 SIGINT (Ctrl+C) 信号
trap cleanup SIGINT

while true
do
    main
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] 开始执行 get_xiaoya_updates..."
    get_xiaoya_updates
    
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] 启动 openlist 同步服务..."
    
    "$OPENLIST_BIN" server
    
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] 进程已退出，将在 3 秒后重启..."

    sleep 600
done
