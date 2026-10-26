# 功能清单（tiptop 切片）

> 本文件是**全量总清单** `vscode-bdl/docs/FEATURES.md` 的 **tiptop 部分**，
> 面向开发者，按功能类型列出 tiptop 已实现的能力，可快速浏览 / 当验收表用。
>
> | 项目 | 值 |
> |---|---|
> | 版本 | `tiptop v0.1.8`（配套 `bdl-lsp v0.3.13` / `vscode-bdl v0.6.2`） |
> | 生成日期 | 2026-10-24 |
> | 完整版 | <https://github.com/darcyjoven/vscode-bdl/blob/main/docs/FEATURES.md> |
> | bdl-lsp 切片 | <https://github.com/darcyjoven/bdl-lsp/blob/main/docs/FEATURES.md> |
> | 用法细节 | [`../README.md`](../README.md) |

> **维护规则**：待办（`vscode-bdl/docs/TODO.md`）完成后，把新功能补进本文对应条目；
> 用户可见的新功能还要同步更新 `vscode-bdl/README.md`（唯一用户引导）与对应组件的功能文档。


## 0. 职责与边界

tiptop 是 TiTop 项目的**链接资料采集 / 表字段资料采集 / 部署**工具（从 bdl-lsp 分离而来）。

| | bdl-lsp | tiptop |
|---|---|---|
| 职责 | 语言智能 | 采集链接资料 + 表字段资料 + 部署 |
| 读取 | `<项目>/.project/link/links.jsonl`、`.project/schema/` | 数据库 / CSV / 命令 |
| 写入 | — | `<项目>/.project/link/links.jsonl`、`.project/schema/`（旧 `.link/` 只读兼容） |
| 部署 | — | rsync/scp 上传 + 远程编译 |

两者**只通过文件系统交互**。换一套链接结构（例如 tiptop100）时照本仓库再做采集器，bdl-lsp 不用改。

复用 bdl-lsp 公开包：`link`（资料格式/模组布局/全语料扫描）、`parser` / `index`（tree-sitter 解析与符号索引）、
`builtin`（内置符号表）、`glob`（文件匹配）。`go.mod` 里 `replace github.com/darcyjoven/bdl-lsp => ../bdl-lsp`，
发布时替换为版本号。

---

## 1. 链接资料（`tiptop link`）

来源 `DS.GAL_FILE`：CSV 表头 `GAL01,GAL02,GAL03,GAL04`；`gal04='Y'` 表示确实链接，`'N'` 过滤。

| # | 功能 | 说明 / 入口 |
|---|---|---|
| F1 | CSV 导入 | `tiptop link import -csv <导出.csv> -root <项目根>`；容忍 UTF-8 BOM，组内去重 |
| F2 | 采集来源 | `link sync -source golang`（纯 Go 直连 Oracle，`go-ora`，**无需 Oracle 客户端**）/ `command`（命令 stdout 出 CSV）/ `file`（读已导出 CSV） |
| F3 | 自定义 SQL | `-query`（默认查 `DS.GAL_FILE` 四列） |
| F4 | 状态查看 | `tiptop link status`：链 / orphan / ambiguous / 文件数、生成时间与天数、缺失文件数、模组根、全局模组；显示局部更新 `partial` 记录 |
| F5 | 局部更新 | `-files aap/4gl/saapt310.4gl`（找出引用该文件的作业，一个公共文件常被多个 MAIN 引用）或 `-modules lib,sub,qry`；写盘**合并**：受影响的程序用新链替换，其余链原样保留 |
| F6 | 局部更新预演（命令行） | `-dry-run` 打印「受影响作业 N 个」，先确认再执行；**前提**：资料目录里已有一次全量导入基线，没有基线时明确报错而不是写半份资料。**W2：编辑器插件已不再自动预演**（dry-run 会让全量扫描跑两遍） |
| F7 | SQL 下推 | `source=golang` + 默认 SQL 时，模组/文件条件下推到 Oracle（少取行）；自定义 `-query` 时只在内存过滤 |
| F8 | MAIN 判定 | 语法树判定 MAIN，文本兜底兼容 `{ MAIN }` 写法 |
| F9 | 外部函数过滤 | 被 ≥3 文件或 ≥5 次调用、全语料无定义的名字 → 视为存在并记进 `links.meta.json`；候选 >2000 个则不启用 |
| F10 | 原子写入 | 先写临时文件再 rename，LSP 不会读到半份资料 |
| F11 | 进度协议 | stderr 一行一条、制表符分隔：`进度\t阶段\t信息`；阶段 `采集 / 扫描源码 / 成链 / 写入完成`（编辑器插件按它显示进度条） |
| F12 | 密码来源 | 环境变量 `BDL_ORA_PASSWORD` 或 `<项目>/.project/secrets.json`（`chmod 600`）；**不写进配置文件**。link 与 schema 统一读 `.project/secrets.json`（此前 link 因资料目录变更错读 `.project/link/secrets.json`） |
| F13 | 模组根 | `-roots` 逗号分隔；留空自动探测 |

