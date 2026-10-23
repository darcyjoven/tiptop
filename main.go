// Command tiptop 是 TiTop 项目的链接资料与部署工具。
//
// 子命令：
//
//	tiptop link import -csv <导出.csv>   导入 DS.GAL_FILE 导出，生成 .project/links.jsonl
//	tiptop link sync                     从数据库/命令/文件采集并导入
//	tiptop link status                   查看资料状态
//	tiptop deploy [文件...]              上传到远程服务器并远程编译
//	tiptop version                       打印版本
//
// 与 bdl-lsp 的分工：
//
//	bdl-lsp  只做语言服务（解析、诊断、跳转、补全）；读取 <项目>/.project/ 下的资料，
//	         但不负责生成它。语言服务不关心链接资料是哪个系统、怎么采来的。
//	tiptop   负责把某个具体系统的链接结构变成 .project/ 资料，并负责部署。
//
// 两者之间**只通过文件系统交互**：tiptop 写 <项目>/.project/links.jsonl，
// bdl-lsp 读它。这样换一个链接结构的系统（如 tiptop100）时，只需要照着
// 本仓库再做一个采集器，bdl-lsp 一行都不用改。
package main

import (
	"fmt"
	"os"

	"github.com/darcyjoven/tiptop/internal/deploycli"
	"github.com/darcyjoven/tiptop/internal/linkcli"
)

// version 由 -ldflags "-X main.version=..." 注入。
var version = "0.1.3"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	sub := args[0]
	args = args[1:]

	switch sub {
	case "version", "--version", "-v":
		fmt.Printf("tiptop %s\n", version)
		return 0
	case "link":
		return linkcli.Run(args, version)
	case "deploy":
		return deploycli.Run(args, version)
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "tiptop: 未知子命令 %q\n", sub)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `用法: tiptop <子命令> [参数...]

子命令:
  link import -csv <file>   导入 DS.GAL_FILE 导出（生成 .project/links.jsonl）
  link sync                 采集并导入（-source golang|command|file）
  link status               查看链接资料状态
  deploy [文件...]          上传并远程编译（-target / -dry-run / -list）
  version                   打印版本

说明: tiptop 生成 <项目>/.project/ 下的资料交给 bdl-lsp 使用（旧目录 .link/ 只读兼容）；
      bdl-lsp 自身不采集链接资料。详见 docs/。
`)
}
