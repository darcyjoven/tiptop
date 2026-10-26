package deploy

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// 上传前的文件检查结果。
const (
	// Kind4GL 是 .4gl 源文件。
	Kind4GL = "4gl"
	// KindGlobal 是 .global 文件（随 4gl 一起上传，不单独编译）。
	KindGlobal = "global"
	// KindPER 是 .per 表单文件。
	KindPER = "per"
)

// File 是待上传的一个本地文件。
type File struct {
	// Abs 是本地绝对路径。
	Abs string
	// Rel 是相对 LocalRoot 的 / 分隔路径，也是远程路径的拼接依据。
	Rel string
	// Kind 是文件类型：4gl / global / per。
	Kind string
	// Warnings 是该文件相关的告警（如 CRLF 行尾），不影响上传。
	Warnings []string
}

// RemotePath 返回该文件在服务器上的绝对路径。
//
// 用 POSIX 拼接（path.Join 语义）而不是 filepath.Join：远程一定是 POSIX，
// 在 Windows 上用 filepath.Join 会拼出反斜杠。
func (f File) RemotePath(remoteRoot string) string {
	return posixJoin(remoteRoot, f.Rel)
}

// CollectFiles 按 include/exclude 收集待上传文件。
//
// 遍历 localRoot；只收 include 命中且 exclude 未命中的常规文件。
// 返回的文件按 Rel 排序，保证 dry-run 输出与上传顺序稳定。
func CollectFiles(localRoot string, up Upload) ([]File, error) {
	inc, err := compileGlobs(up.Include)
	if err != nil {
		return nil, fmt.Errorf("include 模式非法: %w", err)
	}
	exc, err := compileGlobs(up.Exclude)
	if err != nil {
		return nil, fmt.Errorf("exclude 模式非法: %w", err)
	}

	root, err := filepath.Abs(localRoot)
	if err != nil {
		return nil, fmt.Errorf("解析本地根目录失败: %w", err)
	}
	if info, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("本地根目录不可用 %s: %w", root, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("本地根目录不是目录: %s", root)
	}

	var files []File
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 单个目录读不了不该让整次部署失败，但要记下来。
			return nil
		}
		if d.IsDir() {
			// .git 与 .bdl-lsp 永远跳过：前者是版本库，后者是工具配置，
			// 即便用户 include 写了 ** 也不该传上去。
			base := filepath.Base(p)
			// `.git` 是版本库；`.project`（含 link/schema/deploy）与 `.link`
			// 是项目资料，即便 include 写了 ** 也不该上传（W6）。
			if p != root && (base == ".git" || base == ".project" || base == ".link" || base == ".bdl-lsp") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel := relSlash(root, p)
		if !inc.Match(rel) || exc.Match(rel) {
			return nil
		}
		kind, ok := kindOf(p)
		if !ok {
			return nil
		}
		files = append(files, File{Abs: p, Rel: rel, Kind: kind})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("遍历 %s 失败: %w", root, walkErr)
	}

	sortFiles(files)
	// 收集阶段做行尾检查：远程编译器对 CRLF 往往报错信息莫名，提前告警。
	for i := range files {
		if w := checkLineEndings(files[i].Abs); w != "" {
			files[i].Warnings = append(files[i].Warnings, w)
		}
	}
	return files, nil
}

// kindOf 根据扩展名判定文件类型。
func kindOf(p string) (string, bool) {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".4gl":
		return Kind4GL, true
	case ".global":
		return KindGlobal, true
	case ".per":
		return KindPER, true
	default:
		return "", false
	}
}

// checkLineEndings 检查文件是否含 CRLF，返回告警文案（无问题返回空串）。
func checkLineEndings(p string) string {
	// 只读前 64KB：语料里 CRLF 通常在开头就能看到，不必整文件扫描。
	const probe = 64 << 10
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, probe)
	n, _ := f.Read(buf)
	buf = buf[:n]
	if idx := bytes.Index(buf, []byte("\r\n")); idx >= 0 {
		return "含 CRLF 行尾（远程编译可能报奇怪的错），建议转成 LF"
	}
	return ""
}

// sortFiles 按 Rel 排序（插入排序：文件数量通常几十到几千，够用且无依赖）。
func sortFiles(files []File) {
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && files[j].Rel < files[j-1].Rel; j-- {
			files[j], files[j-1] = files[j-1], files[j]
		}
	}
}

// posixJoin 用 POSIX 语义拼接路径（不引入 path 包以免与 filepath 混淆）。
func posixJoin(parts ...string) string {
	var out []string
	for _, p := range parts {
		p = strings.ReplaceAll(p, "\\", "/")
		p = strings.Trim(p, "/")
		if p != "" {
			out = append(out, p)
		}
	}
	if strings.HasPrefix(parts[0], "/") {
		return "/" + strings.Join(out, "/")
	}
	return strings.Join(out, "/")
}
