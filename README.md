# tiptop

TiTop 项目的**链接资料采集**、**表字段资料采集**与**部署**工具。从
[`bdl-lsp`](https://github.com/darcyjoven/bdl-lsp) 分离而来。

## 为什么分离

`bdl-lsp` 只做语言服务：解析、诊断、跳转、补全。它需要知道"某个函数在
哪个文件里定义"、"某张表有哪些字段"，但不该关心这些信息**是哪个系统、
怎么采集来的**。

| | bdl-lsp | tiptop |
|---|---|---|
| 职责 | 语言智能 | 采集链接资料 + 表字段资料 + 部署 |
| 读取 | `<项目>/.project/links.jsonl`、`.project/.schema/` | 数据库 / CSV / 命令 |
| 写入 | — | `<项目>/.project/links.jsonl`、`.project/.schema/`（旧 `.link/` 只读兼容） |
| 部署 | — | rsync/scp 上传 + 远程编译 |

两者**只通过文件系统交互**。换一套链接结构（例如 tiptop100）时，照着
本仓库再做一个采集器即可，`bdl-lsp` 一行都不用改。

## 与 bdl-lsp 的关系

tiptop 复用 `bdl-lsp` 的公开包：

- `link` —— 链接资料的数据格式、模组布局、全语料扫描
- `parser` / `index` —— tree-sitter 解析与符号索引
- `builtin` —— 内置符号表（统计"外部函数"时排除）
- `glob` —— 文件匹配

`go.mod` 里有 `replace github.com/darcyjoven/bdl-lsp => ../bdl-lsp`，
所以**两个仓库要放在同级目录**：

```text
work/
├── bdl-lsp/
└── tiptop/
```

发布时把 `replace` 换成真实版本号。

## 构建

```bash
# Linux / macOS
make build          # 产物：artifacts/bin/tiptop

# Windows（无 make）
powershell -File scripts\build.ps1
```

受限环境（无 root、$HOME 只读）先加载 bdl-lsp 的工具链：

```bash
source ../bdl-lsp/scripts/dev-env.sh
make build
```

## 用法

```bash
# 1) 采集 DS.GAL_FILE 并生成 <项目>/.project/links.jsonl
tiptop link sync -root <项目根> -source golang \
  -host <Oracle主机> -service <服务名> -user <只读账号>

# 或者先在生产端用 sqlplus 导出 CSV，再导入
tiptop link import -csv .project/gal_file.csv -root <项目根>

# 2) 查看资料状态
tiptop link status -root <项目根>

# 2b) 局部更新：只重算受影响的程序，其余链原样保留（秒级，不用整份重跑）
tiptop link sync -files aap/4gl/saapt310.4gl            # 某个 4gl 相关的链接
tiptop link sync -modules lib,sub,qry                   # 某几个模组
tiptop link sync -files aap/4gl/saapt310.4gl -dry-run   # 先看"会影响几个作业"，不写盘
tiptop link import -csv gal_file.csv -files aap/4gl/saapt310.4gl

# 2c) 表字段资料：把 Oracle 导出的"表字段"CSV 转成 <项目>/.project/.schema
tiptop schema import -csv gal_schema.csv -root <项目根>
tiptop schema import -csv gal_schema.csv -root <项目根> -tables aaa_file,bab_file
tiptop schema import -csv gal_schema.csv -root <项目根> -only-new -dry-run
tiptop schema status -root <项目根>

# 3) 部署（上传 + 远程编译）
tiptop deploy -workspace <项目根> -list
tiptop deploy -workspace <项目根> -target prod -dry-run
tiptop deploy -workspace <项目根> -target prod tiptop/aap/4gl/aapi202.4gl
```

密码不要写进配置文件：

```bash
export BDL_ORA_PASSWORD='<密码>'     # 或 .project/secrets.json（0600）
```

## 局部更新（`-modules` / `-files`）

资料量大了以后，改一两个程序还整份重跑（全量约 10–20 秒）很浪费。局部更新：

- 只**采集/解析受影响的程序**：`-files aap/4gl/saapt310.4gl` 会找出"哪些作业引用了这个文件"
  （一个公共文件常被多个 MAIN 引用），`-modules lib,sub,qry` 按模组筛；
- 写盘时**合并**：受影响的程序用新链替换，其余链原样保留（不会把别的作业洗掉）；
- `-dry-run` 会打印"受影响作业 N 个"，可以先确认再执行；
- `meta` 里记 `partial`（时间、条件、作业数、重算链数），`link status` 会显示"局部更新…"；
- **前提**：资料目录里已经有一次**全量**导入的结果；没有基线时命令会明确报错，而不是写出半份资料；
- `source=golang` 且用默认 SQL 时，模组/文件条件会**下推到 Oracle**（少取行）；
  自定义 `-query` 时不做下推，只在内存里过滤。

## 采集来源

| `-source` | 说明 |
|---|---|
| `golang` | 纯 Go 驱动直连 Oracle 查 `DS.GAL_FILE`（默认） |
| `command` | 执行外部命令，CSV 打到 stdout |
| `file` | 读已导出的 CSV（默认 `<dataDir>/gal_file.csv`） |

CSV 需要表头 `GAL01,GAL02,GAL03,GAL04`；`gal04='Y'` 表示"确实链接"，
`'N'` 的行会被过滤。

## 表字段资料（`schema import` / `schema status`）

bdl-lsp 需要知道"某张表有哪些字段、字段的中文说明"，才能做字段说明与提示。
这份资料来自 DBA 从 Oracle 导出的"表字段"CSV（表名、列名、说明、类型、
长度、精度、是否可空等），用 `schema import` 一次性转换成 `.project/.schema/`：

```bash
# 默认写到 <当前目录>/.project/.schema
tiptop schema import -csv gal_schema.csv

# 指定项目根；先看统计不写盘
tiptop schema import -csv gal_schema.csv -root <项目根> -dry-run

# 只重算这两张表（其余表保留不动，表名大小写不敏感）
tiptop schema import -csv gal_schema.csv -tables aaa_file,bab_file

# 只补本地没有的表；已有表文件一个字节都不动（人工修订过的说明不会被覆盖）
tiptop schema import -csv gal_schema.csv -only-new

tiptop schema status -root <项目根>
```

产物（bdl-lsp 按同一约定读取）：

```text
<项目根>/.project/.schema/
├── tables.json             # 索引：importedAt / source / rowsTotal / 每张表的列数与文件名
└── tables/<大写表名>.json  # 列清单：name/desc/type/length/charLength/precision/scale/nullable/default
```

规则：

- **表头不严格按列名解析**：先把单元格归一化（小写、去掉非字母数字字符）再按
  别名匹配，认不出来就回退到默认列位置；实测导出里的 `data_precicison`、
  `default_legth` 两个拼写错误都能容忍；
- 允许 UTF-8 BOM、CRLF、引号包裹的逗号；`nullable` 支持 `Y/N`、`YES/NO`
  （大小写不敏感）；数字列空或非数字按 0 处理；`data_length` 为 0 时回退
  `char_length`；空行、列数不足或缺表名/列名的行**跳过并计数**，不中断整份导入；
- **默认全量**：CSV 里出现的表重算并更新索引条目，CSV 里没有的表**保留不删**；
  表内容逐字节没变化时不重写文件（避免无意义的 mtime 变更让编辑器重载）；
- `-tables` 只处理列出的表；`-only-new` 只写本地不存在的表；两者同时给出时，
  只在 `-tables` 给定的表里挑"本地不存在的"，互不冲突；
- 已有 `tables.json` 是坏 JSON（或 schema 版本过高）时命令**报错退出并保留
  原文件**，不会清空重写；
- 写盘一律先写临时文件再 rename，LSP 不会读到半份资料。

## 部署配置

`.project/deploy.json`（**VSCode 插件会从设置合成**；手写也行，tiptop 的配置模型不变）：

```jsonc
{
  "targets": {
    "prod": {
      "host": "<生产主机>",
      "user": "<账号>",
      "remoteRoot": "/u1/toppord",
      "localRoot": ".",
      "upload": { "include": ["**/*.4gl", "**/*.global", "**/*.per"], "mode": "rsync", "fallback": "scp" },
      "compile": { "4gl": "fglcomp -M {file}", "per": "fglform {file}" }
    }
  },
  "defaultTarget": "prod"
}
```

SSH 认证用 key / ssh-agent，不支持在配置里存密码。

## 仓库边界

本仓库是交付物。计划、测试脚本、中间产物统一放在
[`bdl-lsp`](https://github.com/darcyjoven/bdl-lsp) 仓库的 `docs/` 与
`artifacts/` 下，不散落在本仓库。

- 分离计划：[bdl-lsp/docs/PLAN-SEPARATION.md](https://github.com/darcyjoven/bdl-lsp/blob/main/docs/PLAN-SEPARATION.md)
- 工作区搭建指引：[vscode-bdl/docs/PROJECT-SETUP.md](https://github.com/darcyjoven/vscode-bdl/blob/main/docs/PROJECT-SETUP.md)
