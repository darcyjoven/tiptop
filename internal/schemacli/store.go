// schema 资料的落盘格式与读写。
//
// 目录结构（bdl-lsp 按同一约定读取）：
//
//	<out>/tables.json            索引：schema/importedAt/source/rowsTotal/tables
//	<out>/tables/<大写表名>.json  每张表的列清单
//
// 写盘一律先写临时文件再 rename：LSP 可能同时在读，半份 JSON 会让它读到
// 残缺资料（与 bdl-lsp/link 的 Save 同一做法）。
package schemacli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// SchemaVersion 是 tables.json / tables/*.json 的 schema 版本。
	SchemaVersion = 1
	// IndexFileName 是索引文件名。
	IndexFileName = "tables.json"
	// TablesSubDir 是存放每张表文件的子目录。
	TablesSubDir = "tables"
	// DefaultDataDir 是默认资料根目录（相对项目根，与 link 资料同级）。
	DefaultDataDir = ".project"
	// DefaultSchemaDir 是默认 schema 资料目录（相对 .project）。
	DefaultSchemaDir = ".schema"
)

// Index 是 tables.json 的结构。
//
// Tables 的键是**大写表名**；TableEntry.Name 保留 CSV 里的原始写法。
type Index struct {
	Schema     int                   `json:"schema"`
	ImportedAt string                `json:"importedAt"`
	Source     string                `json:"source"`
	RowsTotal  int                   `json:"rowsTotal"`
	Tables     map[string]TableEntry `json:"tables"`
}

// TableEntry 是索引里的一条表记录。
type TableEntry struct {
	Name    string `json:"name"`
	Desc    string `json:"desc"`
	Columns int    `json:"columns"`
	File    string `json:"file"`
}

// TableFile 是 tables/<大写表名>.json 的结构。
type TableFile struct {
	Table   string   `json:"table"`
	Desc    string   `json:"desc"`
	Columns []Column `json:"columns"`
}

// Column 是一列的元数据，字段顺序即 JSON 输出顺序。
type Column struct {
	Name       string `json:"name"`
	Desc       string `json:"desc"`
	Type       string `json:"type"`
	Length     int    `json:"length"`
	CharLength int    `json:"charLength"`
	Precision  int    `json:"precision"`
	Scale      int    `json:"scale"`
	Nullable   bool   `json:"nullable"`
	// DefaultLength 是 CSV 里 default_legth 列的值：实测是"默认长度"（0/2/4/8…），
	// 不是默认值表达式，界面不展示它。
	DefaultLength string `json:"defaultLength"`
}

// tableFileName 是某张表的数据文件名：<大写表名>.json。
func tableFileName(key string) string {
	return key + ".json"
}

// tablePath 返回某张表数据文件在资料目录里的路径。
func tablePath(dir, key string) string {
	return filepath.Join(dir, TablesSubDir, tableFileName(key))
}

// loadIndex 读索引。
//
// 返回 (索引, 是否存在, 错误)。文件不存在不算错误（首次导入）；
// 文件存在但 JSON 坏了、或 schema 版本过高则**报错**，调用方必须原样保留
// 该文件、不能清空重写——坏索引往往是手工编辑或磁盘问题，直接覆盖会
// 把能人工修的资料整份弄丢。
func loadIndex(dir string) (*Index, bool, error) {
	path := filepath.Join(dir, IndexFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return emptyIndex(), false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取 %s: %w", path, err)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, true, fmt.Errorf("%s 不是合法 JSON（原文件保留未动，请人工修复或删除后重导）: %w", path, err)
	}
	if idx.Schema > SchemaVersion {
		return nil, true, fmt.Errorf("%s 的 schema=%d 高于本工具支持的 %d（原文件保留未动）",
			path, idx.Schema, SchemaVersion)
	}
	if idx.Tables == nil {
		idx.Tables = map[string]TableEntry{}
	}
	return &idx, true, nil
}

func emptyIndex() *Index {
	return &Index{Schema: SchemaVersion, Tables: map[string]TableEntry{}}
}

// encodeJSON 生成缩进的 JSON（不转义 HTML，中文说明原样保留）。
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeTableFile 生成单表文件的 JSON。列顺序即写入顺序。
func encodeTableFile(key string, cols []Column) ([]byte, error) {
	tf := TableFile{Table: key, Desc: "", Columns: cols}
	if tf.Columns == nil {
		tf.Columns = []Column{} // 空表也输出 []，不输出 null
	}
	return encodeJSON(tf)
}

// writeFileAtomic 先写临时文件再 rename，避免读者看到半份文件。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	tmp := f.Name()
	defer func() {
		_ = os.Remove(tmp) // 失败路径清理；成功 rename 后该文件已不存在
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("写 %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("同步 %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("重命名到 %s: %w", path, err)
	}
	return nil
}