产物：

```text
<项目根>/.project/link/
├── links.jsonl      # 每行一条链：程序 → 链内文件/函数
└── links.meta.json  # 生成时间、partial（时间/条件/作业数/重算链数）、外部函数表
```

旧布局（`.project` 根 / `.link/`）只读兼容。

---

## 2. 表字段资料（`tiptop schema`）

来源两个，产物完全一样（都写 `<项目根>/.project/schema/`）：直连 Oracle，或 DBA 导出的表字段 CSV。

| # | 功能 | 说明 / 入口 |
|---|---|---|
| G1 | CSV 导入 | `tiptop schema import -csv <表字段导出.csv> -root <项目根>` |
| G2 | 直连采集（TiTop 字典 + 分批） | `tiptop schema sync -source golang`：默认 SQL 查 **`DS.ZTA_FILE`（`zta02='ds'`）+ `USER_TAB_COLUMNS`**，列说明取 **`DS.GAQ_FILE.gaq03`（`gaq02='2'`）**——不是 Oracle 备注；**先取表清单，再每 `-batch-size`（默认 200）张表查一批**，每批 `-timeout`（默认 120s）。账号须为应用 schema 账号且能读两张字典表；字典表缺失（ORA-00942）提示改用 CSV 导入；`-owner` 仅自定义 `-query` 用。避免单条大查询在慢链路上被总超时取消（ORA-01013） |
| G3 | 状态查看 | `tiptop schema status`：表数 / 列数 / 导入时间 / 来源；没有资料时给下一步命令提示 |
| G4 | 选择与增量 | `-tables a,b`（大小写不敏感）/ `-tables-file 文件`（大清单，插件走这个避免命令行过长）/ `-only-new`（只补本地不存在的表，已有表文件一个字节不动）/ `-prune`（删除本地多出、只与全量一起用）/ `-dry-run`（命令行能力；**W2：插件不再自动预演**） |
| G5 | 逐表增量写 | 全量查询 → 逐表逐字节比较，只有变化的表才重写（避免无意义 mtime 变化让编辑器重载）；CSV 里没有的表默认保留不删 |
| G6 | 容错解析 | 表头先归一化（小写、去非字母数字）再按别名匹配，认不出回退默认列位置；实测容忍导出里的 `data_precicison`、`default_legth` 拼写错误；UTF-8 BOM / CRLF / 引号包裹逗号；`nullable` 支持 `Y/N`、`YES/NO`（大小写不敏感）；数字列空或非数字按 0；`data_length=0` 回退 `char_length`；空行、列数不足或缺表名/列名 → 跳过并计数，不中断整份导入 |
| G7 | 安全写入 | 已有 `tables.json` 是坏 JSON（或 schema 版本过高）→ 报错退出并**保留原文件**；写盘一律临时文件 + rename；**每次导入前清理遗留的 `tables/.tmp-*`**（被取消/被杀留下），删除失败把原因并进错误（不再静默） |
| G8 | 进度协议 | stderr 制表符分隔；阶段 `采集 / 解析 / 清理 / 写入完成`；远端大查询每 5000 行报一次；解析带 `第 N 个/共 M 个` |
| G9 | 密码来源 | 同 F12（环境变量或 `.project/secrets.json`） |

产物：

```text
<项目根>/.project/schema/
├── tables.json             # 索引：importedAt / source / rowsTotal / 每张表的列数与文件名
└── tables/<大写表名>.json   # 列清单：name/desc/type/length/charLength/precision/scale/nullable/default
```

实测规模：2648 表 / 58096 列 / 2.4 s。

---

## 3. 部署（`tiptop deploy`）

部署 = 上传源码到远程服务器 + 远程编译。

