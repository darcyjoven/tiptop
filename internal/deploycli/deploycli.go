// Package deploycli 提供 `tiptop deploy` 子命令。
//
// 与 internal/deploy 分开：后者是纯逻辑（可单测），这里只做参数解析与输出。
package deploycli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/darcyjoven/tiptop/internal/deploy"
)

// Run 执行 deploy 子命令，返回进程退出码。
//
// 退出码：0 = 成功；1 = 上传/编译/诊断有错误；2 = 参数或配置错误。
func Run(args []string, version string) int {
	fs := flag.NewFlagSet("tiptop deploy", flag.ContinueOnError)
	target := fs.String("target", "", "部署目标名（默认取配置的 defaultTarget）")
	configPath := fs.String("config", "", "配置文件路径（默认 <工作区>/.bdl-lsp/deploy.json）")
	workspace := fs.String("workspace", "", "工作区根目录（默认取当前目录）")
	dryRun := fs.Bool("dry-run", false, "只打印计划，不上传不编译")
	skipCompile := fs.Bool("no-compile", false, "只上传，不远程编译")
	skipUpload := fs.Bool("no-upload", false, "只编译，不上传")
	asJSON := fs.Bool("json", false, "以 JSON 输出结果")
	showVersion := fs.Bool("version", false, "打印版本")
	listTargets := fs.Bool("list", false, "列出配置里的目标")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "用法: tiptop deploy [flags] [文件...]\n\n")
		fmt.Fprintf(fs.Output(), "上传 Genero 源码到远程服务器并远程编译。\n\n")
		fmt.Fprintf(fs.Output(), "示例:\n")
		fmt.Fprintf(fs.Output(), "  tiptop deploy -dry-run                 先看要传什么、编译什么\n")
		fmt.Fprintf(fs.Output(), "  tiptop deploy -target test             部署到 test 目标\n")
		fmt.Fprintf(fs.Output(), "  tiptop deploy -target test a.4gl b.per 只部署指定文件\n\n")
		fmt.Fprintf(fs.Output(), "参数:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Printf("tiptop %s\n", version)
		return 0
	}

	ws := *workspace
	if ws == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "tiptop deploy: 无法获取当前目录: %v\n", err)
			return 2
		}
		ws = wd
	}

	if *listTargets {
		cfg, err := deploy.LoadConfig(ws, *configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tiptop deploy: %v\n", err)
			return 2
		}
		fmt.Printf("配置文件 %s\n", cfg.Path)
		for _, name := range cfg.TargetNames() {
			mark := "  "
			if name == cfg.DefaultTarget {
				mark = "* "
			}
			t := cfg.Targets[name]
			fmt.Printf("%s%-12s %s@%s:%d → %s\n",
				mark, name, t.User, t.Host, t.Port, t.RemoteRoot)
		}
		return 0
	}

	// flag.Parse 会在第一个非 flag 参数处停止，所以 `deploy a.4gl -dry-run`
	// 里的 -dry-run 会被当成文件。为了支持这种写法，先重排成"flag 在前、
	// 文件在后"再解析；重排同时给出文件列表（不能再用 fs.Args()，因为
	// 重排后的参数里已经没有文件了）。
	var files []string
	if hasFlagsAfterOperand(args) {
		flags, operands := reorderArgs(args)
		if err := fs.Parse(flags); err != nil {
			return 2
		}
		files = operands
	} else {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		files = fs.Args()
	}
	// 重排后再检查一次 version/list：它们可能出现在文件之后。
	if *showVersion {
		fmt.Printf("tiptop %s\n", version)
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var logs []string
	res, err := deploy.Run(ctx, deploy.Options{
		Workspace:   ws,
		ConfigPath:  *configPath,
		Target:      *target,
		DryRun:      *dryRun,
		SkipCompile: *skipCompile,
		SkipUpload:  *skipUpload,
		Files:       files,
		Log: func(format string, a ...any) {
			line := fmt.Sprintf(format, a...)
			if *asJSON {
				logs = append(logs, line)
				return
			}
			fmt.Println(line)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tiptop deploy: %v\n", err)
		return 2
	}

	if *asJSON {
		out := map[string]any{
			"target":      res.Target,
			"files":       len(res.Files),
			"uploaded":    res.Uploaded,
			"transport":   res.Transport,
			"fallback":    res.Fallback,
			"diagnostics": res.Diagnostics,
			"ok":          res.Ok(),
			"logs":        logs,
		}
		if res.UploadErr != nil {
			out["uploadError"] = res.UploadErr.Error()
		}
		if res.CompileError != nil {
			out["compileError"] = res.CompileError.Error()
		}
		if len(res.UploadFailed) > 0 {
			var fails []map[string]string
			for _, f := range res.UploadFailed {
				fails = append(fails, map[string]string{
					"file": f.File.Rel, "error": f.Err.Error(),
				})
			}
			out["uploadFailed"] = fails
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return exitCode(res)
	}

	printSummary(res)
	return exitCode(res)
}

// hasFlagsAfterOperand 判断是否有 flag 出现在文件参数之后。
//
// 标准 flag 包遇到第一个非 flag 参数就停止解析，`deploy a.4gl --dry-run`
// 会把 --dry-run 当文件名。检测到这种情况才做重排，常见写法（flag 在前）
// 走原生路径，行为与 flag 包一致。
//
// 注意 `-target test` 里的 test 是 flag 的值而不是文件参数，必须跳过，
// 否则会把普通命令误判成需要重排。
func hasFlagsAfterOperand(args []string) bool {
	seenOperand := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a != "-" && strings.HasPrefix(a, "-") {
			if seenOperand {
				return true
			}
			// 跳过带值 flag 的值
			if !strings.Contains(a, "=") && valueFlags[strings.TrimLeft(a, "-")] && i+1 < len(args) {
				i++
			}
			continue
		}
		seenOperand = true
	}
	return false
}

// valueFlags 是"后面必须跟一个值"的 flag 名。
//
// 重排时要认识它们，否则 -target test 里的 test 会被当成文件名。
var valueFlags = map[string]bool{
	"target": true, "config": true, "workspace": true,
}

// reorderArgs 把 flag 与文件分开，返回 (flag 序列, 文件序列)。
//
// 只支持 `-flag value` 与 `-flag=value` 两种写法；布尔 flag 不接受值。
func reorderArgs(args []string) (flags, files []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-" || !strings.HasPrefix(a, "-") {
			files = append(files, a)
			continue
		}
		flags = append(flags, a)
		if strings.Contains(a, "=") {
			continue // -flag=value 自带值
		}
		name := strings.TrimLeft(a, "-")
		if valueFlags[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, files
}

// printSummary 打印部署摘要。
func printSummary(res *deploy.Result) {
	fmt.Println()
	errs, warns := deploy.Summarize(res.Diagnostics)
	fmt.Printf("目标      : %s\n", res.Target)
	fmt.Printf("文件数    : %d\n", len(res.Files))
	if res.Transport != "" {
		suffix := ""
		if res.Fallback {
			suffix = "（已从首选方式回退）"
		}
		fmt.Printf("传输方式  : %s%s\n", res.Transport, suffix)
		fmt.Printf("已上传    : %d\n", res.Uploaded)
	}
	if res.UploadErr != nil {
		fmt.Printf("上传错误  : %v\n", res.UploadErr)
	}
	if len(res.UploadFailed) > 0 {
		fmt.Printf("上传失败  : %d\n", len(res.UploadFailed))
		for _, f := range res.UploadFailed {
			fmt.Printf("  - %s: %v\n", f.File.Rel, f.Err)
		}
	}
	if res.CompileError != nil {
		fmt.Printf("编译错误  : %v\n", res.CompileError)
	}
	if len(res.Diagnostics) > 0 {
		fmt.Printf("编译问题  : %d error / %d warning\n", errs, warns)
		for _, d := range res.Diagnostics {
			loc := d.File
			if d.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, d.Line)
				if d.Column > 0 {
					loc += fmt.Sprintf(":%d", d.Column)
				}
			}
			code := ""
			if d.Code != "" {
				code = "[" + d.Code + "] "
			}
			fmt.Printf("  %s: %s%s%s\n", loc, d.Severity, code, d.Message)
		}
	}
	if res.Ok() {
		fmt.Println("结果      : 成功")
	} else {
		fmt.Println("结果      : 失败")
	}
}

// exitCode 把结果映射成退出码。
func exitCode(res *deploy.Result) int {
	if res.Ok() {
		return 0
	}
	return 1
}
