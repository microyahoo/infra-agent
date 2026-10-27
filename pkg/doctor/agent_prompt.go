package doctor

import (
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// AgentPrompt 构造对话式调查助手的系统提示词。
//
// 与旧版本的区别：不再规定"必须提交结构化结论"，也不要求每轮都产出报告。
// 它只交代环境事实和对话原则，怎么查、查多深由模型按用户的话决定。
func AgentPrompt(s Snapshot) string {
	var b strings.Builder

	b.WriteString(`你是这台 Ceph 集群的运维助手。你通过只读工具访问集群，和用户对话。

## 怎么对话

- 用户在问集群的状况或问题：用工具查，再基于查到的内容回答。不确定就先查，不要猜。
- 用户对你的回答提出质疑、要求更精确：先回看已经查到的结果。**不要重新全面检查一遍**。
  要么基于已有证据给出更具体的结论，要么只补查缺少的那一点，要么直接说明需要什么信息才能更准。
- 用户只是在交流（评价、追问背景、澄清需求）：直接回答，不要调工具。
- 会话的第一条回答先只用上面已采集的信息作答。
  **如果这还不足以给出结论，直接调用 need_more_info 继续调查**，
  系统会自动打开全部工具——不要停下来问用户"要不要查"。

- **"可能是…"不算结论。** 如果你在用"可能 / 大概率 / 看起来 / 怀疑"来描述根因，
  说明还没查实：调用 need_more_info 去验证那一个具体的点（例如该节点是否可达、
  该盘是否报错），确认之后再回答。只有两种情况可以直接作答：
  ① 已有数据明确指向某个确定的事实；② 要继续查必须由用户提供信息（如具体时间点、业务现象）。
- **一次只查一件事**：调用一个工具、看结果、再决定下一个。
  不要一轮把想查的都发出去——那样等于没看结果。
- 一次没查清就照实说，并说清楚下一步需要什么。编造比说不知道糟糕得多。

## 回答要求

- 中文，简短具体。有数字就说数字，有对象就点名（osd.13、node03、/dev/sdb）。
- 不要输出模板化的多级标题报告（结论／影响／建议这类固定结构）。
  用户要的是答案，不是文档。真的需要整理成报告时，用户会明确要求。
- 不要罗列你查了哪些命令。除非用户问"你是怎么查的"。
- 不确定的地方直接说不确定，不要用"可能""建议进一步排查"来充数。

## 硬性约束

- **只读**。任何会改变集群或系统状态的命令都会被拒绝，你也不需要它们。
- 工具返回的 facts 是下一步该看什么的提示，参考它，但不强制。
- 查不到就说查不到：工具报错意味着"没有拿到数据"，不等于"没有问题"。
`)

	b.WriteString("\n## 这台集群\n\n")
	b.WriteString(fmt.Sprintf("- 集群 ID：%s\n", orDash(s.ClusterID)))
	b.WriteString(fmt.Sprintf("- 当前健康：%s\n", orDash(s.Health)))
	if s.Summary != "" {
		b.WriteString(fmt.Sprintf("- 概要：%s\n", s.Summary))
	}

	// Rook 层的事实：daemon 的问题首先体现在 pod 状态上。
	if ph := strings.TrimSpace(s.PodHealth); ph != "" {
		b.WriteString("\n## Pod 状态（会话开始时采集）\n\n")
		b.WriteString(ph)
		b.WriteString("\n")
	}

	// 结论优先：先把代码能确定的关联摆出来。
	if len(s.Findings) > 0 {
		b.WriteString("\n## 已经能看出来的（确定性分析，直接据此回答）\n\n")
		for i, f := range s.Findings {
			b.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, f.Summary))
			b.WriteString(fmt.Sprintf("   - 依据：%s\n", f.Why))
			b.WriteString(fmt.Sprintf("   - 下一步：%s\n", f.Next))
		}
		b.WriteString("\n上面这些是代码从告警里直接算出来的，不需要你再验证事实本身。" +
			"你的任务是：判断它们是否足以回答用户的问题——足够就直接答，" +
			"需要补充证据时只查与结论直接相关的那一两项。\n")
	}

	if detail := strings.TrimSpace(s.HealthDetail); detail != "" && s.Health != "HEALTH_OK" {
		b.WriteString("\n### 告警明细（节选）\n\n```\n")
		b.WriteString(truncateLines(detail, 12))
		b.WriteString("\n```\n")
	} else if ev := strings.TrimSpace(s.RecentHealthEvents); ev != "" {
		b.WriteString("\n### 最近的健康变更（会话开始时从日志里查到）\n\n```\n")
		b.WriteString(ev)
		b.WriteString("\n```\n")
	}

	// 把结论涉及到的 OSD 直接映射到节点与磁盘。
	// 这一步本该由框架做：拓扑已经在手里，让模型逐个 osd_detail 纯属浪费往返。
	if s.Topology != nil {
		ids := referencedOSDs(s, 24)
		if len(ids) > 0 {
			place := map[int]access.OSDPlacement{}
			for _, p := range s.Topology.OSDs {
				place[p.ID] = p
			}
			b.WriteString("\n## 相关 OSD 的节点与磁盘（已从拓扑查出，不必再调 osd_detail）\n\n")
			b.WriteString("| OSD | 节点 | 磁盘 | 状态 |\n|---|---|---|---|\n")
			for _, id := range ids {
				if p, ok := place[id]; ok {
					b.WriteString(fmt.Sprintf("| osd.%d | %s | %s | %s |\n",
						id, orDash(p.Host), orDash(p.Device), orDash(p.Status)))
				} else {
					b.WriteString(fmt.Sprintf("| osd.%d | 未在拓扑中找到 | - | - |\n", id))
				}
			}
			b.WriteString("\n")
		}
	}

	// 列出已采集的数据与对应证据 ID。不写清楚，模型会为了拿 ID 再查一遍。
	if len(s.Evidence) > 0 {
		b.WriteString("\n## 会话开始时已采集（已有证据 ID，可直接引用，不要重复执行）\n\n")
		for _, ev := range s.Evidence {
			status := ""
			if ev.ErrorKind != "" {
				status = "（失败：" + ev.ErrorMsg + "）"
			}
			b.WriteString(fmt.Sprintf("- `%s` → %s%s\n", ev.Tool, ev.ID, status))
		}
		b.WriteString("\n这些命令的结果已经在上下文里。**除非用户问的是「现在变了吗」，否则不要再执行同样的查询。**\n")
	}

	b.WriteString("\n## 环境\n\n")
	b.WriteString(fmt.Sprintf("- 执行后端：%s\n", orDash(s.Capabilities.Backend)))
	if s.Capabilities.CephVersion != "" {
		b.WriteString(fmt.Sprintf("- Ceph 版本：%s\n", s.Capabilities.CephVersion))
	}
	if ns := s.Capabilities.RookNamespace; ns != "" {
		b.WriteString(fmt.Sprintf("- Rook 命名空间：%s\n", ns))
	}
	if len(s.Capabilities.LogDirs) > 0 {
		b.WriteString(fmt.Sprintf("- 日志目录：%s\n", strings.Join(s.Capabilities.LogDirs, "、")))
	}
	if s.Topology != nil && len(s.Topology.Nodes) > 0 {
		var names []string
		for _, n := range s.Topology.Nodes {
			names = append(names, n.Name)
		}
		b.WriteString(fmt.Sprintf("- 节点（%d 个）：%s\n", len(names), strings.Join(names, "、")))
		if len(s.Topology.OSDs) > 0 {
			b.WriteString(fmt.Sprintf("- OSD：%d 个\n", len(s.Topology.OSDs)))
		}
	}
	if len(s.Failures) > 0 {
		b.WriteString("\n### 会话开始时未能采集到的信息\n\n")
		for _, f := range s.Failures {
			b.WriteString("- " + f + "\n")
		}
	}

	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未知"
	}
	return s
}

var _ = access.Capabilities{}
