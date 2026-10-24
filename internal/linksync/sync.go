package linksync

import (
	"context"
	"fmt"
	"time"

	"github.com/darcyjoven/bdl-lsp/link"
	"github.com/darcyjoven/bdl-lsp/parser"
)

// Request 是一次"采集 + 导入"的完整请求。
type Request struct {
	Config      Config
	ProjectRoot string
	DataDir     string
	ModuleRoots []string
	Pool        *parser.Pool
	ToolVersion string
	// Modules / Specs 非空时走**局部更新**（P1-3）：只重算这些模组/文件涉及
	// 的程序，其余链原样保留。Specs 形如 `aap/4gl/saapt310.4gl`。
	// source=golang 且用的是默认 SQL 时，条件会下推到 Oracle（少取行）。
	Modules []string
	Specs   []string
	// Write=false 只采集+校验，不落盘（给 `link sync -dry-run` 用）。
	Write bool
	Now   time.Time
	// Progress 是可选的源码扫描进度回调（CLI 用；后台调度传 nil）。
	Progress func(done, total int)
	// ChainProgress 是"成链"阶段的进度回调（done/total）。
	ChainProgress func(done, total int)
	// OnRows 是直连数据库时的行级进度回调（可为 nil）。
	OnRows func(int)
}

// Result 是采集 + 导入的结果。
//
// Outcome 与 Partial 二选一：全量走 Outcome，局部更新走 Partial。
type Result struct {
	Stat    Stat
	Partial *link.PartialImportOutcome
	Outcome *link.ImportOutcome
}

// Run 采集一次并导入。采集失败不会动已有资料。
func Run(ctx context.Context, req Request) (*Result, error) {
	if req.Pool == nil {
		return nil, fmt.Errorf("缺少解析器池")
	}
	dataDir := req.DataDir
	if dataDir == "" {
		dataDir = req.ProjectRoot + string('/') + link.DefaultDataDir
	}
	src, err := req.Config.NewSource(dataDir)
	if err != nil {
		return nil, err
	}
	data, stat, err := src.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.Modules) > 0 || len(req.Specs) > 0 {
		part, err := link.RunImportPartial(link.PartialImportRequest{
			CSVData:      data,
			ProjectRoot:  req.ProjectRoot,
			DataDir:      dataDir,
			ModuleRoots:  req.ModuleRoots,
			Pool:         req.Pool,
			Modules:      req.Modules,
			Specs:        req.Specs,
			Source:       stat.Source,
			SourceDetail: stat.Detail,
			ToolVersion:  req.ToolVersion,
			Write:        req.Write,
			Now:          req.Now,
			Progress:     req.Progress,
		})
		if err != nil {
			return nil, err
		}
		return &Result{Stat: stat, Partial: part}, nil
	}
	out, err := link.RunImport(link.ImportRequest{
		ChainProgress: req.ChainProgress,
		CSVData:       data,
		ProjectRoot:   req.ProjectRoot,
		DataDir:       dataDir,
		ModuleRoots:   req.ModuleRoots,
		Pool:          req.Pool,
		Source:        stat.Source,
		SourceDetail:  stat.Detail,
		ToolVersion:   req.ToolVersion,
		Write:         req.Write,
		Now:           req.Now,
		Progress:      req.Progress,
	})
	if err != nil {
		return nil, err
	}
	return &Result{Stat: stat, Outcome: out}, nil
}
