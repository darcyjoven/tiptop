// schema 导入的编排逻辑：解析 CSV → 与旧索引比对 → 增量写盘。
//
// 与 CLI 分开：这里是纯逻辑（可单测，不碰 flag/stdout），schemacli.go 只做
// 参数解析与报告（与 internal/deploycli + internal/deploy 的分工一致）。
package schemacli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ImportOptions 是一次 schema 导入的输入。
type ImportOptions struct {
	// CSVPath 是 Oracle 导出的 CSV 路径（与 CSVData 二选一）。
	CSVPath string
	// CSVData 是内存里的 CSV（优先于 CSVPath）：`schema sync` 直连数据库
	// 时直接把它查出来的结果喂进来，与文件导入走**完全同一条**合并/写盘逻辑。
	CSVData []byte
	// DataDir 是资料目录（绝对路径，调用方已解析）。
	DataDir string
	// OnlyTables 非空时只导入这些表（大写；其余表原样保留）。
	OnlyTables []string
	// OnlyNew 为真时只写本地不存在的表，已有表一个字节都不动。
	OnlyNew bool
	// DryRun 为真时只解析与统计，不写盘（也不建目录）。
	DryRun bool
	// Prune 为真时删除"本地有、这次没出现"的表（表文件 + 索引条目）。
	//
	// 只能与全量（不指定 -tables/-only-new）一起用：否则会把这次范围之外
	// 的表删掉（调用方负责拒绝这种组合）。
	Prune bool
	// Source 记进 tables.json（默认用 CSVPath 的基线名；`schema sync` 传
	// "golang@host:port/svc owner=X" 这类来源描述）。
	Source string
	// Progress 是逐表进度回调（phase="解析"/"清理"，detail=表名，i/n 为 1 基）。
	// 可为 nil；调用方通常用 progress.Reporter 把它转成 stderr 的进度行。
	Progress func(phase, detail string, i, n int)
	// Now 注入导入时间（零值取 time.Now）。
	Now time.Time
}

// ImportResult 是一次导入的统计（报告由 CLI 打印）。
type ImportResult struct {
	DataDir       string
	RowsTotal     int    // CSV 数据行数（不含表头）
	RowsSkipped   int    // 跳过的行数
	TablesNew     int    // 本次新写出的表
	TablesChanged int    // 已在索引里、本次内容有变化的表
	TablesSame    int    // 已在索引里、本次内容一致（或 -only-new 跳过）的表
	TablesKept    int    // 索引里有、本次没动的表（CSV 里没有或被 -tables 排除）
	TablesPruned  int    // 本次删除的表（-prune；不删时恒为 0）
	Written       int    // 本次真正写出的表文件数（内容一致或 dry-run 时为 0）
	Source        string // 记进 tables.json 的来源描述
	Columns       int    // 本次处理的表合计列数
	Problems      []string
}

