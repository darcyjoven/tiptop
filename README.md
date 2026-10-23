# tiptop

TiTop 项目的**链接资料采集**与**部署**工具。从
[`bdl-lsp`](https://github.com/darcyjoven/bdl-lsp) 分离而来。

## 为什么分离

`bdl-lsp` 只做语言服务：解析、诊断、跳转、补全。它需要知道"某个函数在
哪个文件里定义"，但不该关心这个信息**是哪个系统、怎么采集来的**。

| | bdl-lsp | tiptop |
|---|---|---|
| 职责 | 语言智能 | 采集链接资料 + 部署 |
| 读取 | `<项目>/.project/links.jsonl` | 数据库 / CSV / 命令 |
| 写入 | — | `<项目>/.project/links.jsonl`（旧 `.link/` 只读兼容） |
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
