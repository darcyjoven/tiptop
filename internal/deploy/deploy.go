package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Options 控制一次部署。
type Options struct {
	// Workspace 是工作区根（用于定位默认配置）。
	Workspace string
	// ConfigPath 是配置文件的显式路径（空则用默认位置）。
	ConfigPath string
	// Target 是目标名（空则用 defaultTarget）。
	Target string
	// DryRun 只打印计划，不真正上传/编译。
	DryRun bool
	// SkipCompile 只上传不编译。
	SkipCompile bool
	// SkipUpload 只编译不上传（远程已是最新代码时）。
	SkipUpload bool
	// Files 是只部署这些文件（相对工作区或绝对路径）；为空表示按 include/exclude 全量。
	Files []string
	// Log 接收过程日志（空则丢弃）。
	Log func(format string, args ...any)
	// Runner 是命令执行器（测试可替换）。
	Runner Runner
}

// Result 是一次部署的结果。
type Result struct {
	// Target 是实际使用的目标名。
	Target string
	// Files 是参与本次部署的文件。
	Files []File
	// Uploaded 是成功上传的文件数。
	Uploaded int
	// Transport 是实际使用的传输方式（rsync / scp）。
	Transport string
	// Fallback 表示是否发生过回退。
	Fallback bool
	// Diagnostics 是编译输出解析结果。
	Diagnostics []Diagnostic
	// UploadFailed 是上传失败的文件与错误（逐文件，scp 路径会填）。
	UploadFailed []UploadError
	// UploadErr 是整体上传失败（如 rsync 与回退都失败）。
	// 与 UploadFailed 一样属于"部署失败"（退出码 1），而不是"无法开始"（退出码 2）。
	UploadErr error
	// CompileError 是编译阶段的整体错误（如 ssh 失败）。
	CompileError error
}

// UploadError 是单个文件的上传失败。
type UploadError struct {
	File File
	Err  error
}

// Ok 判断整体是否成功：无上传失败、无编译错误、无 error 级诊断。
func (r *Result) Ok() bool {
	if len(r.UploadFailed) > 0 || r.UploadErr != nil || r.CompileError != nil {
		return false
	}
	errs, _ := Summarize(r.Diagnostics)
	return errs == 0
}

// Run 执行部署。
//
// 返回的 error 表示"根本无法开始"（配置错、缺工具等）；部署过程中出现的
// 上传/编译失败放在 Result 里，由调用方决定退出码。
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Runner == nil {
		opts.Runner = execRunner{}
	}
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}

	cfg, err := LoadConfig(opts.Workspace, opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	target, name, err := cfg.Pick(opts.Target)
	if err != nil {
		return nil, err
	}
	localRoot := target.LocalRootAbs(cfg.WorkspaceRoot())

	files, err := selectFiles(localRoot, target.Upload, opts.Files)
	if err != nil {
		return nil, err
	}
	res := &Result{Target: name, Files: files}

	logf("目标 %s：%s@%s:%d → %s", name, target.User, target.Host, target.Port, target.RemoteRoot)
	logf("本地根 %s，待处理 %d 个文件", localRoot, len(files))
	for _, f := range files {
		for _, w := range f.Warnings {
			logf("  ⚠ %s: %s", f.Rel, w)
		}
	}
	if len(files) == 0 {
		logf("没有匹配的文件；检查 upload.include（%s）",
			strings.Join(target.Upload.Include, ", "))
	}

	if opts.DryRun {
		for _, f := range files {
			logf("  upload %s → %s", f.Rel, f.RemotePath(target.RemoteRoot))
		}
		logCompilePlan(logf, target, files)
		return res, nil
	}

	if err := requireTool("ssh"); err != nil {
		return nil, err
	}

	if !opts.SkipUpload && len(files) > 0 {
		if err := upload(ctx, opts.Runner, target, localRoot, files, res, logf); err != nil {
			// 上传失败是"部署失败"，不是"无法开始"：记进结果让调用方用退出码 1，
			// 同时不再尝试编译（远程内容不完整，编译结果没有意义）。
			res.UploadErr = err
			return res, nil
		}
	}

	if !opts.SkipCompile {
		if err := compile(ctx, opts.Runner, target, files, localRoot, res, logf); err != nil {
			res.CompileError = err
		}
	}
	sortDiagnostics(res.Diagnostics)
	return res, nil
}

