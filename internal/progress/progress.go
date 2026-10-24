// Package progress 是 CLI 的**进度行协议**（写 stderr）。
//
// 为什么单独定协议：CLI 的输出有两类读者——人（读 stdout 的最终报告）和
// 编辑器插件（要能实时显示"现在在做什么"）。进度必须机器可读，否则插件只能
// 把整段日志糊在输出通道里，用户还是不知道卡在哪一步。
//
// 一行一条，制表符分隔，前缀固定为「进度」：
//
//	进度\t<阶段>\t<详情>\t<i>\t<n>    有计数的阶段（i/n 为 1 基）
//	进度\t<阶段>\t<详情>              无计数的阶段（开始/完成/说明）
//
// 阶段名是约定的稳定字符串：采集 / 扫描源码 / 成链 / 解析 / 清理 / 写入。
// 插件按这个前缀解析，其余 stderr 内容一律当作普通日志。
package progress

import (
	"fmt"
	"io"
)

// Prefix 是进度行的前缀（插件按它识别）。
const Prefix = "进度"

// Reporter 把进度行写到某个 writer（CLI 用 stderr）。
type Reporter struct {
	w io.Writer
	// enabled=false 时不输出（供测试/静默模式）。
	enabled bool
}

// New 返回写到 w 的 reporter；w 为 nil 时返回一个什么都不做的 reporter。
func New(w io.Writer) *Reporter {
	if w == nil {
		return &Reporter{enabled: false}
	}
	return &Reporter{w: w, enabled: true}
}

// Note 报告一条无计数的进度（开始/完成/说明）。
func (r *Reporter) Note(phase, detail string) {
	if r == nil || !r.enabled {
		return
	}
	fmt.Fprintf(r.w, "%s\t%s\t%s\n", Prefix, phase, detail)
}

// Step 报告一条带计数的进度（i 为 1 基，n 为总数）。
//
// i<=0 或 n<=0 时退化成 Note（调用方不必自己判断）。
func (r *Reporter) Step(phase, detail string, i, n int) {
	if r == nil || !r.enabled {
		return
	}
	if i <= 0 || n <= 0 {
		r.Note(phase, detail)
		return
	}
	fmt.Fprintf(r.w, "%s\t%s\t%s\t%d\t%d\n", Prefix, phase, detail, i, n)
}

// Every 返回"该不该在 i/n 处输出"——按需节流，避免几千张表刷屏。
//
// 规则：总数 <= maxLines 时每条都报；否则每 ceil(n/maxLines) 条报一次，
// 且**最后一条一定报**（用户要看到"到 100/100 了"）。
func Every(i, n, maxLines int) bool {
	if i <= 0 || n <= 0 {
		return false
	}
	if i >= n {
		return true
	}
	if maxLines <= 0 || n <= maxLines {
		return true
	}
	step := n / maxLines
	if step < 1 {
		step = 1
	}
	return i%step == 0
}
