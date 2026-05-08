import sqlite3
import json
import csv
import io
import os
import shutil
import subprocess
import time
import re
from datetime import datetime

# ================= 配置区 (默认参数) =================
DB_PATH = "./data/data.db"
OPENLIST_BIN = "./openlist"
INPUT_SQL = "xiaoya.sql"
INIT_WAIT_TIME = 12

# 基础配置项 (可被 config.txt 覆盖)
CONFIG = {
    "CONST_REFRESH_TOKEN_OPEN": "<REFRESH_TOKEN_OPEN>",
    "CONST_REFRESH_TOKEN": "<REFRESH_TOKEN>",
    "CONST_115_COOKIE": "<115_COOKIE>",
    "CONST_115_SYNC_ROOT_ID": "auto",
    "CONST_TEMP_TRANSFER_FOLDER_ID": "root",
    "CONST_ALIPAN_TYPE": "alipan",
}

# 挂载目录白名单：例如 ["/每日更新", "/整理中"]。为空则表示全部挂载。
MOUNT_WHITELIST = [] 

# 需要丢弃的无效驱动
DISCARD_DRIVERS = ["PikPakShare", "QuarkShare", "AList V2", "AList V3", "UCShare"]
# =====================================================

def load_external_config():
    """从 config.txt 加载配置并覆盖默认参数"""
    config_path = "./config.txt"
    if not os.path.exists(config_path):
        return

    print(f">>> [0/5] 检测到 {config_path}，正在加载自定义配置...")
    global MOUNT_WHITELIST

    with open(config_path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            # 跳过注释和空行
            if not line or line.startswith(("#", "--")) or "=" not in line:
                continue
            
            # 拆分键值对
            key, val = line.split("=", 1)
            key = key.strip()
            # 去除值两侧的空格和最外层引号
            clean_val = val.strip().strip("'").strip('"')

            if key in CONFIG:
                CONFIG[key] = clean_val
            elif key == "MOUNT_WHITELIST":
                # 处理格式如: "/目录1","/目录2" 或 /目录1,/目录2 或 ["/目录1"]
                raw_list = clean_val.strip("[]").split(",")
                MOUNT_WHITELIST = [
                    p.strip().strip("'").strip('"') 
                    for p in raw_list 
                    if p.strip()
                ]
                print(f"  - 已加载挂载白名单: {MOUNT_WHITELIST}")

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
        print(f">>> [1/5] 数据库就绪，备份成功: {bak_path}")
        return True
    return False

def transform_addition(driver, addition_str):
    """根据驱动类型转换配置字符串"""
    try:
        data = json.loads(addition_str)
        if driver == "AliyundriveShare":
            data.update({
                "refresh_token": CONFIG["CONST_REFRESH_TOKEN"],
                "RefreshToken": CONFIG["CONST_REFRESH_TOKEN"],
                "RefreshTokenOpen": CONFIG["CONST_REFRESH_TOKEN_OPEN"],
                "TempTransferFolderID": CONFIG["CONST_TEMP_TRANSFER_FOLDER_ID"],
                "use_online_api": True,
                "alipan_type": CONFIG["CONST_ALIPAN_TYPE"],
                "api_url_address": "https://api.oplist.org/alicloud/renewapi"
            })
            return "AliyundriveShare2Open", json.dumps(data, ensure_ascii=False)
        
        elif driver == "115 Share":
            data["cookie"] = CONFIG["CONST_115_COOKIE"]
            return "115 Share", json.dumps(data, ensure_ascii=False)
        
        elif driver == "Alias":
            if "paths" in data:
                data["paths"] = data.get("paths", "").replace("本地:", "")
            return "Alias", json.dumps(data, ensure_ascii=False)
            
        return driver, addition_str
    except:
        return driver, addition_str

def run():
    # 0. 加载外部配置
    load_external_config()

    if not os.path.exists(INPUT_SQL):
        print(f"!!! 错误: 找不到 SQL 文件 {INPUT_SQL}")
        return

    # 1. 初始化/备份
    if not init_db():
        return

    conn = sqlite3.connect(DB_PATH)
    cursor = conn.cursor()
    
    print(">>> [2/5] 清理旧数据...")
    try:
        cursor.execute("DELETE FROM x_storages;")
        cursor.execute("DELETE FROM x_meta;")
        # cursor.execute("DELETE FROM x_setting_items;") # 如需保留设置可注释此行
    except sqlite3.OperationalError as e:
        print(f"!!! 数据库表结构异常: {e}")
        conn.close()
        return
    
    print(">>> [3/5] 处理 SQL 挂载点数据...")
    storage_count = 0
    
    # 正则：匹配 INSERT INTO x_storages VALUES (内容);
    pattern = re.compile(r"INSERT INTO x_storages.*?VALUES\s*\((.*)\);", re.IGNORECASE)
    
    with open(INPUT_SQL, 'r', encoding='utf-8') as f:
        for line in f:
            line_strip = line.strip()
            if not line_strip or line_strip.startswith(("#", "--")) or "X_META" in line_strip.upper():
                continue
            
            # 处理配置项 (设置项)
            if line_strip.startswith("INSERT INTO x_setting_items"):
                try: cursor.execute(line_strip.rstrip(';'))
                except: pass
                continue

            # 处理存储驱动
            match = pattern.search(line_strip)
            if match:
                vals_str = match.group(1)
                
                # 使用 csv 模块解析 SQL 中的逗号（能正确处理引号内的逗号）
                f_io = io.StringIO(vals_str)
                reader = csv.reader(f_io, delimiter=',', quotechar="'", skipinitialspace=True)
                try: 
                    vals = list(reader)[0]
                except: 
                    continue
                
                mount_path = vals[1]
                driver_name = vals[3]

                # --- 过滤器 1: 驱动过滤 ---
                if driver_name in DISCARD_DRIVERS: 
                    continue
                
                # --- 过滤器 2: 挂载目录白名单过滤 (实现 TODO) ---
                if MOUNT_WHITELIST:
                    # 检查 mount_path 是否以白名单中的任何一个开头
                    if not any(mount_path.startswith(prefix) for prefix in MOUNT_WHITELIST):
                        continue
                
                new_driver, new_addition = transform_addition(driver_name, vals[6])
                
                # 重新映射到 21 列 (适配新版数据库结构)
                # 映射索引: 0:id, 1:mount_path, 2:order, 3:driver, 4:status, 6:addition, 8:remark...
                try:
                    new_vals = [
                        vals[0], mount_path, vals[2], new_driver, vals[4],
                        "", vals[5], new_addition, "", vals[8], vals[9],
                        "0", "0", vals[10], vals[11], vals[12], vals[13], vals[14],
                        "0", vals[15], "0"
                    ]
                    cursor.execute(f"INSERT INTO x_storages VALUES ({','.join(['?']*21)})", new_vals)
                    storage_count += 1
                except Exception as e:
                    print(f"  - 插入失败 [{mount_path}]: {e}")

    # --- 手动插入 AliyunTo115 驱动 ---
    print(">>> [4/5] 手动插入 AliyunTo115 驱动 (/115sync)...")
    sync_addition = {
        "open115_cookie": CONFIG["CONST_115_COOKIE"],
        "sync_interval": 20,
        "root_folder_id": CONFIG["CONST_115_SYNC_ROOT_ID"],
        "qrcode_token": "", "qrcode_source": "", "page_size": 0,
        "limit_rate": 0, "delete_after_sync": False
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
    print(f"--- 总存储设备数: {storage_count} ---")

if __name__ == "__main__":
    run()