# tiptop

TiTop 项目的**链接资料采集**、**表字段资料采集**与**部署**工具（从
[bdl-lsp](https://github.com/darcyjoven/bdl-lsp) 分离而来）。

| | bdl-lsp | tiptop |
|---|---|---|
| 职责 | 语言智能（解析/诊断/跳转/补全） | 采集链接资料 + 表字段资料 + 部署 |
| 读取 | `<项目>/.project/link/links.jsonl`、`.project/schema/` | 数据库 / CSV / 命令 |
| 写入 | — | `<项目>/.project/{link,schema,deploy}/`（旧布局只读兼容） |
| 部署 | — | rsync/scp 上传、下载 + 远程编译 |

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

## 从 GitHub Release 安装

不用装 Go：每个 tag 的 Release 都有四平台预编译二进制，资产命名与
[bdl-lsp](https://github.com/darcyjoven/bdl-lsp) 的 Release 一致
（`tiptop-<os>-<arch>.<ext>`），方便编辑器扩展（Zed / VS Code）按同一套规则解析：

| 平台 | 资产 | 内容 |
|---|---|---|
| Linux x86_64 | `tiptop-linux-amd64.tar.gz` | `tiptop` + `LICENSE` |
| macOS Intel | `tiptop-darwin-amd64.tar.gz` | `tiptop` + `LICENSE` |
| macOS Apple Silicon | `tiptop-darwin-arm64.tar.gz` | `tiptop` + `LICENSE` |
| Windows x86_64 | `tiptop-windows-amd64.zip` | `tiptop.exe` + `LICENSE` |

每个 Release 另附 `SHA256SUMS`，是上面四个产物的校验和（格式与 `sha256sum` 相同，
每行 `<sha256>  <文件名>`）。下面的 `v0.1.10` 换成要装的 tag；预发布 tag 带后缀
（如 `v0.1.10-rc1`），不会出现在 `/releases/latest`，必须写全名。

### Linux（x86_64）

```bash
VER=v0.1.10
curl -fsSLO "https://github.com/darcyjoven/tiptop/releases/download/$VER/tiptop-linux-amd64.tar.gz"
curl -fsSLO "https://github.com/darcyjoven/tiptop/releases/download/$VER/SHA256SUMS"
# 只校验自己下的那个文件：SHA256SUMS 含四平台条目，其余文件没下会报缺失
grep 'tiptop-linux-amd64.tar.gz' SHA256SUMS | sha256sum -c -
tar -xzf tiptop-linux-amd64.tar.gz
sudo install -m 0755 tiptop /usr/local/bin/tiptop   # 无 root 时改成 ~/.local/bin/
tiptop version                                       # 输出 tiptop 0.1.10
```

### macOS（Intel / Apple Silicon）

```bash
VER=v0.1.10
ARCH=arm64          # Intel Mac 改成 amd64
curl -fsSLO "https://github.com/darcyjoven/tiptop/releases/download/$VER/tiptop-darwin-$ARCH.tar.gz"
curl -fsSLO "https://github.com/darcyjoven/tiptop/releases/download/$VER/SHA256SUMS"
grep "tiptop-darwin-$ARCH.tar.gz" SHA256SUMS | shasum -a 256 -c -
tar -xzf "tiptop-darwin-$ARCH.tar.gz"
sudo install -m 0755 tiptop /usr/local/bin/tiptop
tiptop version
```

二进制未签名；用 curl 下载不会有 quarantine 属性，正常可直接运行。若被 Gatekeeper
拦截（例如经浏览器下载中转），执行 `xattr -d com.apple.quarantine ./tiptop`。

### Windows（x86_64 / PowerShell）

```powershell
$VER  = "v0.1.10"
$base = "https://github.com/darcyjoven/tiptop/releases/download/$VER"
Invoke-WebRequest "$base/tiptop-windows-amd64.zip" -OutFile tiptop-windows-amd64.zip
Invoke-WebRequest "$base/SHA256SUMS"               -OutFile SHA256SUMS
$expected = (Select-String -Path SHA256SUMS -Pattern 'tiptop-windows-amd64.zip').Line.Split()[0]
$actual   = (Get-FileHash tiptop-windows-amd64.zip -Algorithm SHA256).Hash.ToLower()
if ($actual -ne $expected) { throw "SHA256 校验失败：$actual != $expected" }
Expand-Archive tiptop-windows-amd64.zip -DestinationPath .
.\tiptop.exe version                                # 输出 tiptop 0.1.10
```

Windows 产物用 MinGW **静态链接**了 CGO 运行库（`libwinpthread` / `libgcc`），
解压即用，不需要安装 MinGW 或任何 VC++ 运行库；系统 DLL（KERNEL32 等）仍动态链接，
这是正常现象。把解压目录（或 `tiptop.exe` 所在目录）加进 `PATH` 即可全局调用。

### 说明

- 只提供上表四个平台；其他平台（如 linux/arm64）请按下一节从源码构建。
- `SHA256SUMS` 里如果混入预发布产物，也是同一个文件的一部分；校验时按文件名 `grep` 单条即可。
- tiptop 访问 Oracle 走纯 Go 驱动（`go-ora`），不需要安装 Oracle 客户端；
  数据库密码走 `BDL_ORA_PASSWORD` 或 `<项目>/.project/secrets.json`（`chmod 600`）。

> **发布维护者注意**：tiptop 通过 `go.mod` 的 `replace ... => ../bdl-lsp` 依赖 bdl-lsp，
> 所以 `release` workflow 会把 bdl-lsp 检出成兄弟目录。ref 的选取顺序是：
> **① workflow_dispatch 的 `bdl_lsp_ref` → ② bdl-lsp 最近一个正式 Release 的 tag → ③ `main`**。
> 也就是说 tag 推送时会自动对齐到 bdl-lsp 的**已发布版本**（不会追 main 漂移）；
> **正式发布建议走 Actions → `release` → Run workflow，在 "Use workflow from" 里选中目标 tag，
> 并把 `bdl_lsp_ref` 显式填成 bdl-lsp 的 tag（如 `v0.3.14`）**。
> 构建日志与 job summary 会打印本次实际使用的 ref。

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
tiptop schema sync -root <项目根> -host dbhost -service PROD -user <应用schema账号>
tiptop schema sync -root <项目根> -batch-size 200 -timeout 120   # 分批查询（默认 200 张表/批）
tiptop schema status -root <项目根>

# 部署（整包/多文件上传 + 远程编译）
tiptop deploy -workspace <项目根> -list
tiptop deploy -workspace <项目根> -target prod -dry-run
tiptop deploy -workspace <项目根> -target prod aap/4gl/aapi202.4gl

# 单文件上传/下载与生产端执行（编辑器插件走这条路）
tiptop upload   -workspace <项目根> -file tiptop/apm/4gl/axcp500.4gl -dry-run
tiptop download -workspace <项目根> -file tiptop/apm/4gl/axcp500.4gl -dry-run
tiptop exec     -workspace <项目根> -file tiptop/apm/4gl/axcp500.4gl \
                -command 'cd apm/4gl && r.c2 axcp500'
```

**单文件 vs 整包**：`upload`/`download`/`exec` 是"只动当前打开的一个文件"——`upload` 把
`<工作区根>/<相对路径>` 传到 `<生产根>/<相对路径>`，`download` 反向把生产端同名文件
**覆盖**到本地（都是 rsync 主、scp 备，scp 前先建目录）；`exec` 通过 ssh 在**生产根目录**
下执行命令（模板里的 `cd ${path}` 因此生效），stdout/stderr 原样回传、退出码即远程退出码。
编辑器插件只用这三条，不做批量同步。下载是覆盖写，`tiptop` 自身不备份也不询问——
"本地有未提交差异要不要继续"由插件在调用前确认。
配置文件与 `tiptop deploy` 相同（`.project/deploy/deploy.json`，插件自动生成）。

密码**不写进配置文件**：用环境变量 `BDL_ORA_PASSWORD` 或 `<项目>/.project/secrets.json`（`chmod 600`）——
link 与 schema **共用同一个** `.project/secrets.json`（不要放进 `.project/link/`）；

`schema sync -source golang` 默认查 **TiTop 字典**：表/列来自 `DS.ZTA_FILE`（`zta02='ds'`）+ `USER_TAB_COLUMNS`，
列说明取 `DS.GAQ_FILE.gaq03`（`gaq02='2'`）——**不是 Oracle 备注**（用户环境里备注基本不维护）。
所以连接账号要用**应用 schema 账号**（自己拥有这些表，且对两张字典表有读权限）；字典表不存在时会提示改用 CSV 导入。

查询是**分批**的：先取字典里的表清单，再每 200 张表查一批（`-batch-size`，0=不分批），
每批独立超时（`-timeout`，默认 120 秒），跑完一批继续下一批。慢链路上不会再出现"单条大查询被取消（ORA-01013）"；
SSH 认证用 key / ssh-agent。完整参数与产物格式见 [docs/FEATURES.md](docs/FEATURES.md)。