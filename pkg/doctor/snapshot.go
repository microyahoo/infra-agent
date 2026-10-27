package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
)

// collectStatusArgv 是会话开始时读取集群状态的命令。
var collectStatusArgv = []string{"ceph", "status", "-f", "json"}

// collectHealthArgv 读取健康告警明细。
var collectHealthArgv = []string{"ceph", "health", "detail"}

// collectRecentLogArgv 读取最近的集群日志，用于找间歇性告警。
var collectRecentLogArgv = []string{"ceph", "log", "last", "500"}

// maxHealthDetailLines 限制告警明细进入提示词的行数。
const maxHealthDetailLines = 40

// Collector 提供快照采集所需的集群访问能力。
type Collector interface {
	Run(ctx context.Context, node string, argv []string) (access.Output, error)
	Capabilities(ctx context.Context, node string) access.Capabilities
	Topology(ctx context.Context) (*access.Topology, error)
	Nodes() []string
}

// CollectSnapshot 采集会话开始时需要的全部背景信息。
//
// 只做一次，之后由 Agent 复用。采集失败不阻断：缺什么写进 Failures，
// 交给模型在对话中自行补查。
func CollectSnapshot(ctx context.Context, c *access.Cluster, _ string) (Snapshot, error) {
	return CollectSnapshotFrom(ctx, snapshotCollector{c})
}

// CollectSnapshotFrom 是 CollectSnapshot 的通用版本，便于测试。
func CollectSnapshotFrom(ctx context.Context, c Collector) (Snapshot, error) {
	var s Snapshot

	nodes := c.Nodes()
	if len(nodes) == 0 {
		return s, fmt.Errorf("未能确定任何节点")
	}
	entry := nodes[0]

	s.Capabilities = c.Capabilities(ctx, entry)

	topo, terr := c.Topology(ctx)
	if terr != nil {
		s.Failures = append(s.Failures, fmt.Sprintf("拓扑发现失败：%v", terr))
	} else if topo != nil {
		s.Topology = topo
		s.ClusterID = topo.FSID
	}

	if !s.Capabilities.CephCLI {
		s.Health = "unknown"
		s.Summary = fmt.Sprintf("没有可用的 ceph CLI（后端 %s），只能读日志与主机信息", s.Capabilities.Backend)
		return s, nil
	}

	out, err := c.Run(ctx, entry, collectStatusArgv)
	s.Evidence = append(s.Evidence, evidenceFrom(c, entry, collectStatusArgv, out, err, "snapshot:ceph_status"))
	if err != nil {
		s.Health = "unknown"
		s.Failures = append(s.Failures, fmt.Sprintf("ceph status 失败：%v", err))
		assignEvidenceIDs(&s)
		return s, nil
	}
	st, perr := parseSnapshotStatus(out.Stdout)
	if perr != nil {
		s.Health = "unknown"
		s.Failures = append(s.Failures, fmt.Sprintf("解析 ceph status 失败：%v", perr))
		assignEvidenceIDs(&s)
		return s, nil
	}
	s.Health = st.health
	s.Summary = st.summary
	if st.fsid != "" {
		s.ClusterID = st.fsid
	}

	// 始终取一次健康明细：慢心跳这类告警是间歇性的，采样瞬间可能已经恢复。
	hout, herr := c.Run(ctx, entry, collectHealthArgv)
	s.Evidence = append(s.Evidence, evidenceFrom(c, entry, collectHealthArgv, hout, herr, "snapshot:ceph_health"))
	if herr == nil {
		// 保留完整明细供关联分析；展示时再截断（见 AgentPrompt）。
		if s.Health != "HEALTH_OK" {
			s.HealthDetail = strings.TrimSpace(hout.Stdout)
		}
	} else {
		s.Failures = append(s.Failures, fmt.Sprintf("ceph health detail 失败：%v", herr))
	}

	// 当前健康但用户可能报告过异常：从最近日志里找已经恢复的告警。
	if s.Health == "HEALTH_OK" {
		if hist := recentHealthEvents(ctx, c, entry); hist != "" {
			s.RecentHealthEvents = hist
		}
	}

	s.Findings = correlate(s)
	assignEvidenceIDs(&s)
	return s, nil
}

// assignEvidenceIDs 给采集阶段的证据编号。
// 必须在提示词生成之前完成，否则模型看到的编号与实际链里的对不上。
func assignEvidenceIDs(s *Snapshot) {
	for i := range s.Evidence {
		if s.Evidence[i].ID == "" {
			s.Evidence[i].ID = fmt.Sprintf("ev-%03d", i+1)
		}
	}
}

// evidenceFrom 把一次采集结果包装成证据。
func evidenceFrom(c Collector, node string, argv []string, out access.Output, err error, tool string) loop.Evidence {
	ev := loop.Evidence{
		Tool:      tool,
		Node:      node,
		Command:   access.BuildCommand(argv),
		RawOutput: out.Stdout,
		Duration:  out.Duration,
		ExitCode:  out.ExitCode,
	}
	if ev.RawOutput == "" {
		ev.RawOutput = out.Stderr
	}
	ev.Summary = summarizeOutput(out.Stdout)
	if err != nil {
		ev.ErrorKind = "failed"
		ev.ErrorMsg = err.Error()
	}
	return ev
}

