package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// 单文件上传与远程执行（TODO §0d T2/T3）。
//
// 与 deploy 的区别：deploy 面向「多文件/整包上传 + 批量编译」；这里只处理
// **当前打开的一个文件**——上传它，或在生产根目录下跑一条命令。
// 插件（VSCode）只走这条路：用户明确要求**不允许批量同步**。

// RelPath 规范化并校验「相对工作区根的文件路径」。
func RelPath(rel string) (string, error) {
	s := strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if s == "" {
		return "", fmt.Errorf("文件相对路径为空")
	}
	if strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("必须是相对路径：%s", rel)
	}
	if len(s) >= 2 && s[1] == ':' {
		return "", fmt.Errorf("不接受盘符路径：%s", rel)
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return "", fmt.Errorf("路径不能越出工作区根：%s", rel)
		}
	}
	return path.Clean(s), nil
}

// LocalPathFor 返回相对路径在本地的绝对路径。
func LocalPathFor(localRoot, rel string) (string, error) {
	rp, err := RelPath(rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(localRoot, filepath.FromSlash(rp)), nil
}

// RemotePathFor 返回相对路径在远程生产根下的绝对路径（POSIX，正斜杠）。
func RemotePathFor(remoteRoot, rel string) (string, error) {
	rp, err := RelPath(rel)
	if err != nil {
		return "", err
	}
	return posixJoin(remoteRoot, rp), nil
}

// RemoteScript 生成远程 shell 脚本：先 cd 到生产根，再执行命令。
//
// 生产根做 shell 转义；命令本身**不加引号**（它可能含 `&&`、重定向、引号），
// 交给远端 shell 解析——与 `ssh host 'cmd'` 在终端里的效果一致。
// 命令模板里的 `cd ${path}` 因此能在生产根之下继续生效；没有 `cd` 的命令
// （如 `r.l2 ${name}`）也靠这里的 cd 落到正确目录。
func RemoteScript(remoteRoot, command string) string {
	return "cd " + shellQuote(strings.TrimSuffix(remoteRoot, "/")) + " && " + command
}

// NewExecRunner 返回生产用的命令执行器（测试可注入 Runner）。
func NewExecRunner() Runner { return execRunner{} }

// UploadOne 上传单个文件；远端落到 <remoteRoot>/<rel> 同路径。
//
// mode 取 Target.Upload（默认 rsync；失败按 Fallback 回退 scp）。
// dryRun 只打印计划、不执行。rel 必须是相对工作区根的路径。
func UploadOne(ctx context.Context, r Runner, t *Target, localRoot, rel string, dryRun bool, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	local, err := LocalPathFor(localRoot, rel)
	if err != nil {
		return err
	}
	remote, err := RemotePathFor(t.RemoteRoot, rel)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(local); statErr != nil {
		return fmt.Errorf("本地文件不存在：%s", local)
	}

	mode := strings.ToLower(strings.TrimSpace(t.Upload.Mode))
	if mode == "" {
		mode = TransportRsync
	}
	fallback := strings.ToLower(strings.TrimSpace(t.Upload.Fallback))
	logf("目标 %s@%s:%d  生产根 %s", t.User, t.Host, t.Port, t.RemoteRoot)

	switch mode {
	case TransportScp:
		if dryRun {
			logf("scp %s → %s:%s", local, sshTarget(t), remote)
			return nil
		}
		return uploadOneScp(ctx, r, t, local, remote, logf)
	case TransportRsync:
		if dryRun {
			logf("rsync %s → %s:%s（失败回退 %s）", local, sshTarget(t), remote, fallback)
			return nil
		}
		if err := uploadOneRsync(ctx, r, t, local, remote, logf); err != nil {
			if fallback != TransportScp {
				return err
			}
			logf("rsync 失败，回退 scp：%v", err)
			return uploadOneScp(ctx, r, t, local, remote, logf)
		}
		return nil
	default:
		return fmt.Errorf("未知上传方式 %q（可选 rsync / scp）", mode)
	}
}

// remoteDirOf 返回远程路径的父目录（POSIX）。
func remoteDirOf(remote string) string {
	if i := strings.LastIndex(remote, "/"); i > 0 {
		return remote[:i]
	}
	return "/"
}

// ensureRemoteDir 在远程建好目录（scp 不会自动建）。
func ensureRemoteDir(ctx context.Context, r Runner, t *Target, dir string, logf func(string, ...any)) error {
	script := "mkdir -p " + shellQuote(dir)
	out, err := remoteCommand(ctx, r, t, script)
	if err != nil {
		return err
	}
	if out.Err != nil {
		return fmt.Errorf("创建远程目录 %s 失败: %v %s", dir, out.Err, strings.TrimSpace(out.Combined()))
	}
	logf("  mkdir -p %s", dir)
	return nil
}

// uploadOneScp 用 scp 上传单个文件（先建远程目录）。
func uploadOneScp(ctx context.Context, r Runner, t *Target, local, remote string, logf func(string, ...any)) error {
	if err := requireTool("scp"); err != nil {
		return err
	}
	dir := remoteDirOf(remote)
	if err := ensureRemoteDir(ctx, r, t, dir, logf); err != nil {
		return err
	}
	dest := sshTarget(t) + ":" + remote
	logf("  scp %s → %s", local, dest)
	out, err := r.Run(ctx, "scp", scpArgs(t, []string{local}, dest))
	if err != nil {
		return err
	}
	if out.Err != nil {
		return fmt.Errorf("scp 失败: %v %s", out.Err, tail(out.Combined(), 10))
	}
	return nil
}

// uploadOneRsync 用 rsync over ssh 上传单个文件（先建远程目录）。
func uploadOneRsync(ctx context.Context, r Runner, t *Target, local, remote string, logf func(string, ...any)) error {
	if err := requireTool("rsync"); err != nil {
		return err
	}
	dir := remoteDirOf(remote)
	if err := ensureRemoteDir(ctx, r, t, dir, logf); err != nil {
		return err
	}
	args := append([]string{}, t.Upload.RsyncArgs...)
	sshCmd := []string{"ssh", "-p", strconv.Itoa(t.Port)}
	sshCmd = append(sshCmd, sshOptions(t)...)
	args = append(args, "-e", strings.Join(sshCmd, " "))
	dest := sshTarget(t) + ":" + remote
	logf("  rsync %s → %s", local, dest)
	out, err := r.Run(ctx, "rsync", append(args, local, dest))
	if err != nil {
		return err
	}
	if out.Err != nil {
		return fmt.Errorf("rsync 失败: %v %s", out.Err, tail(out.Combined(), 10))
	}
	return nil
}

// RunRemoteStream 在远程生产根执行命令；stdout/stderr 直接流给调用方。
//
// 返回远程退出码（拿不到时 -1）；err 仅表示"连 ssh 都没跑起来"（如缺 ssh 命令）。
func RunRemoteStream(ctx context.Context, t *Target, command string, stdout, stderr io.Writer) (int, error) {
	if err := requireTool("ssh"); err != nil {
		return -1, err
	}
	args := sshCommandArgs(t, RemoteScript(t.RemoteRoot, command))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}
