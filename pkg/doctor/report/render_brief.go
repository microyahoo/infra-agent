package report

import (
	"fmt"
	"strings"
)

// RenderBrief 把报告渲染为适合聊天工具或通知的简短摘要（约 7 行）。
// 不包含任何原始输出，只保留结论、影响、首条建议与首个待确认项。
func RenderBrief(r *Report) string {
	var lines []string

	lines = append(lines, fmt.Sprintf("[%s] %s · %s", severityUpper(r.Severity), r.ClusterID, r.Health))
	lines = append(lines, oneLine(r.Headline))

	if r.Impact != "" {
		lines = append(lines, "影响："+oneLine(r.Impact))
	}

	if len(r.Recommendations) > 0 {
		rec := r.Recommendations[0]
		line := fmt.Sprintf("建议：%s", oneLine(rec.Action))
		if rec.Command != "" {
			line += fmt.Sprintf("（`%s`）", oneLine(rec.Command))
		}
		lines = append(lines, line)
	}

	if len(r.Unknowns) > 0 {
		lines = append(lines, "待确认："+oneLine(r.Unknowns[0].What))
	}

	lines = append(lines, fmt.Sprintf("证据 %d 条 · %d 轮 · %s",
		r.Stats.EvidenceCount, r.Stats.Rounds, durationText(r.Duration)))

	return strings.Join(lines, "\n") + "\n"
}