| # | 功能 | 说明 / 入口 |
|---|---|---|
| H1 | 部署命令 | `tiptop deploy [-target <名>] [-config <文件>] [-workspace <项目根>] [文件...]`；只部署指定文件时把文件名放最后也可以 |
| H2 | 上传 | `rsync` 为主（`upload.mode`），失败退 `scp`（`upload.fallback`）；`include`/`exclude` glob 控制范围，`delete` 控制是否删除远端多余文件 |
| H3 | 远程编译 | 按文件类型映射到编译命令：`compile.4gl`（如 `fglcomp -M {file}`）、`compile.per`（如 `fglform {file}`）；`{file}` 替换为远端路径 |
| H4 | 模式开关 | `-dry-run`（只打印计划，不连接/不上传）、`-no-compile`（只上传）、`-no-upload`（只编译）、`-json`（结构化结果，给插件用）、`-list`（列出配置里的目标） |
| H5 | 配置模型 | `.project/deploy/deploy.json`（旧 `.bdl-lsp/deploy.json` 兼容），字段见 §5.1；`-target` 覆盖 `defaultTarget` |
| H6 | 认证 | SSH key / ssh-agent；**不支持在配置里存密码** |
| H7 | 编译结果 | 解析远端编译输出（文件/行号/消息），`-json` 输出映射结果供编辑器转成诊断 |
| H8 | 单文件上传（`tiptop upload`，§0d） | `-file <相对路径>` 把 `<工作区根>/<相对路径>` 传到 `<生产根>/<相对路径>`（rsync 主、scp 备；scp 前先 `mkdir -p`）；`-dry-run` 只打印计划。**不做批量同步**、不做 include/exclude/delete |
| H9 | 生产端执行（`tiptop exec`，§0d） | `-command <命令>` 通过 ssh 在**生产根目录**执行（模板里的 `cd ${path}` 由此生效），stdout/stderr 流式回传，退出码 = 远程退出码；`-file` 只用于打印上下文；`-dry-run`/`-json`。编辑器插件的编译/链接/预览走这条 |
| H10 | 路径安全 | `-file` 必须是相对路径：拒绝绝对路径、盘符、`..` 越界；`-command` 里的生产根做 shell 转义 |

---

## 4. CLI 参考

| 子命令 | 关键参数 |
|---|---|
| `tiptop link import` | `-csv <文件>`（必填）`-root` `-out` `-roots` `-modules` `-files` `-dry-run` `-replace` |
| `tiptop link sync` | `-root` `-out` `-roots` `-source golang\|command\|file` `-command` `-file` `-host` `-port` `-service` `-user` `-query` `-modules` `-files` `-dry-run` |
| `tiptop link status` | `-root` `-out` `-global`（全局可见模组，默认 `sub,lib,qry`） |
| `tiptop schema import` | `-csv <文件>`（必填）`-root` `-out` `-tables` `-tables-file` `-only-new` `-dry-run` |
| `tiptop schema sync` | `-root` `-out` `-source golang\|file` `-file` `-host` `-port` `-service` `-user` `-query` `-tables` `-tables-file` `-only-new` `-prune` `-dry-run` `-batch-size` `-timeout`（`-owner` 仅自定义 SQL 用） |
| `tiptop schema status` | `-root` `-out` |
| `tiptop deploy` | `-target` `-config` `-workspace` `-dry-run` `-no-compile` `-no-upload` `-json` `-list` `-version` `[文件...]` |
| `tiptop upload` | `-file <相对路径>`（必填）`-workspace` `-config` `-target` `-dry-run` `-json` `-version` |
| `tiptop exec` | `-command <命令>`（必填）`-file` `-workspace` `-config` `-target` `-dry-run` `-json` `-version` |
| `tiptop version` | 打印版本 |

进度输出格式（stderr，一行一条、制表符分隔）：

```text
进度	采集	连接 dbhost:1521/PROD（owner=TOPPORD）…
进度	采集	已收到 12000 行          # 远端大查询期间每 5000 行一次
进度	采集完成	58096 行
进度	解析	ima_file	12	2648       # 阶段/表名/第几个/共几个
进度	清理	old_file	3	5
进度	写入完成	2648 个表文件
```

`link import` / `link sync` 用同一套协议（阶段：采集 / 扫描源码 / 成链 / 写入完成）。

---

## 5. 配置与数据格式

### 5.1 `.project/deploy/deploy.json`

> VSCode 插件会从设置**合成**此文件；手写也行，tiptop 的配置模型不变。

```jsonc
{
  "targets": {
    "prod": {
      "host": "<生产主机>",
      "port": 22,
      "user": "<账号>",
      "remoteRoot": "/u1/toppord",
      "localRoot": ".",
      "upload": {
        "include": ["**/*.4gl", "**/*.global", "**/*.per"],
        "exclude": ["**/.git/**", "**/.project/**"],
        "mode": "rsync",
        "rsyncArgs": ["-az"],
        "fallback": "scp",
        "delete": false
      },
      "compile": { "4gl": "fglcomp -M {file}", "per": "fglform {file}" },
      "strictHostKey": true,
      "connectTimeout": 20
    }
  },
  "defaultTarget": "prod"
}
```

支持字段：`targets.<名>.{host,port,user,remoteRoot,localRoot,upload{include,exclude,mode,rsyncArgs,fallback,delete},compile{...},strictHostKey,connectTimeout}`、
`defaultTarget`；插件生成的文件带 `createdBy` / `generatedAt` 标记。认证用 SSH key / ssh-agent。

