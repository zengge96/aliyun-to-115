import sqlite3
import json
import csv
import io
import os
import shutil
import subprocess
import time
from datetime import datetime

# ================= 配置区 (参数已填入) =================
DB_PATH = "./data/data.db"
OPENLIST_BIN = "./openlist"  # 主程序路径
INPUT_SQL = "xiaoya.sql"
INIT_WAIT_TIME = 12          # 等待程序初始化数据库的时间(秒)

# 1. 阿里云 Open 令牌 (RefreshTokenOpen)
CONST_REFRESH_TOKEN_OPEN = "<REFRESH_TOKEN_OPEN>"

# 2. 阿里云普通刷新令牌 (RefreshToken)
CONST_REFRESH_TOKEN = "<REFRESH_TOKEN>"

# 3. 115 网盘 Cookie
CONST_115_COOKIE = "<115_COOKIE>"

# 4. 阿里云转存临时目录 ID
CONST_TEMP_TRANSFER_FOLDER_ID = "root"

# 5. 115 同步专用根目录 ID
CONST_SYNC_ROOT_ID = "<SYNC_ROOT_ID>"

# 6. 阿里云盘类型 (alipan 或 alipanTV)
CONST_ALIPAN_TYPE = "alipan"

# 丢弃的无效驱动
DISCARD_DRIVERS = ["PikPakShare", "QuarkShare", "AList V2", "AList V3", "UCShare"]
# =====================================================

def init_db():
    """初始化数据库逻辑"""
    # 确保 data 目录存在
    os.makedirs(os.path.dirname(DB_PATH), exist_ok=True)

    if not os.path.exists(DB_PATH):
        print(f">>> [1/5] 数据库不存在，尝试通过 {OPENLIST_BIN} 自动初始化...")
        if not os.path.exists(OPENLIST_BIN):
            print(f"!!! 错误: 找不到执行文件 {OPENLIST_BIN}，请检查路径。")
            return False
        
        # 赋予执行权限 (Linux/macOS)
        try:
            os.chmod(OPENLIST_BIN, 0o755)
        except:
            pass

        # 后台运行 openlist
        try:
            # 使用 Popen 启动，将输出重定向到黑洞，避免阻塞
            process = subprocess.Popen(
                [OPENLIST_BIN], 
                stdout=subprocess.DEVNULL, 
                stderr=subprocess.DEVNULL,
                cwd=os.getcwd()
            )
            print(f"  - 程序已启动 (PID: {process.pid})，等待 {INIT_WAIT_TIME}s 生成数据库结构...")
            time.sleep(INIT_WAIT_TIME)
            
            # 强制结束进程，否则 SQLite 会被占用（File Locked）
            process.terminate()
            process.wait(timeout=5)
            print("  - 初始进程已关闭，准备写入数据。")
        except Exception as e:
            print(f"!!! 启动程序失败: {e}")
            return False

    if os.path.exists(DB_PATH):
        # 备份现有数据库
        timestamp = datetime.now().strftime("%H%M%S")
        bak_path = f"{DB_PATH}.{timestamp}.bak"
        shutil.copy2(DB_PATH, bak_path)
        print(f">>> [1/5] 数据库已就绪，备份成功: {bak_path}")
        return True
    else:
        print(f"!!! 错误: 程序运行后未能生成 {DB_PATH}")
        return False

def transform_addition(driver, addition_str):
    try:
        data = json.loads(addition_str)
        if driver == "AliyundriveShare":
            data["refresh_token"] = CONST_REFRESH_TOKEN
            data["RefreshToken"] = CONST_REFRESH_TOKEN
            data["RefreshTokenOpen"] = CONST_REFRESH_TOKEN_OPEN
            data["TempTransferFolderID"] = CONST_TEMP_TRANSFER_FOLDER_ID
            data["use_online_api"] = True
            data["alipan_type"] = CONST_ALIPAN_TYPE
            data["api_url_address"] = "https://api.oplist.org/alicloud/renewapi"
            return "AliyundriveShare2Open", json.dumps(data, ensure_ascii=False)
        
        elif driver == "115 Share":
            data["cookie"] = CONST_115_COOKIE
            return "115 Share", json.dumps(data, ensure_ascii=False)
        
        elif driver == "Alias":
            if "paths" in data:
                data["paths"] = data.get("paths", "").replace("本地:", "")
            return "Alias", json.dumps(data, ensure_ascii=False)
            
        return driver, addition_str
    except:
        return driver, addition_str

