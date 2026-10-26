// Package execcli 实现 `tiptop exec` / `tiptop upload`（TODO §0d T2/T3）。
//
// 与 deploy 的分工：deploy 是「多文件上传 + 批量编译」；exec/upload 只处理
// **当前打开的一个文件**——上传它，或在生产根目录下跑一条命令。插件只走这条路。
package execcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/darcyjoven/tiptop/internal/deploy"
)

// Run 分发 exec / upload 子命令。
func Run(args []string, version string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	sub := args[0]
	args = args[1:]
	switch sub {
	case "exec":
		return runExec(args, version)
	case "upload":
		return runUpload(args, version)
	case "version", "--version", "-v":
		fmt.Printf("tiptop %s\n", version)
		return 0
	case "help", "--help", "-h":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "tiptop exec: 未知子命令 %q\n", sub)
		usage(os.Stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `用法: tiptop <exec|upload> [参数...]

子命令:
  exec   在生产服务器上执行一条命令（工作目录 = 生产根目录）
         -command <命令>（必填）[-file <相对路径>] [-dry-run] [-json]
  upload 只上传当前文件（相对工作区根 → 生产根同路径）
         -file <相对路径>（必填）[-dry-run] [-json]

公共参数:
  -workspace <工作区根>（默认当前目录）
  -config <部署配置>（默认 <工作区>/.project/deploy/deploy.json；旧路径兼容）
  -target <目标名>（默认配置的 defaultTarget）

说明: 配置文件与 tiptop deploy 相同（host/port/user/remoteRoot/upload.mode）。
      只操作单个文件，不做批量同步；编译/链接/预览由 -command 交给生产端脚本。
`)
}

// workspaceOrCwd 解析工作区根（缺省用当前目录）。
func workspaceOrCwd(ws, who string) string {
	if strings.TrimSpace(ws) != "" {
		return ws
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: 无法获取当前目录: %v\n", who, err)
		return ""
	}
	return wd
}

// loadTarget 加载配置并选目标。
func loadTarget(ws, configPath, targetName, who string) (*deploy.Config, *deploy.Target, string, bool) {
	cfg, err := deploy.LoadConfig(ws, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", who, err)
		return nil, nil, "", false
	}
	t, name, err := cfg.Pick(targetName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", who, err)
		return nil, nil, "", false
	}
	return cfg, t, name, true
}

func runExec(args []string, version string) int {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspace := fs.String("workspace", "", "工作区根（默认当前目录）")
	configPath := fs.String("config", "", "部署配置路径（默认 <工作区>/.project/deploy/deploy.json）")
	targetName := fs.String("target", "", "目标名（默认配置的 defaultTarget）")
	file := fs.String("file", "", "当前文件（相对工作区根；仅用于打印上下文）")
	command := fs.String("command", "", "在生产根目录下执行的命令（必填）")
	dryRun := fs.Bool("dry-run", false, "只打印要执行的命令，不连接")
	asJSON := fs.Bool("json", false, "额外输出一行 JSON 结果")
	showVersion := fs.Bool("version", false, "打印版本")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Printf("tiptop %s\n", version)
		return 0
	}
	if strings.TrimSpace(*command) == "" {
		fmt.Fprintln(os.Stderr, "tiptop exec: 必须给 -command")
		return 2
	}
	ws := workspaceOrCwd(*workspace, "tiptop exec")
	if ws == "" {
		return 2
	}
	_, t, name, ok := loadTarget(ws, *configPath, *targetName, "tiptop exec")
	if !ok {
		return 1
	}
	if strings.TrimSpace(*file) != "" {
		rp, err := deploy.RelPath(*file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tiptop exec: %v\n", err)
			return 2
		}
		fmt.Fprintf(os.Stderr, "文件 %s\n", rp)
	}
	fmt.Fprintf(os.Stderr, "目标 %s（%s@%s:%d）生产根 %s\n", name, t.User, t.Host, t.Port, t.RemoteRoot)
	if *dryRun {
		fmt.Printf("ssh %s@%s -p %d <%s>\n", t.User, t.Host, t.Port, deploy.RemoteScript(t.RemoteRoot, *command))
		return 0
	}
	code, err := deploy.RunRemoteStream(context.Background(), t, *command, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tiptop exec: %v\n", err)
		return 2
	}
	if *asJSON {
		printJSON(map[string]any{"target": name, "exitCode": code, "remoteRoot": t.RemoteRoot})
	}
	return code
}

func runUpload(args []string, version string) int {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspace := fs.String("workspace", "", "工作区根（默认当前目录）")
	configPath := fs.String("config", "", "部署配置路径（默认 <工作区>/.project/deploy/deploy.json）")
	targetName := fs.String("target", "", "目标名（默认配置的 defaultTarget）")
	file := fs.String("file", "", "要上传的文件（相对工作区根，必填）")
	dryRun := fs.Bool("dry-run", false, "只打印上传计划，不连接")
	asJSON := fs.Bool("json", false, "额外输出一行 JSON 结果")
	showVersion := fs.Bool("version", false, "打印版本")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Printf("tiptop %s\n", version)
		return 0
	}
	if strings.TrimSpace(*file) == "" {
		fmt.Fprintln(os.Stderr, "tiptop upload: 必须给 -file（相对工作区根）")
		return 2
	}
	ws := workspaceOrCwd(*workspace, "tiptop upload")
	if ws == "" {
		return 2
	}
	_, t, name, ok := loadTarget(ws, *configPath, *targetName, "tiptop upload")
	if !ok {
		return 1
	}
	rel, err := deploy.RelPath(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tiptop upload: %v\n", err)
		return 2
	}
	remote, err := deploy.RemotePathFor(t.RemoteRoot, rel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tiptop upload: %v\n", err)
		return 2
	}
	localRoot := t.LocalRootAbs(ws)
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
	}
	fmt.Fprintf(os.Stderr, "目标 %s（%s@%s:%d）生产根 %s\n", name, t.User, t.Host, t.Port, t.RemoteRoot)
	if err := deploy.UploadOne(context.Background(), deploy.NewExecRunner(), t, localRoot, rel, *dryRun, logf); err != nil {
		fmt.Fprintf(os.Stderr, "tiptop upload: %v\n", err)
		return 1
	}
	if !*dryRun {
		fmt.Fprintf(os.Stderr, "已上传 → %s:%s\n", t.Host, remote)
	}
	if *asJSON {
		printJSON(map[string]any{
			"target": name, "file": rel, "remotePath": remote, "dryRun": *dryRun,
		})
	}
	return 0
}

func printJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Printf("\n%s\n", b)
}
