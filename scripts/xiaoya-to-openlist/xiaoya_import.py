import sqlite3
import json
import csv
import io
import os
import shutil
import subprocess
import time
import ast  # 新增：用于安全解析列表字符串
from datetime import datetime

# ================= 配置区 (默认参数) =================
DB_PATH = "./data/data.db"
OPENLIST_BIN = "./openlist"
INPUT_SQL = "xiaoya.sql"
INIT_WAIT_TIME = 12

# 以下配置可被 config.txt 覆盖
CONST_REFRESH_TOKEN_OPEN = "<REFRESH_TOKEN_OPEN>"
CONST_REFRESH_TOKEN = "<REFRESH_TOKEN>"
CONST_115_COOKIE = "<115_COOKIE>"
CONST_115_SYNC_ROOT_ID = "auto"
CONST_TEMP_TRANSFER_FOLDER_ID = "root"
CONST_ALIPAN_TYPE = "alipan"

# 挂载目录过滤列表。支持 config.txt 中格式如 ["/路径1", "/路径2"]
MOUNT_PATHS = ["/每日更新"] 

# 丢弃的无效驱动
DISCARD_DRIVERS = ["PikPakShare", "QuarkShare", "AList V2", "AList V3", "UCShare"]
# =====================================================

def load_external_config():
    """从 config.txt 加载配置并覆盖全局变量"""
    config_path = "./config.txt"
    if not os.path.exists(config_path):
        return

    print(f">>> [0/5] 检测到 {config_path}，正在加载自定义配置...")
    global CONST_REFRESH_TOKEN_OPEN, CONST_REFRESH_TOKEN, CONST_115_COOKIE
    global CONST_115_SYNC_ROOT_ID, CONST_TEMP_TRANSFER_FOLDER_ID, CONST_ALIPAN_TYPE, MOUNT_PATHS

    with open(config_path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            
            if "=" in line:
                key, val = line.split("=", 1)
                key = key.strip()
                val = val.strip()

                # 特殊处理 MOUNT_PATHS，解析 ["a", "b"] 格式
                if key == "MOUNT_PATHS":
                    try:
                        # 使用 ast.literal_eval 将字符串 ["/a", "/b"] 转为真正的 list
                        parsed_list = ast.literal_eval(val)
                        if isinstance(parsed_list, list):
                            MOUNT_PATHS = [str(p).strip() for p in parsed_list]
                    except Exception as e:
                        print(f"!!! 警告: MOUNT_PATHS 格式解析失败，请确保格式为 [\"/A\", \"/B\"]。错误: {e}")
                    continue

                # 其他常规配置处理
                val = val.strip("'").strip('"')
                if key == "CONST_REFRESH_TOKEN_OPEN": CONST_REFRESH_TOKEN_OPEN = val
                elif key == "CONST_REFRESH_TOKEN": CONST_REFRESH_TOKEN = val
                elif key == "CONST_115_COOKIE": CONST_115_COOKIE = val
                elif key == "CONST_115_SYNC_ROOT_ID": CONST_115_SYNC_ROOT_ID = val
                elif key == "CONST_TEMP_TRANSFER_FOLDER_ID": CONST_TEMP_TRANSFER_FOLDER_ID = val
                elif key == "CONST_ALIPAN_TYPE": CONST_ALIPAN_TYPE = val

def init_db():
    """初始化数据库逻辑"""
    os.makedirs(os.path.dirname(DB_PATH), exist_ok=True)

    if not os.path.exists(DB_PATH):
        print(f">>> [1/5] 数据库不存在，尝试通过 {OPENLIST_BIN} 自动初始化...")
        if not os.path.exists(OPENLIST_BIN):
            print(f"!!! 错误: 找不到执行文件 {OPENLIST_BIN}")
            return False
        
        try:
            os.chmod(OPENLIST_BIN, 0o755)
            process = subprocess.Popen(
                [OPENLIST_BIN], 
                stdout=subprocess.DEVNULL, 
                stderr=subprocess.DEVNULL,
                cwd=os.getcwd()
            )
            print(f"  - 程序已启动 (PID: {process.pid})，等待 {INIT_WAIT_TIME}s 生成数据库结构...")
            time.sleep(INIT_WAIT_TIME)
            
            process.terminate()
            process.wait(timeout=5)
            print("  - 初始进程已关闭，准备写入数据。")
        except Exception as e:
            print(f"!!! 启动程序失败: {e}")
            return False

    if os.path.exists(DB_PATH):
        timestamp = datetime.now().strftime("%H%M%S")
        bak_path = f"{DB_PATH}.{timestamp}.bak"
        shutil.copy2(DB_PATH, bak_path)
        print(f">>> [1/5] 数据库已就绪，备份成功: {bak_path}")
        return True
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

def insert_storage_record(cursor, mount_path, order, driver, addition, status="work", modified_at=None):
    """映射 AList 存储表的 21 个字段"""
    if modified_at is None:
        modified_at = datetime.now().strftime('%Y-%m-%d %H:%M:%S')
    
    new_vals = [
        None, mount_path, order, driver, status, "", "0", 
        addition, "", modified_at, "0", "0", "0", 
        "false", "302_redirect", "", "0", "none", 0, "", 0
    ]
    cursor.execute(f"INSERT INTO x_storages VALUES ({','.join(['?']*21)})", new_vals)

def run():
    load_external_config()

    if not os.path.exists(INPUT_SQL):
        print(f"!!! 错误: 找不到 SQL 文件 {INPUT_SQL}")
        return

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
        print(f"!!! 数据库表结构异常: {e}")
        return
    
    print(">>> [3/5] 处理小雅 SQL 数据并应用过滤...")
    storage_count = 0
    
    with open(INPUT_SQL, 'r', encoding='utf-8') as f:
        for line in f:
            line_strip = line.strip()
            if not line_strip or line_strip.startswith("#") or "X_META" in line_strip.upper():
                continue
            
            if line_strip.startswith("INSERT INTO x_setting_items"):
                try: cursor.execute(line_strip.rstrip(';'))
                except: pass
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
                
                # 过滤驱动
                if vals[3] in DISCARD_DRIVERS: 
                    continue
                
                # 过滤挂载目录
                mount_path = vals[1]
                if MOUNT_PATHS:
                    if not any(mount_path.startswith(p) for p in MOUNT_PATHS):
                        continue

                new_driver, new_addition = transform_addition(vals[3], vals[6])
                
                try:
                    insert_storage_record(
                        cursor, 
                        mount_path=mount_path,
                        order=vals[2],
                        driver=new_driver,
                        addition=new_addition,
                        modified_at=vals[7]
                    )
                    storage_count += 1
                except Exception as e:
                    print(f"  - 跳过错误行 ({mount_path}): {e}")

    # 手动插入 AliyunTo115
    print(">>> [4/5] 手动插入 AliyunTo115 驱动 (/115sync)...")
    sync_addition = {
        "open115_cookie": CONST_115_COOKIE,
        "sync_interval": 20,
        "root_folder_id": CONST_115_SYNC_ROOT_ID,
        "qrcode_token": "", "qrcode_source": "", "page_size": 0,
        "limit_rate": 0, "delete_after_sync": False
    }
    
    try:
        insert_storage_record(
            cursor,
            mount_path='/115sync',
            order=100,
            driver='AliyunTo115',
            addition=json.dumps(sync_addition)
        )
        storage_count += 1
        print("  - /115sync 插入成功")
    except Exception as e:
        print(f"  - /115sync 插入失败: {e}")

    conn.commit()
    conn.close()
    
    print(">>> [5/5] 全部同步成功！")
    print(f"--- 总存储设备数: {storage_count} ---")
    if MOUNT_PATHS:
        print(f"--- 仅包含白名单目录: {MOUNT_PATHS} ---")

if __name__ == "__main__":
    run()