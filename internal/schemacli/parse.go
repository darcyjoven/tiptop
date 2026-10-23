// schema 资料的 CSV 解析：把 Oracle 导出的"表字段"CSV 读成内存里的表/列。
//
// 表头实测有两个拼写错误（data_precicison / default_legth），所以这里
// **不按列名下死理**：先把表头单元格归一化（小写、只留字母数字），再按
// 别名匹配；匹配不到的字段回退到默认位置。这样换个拼写、换个列顺序、
// 甚至换成"表名/字段名/说明"这种中文表头都还能导入。
package schemacli

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

// 逻辑字段的编号。注释里的数字是它在实测 CSV 里的默认位置。
const (
	fTableName  = iota // 0 table_name
	fColumnName        // 1 column_name
	fColumnDesc        // 2 column_desc
	fDataType          // 3 data_type
	fDataLength        // 4 data_length
	fCharLength        // 5 char_length
	fPrecision         // 6 data_precicison（拼写错误，原样容忍）
	fScale             // 7 data_scale
	fNullable          // 8 nullable
	fDefault           // 9 default_legth：实测是"默认长度"（0/2/4/8…），不是默认值表达式
	fieldCount
)

// headerAliases 是归一化后的表头名 → 逻辑字段。
//
// 拼写错误的那两个列名（dataprecicison / defaultlegth）显式列出；
// 它们归一化后分别得到 "dataprecicison"、"defaultlegth"。
var headerAliases = map[string]int{
	// 表名
	"tablename": fTableName, "table": fTableName, "tablenm": fTableName, "tab": fTableName,
	"表名": fTableName, "表": fTableName, "资料表": fTableName,
	// 列名
	"columnname": fColumnName, "column": fColumnName, "colname": fColumnName,
	"fieldname": fColumnName, "field": fColumnName,
	"字段名": fColumnName, "字段": fColumnName, "列名": fColumnName, "列": fColumnName, "栏位": fColumnName,
	// 列说明
	"columndesc": fColumnDesc, "columndescription": fColumnDesc, "columncomment": fColumnDesc,
	"colcomment": fColumnDesc, "columnlabel": fColumnDesc, "columndisplay": fColumnDesc,
	"description": fColumnDesc, "comment": fColumnDesc, "desc": fColumnDesc, "label": fColumnDesc,
	"说明": fColumnDesc, "描述": fColumnDesc, "注释": fColumnDesc, "备注": fColumnDesc,
	"字段说明": fColumnDesc, "栏位说明": fColumnDesc, "中文说明": fColumnDesc,
	// 数据类型
	"datatype": fDataType, "type": fDataType, "coltype": fDataType,
	"类型": fDataType, "数据类型": fDataType,
	// 字节长度
	"datalength": fDataLength, "length": fDataLength, "datalen": fDataLength,
	"len": fDataLength, "bytelen": fDataLength, "bytesize": fDataLength,
	"长度": fDataLength, "字节长度": fDataLength,
	// 字符长度
	"charlength": fCharLength, "charlen": fCharLength, "charsize": fCharLength,
	"字符长度": fCharLength,
	// 精度（含实测拼写错误）
	"dataprecision": fPrecision, "dataprecicison": fPrecision, "dataprecicision": fPrecision,
	"datapercision": fPrecision, "precision": fPrecision, "precicison": fPrecision,
	"prec": fPrecision, "precis": fPrecision,
	"精度": fPrecision, "总位数": fPrecision,
	// 小数位
	"datascale": fScale, "scale": fScale, "小数位": fScale, "小数位数": fScale, "标度": fScale,
	// 允许空
	"nullable": fNullable, "isnullable": fNullable, "isnull": fNullable, "null": fNullable,
	"允许空": fNullable, "可空": fNullable, "为空": fNullable, "是否为空": fNullable,
	// 默认（含实测拼写错误）
	"defaultlength": fDefault, "defaultlegth": fDefault, "defaultlen": fDefault,
	"datadefault": fDefault, "defaultvalue": fDefault, "defaultval": fDefault, "default": fDefault,
	"默认值": fDefault, "默认": fDefault, "缺省值": fDefault,
}