### 5.2 资料目录

```text
<项目根>/.project/
├── secrets.json           # 密码（0600，可选）；根只留这一个文件
├── link/
│   ├── links.jsonl
│   ├── links.meta.json
│   └── gal_file.csv       # CSV 导入时复制的源文件（可选）
├── schema/                # 注意没有点号（旧布局是 .project/.schema/）
│   ├── tables.json
│   ├── tables/<大写表名>.json
│   └── gal_schema.csv     # 表字段 CSV 落点
└── deploy/
    └── deploy.json        # 部署配置（插件合成；旧 .project/deploy.json、.bdl-lsp/deploy.json 只读兼容）
```

---

## 6. 编辑器集成（消费方在 vscode-bdl）

tiptop 被 VSCode 插件（`vscode-bdl`）调用，插件负责命令、视图、进度与结果展示：

| 能力 | 说明 |
|---|---|
| 链接资料命令 | `从 CSV 导入 / 按模组更新 / 全量更新 / 查看状态 / 刷新` + `从数据库更新`（唯一的「更新」入口，只重算当前文件涉及的作业） |
| 数据库字段命令 | `从 CSV 导入 / 从数据库同步 / 同步已选中的表 / 全选当前列表 / 清除勾选 / 刷新 / 正则过滤`；单表与复制列定义走树节点右键 |
| 上传命令 | `上传当前文件` / `预演上传`（**只上传单文件**，不做批量同步）→ `tiptop upload` |
| 编译/链接/预览 | 六条命令（4GL 预演编译/编译/链接，Form 预演编译/编译/预览画面）→ `tiptop exec` **在生产服务器上执行**（工作目录 = 生产根）；插件**不自动上传** |
| 进度与取消 | 插件解析 tiptop 的 stderr 进度协议，弹进度通知并允许取消 |
| 配置合成 | 插件从上传/生产环境设置合成 `.project/deploy/deploy.json`（覆盖手写文件前备份 `.bak`） |
| 输出与日志 | 输出进「Genero BDL 任务」通道；每次运行落盘 `last-run-<cmd>.log`，失败通知可点「打开日志」 |
| 开关 | `bdl.link.mode` / `bdl.schema.mode`（`off|file|db`）与 `bdl.deploy.enable` + `bdl.prod.deploy.transport`、`bdl.build.enable`；关闭时命令隐藏且直接调用只提示不执行 |

---

## 7. 构建与测试

| # | 工具 | 说明 |
|---|---|---|
| L1 | `make build` / `build-ps` / `test` / `test-race` / `vet` / `fmt` / `lint` / `clean` | Linux/macOS 用 Makefile；Windows 无 make 走 `scripts/build.ps1` |
| L2 | 依赖布局 | `tiptop` 与 `bdl-lsp` 必须同级目录（`replace ../bdl-lsp`） |
| L3 | 受限环境 | 先 `source ../bdl-lsp/scripts/dev-env.sh` 再 `make build` |
| L4 | 测试覆盖 | `link` 导入/局部更新/过滤、`schema` 解析/导入/同步、`deploy` 配置与部署均有单元测试 |

---

## 8. 明确不做

| # | 不做 | 原因 |
|---|---|---|
| N1 | 语言智能（解析/诊断/跳转） | 归 bdl-lsp |
| N2 | 在配置里存密码 | 已选环境变量 / `secrets.json`（0600） |
| N3 | 纯 Go SFTP | 已选 rsync/scp |
| N4 | 后台自动定时同步 | 已彻底移除（`bdl.link.sync.*` 与 `Scheduler` 死代码都删了）；同步只由命令触发 |
| N5 | 链接资料过期告警 | 设计如此（`maxAgeDays` 已删除） |
| N6 | 计划/测试脚本/中间产物放本仓库 | 统一放 bdl-lsp 的 `docs/` 与 `artifacts/` |

---

## 9. 相关文档

| 文档 | 内容 |
|---|---|
| [`../README.md`](../README.md) | 仓库入口：构建与快速用法 |
| [`../examples/deploy.json`](../examples/deploy.json) | 部署配置示例 |
| [完整功能总清单](https://github.com/darcyjoven/vscode-bdl/blob/main/docs/FEATURES.md) | 三个组件按类型整合 |
| [bdl-lsp 功能切片](https://github.com/darcyjoven/bdl-lsp/blob/main/docs/FEATURES.md) | 语言服务能力 |
| [生产项目工作区搭建](https://github.com/darcyjoven/vscode-bdl/blob/main/README.md#附录-a从生产端搭建工作区) | 拉源码建工作区 → 资料 → 部署（用户引导附录 A） |