// selectFiles 应用 opts.Files 过滤（若指定）；否则按 include/exclude 全量收集。
func selectFiles(localRoot string, up Upload, only []string) ([]File, error) {
	all, err := CollectFiles(localRoot, up)
	if err != nil {
		return nil, err
	}
	if len(only) == 0 {
		return all, nil
	}

	// 显式指定的文件允许超出 include 范围（用户点名要传的就是要传的），
	// 但仍要满足类型检查，并用相对路径对齐远程结构。
	byRel := make(map[string]File, len(all))
	for _, f := range all {
		byRel[f.Rel] = f
	}
	root, err := filepath.Abs(localRoot)
	if err != nil {
		return nil, fmt.Errorf("解析本地根失败: %w", err)
	}
	// 相对路径按 localRoot 解析（而不是进程 CWD）：用户写的 `a.4gl` 指的是
	// 工作区里的文件，不是"恰好执行命令时所在目录"的文件。用 CWD 会让命令
	// 依赖调用位置，且会拼出 localRoot/../../../x 这种越界路径。
	absOf := func(p string) (string, error) {
		if filepath.IsAbs(p) {
			return filepath.Clean(p), nil
		}
		return filepath.Clean(filepath.Join(root, p)), nil
	}

	var out []File
	seen := map[string]bool{}
	for _, p := range only {
		abs, err := absOf(p)
		if err != nil {
			return nil, fmt.Errorf("解析路径 %s 失败: %w", p, err)
		}
		rel := relSlash(root, abs)
		if strings.HasPrefix(rel, "../") || rel == ".." {
			return nil, fmt.Errorf("文件 %s 不在本地根 %s 内（相对路径按本地根解析）", p, localRoot)
		}
		if seen[rel] {
			continue
		}
		kind, ok := kindOf(abs)
		if !ok {
			return nil, fmt.Errorf("不支持的文件类型（只部署 .4gl/.global/.per）: %s", p)
		}
		if f, found := byRel[rel]; found {
			out = append(out, f)
		} else {
			out = append(out, File{Abs: abs, Rel: rel, Kind: kind})
		}
		seen[rel] = true
	}
	sortFiles(out)
	return out, nil
}

// logCompilePlan 打印编译计划（dry-run 用）。
func logCompilePlan(logf func(string, ...any), t *Target, files []File) {
	for _, f := range files {
		tpl, ok := t.Compile[f.Kind]
		if !ok {
			continue // .global 等不编译
		}
		remote := f.RemotePath(t.RemoteRoot)
		logf("  compile %s: %s", f.Rel, expandTemplate(tpl, remote))
	}
}

// expandTemplate 把 {file} 替换成远程路径（已 shell 转义）。
func expandTemplate(tpl, remoteFile string) string {
	return strings.ReplaceAll(tpl, "{file}", shellQuote(remoteFile))
}

// upload 上传文件，处理"首选方式失败 → 显式回退"。
func upload(ctx context.Context, r Runner, t *Target, localRoot string,
	files []File, res *Result, logf func(string, ...any)) error {

	mode := t.Upload.Mode
	_, err := tryUpload(ctx, r, t, localRoot, files, mode, res, logf)
	if err == nil {
		res.Transport = mode
		res.Uploaded = len(files) - len(res.UploadFailed)
		return nil
	}

	logf("  %s 上传失败：%v", mode, err)
	if t.Upload.Fallback == FallbackNone {
		// 即使整体失败也要如实报告成功了多少个，方便用户判断重试范围。
		res.Uploaded = len(files) - len(res.UploadFailed)
		return fmt.Errorf("%s 上传失败且未配置 fallback: %w", mode, err)
	}
	// 回退必须显式出现在输出里：静默降级会让用户以为在用 rsync。
	logf("  ↪ 回退到 %s（请在输出中确认，这不是静默降级）", t.Upload.Fallback)
	res.Fallback = true
	res.UploadFailed = nil // 回退是完整重试，失败集要重新统计

	if _, err2 := tryUpload(ctx, r, t, localRoot, files, t.Upload.Fallback, res, logf); err2 != nil {
		res.Uploaded = len(files) - len(res.UploadFailed)
		return fmt.Errorf("回退到 %s 仍失败: %w（首选 %s 的错误：%v）",
			t.Upload.Fallback, err2, mode, err)
	}
	res.Transport = t.Upload.Fallback
	res.Uploaded = len(files) - len(res.UploadFailed)
	return nil
}

