#!/bin/bash
#
# xiaoya-to-openlist.sh
# 从 xiaoya 的 update.zip 拉取数据，转换格式后导入 OpenList
#
# 用法:
#   ./xiaoya-to-openlist.sh           # 交互式（下载 → 转换 → 询问是否导入）
#   ./xiaoya-to-openlist.sh --auto    # 自动完成所有步骤（用于调试）
#

set -e

WORK_DIR="/root/.openclaw/workspace/user/xiaoya_import"
SQL_URL="https://github.com/xiaoyaDev/data/raw/refs/heads/main/update.zip"
PY_SCRIPT="/root/.openclaw/workspace/user/convert_xiaoya.py"

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

need_confirm=true
if [[ "${1:-}" == "--auto" ]]; then
    need_confirm=false
fi

# ========== 步骤1: 清理并创建工作目录 ==========
log_info "创建工作目录: $WORK_DIR"
rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"

# ========== 步骤2: 下载 update.zip ==========
log_info "下载 xiaoya update.zip ..."
cd "$WORK_DIR"
curl -sL "$SQL_URL" -o update.zip

if [[ ! -s update.zip ]]; then
    log_error "下载失败，文件为空"
    exit 1
fi
log_info "下载完成: $(du -sh update.zip | cut -f1)"

# ========== 步骤3: 解压 ==========
log_info "解压..."
rm -rf update_extracted
unzip -q update.zip -d update_extracted

if [[ ! -f update_extracted/update.sql ]]; then
    log_error "解压后未找到 update.sql"
    ls update_extracted/
    exit 1
fi
log_info "解压完成"

# ========== 步骤4: 转换 ==========
log_info "运行格式转换..."
OUTPUT_SQL="$WORK_DIR/openlist_import.sql"

python3 "$PY_SCRIPT" update_extracted/update.sql "$OUTPUT_SQL" 2>&1 | tail -3

if [[ ! -s "$OUTPUT_SQL" ]]; then
    log_error "转换失败，输出文件为空"
    exit 1
fi

# 统计
total=$(grep -c "^INSERT INTO storages" "$OUTPUT_SQL" 2>/dev/null || echo 0)
ali_count=$(grep -c "AliyundriveShare2Open" "$OUTPUT_SQL" 2>/dev/null || echo 0)
share115_count=$(grep -c "115Share" "$OUTPUT_SQL" 2>/dev/null || echo 0)
local_count=$(grep -c "'Local'" "$OUTPUT_SQL" 2>/dev/null || echo 0)
cookie_count=$(grep -c 'cookie":"xxx"' "$OUTPUT_SQL" 2>/dev/null || echo 0)

echo ""
log_info "========== 转换结果 =========="
echo "  总记录数:   $total"
echo "  阿里分享:   $ali_count"
echo "  115分享:    $share115_count"
echo "  本地路径:   $local_count"
echo ""

if [[ $cookie_count -gt 0 ]]; then
    log_warn "有 ${cookie_count} 条记录的 cookie 是占位符 'xxx'，需要手动替换"
fi

echo ""
echo "输出文件: $OUTPUT_SQL"
echo "文件大小: $(du -sh $OUTPUT_SQL | cut -f1)"
echo ""

# ========== 步骤5: 导入 ==========
do_import() {
    log_info "开始导入 OpenList 数据库..."
    
    # 找数据库路径
    DB_PATH=""
    for candidate in \
        "/root/.openclaw/data/openlist.db" \
        "/root/.openclaw/openlist.db" \
        "/opt/openlist/data/openlist.db" \
        "./openlist.db"; do
        if [[ -f "$candidate" ]]; then
            DB_PATH="$candidate"
            break
        fi
    done
    
    if [[ -z "$DB_PATH" ]]; then
        # 尝试用 openclaw 命令找
        DB_PATH=$(openclaw config get database.path 2>/dev/null || true)
        if [[ -z "$DB_PATH" || "$DB_PATH" == "null" ]]; then
            log_warn "找不到 OpenList 数据库路径，跳过自动导入"
            log_warn "请手动执行: sqlite3 <你的数据库路径> < $OUTPUT_SQL"
            return 1
        fi
    fi
    
    log_info "使用数据库: $DB_PATH"
    
    # 备份
    cp "$DB_PATH" "${DB_PATH}.bak.$(date +%Y%m%d_%H%M%S)"
    log_info "备份完成"
    
    # 导入
    sqlite3 "$DB_PATH" < "$OUTPUT_SQL"
    log_info "导入完成！"
}

if $need_confirm; then
    echo -n "是否立即导入到 OpenList 数据库? (y/N): "
    read -r answer
    if [[ "$answer" =~ ^[Yy]$ ]]; then
        do_import
    else
        log_info "已跳过导入，请手动执行:"
        echo "  sqlite3 <你的openlist数据库路径> < $OUTPUT_SQL"
    fi
else
    do_import
fi