// tableData 是一张表在 CSV 里解析出的原始数据。
type tableData struct {
	Key     string // 大写表名（索引键）
	Name    string // CSV 里的原始写法（首见）
	Columns []Column
	seen    map[string]bool // 已收列名（大写），用于同表内去重
}

// parseResult 是一次 CSV 解析的结果。
type parseResult struct {
	Tables      []*tableData // 首次出现顺序
	RowsTotal   int          // 数据行数（不含表头）
	RowsSkipped int          // 跳过的行（解析失败 / 缺表名或列名 / 列数不足）
	Problems    []string     // 跳过原因，逐行一条
}

// stripBOM 去掉 UTF-8 BOM。实测 CSV 可能带 BOM，不处理会让首个表头名
// 变成 "\ufefftable_name"，按别名匹配就全部落空
// （与 bdl-lsp/link/import.go 的 stripBOM 同一处理）。
func stripBOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
}

// parseCSV 解析 CSV 数据。（CRLF 由 encoding/csv 处理，引号包裹的逗号同理）
func parseCSV(data []byte) (*parseResult, error) {
	data = stripBOM(data)
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1 // 列数不一致时不要整体报错，交给下面逐行判断
	r.LazyQuotes = true    // 容忍个别脏引号，别让一行坏数据毁掉整份导入

	hdr, err := r.Read()
	if err == io.EOF {
		return nil, fmt.Errorf("CSV 为空（没有表头）")
	}
	if err != nil {
		return nil, fmt.Errorf("读取表头: %w", err)
	}
	pos := headerPositions(hdr)
	// 表头声明的列数（末尾空的表头单元格不算，容忍导出末尾多一个逗号）。
	// 数据行列数少于它就是"列数不足"，跳过并计数，不中断整份导入。
	required := 0
	for i, cell := range hdr {
		if strings.TrimSpace(cell) != "" {
			required = i + 1
		}
	}

	res := &parseResult{}
	byKey := map[string]*tableData{}
	var consecErr int
	for n := 1; ; n++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			res.RowsSkipped++
			res.Problems = append(res.Problems,
				fmt.Sprintf("第 %d 条数据记录解析失败，已跳过：%v", n, err))
			// csv.Reader 遇到坏引号时不保证前进，连错太多就认为文件坏了。
			consecErr++
			if consecErr > 1000 {
				return nil, fmt.Errorf("第 %d 条数据记录起连续解析失败，疑似 CSV 损坏，已中止", n)
			}
			continue
		}
		consecErr = 0
		res.RowsTotal++

		get := func(f int) string {
			i := pos[f]
			if i < 0 || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		tableRaw, colRaw := get(fTableName), get(fColumnName)
		if len(rec) < required || tableRaw == "" || colRaw == "" {
			res.RowsSkipped++
			res.Problems = append(res.Problems,
				fmt.Sprintf("第 %d 条数据记录列数不足或缺表名/列名（%d 列 < 表头 %d 列），已跳过",
					n, len(rec), required))
			continue
		}

		key := strings.ToUpper(tableRaw)
		td := byKey[key]
		if td == nil {
			td = &tableData{Key: key, Name: tableRaw, seen: map[string]bool{}}
			byKey[key] = td
			res.Tables = append(res.Tables, td)
		}
		colKey := strings.ToUpper(colRaw)
		if td.seen[colKey] {
			continue // 同一表内重复列名只保留第一条
		}
		td.seen[colKey] = true

		dataLen := parseInt(get(fDataLength))
		charLen := parseInt(get(fCharLength))
		length := dataLen
		if length == 0 {
			length = charLen // data_length 为空/为 0/非数字时回退 char_length
		}
		td.Columns = append(td.Columns, Column{
			Name:          colRaw,
			Desc:          get(fColumnDesc),
			Type:          get(fDataType),
			Length:        length,
			CharLength:    charLen,
			Precision:     parseInt(get(fPrecision)),
			Scale:         parseInt(get(fScale)),
			Nullable:      parseBool(get(fNullable)),
			DefaultLength: get(fDefault),
		})
	}
	return res, nil
}

