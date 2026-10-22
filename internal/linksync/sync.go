package linksync

import (
	"context"
	"fmt"
	"log/slog"
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
	// Write=false 只采集+校验，不落盘（给 `link sync -dry-run` 用）。
	Write bool
	Now   time.Time
	// Progress 是可选的源码扫描进度回调（CLI 用；后台调度传 nil）。
	Progress func(done, total int)
}

// Result 是采集 + 导入的结果。
type Result struct {
	Stat    Stat
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
	out, err := link.RunImport(link.ImportRequest{
		CSVData:      data,
		ProjectRoot:  req.ProjectRoot,
		DataDir:      dataDir,
		ModuleRoots:  req.ModuleRoots,
		Pool:         req.Pool,
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
	return &Result{Stat: stat, Outcome: out}, nil
}

// Scheduler 在 LSP 内部按 interval 自动同步（失败指数退避）。
//
// 设计取舍：
//   - 同步是**后台**的，绝不阻塞 initialize/didOpen；
//   - 失败只记日志，不产生诊断噪声（用户的数据库可能就没配）；
//   - 退避上限 1 h，连续失败 3 次后降到每 1 h 一次，避免刷日志。
type Scheduler struct {
	Config  Config
	Request Request
	Log     *slog.Logger
	// OnSynced 在每次成功同步后回调（server 用它重载资料 + 重发诊断）。
	OnSynced func(*Result)
}

// Start 启动后台同步（onStart 立即跑一次 + interval 定时）。
//
// 返回的 stop 用于关闭；ctx 取消也会停。
func (s *Scheduler) Start(ctx context.Context) (stop func()) {
	if s == nil || !s.Config.Enable {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if s.Config.OnStart {
			s.once(ctx)
		}
		interval := time.Duration(s.Config.Interval) * time.Second
		if interval <= 0 {
			return
		}
		delay := interval
		fails := 0
		timer := time.NewTimer(delay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if s.once(ctx) {
					fails, delay = 0, interval
				} else {
					fails++
					delay = interval << min(fails, 6) // 2×/4×/…，上限 64×
					if delay > time.Hour {
						delay = time.Hour
					}
				}
				timer.Reset(delay)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// once 跑一次同步，返回是否成功。
func (s *Scheduler) once(ctx context.Context) bool {
	res, err := Run(ctx, s.Request)
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("链接资料自动同步失败", "err", err)
		}
		return false
	}
	if s.Log != nil {
		s.Log.Info("链接资料已自动同步",
			"source", res.Stat.Source, "chains", res.Outcome.Result.Meta.Chains,
			"elapsed", res.Outcome.Elapsed.String())
	}
	if s.OnSynced != nil {
		s.OnSynced(res)
	}
	return true
}
