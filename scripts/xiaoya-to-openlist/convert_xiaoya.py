#!/usr/bin/env python3
"""
convert_xiaoya.py - 将 xiaoya update.sql 转换为 OpenList 可导入格式
"""
import sys, re, json, os

DRIVER_MAP = {
    "AliyundriveShare": "AliyundriveShare2Open",
    "115 Share": "115Share",
    "Alias": "Local",
    "AList V2": None,
    "AList V3": None,
    "QuarkShare": None,
    "PikPakShare": None,
}


def extract_addition_from_line(line_str):
    """从 SQL 行中安全提取 addition 字段（处理嵌套引号）"""
    # addition 字段是 JSON 对象，形如 {'work','{"paths":"...\\n..."}',''
    # 在 'work',' 之后，以 }',' 结尾
    m = re.search(r"'work','(\{[^}]+(?:\{[^}]*\}[^}]*)*\})','", line_str)
    if m:
        return m.group(1)
    return None


def parse_insert(line_bytes):
    """解析 INSERT 行，返回字段列表"""
    line_str = line_bytes.decode("utf-8", errors="replace")
    m = re.match(r"INSERT INTO x_storages VALUES\((.*)\);", line_str, re.DOTALL)
    if not m:
        return None
    vals_str = m.group(1)

    # 用 ',' 分隔，但要在 SQL 字符串内跳过
    fields, i, cur, in_str, esc_next = [], 0, [], False, False
    while i < len(vals_str):
        c = vals_str[i]
        if esc_next:
            cur.append(c)
            esc_next = False
            i += 1
            continue
        if c == "\\":
            esc_next = True
            i += 1
            continue
        if c == "'":
            in_str = not in_str
            i += 1
            continue
        if c == "," and not in_str:
            fields.append("".join(cur).strip())
            cur = []
            i += 1
            continue
        cur.append(c)
        i += 1
    if cur:
        fields.append("".join(cur).strip())
    return fields


def strip_q(s):
    if len(s) >= 2 and s[0] == "'" and s[-1] == "'":
        return s[1:-1]
    return s


def convert_datetime(s):
    m = re.match(r"(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\.(\d+)\+00:00", s)
    if m:
        return m.group(1).replace(" ", "T") + "Z"
    return s


def convert_alias_addition(addition_str):
    """处理 Alias paths: 分割 \\n 并去掉 '本地:' 前缀"""
    try:
        pm = re.search(r'"paths"\s*:\s*"([^"]+)"', addition_str)
        if not pm:
            return addition_str
        raw_paths = pm.group(1)
        parts = raw_paths.split("\\n")
        cleaned = [re.sub(r"^本地:", "", p.strip()) for p in parts if p.strip()]
        new_paths = "\n".join(cleaned)
        new_add = re.sub(r'"paths"\s*:\s*"[^"]+"', f'"paths":"{new_paths}"', addition_str)
        return new_add
    except:
        return addition_str


def convert_addition(driver, addition_str):
    if driver == "AliyundriveShare":
        try:
            j = json.loads(addition_str)
            if "share_pwd" in j:
                j["share_password"] = j.pop("share_pwd")
            return json.dumps(j, ensure_ascii=False, separators=(",", ":"))
        except:
            return addition_str
    elif driver == "Alias":
        return convert_alias_addition(addition_str)
    return addition_str


def main():
    input_file = sys.argv[1] if len(sys.argv) > 1 else "update.sql"
    output_file = sys.argv[2] if len(sys.argv) > 2 else "openlist_import.sql"

    if not os.path.exists(input_file):
        print(f"文件不存在: {input_file}", file=sys.stderr)
        sys.exit(1)

    with open(input_file, "rb") as f:
        content = f.read()

    total = skipped = converted = 0
    output_lines = [
        "-- Converted from xiaoya update.sql for OpenList",
        "-- Source: https://github.com/xiaoyaDev/data/raw/refs/heads/main/update.zip",
        "",
    ]

    for line_bytes in content.split(b"\n"):
        if not line_bytes.startswith(b"INSERT INTO x_storages "):
            continue

        line_str = line_bytes.decode("utf-8", errors="replace")
        fields = parse_insert(line_bytes)
        if not fields or len(fields) < 15:
            total += 1
            skipped += 1
            continue

        total += 1
        id_         = strip_q(fields[0])
        mount_path  = strip_q(fields[1])
        order       = strip_q(fields[2])
        driver      = strip_q(fields[3])
        cache_exp   = strip_q(fields[4])
        status      = strip_q(fields[5])
        remark      = strip_q(fields[7]) if len(fields) > 7 else ""
        modified    = convert_datetime(strip_q(fields[8]))
        disabled    = "0" if strip_q(fields[9]) in ("0", "false") else "1"
        order_by    = strip_q(fields[10]) if len(fields) > 10 else "name"
        order_dir   = strip_q(fields[11]) if len(fields) > 11 else "asc"
        extract_fol = strip_q(fields[12]) if len(fields) > 12 else "front"
        web_proxy   = strip_q(fields[13]) if len(fields) > 13 else "0"
        webdav_pol  = strip_q(fields[14]) if len(fields) > 14 else "302_redirect"
        down_proxy  = strip_q(fields[15]) if len(fields) > 15 else ""

        mapped = DRIVER_MAP.get(driver)
        if mapped is None:
            print(f"  [跳过] id={id_} driver={driver} - 不支持")
            skipped += 1
            continue

        # addition 用专用提取函数
        addition = extract_addition_from_line(line_str)
        if not addition:
            skipped += 1
            continue
        addition = convert_addition(driver, addition)

        out_line = (
            f"INSERT INTO storages VALUES("
            f"{id_},'{mount_path}',{order},'{mapped}',{cache_exp},'{status}',"
            f"'{addition}','{remark}','{modified}',{disabled},"
            f"'{order_by}','{order_dir}','{extract_fol}',"
            f"{web_proxy},'{webdav_pol}','{down_proxy}');"
        )
        output_lines.append(out_line)
        converted += 1
        print(f"  [转换] id={id_} {driver} -> {mapped}: {mount_path}")

    with open(output_file, "w", encoding="utf-8") as f:
        f.write("\n".join(output_lines) + "\n")

    print(f"\n完成: 总计={total} 转换={converted} 跳过={skipped}")
    print(f"输出: {output_file}")

if __name__ == "__main__":
    main()