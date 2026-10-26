# tiptop

TiTop 项目的**链接资料采集**、**表字段资料采集**与**部署**工具（从
[bdl-lsp](https://github.com/darcyjoven/bdl-lsp) 分离而来）。

| | bdl-lsp | tiptop |
|---|---|---|
| 职责 | 语言智能（解析/诊断/跳转/补全） | 采集链接资料 + 表字段资料 + 部署 |
| 读取 | `<项目>/.project/link/links.jsonl`、`.project/schema/` | 数据库 / CSV / 命令 |
| 写入 | — | `<项目>/.project/{link,schema,deploy}/`（旧布局只读兼容，W6） |
| 部署 | — | rsync/scp 上传 + 远程编译 |

两者**只通过文件系统交互**。`go.mod` 里 `replace github.com/darcyjoven/bdl-lsp => ../bdl-lsp`，
所以**两个仓库要放在同级目录**；发布时把 `replace` 换成版本号。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/FEATURES.md](docs/FEATURES.md) | **本仓库唯一功能文档**（面向开发者，详细到每个功能） |
| [examples/deploy.json](examples/deploy.json) | 部署配置示例 |
| [vscode-bdl/README.md](https://github.com/darcyjoven/vscode-bdl/blob/main/README.md) | **唯一用户引导**（含链接资料/字段资料/部署的完整操作） |
| [vscode-bdl/docs/FEATURES.md](https://github.com/darcyjoven/vscode-bdl/blob/main/docs/FEATURES.md) | 三组件整合功能文档 |
| [vscode-bdl/docs/TODO.md](https://github.com/darcyjoven/vscode-bdl/blob/main/docs/TODO.md) | 待办清单 |
| [vscode-bdl/docs/OPEN-ITEMS.md](https://github.com/darcyjoven/vscode-bdl/blob/main/docs/OPEN-ITEMS.md) | 待确认记录 |
| [bdl-lsp/docs/FEATURES.md](https://github.com/darcyjoven/bdl-lsp/blob/main/docs/FEATURES.md) | bdl-lsp 功能文档 |

## 构建

```bash
# Linux / macOS
make build            # 产物：artifacts/bin/tiptop

# Windows（无 make）
powershell -File scripts\build.ps1
```

受限环境（无 root、$HOME 只读）先加载 bdl-lsp 的工具链：

```bash
source ../bdl-lsp/scripts/dev-env.sh
make build
```

## 快速用法

```bash
# 链接资料：直连 Oracle 采集（纯 Go 驱动，无需 Oracle 客户端）
export BDL_ORA_PASSWORD='<密码>'          # 或 <项目>/.project/secrets.json（0600）
tiptop link sync -root <项目根> -source golang \
  -host <Oracle主机> -service <服务名> -user <只读账号>

# 或先在生产端导出 CSV 再导入
tiptop link import -csv .project/link/gal_file.csv -root <项目根>
tiptop link status -root <项目根>

# 局部更新：只重算受影响的程序（先 -dry-run 看影响几个作业）
tiptop link sync -files aap/4gl/saapt310.4gl
tiptop link sync -modules lib,sub,qry

# 表字段资料
tiptop schema import -csv gal_schema.csv -root <项目根>
tiptop schema sync -root <项目根> -host dbhost -service PROD -user readonly
tiptop schema sync -root <项目根> -batch-size 200 -timeout 120   # 分批查询（默认 200 张表/批）
tiptop schema status -root <项目根>

# 部署（上传 + 远程编译）
tiptop deploy -workspace <项目根> -list
tiptop deploy -workspace <项目根> -target prod -dry-run
tiptop deploy -workspace <项目根> -target prod aap/4gl/aapi202.4gl
```

密码**不写进配置文件**：用环境变量 `BDL_ORA_PASSWORD` 或 `<项目>/.project/secrets.json`（`chmod 600`）——
link 与 schema **共用同一个** `.project/secrets.json`（不要放进 `.project/link/`）；

`schema sync -source golang` 是**分批查询**的：先取 owner 下的表清单，再每 200 张表查一批（`-batch-size`，0=不分批），
每批独立超时（`-timeout`，默认 120 秒），跑完一批继续下一批。慢链路上不会再出现"单条大查询被取消（ORA-01013）"；
SSH 认证用 key / ssh-agent。完整参数与产物格式见 [docs/FEATURES.md](docs/FEATURES.md)。