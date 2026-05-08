import sqlite3
import json
import csv
import io
import os
import shutil
import subprocess
import time
import ast
from datetime import datetime

# ================= 配置区 =================
DB_PATH = "./data/data.db"
OPENLIST_BIN = "./openlist"
INPUT_SQL = "xiaoya.sql"
INIT_WAIT_TIME = 12

# 默认参数 (可被 config.txt 覆盖)
CONST_REFRESH_TOKEN_OPEN = "<REFRESH_TOKEN_OPEN>"
CONST_REFRESH_TOKEN = "<REFRESH_TOKEN>"
CONST_115_COOKIE = "<115_COOKIE>"
CONST_115_SYNC_ROOT_ID = "root"
CONST_TEMP_TRANSFER_FOLDER_ID = "root"
CONST_ALIPAN_TYPE = "alipan"
MOUNT_PATHS = [] 

DISCARD_DRIVERS = ["PikPakShare", "QuarkShare", "AList V2", "AList V3", "UCShare"]

def load_external_config():
    config_path = "./config.txt"
    if not os.path.exists(config_path): return
    print(f">>> [0/5] 加载自定义配置...")
    global CONST_REFRESH_TOKEN_OPEN, CONST_REFRESH_TOKEN, CONST_115_COOKIE
    global CONST_115_SYNC_ROOT_ID, CONST_TEMP_TRANSFER_FOLDER_ID, CONST_ALIPAN_TYPE, MOUNT_PATHS
    with open(config_path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line: continue
            key, val = line.split("=", 1)
            key, val = key.strip(), val.strip()
            if key == "MOUNT_PATHS":
                try:
                    parsed = ast.literal_eval(val)
                    if isinstance(parsed, list): MOUNT_PATHS = parsed
                except: pass
                continue
            val = val.strip("'").strip('"')
            if key == "CONST_REFRESH_TOKEN_OPEN": CONST_REFRESH_TOKEN_OPEN = val
            elif key == "CONST_REFRESH_TOKEN": CONST_REFRESH_TOKEN = val
            elif key == "CONST_115_COOKIE": CONST_115_COOKIE = val
            elif key == "CONST_115_SYNC_ROOT_ID": CONST_115_SYNC_ROOT_ID = val
            elif key == "CONST_TEMP_TRANSFER_FOLDER_ID": CONST_TEMP_TRANSFER_FOLDER_ID = val
            elif key == "CONST_ALIPAN_TYPE": CONST_ALIPAN_TYPE = val

def init_db():
    os.makedirs(os.path.dirname(DB_PATH), exist_ok=True)
    if not os.path.exists(DB_PATH):
        print(f">>> [1/5] 正在通过 {OPENLIST_BIN} 初始化数据库...")
        try:
            if os.path.exists(OPENLIST_BIN):
                os.chmod(OPENLIST_BIN, 0o755)
            # 确保在当前目录下运行，以便相对路径生效
            process = subprocess.Popen([OPENLIST_BIN], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            time.sleep(INIT_WAIT_TIME)
            process.terminate()
            process.wait()
        except Exception as e:
            print(f"!!! 初始化失败: {e}")
            return False
            
    # 修复：确保文件真的生成了再去备份，否则 shutil 会报错
    if os.path.exists(DB_PATH):
        timestamp = datetime.now().strftime("%H%M%S")
        shutil.copy2(DB_PATH, f"{DB_PATH}.{timestamp}.bak")
        return True
    else:
        print(f"!!! 错误: {DB_PATH} 未生成，请检查 OpenList 是否正常启动。")
        return False

def transform_addition(driver, addition_str):
    try:
        data = json.loads(addition_str)
        if driver == "AliyundriveShare":
            data.update({
                "refresh_token": CONST_REFRESH_TOKEN,
                "RefreshToken": CONST_REFRESH_TOKEN,
                "RefreshTokenOpen": CONST_REFRESH_TOKEN_OPEN,
                "TempTransferFolderID": CONST_TEMP_TRANSFER_FOLDER_ID,
                "use_online_api": True,
                "alipan_type": CONST_ALIPAN_TYPE,
                "api_url_address": "https://api.oplist.org/alicloud/renewapi"
            })
            return "AliyundriveShare2Open", json.dumps(data, ensure_ascii=False)
        elif driver == "115 Share":
            data["cookie"] = CONST_115_COOKIE
            return "115 Share", json.dumps(data, ensure_ascii=False)
        elif driver == "Alias":
            if "paths" in data and isinstance(data["paths"], str):
                data["paths"] = data["paths"].replace("本地:", "")
            return "Alias", json.dumps(data, ensure_ascii=False)
        return driver, addition_str
    except:
        return driver, addition_str

def run():
    load_external_config()
    if not os.path.exists(INPUT_SQL):
        print(f"!!! 错误: 找不到文件 {INPUT_SQL}")
        return
        
    if not init_db(): return

    conn = sqlite3.connect(DB_PATH)
    cursor = conn.cursor()
    
    print(">>> [2/5] 清理旧数据...")
    for table in ["x_storages", "x_meta", "x_setting_items"]:
        try: cursor.execute(f"DELETE FROM {table};")
        except: pass
    
    print(">>> [3/5] 正在同步 SQL 数据...")
    storage_count = 0
    with open(INPUT_SQL, 'r', encoding='utf-8') as f:
        for line in f:
            line = line.strip()
            if not line or "X_META" in line.upper(): continue
            
            if line.startswith("INSERT INTO x_setting_items"):
                try: cursor.execute(line.rstrip(';'))
                except: pass
                continue

            if line.startswith("INSERT INTO x_storages"):
                start_idx, end_idx = line.find("("), line.rfind(")")
                f_io = io.StringIO(line[start_idx+1:end_idx])
                reader = csv.reader(f_io, delimiter=',', quotechar="'", skipinitialspace=True)
                try: vals = list(reader)[0]
                except: continue
                
                # 过滤
                if vals[3] in DISCARD_DRIVERS: continue
                mount_path = vals[1]
                if MOUNT_PATHS and not any(mount_path.startswith(p) for p in MOUNT_PATHS): continue

                # 转换驱动和配置
                new_driver, new_addition = transform_addition(vals[3], vals[6])
                
                # 21 列映射逻辑 (与老版本成功逻辑完全对齐)
                new_vals = [
                    vals[0], mount_path, vals[2], new_driver, vals[4],
                    "", vals[5], new_addition, "", vals[8], vals[9],
                    "0", "0", vals[10], vals[11], vals[12], vals[13], vals[14],
                    "0", vals[15], "0"
                ]
                
                try:
                    cursor.execute(f"INSERT INTO x_storages VALUES ({','.join(['?']*21)})", new_vals)
                    storage_count += 1
                except Exception as e:
                    print(f"  - 写入失败 ({mount_path}): {e}")

    print(">>> [4/5] 插入 AliyunTo115 驱动...")
    sync_addition = {
        "open115_cookie": CONST_115_COOKIE, 
        "sync_interval": 20,
        "root_folder_id": CONST_115_SYNC_ROOT_ID, 
        "qrcode_token": "", 
        "qrcode_source": "", 
        "page_size": 0, 
        "limit_rate": 0, 
        "delete_after_sync": False
    }
    
    # 【核心修复】：还原为老脚本能够正常被 OpenList Go语言解析的列顺序和类型！
    # 新脚本中此处错乱把字符串 'work' 放在了数字字段，导致 OpenList 读取数据库时必定 Panic 崩溃。
    sync_vals = [
        None, '/115sync', 0, 'AliyunTo115', 1, '', 'work', 
        json.dumps(sync_addition), '', 
        datetime.now().strftime('%Y-%m-%d %H:%M:%S'), 0,
        0, 0, '', '', '', 0, '302_redirect', 0, '', 0
    ]
    try:
        cursor.execute(f"INSERT INTO x_storages VALUES ({','.join(['?']*21)})", sync_vals)
        storage_count += 1
    except Exception as e: 
        print(f"  - /115sync 插入失败: {e}")

    conn.commit()
    conn.close()
    print(f">>> [5/5] 同步完成！总挂载数: {storage_count}")

if __name__ == "__main__":
    run()