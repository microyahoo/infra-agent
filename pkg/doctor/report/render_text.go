package report

import (
	"fmt"
	"strings"
	"time"
)

// 严重程度展示标签。
var severityLabels = map[string]string{
	"critical": "Critical",
	"high":     "High",
	"medium":   "Medium",
	"low":      "Low",
	"none":     "无异常",
	"unknown":  "未知",
}

// 建议风险展示标签。
var riskLabels = map[string]string{
	"low":    "低",
	"medium": "中",
	"high":   "高",
}

// severityLabel 返回严重程度的展示标签；未知取值按 unknown 处理。
func severityLabel(s string) string {
	if l, ok := severityLabels[s]; ok {
		return l
	}
	return severityLabels["unknown"]
}

// severityUpper 返回严重程度的大写标识（用于 brief），无结论时为 UNKNOWN。
func severityUpper(s string) string {
	if s == "" {
		s = "unknown"
	}
	return strings.ToUpper(s)
}

// riskLabel 返回建议风险的展示标签；未知取值按中风险处理。
func riskLabel(s string) string {
	if l, ok := riskLabels[s]; ok {
		return l
	}
	return riskLabels["medium"]
}

// durationText 以秒为单位格式化耗时（保留一位小数）。
func durationText(d time.Duration) string {
	secs := d.Seconds()
	if secs == float64(int64(secs)) {
		return fmt.Sprintf("%ds", int64(secs))
	}
	return fmt.Sprintf("%.1fs", secs)
}

// escapeCell 转义 Markdown 表格单元格：竖线加反斜杠，换行与回车替换为空格。
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "|", `\|`)
	return s
}

// oneLine 把多行文本压成单行，用于表格与 brief。
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

// summarizeRaw 在缺少摘要时，从原始输出首行提取一段简述。
func summarizeRaw(raw string) string {
	const maxRunes = 80
	s := oneLine(raw)
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}

// resultCell 生成证据表格中的“结果”单元格内容。
func resultCell(ev EvidenceView) string {
	if ev.ErrorKind != "" || ev.ErrorMsg != "" {
		kind := ev.ErrorKind
		if kind == "" {
			kind = "error"
		}
		return fmt.Sprintf("失败（%s）：%s", kind, ev.ErrorMsg)
	}
	if ev.Summary != "" {
		return ev.Summary
	}
	return summarizeRaw(ev.RawOutput)
}

// RenderText 把报告渲染为完整的 Markdown 文本，供人工审阅。
func RenderText(r *Report) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# 集群 %s · %s · %s\n\n", r.ClusterID, r.Health, r.StartedAt.UTC().Format("2006-01-02 15:04:05"))

	if r.Capabilities != "" {
		fmt.Fprintf(&b, "> 诊断能力后端：%s\n\n", r.Capabilities)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "> ⚠ %s\n", oneLine(n))
	}
	if len(r.Notes) > 0 {
		b.WriteString("\n")
	}

	b.WriteString("## 结论\n")
	fmt.Fprintf(&b, "%s\n\n", r.Headline)

	fmt.Fprintf(&b, "## 严重程度：%s\n", severityLabel(r.Severity))
	fmt.Fprintf(&b, "影响：%s\n\n", r.Impact)

	b.WriteString("## 判断依据\n\n")
	if len(r.Evidence) == 0 {
		b.WriteString("（无采集到证据）\n\n")
	} else {
		b.WriteString("| 证据 | 查了什么 | 结果 |\n")
		b.WriteString("|---|---|---|\n")
		for _, ev := range r.Evidence {
			what := ev.Tool
			if ev.Command != "" {
				what = "`" + escapeCell(oneLine(ev.Command)) + "`"
			}
			if ev.Node != "" {
				what += " @" + escapeCell(ev.Node)
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", escapeCell(ev.ID), what, escapeCell(resultCell(ev)))
		}
		b.WriteString("\n")
	}

	b.WriteString("## 已排除\n")
	if len(r.RuledOut) == 0 {
		b.WriteString("（无）\n\n")
	} else {
		for _, ro := range r.RuledOut {
			if ro.Ref != "" {
				fmt.Fprintf(&b, "- %s [%s]\n", ro.Hypothesis, ro.Ref)
			} else {
				fmt.Fprintf(&b, "- %s\n", ro.Hypothesis)
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## 建议操作（不自动执行）\n")
	if len(r.Recommendations) == 0 {
		b.WriteString("（无）\n\n")
	} else {
		for i, rec := range r.Recommendations {
			fmt.Fprintf(&b, "%d. **[%s风险]** %s\n", i+1, riskLabel(rec.Risk), rec.Action)
			if rec.Command != "" {
				b.WriteString("   ```\n")
				fmt.Fprintf(&b, "   %s\n", rec.Command)
				b.WriteString("   ```\n")
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## 未能确定\n")
	if len(r.Unknowns) == 0 {
		b.WriteString("无\n\n")
	} else {
		for _, u := range r.Unknowns {
			line := fmt.Sprintf("- **%s**：%s", u.What, u.Why)
			if u.How != "" {
				line += fmt.Sprintf("（获取方式：%s）", u.How)
			}
			fmt.Fprintf(&b, "%s\n", line)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "---\n执行了 %d 次工具调用，%d 轮，耗时 %s · 完整命令记录见 --format json\n",
		r.Stats.ToolCalls, r.Stats.Rounds, durationText(r.Duration))

	return b.String()
}
