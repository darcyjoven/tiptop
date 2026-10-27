package deploy

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner 执行外部命令，便于测试时替换。
type Runner interface {
	// Run 执行命令并返回输出。返回的 error 仅表示"执行器本身失败"
	// （如二进制不存在）；非零退出码放在 Output.Err 里，由调用方判断。
	Run(ctx context.Context, name string, args []string) (Output, error)
}

// Output 是一次命令执行的结果。
type Output struct {
	// Stdout / Stderr 分开保存：有些编译器把错误写 stderr，有些写 stdout。
	Stdout string
	Stderr string
	// Err 是执行错误（非零退出码或启动失败）。
	Err error
	// CmdLine 是实际执行的命令行（已脱敏，便于日志与 dry-run 展示）。
	CmdLine string
}

// Combined 返回合并输出（先 stdout 后 stderr）。
func (o Output) Combined() string {
	return o.Stdout + o.Stderr
}

// execRunner 是生产用的 Runner。
type execRunner struct{}

// Run 实现 Runner。
func (execRunner) Run(ctx context.Context, name string, args []string) (Output, error) {
	return runCapture(ctx, name, args, "")
}

// RunStdin 实现 stdinRunner：先把 stdin 写进子进程再等它结束。
func (execRunner) RunStdin(ctx context.Context, name string, args []string, stdin string) (Output, error) {
	return runCapture(ctx, name, args, stdin)
}

// runCapture 执行命令并收集 stdout/stderr；stdin 非空时作为子进程的标准输入。
func runCapture(ctx context.Context, name string, args []string, stdin string) (Output, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	err := cmd.Run()
	return Output{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Err:     err,
		CmdLine: name + " " + strings.Join(args, " "),
	}, nil
}

// sshOptions 返回 ssh/scp 共用的连接参数（不含目标与命令）。
//
// 安全要点：
//   - 默认 StrictHostKeyChecking=yes；
//   - strictHostKey=false 时用 accept-new（首次接受、之后仍校验变更），
//     绝不使用 StrictHostKeyChecking=no（那会静默接受任何主机）；
//   - 不强制 BatchMode，让 ssh 能走 agent 或交互式解密密钥；
//     用 ConnectTimeout 限时避免卡死。
func sshOptions(t *Target) []string {
	timeout := strconv.Itoa(int(t.timeout() / time.Second))
	opts := []string{"-o", "ConnectTimeout=" + timeout}
	if t.strictHostKey() {
		opts = append(opts, "-o", "StrictHostKeyChecking=yes")
	} else {
		opts = append(opts, "-o", "StrictHostKeyChecking=accept-new")
	}
	return opts
}

// sshTarget 返回 user@host。
func sshTarget(t *Target) string { return t.User + "@" + t.Host }

// sshCommandArgs 构造 `ssh ... user@host <script>` 的参数。
//
// 顺序必须是：选项 → 目标 → 命令。目标之后的参数会被 ssh 用空格拼成一条
// 字符串交给远程登录 shell，所以这里只传**一个**元素，且**不能**再加外层引号。
//
// 踩坑：曾把脚本事先包成 `'mkdir -p /x'` 再传给 ssh，结果远程 shell 把整串
// 当成一个文件名，报 `zsh: no such file or directory: mkdir -p /x`。
// 原因：`ssh host 'cmd'` 在终端里能用，是因为**本地 shell** 先把引号剥掉了；
// 这里用 exec 直接调用没有本地 shell，再加引号就多了一层。
// 单引号只用于脚本内部插值的值（见 shellQuote）。
func sshCommandArgs(t *Target, script string) []string {
	args := []string{"-p", strconv.Itoa(t.Port)}
	args = append(args, sshOptions(t)...)
	args = append(args, sshTarget(t), script)
	return args
}

// scpArgs 构造 `scp ... src... dest` 的参数。
func scpArgs(t *Target, sources []string, dest string) []string {
	args := []string{"-P", strconv.Itoa(t.Port)} // 注意 scp 是大写 -P
	args = append(args, sshOptions(t)...)
	args = append(args, sources...)
	return append(args, dest)
}

// stdinRunner 是 Runner 的可选扩展：把一段文本喂给被调命令的 stdin。
//
// 远端登录菜单（选「区」）只能靠它——ssh 没有 stdin 时菜单读到 EOF，
// 后面的命令等于没执行。测试里的假 Runner 不实现它，行为保持不变。
type stdinRunner interface {
	RunStdin(ctx context.Context, name string, args []string, stdin string) (Output, error)
}

// remoteCommand 在远程执行一段 shell 脚本；Target.Region 非空时先把这一行喂给远端菜单。
func remoteCommand(ctx context.Context, r Runner, t *Target, script string) (Output, error) {
	args := sshCommandArgs(t, script)
	if region := strings.TrimSpace(t.Region); region != "" {
		if sr, ok := r.(stdinRunner); ok {
			return sr.RunStdin(ctx, "ssh", args, region+"\n")
		}
	}
	return r.Run(ctx, "ssh", args)
}

// shellQuote 对单个值做 POSIX shell 转义（用于路径等）。
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case r == '_' || r == '-' || r == '.' || r == '/' || r == ':' ||
			r == '=' || r == '@' || r == ',' || r == '+':
			return false
		}
		return true
	}) < 0 {
		return s // 全是安全字符，省掉引号便于日志阅读
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// lookPath 检查工具是否存在（变量便于测试替换）。
var lookPath = exec.LookPath

// requireTool 检查必需的外部工具，缺失时给出可操作的提示。
func requireTool(names ...string) error {
	var missing []string
	for _, n := range names {
		if _, err := lookPath(n); err != nil {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少外部命令 %s；请安装 OpenSSH（ssh/scp）；"+
			"rsync 模式还需要 rsync", strings.Join(missing, " / "))
	}
	return nil
}

// withTimeout 在父 context 上加超时。
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}