def run():
    if not os.path.exists(INPUT_SQL):
        print(f"!!! 错误: 找不到 SQL 文件 {INPUT_SQL}")
        return

    # 第一步：初始化/备份
    if not init_db():
        return

    conn = sqlite3.connect(DB_PATH)
    cursor = conn.cursor()
    
    print(">>> [2/5] 清理旧数据...")
    try:
        cursor.execute("DELETE FROM x_storages;")
        cursor.execute("DELETE FROM x_meta;")
        cursor.execute("DELETE FROM x_setting_items;")
    except sqlite3.OperationalError as e:
        print(f"!!! 数据库表结构异常: {e}。请确保 openlist 版本匹配。")
        return
    
    print(">>> [3/5] 处理小雅 SQL 数据并同步...")
    storage_count = 0
    
    with open(INPUT_SQL, 'r', encoding='utf-8') as f:
        for line in f:
            line_strip = line.strip()
            if not line_strip or line_strip.startswith("#") or "X_META" in line_strip.upper():
                continue
            
            if line_strip.startswith("INSERT INTO x_storages"):
                start_idx = line_strip.find("(")
                end_idx = line_strip.rfind(")")
                vals_str = line_strip[start_idx+1:end_idx]
                
                f_io = io.StringIO(vals_str)
                reader = csv.reader(f_io, delimiter=',', quotechar="'", skipinitialspace=True)
                try: 
                    vals = list(reader)[0]
                except: 
                    continue
                
                driver_name = vals[3]
                if driver_name in DISCARD_DRIVERS: 
                    continue
                
                new_driver, new_addition = transform_addition(driver_name, vals[6])
                
                # 重新映射到 21 列
                new_vals = [
                    vals[0], vals[1], vals[2], new_driver, vals[4],
                    "", vals[5], new_addition, "", vals[8], vals[9],
                    "0", "0", vals[10], vals[11], vals[12], vals[13], vals[14],
                    "0", vals[15], "0"
                ]
                
                placeholders = ",".join(["?"] * 21)
                cursor.execute(f"INSERT INTO x_storages VALUES ({placeholders})", new_vals)
                storage_count += 1
            
            elif line_strip.startswith("INSERT INTO x_setting_items"):
                try: 
                    cursor.execute(line_strip.rstrip(';'))
                except: 
                    pass

    # --- 手动插入 AliyunTo115 驱动 ---
    print(">>> [4/5] 手动插入 AliyunTo115 驱动 (/115sync)...")
    sync_addition = {
        "open115_cookie": CONST_115_COOKIE,
        "sync_interval": 20,
        "qrcode_token": "", "qrcode_source": "", "page_size": 0,
        "limit_rate": 0, "delete_after_sync": False,
        "root_folder_id": CONST_SYNC_ROOT_ID
    }
    
    now_str = datetime.now().strftime('%Y-%m-%d %H:%M:%S')
    sync_vals = [
        None, '/115sync', 100, 'AliyunTo115', 'work', '', 0, 
        json.dumps(sync_addition), '', now_str, 0, 0, 0, 
        'false', '302_redirect', '', 0, 'none', 0, '', 0
    ]
    
    try:
        cursor.execute(f"INSERT INTO x_storages VALUES ({','.join(['?']*21)})", sync_vals)
        storage_count += 1
        print("  - /115sync 手动插入成功")
    except Exception as e:
        print(f"  - /115sync 插入失败: {e}")

    conn.commit()
    conn.close()
    
    print(">>> [5/5] 全部同步成功！")
    print(f"--- 迁移/新增总存储设备数: {storage_count} 个")
    print(f"\n[完成] 数据库已就绪，现在你可以正常启动 {OPENLIST_BIN} 了。")

if __name__ == "__main__":
    run()