// tryUpload 用一种传输方式上传。
func tryUpload(ctx context.Context, r Runner, t *Target, localRoot string,
	files []File, mode string, res *Result, logf func(string, ...any)) (Output, error) {

	switch mode {
	case TransportRsync:
		return tryRsync(ctx, r, t, localRoot, files, logf)
	case TransportScp:
		return tryScp(ctx, r, t, localRoot, files, res, logf)
	default:
		return Output{}, fmt.Errorf("未知传输方式 %q", mode)
	}
}

// tryRsync 用 rsync over ssh 上传。
//
// 参数要点：
//   - `-e "ssh ..."` 让 rsync 复用我们的 ssh 选项（端口、host key 策略）；
//   - `--delay-updates` 让所有文件先写临时名、最后统一 rename，
//     避免远程编译读到半个文件；
//   - 文件清单走 `--files-from` 临时文件，不受命令行长度限制；
//   - 默认不加 `--delete`（远程目录常混有编译产物与运行时文件）。
func tryRsync(ctx context.Context, r Runner, t *Target, localRoot string,
	files []File, logf func(string, ...any)) (Output, error) {

	if err := requireTool("rsync"); err != nil {
		return Output{}, err
	}

	list, err := writeFileList(files)
	if err != nil {
		return Output{}, err
	}
	defer os.Remove(list)

	args := append([]string{}, t.Upload.RsyncArgs...)
	args = append(args, "--delay-updates", "--files-from="+list)
	if t.Upload.Delete {
		args = append(args, "--delete")
	}
	for _, pat := range t.Upload.Exclude {
		args = append(args, "--exclude", pat)
	}

	sshCmd := []string{"ssh", "-p", strconv.Itoa(t.Port)}
	sshCmd = append(sshCmd, sshOptions(t)...)
	args = append(args, "-e", strings.Join(sshCmd, " "))

	// 源写 localRoot/（带尾斜杠）：配合 --files-from 的相对路径，
	// 远程会得到 <remoteRoot>/<rel>。
	src := filepath.ToSlash(strings.TrimSuffix(localRoot, string(filepath.Separator))) + "/"
	dest := sshTarget(t) + ":" + strings.TrimSuffix(t.RemoteRoot, "/") + "/"

	// 先确保远程根存在：rsync 不会自动创建多级目录。
	mk := "mkdir -p " + shellQuote(t.RemoteRoot)
	if out, err := remoteCommand(ctx, r, t, mk); err != nil || out.Err != nil {
		return Output{}, fmt.Errorf("创建远程目录 %s 失败: %v %s",
			t.RemoteRoot, firstErr(err, out.Err), strings.TrimSpace(out.Combined()))
	}

	logf("  rsync → %s（%d 个文件）", dest, len(files))
	out, err := r.Run(ctx, "rsync", append(args, src, dest))
	if err != nil {
		return out, err
	}
	if out.Err != nil {
		return out, fmt.Errorf("rsync 退出失败: %v: %s", out.Err, tail(out.Combined(), 20))
	}
	return out, nil
}

// tryScp 用 scp 逐文件上传（Windows 无 rsync 时的主路径）。
//
// 逐文件而不是 `scp -r`：整目录会把 .git、构建产物一起搬过去，且失败粒度
// 只到目录。逐文件能把每个文件的错误精确聚合到 UploadFailed。
func tryScp(ctx context.Context, r Runner, t *Target, localRoot string,
	files []File, res *Result, logf func(string, ...any)) (Output, error) {

	if err := requireTool("scp"); err != nil {
		return Output{}, err
	}

	// 先建好所有需要的远程子目录（scp 不会自动建）。
	dirs := map[string]bool{}
	for _, f := range files {
		remote := f.RemotePath(t.RemoteRoot)
		d := remote[:strings.LastIndex(remote, "/")+1]
		if d != "" {
			dirs[d] = true
		}
	}
	if len(dirs) > 0 {
		keys := make([]string, 0, len(dirs))
		for d := range dirs {
			keys = append(keys, shellQuote(strings.TrimSuffix(d, "/")))
		}
		sort.Strings(keys)
		mk := "mkdir -p " + strings.Join(keys, " ")
		if out, err := remoteCommand(ctx, r, t, mk); err != nil || out.Err != nil {
			return Output{}, fmt.Errorf("创建远程目录失败: %v %s",
				firstErr(err, out.Err), tail(out.Combined(), 10))
		}
	}

	var combined Output
	for _, f := range files {
		remote := sshTarget(t) + ":" + f.RemotePath(t.RemoteRoot)
		args := scpArgs(t, []string{f.Abs}, remote)
		logf("  scp %s → %s", f.Rel, f.RemotePath(t.RemoteRoot))
		out, err := r.Run(ctx, "scp", args)
		combined.Stdout += out.Stdout
		combined.Stderr += out.Stderr
		if err != nil || out.Err != nil {
			res.UploadFailed = append(res.UploadFailed, UploadError{
				File: f,
				Err:  fmt.Errorf("%v %s", firstErr(err, out.Err), strings.TrimSpace(out.Combined())),
			})
		}
	}
	if len(res.UploadFailed) > 0 {
		return combined, uploadSummary(res.UploadFailed)
	}
	return combined, nil
}

