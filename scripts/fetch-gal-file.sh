#!/usr/bin/env bash
# S2 采集：在装了 sqlplus 的服务器上导出 DS.GAL_FILE 为 CSV（零依赖兜底方案）。
#
# 用法（在服务器上跑，或本地有 sqlplus 时跑）：
#   bash scripts/fetch-gal-file.sh [输出文件]
#
# 默认输出到 .project/gal_file.csv；再在本地执行：
#   tiptop link import -csv .project/gal_file.csv
# 或者直接：
#   tiptop link sync -source file           # 默认就读 .project/gal_file.csv
#
# 为什么不把账号密码写进脚本：`read -s` 交互输入，不进 shell history，
# 也不出现在 ps 的命令行里。要无人值守请用 Oracle wallet 或
# `tiptop link sync -source golang`（密码走环境变量/密文件）。
set -euo pipefail

OUT="${1:-.project/gal_file.csv}"

if ! command -v sqlplus >/dev/null 2>&1; then
  echo "找不到 sqlplus：这个脚本要在装了 Oracle 客户端的服务器上跑；" >&2
  echo "本地纯 Go 方案请用：tiptop link sync -source golang" >&2
  exit 2
fi

# 必填：连接串（host:port/service 或 TNS 别名）与只读账号。
read -r -p "Oracle 连接串（如 dbhost:1521/PROD）: " DB_CONN
read -r -p "只读账号: " DB_USER
read -r -s -p "密码: " DB_PASS
echo
if [ -z "$DB_CONN" ] || [ -z "$DB_USER" ]; then
  echo "连接串与账号都不能为空" >&2
  exit 2
fi

mkdir -p "$(dirname "$OUT")"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

# `SET MARKUP CSV ON` 让 sqlplus 直接吐 CSV（12.2+），DELIMITER 用逗号、
# QUOTE ON 保证字段带引号——与 `link import` 的解析假设一致。
# gal04='Y' 在 SQL 里就过滤掉（实测 51 行是 'N'）。
cat >"$TMP" <<SQL
SET HEADING ON
SET FEEDBACK OFF
SET PAGESIZE 0
SET LINESIZE 32767
SET TRIMSPOOL ON
SET MARKUP CSV ON DELIMITER , QUOTE ON
SELECT gal01, gal02, gal03, gal04 FROM DS.GAL_FILE WHERE gal04 = 'Y';
EXIT
SQL

# 密码走 stdin（sqlplus 的 CONNECT 支持从这里读），不进命令行参数。
printf '%s/%s@%s\n@%s\nEXIT\n' "$DB_USER" "$DB_PASS" "$DB_CONN" "$TMP" \
  | sqlplus -s /nolog >"$OUT"

LINES=$(wc -l <"$OUT" | tr -d ' ')
if [ "$LINES" -lt 2 ]; then
  echo "导出结果只有 $LINES 行，检查权限与 gal04 条件；文件：$OUT" >&2
  exit 1
fi
echo "已导出 $((LINES - 1)) 行到 $OUT"
echo "接下来：tiptop link import -csv $OUT"
