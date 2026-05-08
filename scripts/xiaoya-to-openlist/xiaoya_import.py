import sqlite3
import json
import csv
import io
import os
import shutil
from datetime import datetime

# ================= 配置区 (参数已填入) =================
DB_PATH = "data.db"
INPUT_SQL = "xiaoya.sql"

# 1. 阿里云 Open 令牌 (RefreshTokenOpen)
CONST_REFRESH_TOKEN_OPEN = "<REFRESH_TOKEN_OPEN>"

# 2. 阿里云普通刷新令牌 (RefreshToken)
CONST_REFRESH_TOKEN = "<REFRESH_TOKEN>"

# 3. 115 网盘 Cookie
CONST_115_COOKIE = "<115_COOKIE>"

# 4. 阿里云转存临时目录 ID
CONST_TEMP_TRANSFER_FOLDER_ID = "root"

# 5. 115 同步专用根目录 ID (来自你之前的 newsyc ID)
CONST_SYNC_ROOT_ID = "<SYNC_ROOT_ID>"

# 丢弃的无效驱动
DISCARD_DRIVERS = ["PikPakShare", "QuarkShare", "AList V2", "AList V3", "UCShare"]
# =====================================================

def backup_db(path):
    if os.path.exists(path):
        timestamp = datetime.now().strftime("%H%M%S")
        bak_path = f"{path}.{timestamp}.bak"
        shutil.copy2(path, bak_path)
        print(f">>> [1/5] 备份成功: {bak_path}")

def transform_addition(driver, addition_str):
    try:
        data = json.loads(addition_str)
        if driver == "AliyundriveShare":
            data["refresh_token"] = CONST_REFRESH_TOKEN
            data["RefreshToken"] = CONST_REFRESH_TOKEN
            data["RefreshTokenOpen"] = CONST_REFRESH_TOKEN_OPEN
            data["TempTransferFolderID"] = CONST_TEMP_TRANSFER_FOLDER_ID
            data["use_online_api"] = True
            data["api_url_address"] = "https://api.oplist.org/alicloud/renewapi"
            return "AliyundriveShare2Open", json.dumps(data, ensure_ascii=False)
        elif driver == "115 Share":
            data["cookie"] = CONST_115_COOKIE
            return "115 Share", json.dumps(data, ensure_ascii=False)
        elif driver == "Alias":
            data["paths"] = data.get("paths", "").replace("本地:", "")
            return "Alias", json.dumps(data, ensure_ascii=False)
        return driver, addition_str
    except:
        return driver, addition_str

def run():
    if not os.path.exists(INPUT_SQL):
        print(f"!!! 错误: 找不到文件 {INPUT_SQL}")
        return

    backup_db(DB_PATH)
    conn = sqlite3.connect(DB_PATH)
    cursor = conn.cursor()
    
    print(">>> [2/5] 清理旧数据...")
    cursor.execute("DELETE FROM x_storages;")
    cursor.execute("DELETE FROM x_meta;")
    cursor.execute("DELETE FROM x_setting_items;")
    
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
                try: vals = list(reader)[0]
                except: continue
                
                if vals[3] in DISCARD_DRIVERS: continue
                new_driver, new_addition = transform_addition(vals[3], vals[6])
                
                # 补全到 21 列，强制清除 remark 字段
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
                try: cursor.execute(line_strip.rstrip(';'))
                except: pass

    # --- 手动插入 AliyunTo115 驱动 ---
    print(">>> [4/5] 手动插入 AliyunTo115 驱动 (/115sync)...")
    sync_addition = {
        "open115_cookie": CONST_115_COOKIE,
        "sync_interval": 20,
        "qrcode_token": "",
        "qrcode_source": "",
        "page_size": 0,
        "limit_rate": 0,
        "delete_after_sync": False,
        "root_folder_id": CONST_SYNC_ROOT_ID
    }
    
    # 构造 21 列数据，挂载目录设为 /115sync
    sync_vals = [
        None, '/115sync', 0, 'AliyunTo115', 1, '', 'work', 
        json.dumps(sync_addition), '', 
        datetime.now().strftime('%Y-%m-%d %H:%M:%S'), 0,
        0, 0, '', '', '', 0, '302_redirect', 0, '', 0
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
    print(f"--- 迁移/新增总驱动数: {storage_count} 个")
    print(f"--- 所有的备注及 x_meta 乱码已清除。")
    print(f"\n请重启 OpenList 使数据库生效。")

if __name__ == "__main__":
    run()