package deploy

import (
	"path/filepath"

	globpkg "github.com/darcyjoven/bdl-lsp/glob"
)

// glob 是编译后的包含/排除模式（实现见 internal/glob，与 bdl.ignore 共用）。
type glob = globpkg.Pattern

func compileGlob(pattern string) (*glob, error) { return globpkg.Compile(pattern) }

// globSet 是一组编译后的模式。
type globSet = globpkg.Set

func compileGlobs(patterns []string) (globSet, error) { return globpkg.CompileSet(patterns) }

// checkGlobs 校验一组模式串，返回带字段前缀的错误。
//
// 单独抽出来是为了让 config 的 Validate 能给出精确位置（include 还是 exclude）。
func checkGlobs(field string, patterns []string) []string {
	return globpkg.Validate(field, patterns)
}

// relSlash 返回相对 root 的 / 分隔路径；失败时退回原路径。
func relSlash(root, file string) string {
	rel, err := filepath.Rel(root, file)
	if err != nil {
		return filepath.ToSlash(file)
	}
	return filepath.ToSlash(rel)
}