// summarizeOutput 取输出的前若干行作为摘要。
func summarizeOutput(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "（无输出）"
	}
	return truncateLines(s, 6)
}

// recentHealthEvents 从最近的集群日志里筛出健康变更行。
func recentHealthEvents(ctx context.Context, c Collector, node string) string {
	out, err := c.Run(ctx, node, collectRecentLogArgv)
	if err != nil {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(out.Stdout, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "health") || strings.Contains(low, "slow heartbeat") ||
			strings.Contains(low, "osd_slow") || strings.Contains(low, "slow ops") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	const max = 10
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return strings.Join(lines, "\n")
}

// snapshotCollector 把 access.Cluster 适配为 Collector。
type snapshotCollector struct{ c *access.Cluster }

func (r snapshotCollector) Run(ctx context.Context, node string, argv []string) (access.Output, error) {
	return r.c.Run(ctx, node, argv)
}
func (r snapshotCollector) Capabilities(ctx context.Context, node string) access.Capabilities {
	return r.c.Capabilities(ctx, node)
}
func (r snapshotCollector) Topology(ctx context.Context) (*access.Topology, error) {
	return r.c.Topology(ctx)
}
func (r snapshotCollector) Nodes() []string { return r.c.Nodes() }

// Snapshot 是会话开始时采集的集群背景信息。
type Snapshot struct {
	ClusterID string
	Health    string
	Summary   string
	// HealthDetail 是 ceph health detail 原文（非健康状态下采集）。
	// 完整保留：关联分析需要看到全部条目，截断只能发生在展示时。
	HealthDetail string
	// RecentHealthEvents 是最近日志里的健康变更（健康但用户报告异常时使用）。
	RecentHealthEvents string
	Capabilities       access.Capabilities
	Topology           *access.Topology
	Failures           []string

	// Findings 是从告警里直接推出来的确定结论（见 correlate.go）。
	// 放在提示词最前面，模型才有明确的起点，而不是从零开始扫工具。
	Findings []Finding

	// Evidence 是采集阶段产生的证据。
	// 交给 Agent 预置进证据链，模型就能直接引用这些数据，
	// 而不必为了拿到 evidence_id 再执行一遍同样的命令。
	Evidence []loop.Evidence
}

// statusView 是从 ceph status 提取的关键字段。
type statusView struct {
	fsid    string
	health  string
	summary string
}

type rawSnapshotStatus struct {
	FSID   string `json:"fsid"`
	Health struct {
		Status string `json:"status"`
	} `json:"health"`
	Monmap struct {
		NumMons int `json:"num_mons"`
	} `json:"monmap"`
	OSDMap struct {
		NumOSDs   int `json:"num_osds"`
		NumUpOSDs int `json:"num_up_osds"`
		NumInOSDs int `json:"num_in_osds"`
	} `json:"osdmap"`
	PGMap struct {
		NumPGs     int `json:"num_pgs"`
		PGsByState []struct {
			StateName string `json:"state_name"`
			Count     int    `json:"count"`
		} `json:"pgs_by_state"`
		BytesTotal uint64 `json:"bytes_total"`
		BytesUsed  uint64 `json:"bytes_used"`
	} `json:"pgmap"`
}

// parseSnapshotStatus 从 ceph status JSON 提取健康状态与概要。
func parseSnapshotStatus(raw string) (statusView, error) {
	var rs rawSnapshotStatus
	if err := json.Unmarshal([]byte(raw), &rs); err != nil {
		return statusView{}, err
	}
	degraded := 0
	for _, s := range rs.PGMap.PGsByState {
		if s.StateName != "active+clean" {
			degraded += s.Count
		}
	}
	var used float64
	if rs.PGMap.BytesTotal > 0 {
		used = float64(rs.PGMap.BytesUsed) / float64(rs.PGMap.BytesTotal) * 100
	}
	return statusView{
		fsid:   strings.TrimSpace(rs.FSID),
		health: strings.TrimSpace(rs.Health.Status),
		summary: fmt.Sprintf("mon %d，OSD %d 总/%d up/%d in，PG %d（异常 %d），容量 %s",
			rs.Monmap.NumMons, rs.OSDMap.NumOSDs, rs.OSDMap.NumUpOSDs, rs.OSDMap.NumInOSDs,
			rs.PGMap.NumPGs, degraded, formatPercent(used)),
	}, nil
}

// truncateLines 取前 n 行。
func truncateLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:n], "\n")
}

// formatPercent 按数值大小选择有效位数：大容量集群早期使用率极低，
// 固定 %.1f 会显示成 0.0%，看起来像数据缺失。
func formatPercent(p float64) string {
	switch {
	case p == 0:
		return "0%"
	case p < 0.01:
		return fmt.Sprintf("%.4f%%", p)
	case p < 1:
		return fmt.Sprintf("%.2f%%", p)
	}
	return fmt.Sprintf("%.1f%%", p)
}
