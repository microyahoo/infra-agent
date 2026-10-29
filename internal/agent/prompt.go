package agent

import (
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/ceph/snapshot"
	"github.com/microyahoo/infra-agent/skills"
)

// SystemPrompt 由两部分组成：
//  1. skills/ceph-doctor/SKILL.md —— 与外部 agent 共用的领域知识
//  2. 本次会话的动态事实 —— 集群 ID、健康、快照、证据 ID
//
// 静态知识放在 SKILL.md 里，是为了让内置 agent 与外部 agent
// 读到的内容完全一致；动态部分只能在这里拼。
func SystemPrompt(s snapshot.Snapshot) string {
	return strings.TrimSpace(skills.MustRead("ceph-doctor/SKILL.md")) +
		"\n\n---\n\n# 本次会话\n\n" + clusterSection(s)
}

func clusterSection(s snapshot.Snapshot) string {
	var b strings.Builder

	b.WriteString("下面是会话开始时已经采集到的事实。**除非用户问「现在变了吗」，否则不要重复执行这些查询。**\n\n")
	fmt.Fprintf(&b, "- 集群 ID：%s\n", orDash(s.ClusterID))
	fmt.Fprintf(&b, "- 当前健康：%s\n", orDash(s.Health))
	if s.Summary != "" {
		fmt.Fprintf(&b, "- 概要：%s\n", s.Summary)
	}

	if ph := strings.TrimSpace(s.PodHealth); ph != "" {
		b.WriteString("\n## Pod 状态（会话开始时采集）\n\n")
		b.WriteString(ph)
		b.WriteString("\n")
	}

	if len(s.Findings) > 0 {
		b.WriteString("\n## 已经能看出来的（确定性分析，直接据此回答）\n\n")
		for i, f := range s.Findings {
			fmt.Fprintf(&b, "%d. **%s**\n   - 依据：%s\n   - 下一步：%s\n", i+1, f.Summary, f.Why, f.Next)
		}
		b.WriteString("\n这些是代码从告警里直接算出来的，事实本身不需要再验证。" +
			"你只需要判断它们是否足以回答用户的问题。\n")
	}

	if detail := strings.TrimSpace(s.HealthDetail); detail != "" && s.Health != "HEALTH_OK" {
		b.WriteString("\n## 告警明细（节选）\n\n```\n")
		b.WriteString(truncateLines(detail, 12))
		b.WriteString("\n```\n")
	} else if ev := strings.TrimSpace(s.RecentHealthEvents); ev != "" {
		b.WriteString("\n## 最近的健康变更（会话开始时从日志里查到）\n\n```\n")
		b.WriteString(ev)
		b.WriteString("\n```\n")
	}

	if s.Topology != nil {
		if rows := placementRows(s); len(rows) > 0 {
			b.WriteString("\n## 相关 OSD 的节点与磁盘（已从拓扑查出，不必再调 osd_detail）\n\n")
			b.WriteString("| OSD | 节点 | 磁盘 | 状态 |\n|---|---|---|---|\n")
			for _, r := range rows {
				fmt.Fprintf(&b, "| osd.%d | %s | %s | %s |\n", r.ID, orDash(r.Host), orDash(r.Device), orDash(r.Status))
			}
			b.WriteString("\n")
		}
	}

	if len(s.Evidence) > 0 {
		b.WriteString("\n## 会话开始时已采集（已有证据 ID，可直接引用，不要重复执行）\n\n")
		for _, ev := range s.Evidence {
			status := ""
			if ev.ErrorKind != "" {
				status = "（失败：" + ev.ErrorMsg + "）"
			}
			fmt.Fprintf(&b, "- `%s` → %s%s\n", ev.Tool, ev.ID, status)
		}
		b.WriteString("\n这些命令的结果已经在上下文里。**除非用户问的是「现在变了吗」，否则不要再执行同样的查询。**\n")
	}

	b.WriteString("\n## 环境\n\n")
	fmt.Fprintf(&b, "- 执行后端：%s\n", orDash(s.Capabilities.Backend))
	if s.Capabilities.CephVersion != "" {
		fmt.Fprintf(&b, "- Ceph 版本：%s\n", s.Capabilities.CephVersion)
	}
	if ns := s.Capabilities.RookNamespace; ns != "" {
		fmt.Fprintf(&b, "- Rook 命名空间：%s\n", ns)
	}
	if len(s.Capabilities.LogDirs) > 0 {
		fmt.Fprintf(&b, "- 日志目录：%s\n", strings.Join(s.Capabilities.LogDirs, "、"))
	}
	if s.Topology != nil && len(s.Topology.Nodes) > 0 {
		var names []string
		for _, n := range s.Topology.Nodes {
			names = append(names, n.Name)
		}
		fmt.Fprintf(&b, "- 节点（%d 个）：%s\n", len(names), strings.Join(names, "、"))
	}

	if len(s.Failures) > 0 {
		b.WriteString("\n### 会话开始时未能采集到的信息\n\n")
		for _, f := range s.Failures {
			b.WriteString("- " + f + "\n")
		}
	}
	return b.String()
}

func placementRows(s snapshot.Snapshot) []access.OSDPlacement {
	place := make(map[int]access.OSDPlacement, len(s.Topology.OSDs))
	for _, p := range s.Topology.OSDs {
		place[p.ID] = p
	}
	var rows []access.OSDPlacement
	for _, id := range snapshot.ReferencedOSDs(s, 24) {
		if p, ok := place[id]; ok {
			rows = append(rows, p)
		} else {
			rows = append(rows, access.OSDPlacement{ID: id})
		}
	}
	return rows
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未知"
	}
	return s
}

func truncateLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:n], "\n")
}
