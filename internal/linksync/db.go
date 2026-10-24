package linksync

import (
	"context"
	"fmt"
	"time"

	"github.com/darcyjoven/tiptop/internal/oradb"
)

// dbSource 用纯 Go 驱动直连 Oracle 查 DS.GAL_FILE。
//
// 为什么是 go-ora（Q15 三度定稿）：它**不需要 Oracle 客户端**，纯 Go 编译，
// 也没有 CGO 依赖；代价只是二进制大 2–3 MB。用 godror/Instant Client 的
// 方案已明确取消（会让构建、分发、跨平台全部复杂化）。
type dbSource struct {
	cfg     DBConfig
	dataDir string
	// onRows 是行级进度回调（可为 nil）。
	onRows func(int)
}

func newDBSource(cfg DBConfig, dataDir string, onRows func(int)) (*dbSource, error) {
	if err := cfg.toOraDB().Validate(); err != nil {
		return nil, err
	}
	if cfg.Query == "" {
		cfg.Query = DefaultQuery
	}
	return &dbSource{cfg: cfg, dataDir: dataDir, onRows: onRows}, nil
}

func (d *dbSource) Name() string { return "golang" }

func (d *dbSource) Fetch(ctx context.Context) ([]byte, Stat, error) {
	data, lines, err := oradb.QueryCSV(ctx, d.cfg.toOraDB(), d.dataDir, 60*time.Second, d.onRows)
	if err != nil {
		return nil, Stat{}, err
	}
	if lines == 0 {
		return nil, Stat{}, fmt.Errorf("查询返回 0 行：确认表名/账号权限/`gal04` 过滤条件")
	}
	return data, Stat{
		Source: "golang",
		Detail: fmt.Sprintf("DS.GAL_FILE@%s:%d/%s", d.cfg.Host, d.cfg.toOraDB().PortOr(), d.cfg.Service),
		Rows:   lines,
	}, nil
}