// Import 执行一次 schema 导入。
//
// 增量语义：
//   - 默认全量：CSV 里出现的表（受 -tables 限制）重算并更新表文件与索引条目；
//     CSV 里没有、旧索引里有的表原样保留，不删。
//   - -only-new：只写**本地没有表文件**的表；已有表文件一个字节都不动
//     （索引里有条目但文件被删掉时会补写回来）。
//   - 内容与现有表文件逐字节一致时不重写该文件（少一次无意义的 mtime 变更，
//     LSP 也就不会被无谓地触发重载），计为"未变"。
//   - 索引损坏直接报错返回，调用方不写盘，原文件保持原样。
func Import(opts ImportOptions) (*ImportResult, error) {
	data := opts.CSVData
	if len(data) == 0 {
		b, err := os.ReadFile(opts.CSVPath)
		if err != nil {
			return nil, fmt.Errorf("读取 CSV: %w", err)
		}
		data = b
	}
	parsed, err := parseCSV(data)
	if err != nil {
		return nil, err
	}
	if opts.Prune && (len(opts.OnlyTables) > 0 || opts.OnlyNew) {
		return nil, fmt.Errorf("-prune 不能与 -tables/-only-new 一起用（会把这次范围之外的表删掉）")
	}

	old, _, err := loadIndex(opts.DataDir)
	if err != nil {
		return nil, err
	}

	only := map[string]bool{}
	for _, t := range opts.OnlyTables {
		only[t] = true
	}
	selected := func(key string) bool {
		return len(only) == 0 || only[key]
	}

	// 先整份保留旧索引，再按 CSV 覆盖；这样"CSV 里没有的表"天然不丢。
	idx := emptyIndex()
	for k, v := range old.Tables {
		idx.Tables[k] = v
	}

	res := &ImportResult{
		DataDir:     opts.DataDir,
		RowsTotal:   parsed.RowsTotal,
		RowsSkipped: parsed.RowsSkipped,
		Problems:    parsed.Problems,
		Source:      opts.Source,
	}
	processedOld := map[string]bool{}
	// 逐表进度：先把这次要处理的表挑出来（i/n 要用它），再逐表解析+写盘。
	toProcess := make([]*tableData, 0, len(parsed.Tables))
	for _, td := range parsed.Tables {
		if selected(td.Key) {
			toProcess = append(toProcess, td)
		}
	}
	written := 0
	for i, td := range toProcess {
		if opts.Progress != nil {
			opts.Progress("解析", td.Name, i+1, len(toProcess))
		}
		_, inIndex := old.Tables[td.Key]
		processedOld[td.Key] = true

		path := tablePath(opts.DataDir, td.Key)
		oldBytes, readErr := os.ReadFile(path)
		fileExists := readErr == nil
		if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("读取 %s: %w", path, readErr)
		}

		// -only-new：本地已有表文件就一律不碰（保护人工修订过的说明）。
		// 只看文件在不在：索引残留一条、文件被删掉的表会补写回来。
		if opts.OnlyNew && fileExists {
			res.TablesSame++
			res.Columns += len(td.Columns)
			continue
		}

		want, err := encodeTableFile(td.Key, td.Columns)
		if err != nil {
			return nil, fmt.Errorf("编码表 %s: %w", td.Key, err)
		}
		identical := fileExists && bytes.Equal(oldBytes, want)
		switch {
		case !inIndex:
			res.TablesNew++
		case identical:
			res.TablesSame++
		default:
			res.TablesChanged++
		}
		res.Columns += len(td.Columns)
		idx.Tables[td.Key] = TableEntry{
			Name:    td.Name,
			Desc:    "", // 表说明 CSV 里没有来源，留空串，不瞎猜
			Columns: len(td.Columns),
			File:    tableFileName(td.Key),
		}
		if !identical && !opts.DryRun {
			if err := writeFileAtomic(path, want); err != nil {
				return nil, err
			}
		}
		if !identical {
			written++
		}
	}
	for k, v := range old.Tables {
		if processedOld[k] {
			continue
		}
		// -prune：本地有、这次（全量）没出现的表 → 删表文件 + 索引条目。
		if opts.Prune {
			if opts.Progress != nil {
				opts.Progress("清理", k, res.TablesPruned+1, len(old.Tables)-len(processedOld))
			}
			if !opts.DryRun {
				if err := os.Remove(tablePath(opts.DataDir, k)); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return nil, fmt.Errorf("删除 %s: %w", tablePath(opts.DataDir, k), err)
				}
			}
			delete(idx.Tables, k)
			res.TablesPruned++
			continue
		}
		_ = v
		res.TablesKept++
	}

	res.Written = written
	if opts.DryRun {
		return res, nil
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	idx.Schema = SchemaVersion
	idx.ImportedAt = now.Format(time.RFC3339)
	idx.Source = opts.Source
	if idx.Source == "" {
		idx.Source = filepath.Base(opts.CSVPath)
	}
	idx.RowsTotal = parsed.RowsTotal
	out, err := encodeJSON(idx)
	if err != nil {
		return nil, fmt.Errorf("编码索引: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(opts.DataDir, IndexFileName), out); err != nil {
		return nil, err
	}
	return res, nil
}
