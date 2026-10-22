package deploy

import (
	"regexp"
	"strconv"
	"strings"
)

// Diagnostic 是远程编译器报告的一条问题。
type Diagnostic struct {
	// Severity 是 error / warning。
	Severity string
	// File 是本地文件路径（已从远程路径映射回来）；映射失败时为远程路径。
	File string
	// RemoteFile 是原始远程路径（日志与排查用）。
	RemoteFile string
	// Line / Column 是 1 基位置；0 表示编译器没给。
	Line   int
	Column int
	// Code 是错误码（如 fglcomp 的 -8403），无则为空。
	Code string
	// Message 是消息正文。
	Message string
	// Raw 是未解析的原始行（解析失败时也在诊断里保留）。
	Raw string
}

// 严重级别常量。
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// 常见 Genero 编译器输出格式（fglcomp / fglform）：
//
//	aapr106.4gl:232:12: error: -8403: Identifier not defined
//	aapt110.per:15: error: Undefined item
//	aapr106.4gl:232: error: ...
//	** Error: ...                （无位置信息）
//
// 正则刻意写得宽松：不同 Genero 版本与语言设置下分隔符略有差异，
// 漏解析比多解析更糟（漏掉等于用户看不到错误）。
var (
	// reDiagnostic 匹配 `path:line[:col]: severity: [code:] message`。
	reDiagnostic = regexp.MustCompile(
		`^(?P<file>[^:\s][^:]*):(?P<line>\d+):(?:(?P<col>\d+):)?\s*` +
			`(?P<sev>error|warning|fatal|fatal error|Error|Warning|ERROR|WARNING)\s*[:：]\s*` +
			`(?:-?(?P<code>\d+)\s*[:：]\s*)?(?P<msg>.*)$`)

	// reBareDiagnostic 匹配没有位置信息的错误行，例如 `** Error: ...`。
	reBareDiagnostic = regexp.MustCompile(
		`^(?:\*\*\s*)?(?P<sev>error|warning|fatal)\s*[:：]\s*(?P<msg>.*)$`)
)

// ParseCompilerOutput 解析远程编译器输出，把远程路径映射回本地路径。
//
// remoteRoot 用于把 `<remoteRoot>/sub/a.4gl` 映射成 `<localRoot>/sub/a.4gl`。
// 映射由调用方传入的函数完成（不同目标规则可能不同）。
func ParseCompilerOutput(output string, mapPath func(remote string) string) []Diagnostic {
	var out []Diagnostic
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if d, ok := parseDiagnosticLine(trimmed, mapPath); ok {
			d.Raw = trimmed
			out = append(out, d)
		}
	}
	return out
}

// parseDiagnosticLine 解析单行。
func parseDiagnosticLine(line string, mapPath func(string) string) (Diagnostic, bool) {
	if m := reDiagnostic.FindStringSubmatch(line); m != nil {
		file := m[reDiagnostic.SubexpIndex("file")]
		lineno, _ := strconv.Atoi(m[reDiagnostic.SubexpIndex("line")])
		col := 0
		if c := m[reDiagnostic.SubexpIndex("col")]; c != "" {
			col, _ = strconv.Atoi(c)
		}
		d := Diagnostic{
			Severity:   normalizeSeverity(m[reDiagnostic.SubexpIndex("sev")]),
			RemoteFile: file,
			File:       mapPath(file),
			Line:       lineno,
			Column:     col,
			Code:       m[reDiagnostic.SubexpIndex("code")],
			Message:    strings.TrimSpace(m[reDiagnostic.SubexpIndex("msg")]),
		}
		return d, true
	}
	if m := reBareDiagnostic.FindStringSubmatch(strings.ToLower(line)); m != nil {
		// 只接受明确以 error/warning 开头的行，避免把编译器的普通输出
		// （如 "Compiling ..."）误当诊断。
		sev := normalizeSeverity(m[reBareDiagnostic.SubexpIndex("sev")])
		return Diagnostic{
			Severity: sev,
			Message:  strings.TrimSpace(line[strings.Index(line, ":")+1:]),
		}, true
	}
	return Diagnostic{}, false
}

// normalizeSeverity 归一化严重级别。
func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "warning":
		return SeverityWarning
	case "fatal", "fatal error":
		return SeverityError
	default:
		return SeverityError
	}
}

// Summarize 汇总诊断，返回错误数、告警数与是否有错误。
func Summarize(diags []Diagnostic) (errs, warns int) {
	for _, d := range diags {
		if d.Severity == SeverityWarning {
			warns++
		} else {
			errs++
		}
	}
	return errs, warns
}