// uploadSummary 把逐文件失败聚合成一条错误。
//
// 计划明确要求："部分成功"不能显示成成功，所以这里必须报错并列出全部失败文件。
func uploadSummary(fails []UploadError) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%d 个文件上传失败（其余成功）:", len(fails))
	for _, f := range fails {
		fmt.Fprintf(&b, "\n  - %s: %v", f.File.Rel, f.Err)
	}
	return fmt.Errorf("%s", b.String())
}

// compile 远程编译每个文件。
func compile(ctx context.Context, r Runner, t *Target, files []File,
	localRoot string, res *Result, logf func(string, ...any)) error {

	if len(t.Compile) == 0 {
		logf("  未配置 compile 命令，跳过编译")
		return nil
	}

	var all Output
	for _, f := range files {
		tpl, ok := t.Compile[f.Kind]
		if !ok {
			continue // .global 等不单独编译
		}
		remote := f.RemotePath(t.RemoteRoot)
		script := expandTemplate(tpl, remote)
		logf("  compile %s", f.Rel)

		// 先进远程根目录再编译：fglcomp 常需在当前目录找依赖。
		full := "cd " + shellQuote(t.RemoteRoot) + " && " + script
		out, err := remoteCommand(ctx, r, t, full)
		if err != nil {
			return fmt.Errorf("执行 ssh 失败（%s）: %w", f.Rel, err)
		}
		all.Stdout += out.Stdout
		all.Stderr += out.Stderr
	}

	res.Diagnostics = ParseCompilerOutput(all.Combined(), func(remote string) string {
		return mapRemoteToLocal(remote, t.RemoteRoot, localRoot)
	})
	return nil
}

// mapRemoteToLocal 把远程路径映射回本地路径。
//
// 编译器可能输出相对路径（aapr106.4gl）或绝对路径（/u1/app/aapr106.4gl），
// 两种都要能映射。
func mapRemoteToLocal(remote, remoteRoot, localRoot string) string {
	rp := strings.ReplaceAll(remote, "\\", "/")
	root := strings.TrimSuffix(strings.ReplaceAll(remoteRoot, "\\", "/"), "/")
	if strings.HasPrefix(rp, root+"/") {
		rel := strings.TrimPrefix(rp, root+"/")
		return filepath.Join(localRoot, filepath.FromSlash(rel))
	}
	if strings.HasPrefix(rp, "/") {
		// 绝对路径但不在 remoteRoot 下：保留原样，让用户看到真实位置。
		return rp
	}
	return filepath.Join(localRoot, filepath.FromSlash(rp))
}

// writeFileList 把相对路径清单写进临时文件（rsync --files-from 用）。
func writeFileList(files []File) (string, error) {
	f, err := os.CreateTemp("", "bdl-deploy-files-*.txt")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	defer f.Close()
	for _, file := range files {
		if _, err := fmt.Fprintln(f, file.Rel); err != nil {
			os.Remove(f.Name())
			return "", fmt.Errorf("写入文件清单失败: %w", err)
		}
	}
	return f.Name(), nil
}

// sortDiagnostics 按文件、行号排序诊断，保证输出稳定。
func sortDiagnostics(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].File != ds[j].File {
			return ds[i].File < ds[j].File
		}
		return ds[i].Line < ds[j].Line
	})
}

// firstErr 返回第一个非 nil 的错误。
func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// tail 返回最后 n 行（日志里贴全量输出没人看）。
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
