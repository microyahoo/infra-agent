package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// RenderJSON 把报告序列化为两空格缩进的 JSON，供机器读取与归档。
func RenderJSON(r *Report) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return "", fmt.Errorf("序列化报告失败: %w", err)
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// OverviewRow 是多集群总览中的一行。
type OverviewRow struct {
	ClusterID string
	Health    string
	Severity  string
	OSDs      string
	Capacity  string
	Top       string
}

// 总览表头，顺序与 OverviewRow 字段一致。
var overviewHeader = []string{"集群", "健康", "严重程度", "OSD", "容量", "主要问题"}

// RenderOverview 把多集群结果渲染为固定列宽对齐的文本表格。
func RenderOverview(rows []OverviewRow) string {
	table := make([][]string, 0, len(rows)+1)
	table = append(table, overviewHeader)
	for _, row := range rows {
		top := row.Top
		if top == "" {
			top = "-"
		}
		table = append(table, []string{
			row.ClusterID,
			row.Health,
			severityLabel(row.Severity),
			row.OSDs,
			row.Capacity,
			oneLine(top),
		})
	}

	// 计算每列显示宽度：中文等宽字符占两格。
	widths := make([]int, len(overviewHeader))
	for _, line := range table {
		for i, cell := range line {
			if w := displayWidth(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}

	var b strings.Builder
	for _, line := range table {
		for i, cell := range line {
			b.WriteString(cell)
			if i < len(line)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-displayWidth(cell)+2))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// displayWidth 估算字符串在等宽终端中的显示宽度。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
			(r >= 0x2e80 && r <= 0xa4cf) ||
			(r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) ||
			(r >= 0xfe30 && r <= 0xfe6f) ||
			(r >= 0xff00 && r <= 0xff60) ||
			(r >= 0xffe0 && r <= 0xffe6) ||
			(r >= 0x1f300 && r <= 0x1f64f) ||
			(r >= 0x1f900 && r <= 0x1f9ff) ||
			(r >= 0x20000 && r <= 0x3fffd)) {
			w += 2
		} else if r == utf8.RuneError {
			w++
		} else {
			w++
		}
	}
	return w
}
