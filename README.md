# tiptop

TiTop 项目的**链接资料采集**与**部署**工具。从
[`bdl-lsp`](https://github.com/darcyjoven/bdl-lsp) 分离而来。

## 为什么分离

`bdl-lsp` 只做语言服务：解析、诊断、跳转、补全。它需要知道"某个函数在
哪个文件里定义"，但不该关心这个信息**是哪个系统、怎么采集来的**。

| | bdl-lsp | tiptop |
|---|---|---|
| 职责 | 语言智能 | 采集链接资料 + 部署 |
| 读取 | `<项目>/.link/links.jsonl` | 数据库 / CSV / 命令 |
| 写入 | — | `<项目>/.link/links.jsonl` |
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
# 1) 采集 DS.GAL_FILE 并生成 <项目>/.link/links.jsonl
tiptop link sync -root <项目根> -source golang \
  -host <Oracle主机> -service <服务名> -user <只读账号>

# 或者先在生产端用 sqlplus 导出 CSV，再导入
tiptop link import -csv .link/gal_file.csv -root <项目根>

# 2) 查看资料状态
tiptop link status -root <项目根>

# 3) 部署（上传 + 远程编译）
tiptop deploy -workspace <项目根> -list
tiptop deploy -workspace <项目根> -target prod -dry-run
tiptop deploy -workspace <项目根> -target prod tiptop/aap/4gl/aapi202.4gl
```

密码不要写进配置文件：

```bash
export BDL_ORA_PASSWORD='<密码>'     # 或 .link/secrets.json（0600）
```

## 采集来源

| `-source` | 说明 |
|---|---|
| `golang` | 纯 Go 驱动直连 Oracle 查 `DS.GAL_FILE`（默认） |
| `command` | 执行外部命令，CSV 打到 stdout |
| `file` | 读已导出的 CSV（默认 `<dataDir>/gal_file.csv`） |

CSV 需要表头 `GAL01,GAL02,GAL03,GAL04`；`gal04='Y'` 表示"确实链接"，
`'N'` 的行会被过滤。

## 部署配置

`.bdl-lsp/deploy.json`（与 bdl-lsp 共用同一份配置）：

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