// headerPositions 把表头映射成"逻辑字段 → 列下标"。
//
// 先用别名匹配；某个字段的表头一个都没认出来时，回退到它在该格式里的
// 默认位置（fTableName=0、fColumnName=1……），保证"列名换了但顺序没变"
// 的导出仍能导入。
func headerPositions(hdr []string) [fieldCount]int {
	var pos [fieldCount]int
	var matched [fieldCount]bool
	for f := 0; f < fieldCount; f++ {
		pos[f] = f // 默认位置
	}
	for i, cell := range hdr {
		f, ok := matchHeader(cell)
		if !ok || matched[f] {
			continue // 同名列取最靠左的那个
		}
		pos[f] = i
		matched[f] = true
	}
	return pos
}

// matchHeader 归一化表头名后按别名匹配，匹配不到再走模糊规则。
func matchHeader(cell string) (int, bool) {
	n := normalizeHeader(cell)
	if n == "" {
		return 0, false
	}
	if f, ok := headerAliases[n]; ok {
		return f, true
	}
	return fuzzyField(n)
}

// normalizeHeader 转小写并去掉所有非字母、非数字字符。
// 保留 Unicode 字母/数字，"表名""字段说明"这类中文表头也能用。
func normalizeHeader(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// fuzzyField 是别名表之外的兜底规则：容忍拼写错误与自定义后缀
// （如 data_prec 写成了 data_percision、default_len_x）。
//
// 规则顺序有讲究：先认特征强的（precision/default/scale），再认
// char before length，避免 "char_length" 落进 data_length。
func fuzzyField(n string) (int, bool) {
	switch {
	case strings.Contains(n, "precic"), strings.Contains(n, "precis"),
		strings.Contains(n, "percision"), strings.Contains(n, "精度"):
		return fPrecision, true
	case strings.Contains(n, "default"), strings.Contains(n, "默认"), strings.Contains(n, "缺省"):
		return fDefault, true
	case strings.Contains(n, "nullable"), n == "null", strings.Contains(n, "isnull"),
		strings.Contains(n, "允许空"), strings.Contains(n, "可空"):
		return fNullable, true
	case strings.Contains(n, "scale"), strings.Contains(n, "小数"), strings.Contains(n, "标度"):
		return fScale, true
	case strings.Contains(n, "char") && (strings.Contains(n, "len") || strings.Contains(n, "size")),
		strings.Contains(n, "字符长度"):
		return fCharLength, true
	case strings.Contains(n, "length"), strings.Contains(n, "len"), strings.Contains(n, "长度"):
		return fDataLength, true
	case strings.Contains(n, "type"), strings.Contains(n, "类型"):
		return fDataType, true
	case strings.Contains(n, "table"), n == "表", strings.Contains(n, "表名"):
		return fTableName, true
	case strings.Contains(n, "column"), strings.Contains(n, "field"):
		if strings.Contains(n, "desc") || strings.Contains(n, "comment") ||
			strings.Contains(n, "label") || strings.Contains(n, "说明") || strings.Contains(n, "描述") {
			return fColumnDesc, true
		}
		return fColumnName, true
	case strings.Contains(n, "desc"), strings.Contains(n, "comment"), strings.Contains(n, "label"),
		strings.Contains(n, "说明"), strings.Contains(n, "描述"), strings.Contains(n, "注释"):
		return fColumnDesc, true
	}
	return 0, false
}

// parseInt 容错解析整数：空、非数字都当 0（不报错中断）。
func parseInt(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// parseBool 解析 nullable：Y/N 或 YES/NO，大小写不敏感；认不出来当 false。
func parseBool(s string) bool {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "Y", "YES", "T", "TRUE", "1":
		return true
	default:
		return false
	}
}
