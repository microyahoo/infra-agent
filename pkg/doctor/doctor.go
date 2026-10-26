// Package doctor 是 Ceph 诊断的编排入口：把集群接入层、工具层与模型循环组装起来，
// 产出一份命令与输出均可溯源的诊断报告。
//
// 一次诊断的流程：
//  1. 对集群做只读快照（能力、拓扑、ceph status），拼装系统提示词；
//  2. 交由调查循环驱动模型调用工具、记录证据链、提交结构化结论；
//  3. 把快照证据与调查证据按顺序合并为一条证据链，并重映射结论中的证据引用；
//  4. 构建报告，按需渲染为文本、摘要或 JSON。
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
	"github.com/microyahoo/infra-agent/pkg/doctor/report"
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// defaultQuestion 是用户未提供问题时使用的默认体检问题。
const defaultQuestion = "请全面检查这个集群的健康状况，指出存在的问题并给出处理建议。"

// clusterIDUnknown 是无法确定集群标识时的占位值。
const clusterIDUnknown = "unknown"

// snapshotHealthUnknown 是快照或拓扑均无法给出健康状态时的占位值。
const snapshotHealthUnknown = "unknown"

// snapshotStatusArgv 是快照阶段获取集群整体状态的命令。
var snapshotStatusArgv = []string{"ceph", "status", "-f", "json"}

// healthDetailArgv 是快照阶段获取健康检查明细的命令。
var healthDetailArgv = []string{"ceph", "health", "detail"}

// Cluster 是 Doctor 对集群的全部依赖，由 access.Cluster 实现。
type Cluster interface {
	Run(ctx context.Context, node string, argv []string) (access.Output, error)
	Capabilities(ctx context.Context, node string) access.Capabilities
	Topology(ctx context.Context) (*access.Topology, error)
	Nodes() []string
	Close() error
}

// Config 是 Doctor 的配置。
type Config struct {
	Cluster      Cluster
	Model        llm.ChatModel
	SourceDir    string // Ceph 源码目录，供源码查找工具使用，可为空
	MaxRounds    int    // 调查最大轮数，0 表示使用 loop 默认值
	MaxToolCalls int    // 工具调用总预算，0 表示使用 loop 默认值
	MaxPerTool   int    // 单个工具的最大调用次数，0 表示使用 loop 默认值
	OnProgress   func(msg string)
}

// Doctor 把一次诊断所需的下游依赖固定下来，可被并发复用。
type Doctor struct {
	cfg Config
}

// New 创建 Doctor。
func New(cfg Config) *Doctor {
	return &Doctor{cfg: cfg}
}

// Task 是一次诊断任务。
type Task struct {
	Question string
}

// Report 是最终诊断报告。
type Report = report.Report

// Format 是报告的渲染格式。
type Format string

const (
	FormatText  Format = "text"
	FormatBrief Format = "brief"
	FormatJSON  Format = "json"
)

// snapshotResult 是一次快照的结果，同时携带要写入证据链的材料。
type snapshotResult struct {
	ClusterID    string
	Health       string
	Summary      string
	Capabilities access.Capabilities
	Topology     *access.Topology
	Failures     []string
	Evidence     []loop.Evidence
}

// degraded 判断快照是否表明能力降级。
func (s snapshotResult) degraded() bool {
	c := s.Capabilities
	return !c.CephCLI || s.Topology == nil || len(s.Failures) > 0
}

// toSnapshot 把快照结果转换为提示词使用的 Snapshot。
func (s snapshotResult) toSnapshot() Snapshot {
	return Snapshot{
		ClusterID:    s.ClusterID,
		Health:       s.Health,
		Summary:      s.Summary,
		Capabilities: s.Capabilities,
		Topology:     s.Topology,
		Failures:     append([]string{}, s.Failures...),
	}
}

// Run 执行一次诊断并返回报告。
func (d *Doctor) Run(ctx context.Context, task Task) (*Report, error) {
	if d.cfg.Cluster == nil {
		return nil, fmt.Errorf("诊断缺少集群连接")
	}
	if d.cfg.Model == nil {
		return nil, fmt.Errorf("诊断缺少模型")
	}

	startedAt := time.Now()

	// 1. 工具层只依赖 Runner，接入层适配为 Runner 后即可复用全部工具。
	runner := &clusterRunner{Cluster: d.cfg.Cluster}
	reg := tools.RegistryFrom(tools.All(runner, d.cfg.SourceDir))

	// 2. 只读快照。快照失败不阻断诊断，只记录失败项交由提示词与报告展示。
	snap := d.snapshot(ctx)

	question := strings.TrimSpace(task.Question)
	if question == "" {
		question = defaultQuestion
	}

	// 3. 调查循环。
	lr := loop.NewRunner(loop.RunnerConfig{
		Model:        d.cfg.Model,
		Registry:     reg,
		SystemPrompt: SystemPrompt(snap.toSnapshot(), question),
		MaxRounds:    d.cfg.MaxRounds,
		MaxToolCalls: d.cfg.MaxToolCalls,
		MaxPerTool:   d.cfg.MaxPerTool,
		OnProgress:   d.cfg.OnProgress,
	})
	res, err := lr.Run(ctx, loop.Task{ClusterID: snap.ClusterID, Question: question})
	return d.buildReport(runner, snap, startedAt, res, err)
}

// buildReport 把一轮调查结果整理成报告：合并快照证据、重映射引用。
func (d *Doctor) buildReport(runner *clusterRunner, snap snapshotResult, startedAt time.Time, res *loop.Result, runErr error) (*report.Report, error) {
	var chain *loop.Chain
	var conclusion *loop.Conclusion
	if res != nil && res.Chain != nil {
		var remap map[string]string
		chain, remap = mergeChains(snap.Evidence, res.Chain)
		conclusion = res.Conclusion
		if conclusion != nil {
			conclusion = remapConclusion(conclusion, remap)
		}
	}

	if runErr != nil {
		if chain == nil || chain.Len() == 0 {
			return nil, runErr
		}
		return report.Build(report.Input{
			ClusterID:    snapshotClusterID(snap),
			Health:       snap.Health,
			Capabilities: snap.Capabilities.Backend,
			StartedAt:    startedAt,
			Duration:     time.Since(startedAt),
			Chain:        chain,
			Notes:        d.notes(snap, runErr),
		})
	}

	rounds, toolCalls := 0, 0
	if res != nil {
		rounds, toolCalls = res.Rounds, res.ToolCalls
	}
	return report.Build(report.Input{
		ClusterID:    snapshotClusterID(snap),
		Health:       snapshotHealth(snap),
		Capabilities: snap.Capabilities.Backend,
		StartedAt:    startedAt,
		Duration:     time.Since(startedAt),
		Chain:        chain,
		Conclusion:   conclusion,
		Rounds:       rounds,
		ToolCalls:    toolCalls,
		Notes:        d.notes(snap, nil),
	})
}

var (
	errNoCluster = fmt.Errorf("诊断缺少集群连接")
	errNoModel   = fmt.Errorf("诊断缺少模型")
)

// initialEvidenceRound 是快照证据的轮次：负数表示发生在调查开始之前。
// （证据链的 Add 在轮次为 0 时会填当前轮次，因此不能用 0。）
const initialEvidenceRound = -1

// snapshot 对集群做只读快照，并把每条成功的命令记录为证据材料。
//
// 快照失败一律转为失败说明，不返回错误：诊断的价值恰恰在于能给出「哪一层出了什么问题」。
func (d *Doctor) snapshot(ctx context.Context) snapshotResult {
	var s snapshotResult

	s.Capabilities = d.cfg.Cluster.Capabilities(ctx, d.primaryNode())

	// 拓扑失败时用 fsid 命令兜底，尽量拿到集群标识。
	topo, err := d.cfg.Cluster.Topology(ctx)
	if err != nil {
		s.Failures = append(s.Failures, fmt.Sprintf("拓扑发现失败：%v", err))
	} else {
		s.Topology = topo
		s.ClusterID = topo.FSID
	}

	if !s.Capabilities.CephCLI {
		s.Health = snapshotHealthUnknown
		s.Summary = fmt.Sprintf("当前环境没有可用的 ceph CLI（后端 %s），无法获取集群操作视图，只能基于日志与主机信息诊断", backendName(s.Capabilities.Backend))
		if len(s.Capabilities.LogDirs) == 0 {
			s.Failures = append(s.Failures, "ceph CLI 不可用，且未发现任何日志目录，可用数据源非常有限")
		}
		return s
	}

	node := d.primaryNode()
	s.Evidence = append(s.Evidence, d.snapshotStatus(ctx, node, &s)...)
	return s
}

// snapshotStatus 执行 ceph status 并提取健康状态与 FSID，同时记录证据。
func (d *Doctor) snapshotStatus(ctx context.Context, node string, s *snapshotResult) []loop.Evidence {
	start := time.Now()
	out, err := d.cfg.Cluster.Run(ctx, node, snapshotStatusArgv)
	dur := time.Since(start)
	if out.Duration > 0 {
		dur = out.Duration
	}

	ev := loop.Evidence{
		Round:     initialEvidenceRound,
		Tool:      "snapshot:ceph_status",
		Node:      node,
		Command:   access.BuildCommand(snapshotStatusArgv),
		StartedAt: start,
		Duration:  dur,
		RawOutput: firstNonEmpty(out.Stdout, out.Stderr),
		ExitCode:  out.ExitCode,
	}
	if err != nil {
		s.Health = snapshotHealthUnknown
		s.Summary = "快照阶段执行 ceph status 失败，集群健康状态未知"
		s.Failures = append(s.Failures, fmt.Sprintf("ceph status 执行失败：%v", err))
		ev.ErrorKind, ev.ErrorMsg = errorKindMessage(err)
		return []loop.Evidence{ev}
	}

	st, perr := parseSnapshotStatus(out.Stdout)
	if perr != nil {
		s.Health = snapshotHealthUnknown
		s.Summary = "ceph status 输出无法解析，集群健康状态未知"
		s.Failures = append(s.Failures, fmt.Sprintf("解析 ceph status 输出失败：%v", perr))
		ev.ErrorKind = string(access.KindFailed)
		ev.ErrorMsg = perr.Error()
		return []loop.Evidence{ev}
	}

	s.Health = st.health
	if st.fsid != "" {
		s.ClusterID = st.fsid
	}
	s.Summary = st.summary
	ev.Summary = st.summary

	evidence := []loop.Evidence{ev}

	// 非健康状态下顺带取一次健康检查明细，帮助模型确定下钻方向。
	if st.health != "" && st.health != "HEALTH_OK" {
		hstart := time.Now()
		hout, herr := d.cfg.Cluster.Run(ctx, node, healthDetailArgv)
		hdur := time.Since(hstart)
		if hout.Duration > 0 {
			hdur = hout.Duration
		}
		hev := loop.Evidence{
			Round:     initialEvidenceRound,
			Tool:      "snapshot:health_detail",
			Node:      node,
			Command:   access.BuildCommand(healthDetailArgv),
			StartedAt: hstart,
			Duration:  hdur,
			RawOutput: firstNonEmpty(hout.Stdout, hout.Stderr),
			ExitCode:  hout.ExitCode,
			Summary:   truncateLines(hout.Stdout, 3),
		}
		if herr != nil {
			hev.ErrorKind, hev.ErrorMsg = errorKindMessage(herr)
		}
		evidence = append(evidence, hev)
	}

	return evidence
}

// primaryNode 返回诊断入口节点。
func (d *Doctor) primaryNode() string {
	nodes := d.cfg.Cluster.Nodes()
	if len(nodes) > 0 {
		return nodes[0]
	}
	return ""
}

// notes 汇总快照失败与能力降级说明。
func (d *Doctor) notes(s snapshotResult, runErr error) []string {
	var notes []string
	// 连接层的安全放宽项（例如未校验主机键）必须让读者看到。
	if sn, ok := d.cfg.Cluster.(interface{ SecurityNotes() []string }); ok {
		notes = append(notes, sn.SecurityNotes()...)
	}
	if len(s.Failures) > 0 {
		notes = append(notes, fmt.Sprintf("快照阶段有 %d 项失败：%s", len(s.Failures), strings.Join(s.Failures, "；")))
	}
	if s.degraded() {
		notes = append(notes, fmt.Sprintf("诊断在降级模式下进行：后端 %s，拓扑%s，结论覆盖范围可能受限",
			backendName(s.Capabilities.Backend), topoState(s.Topology != nil)))
	}
	if runErr != nil {
		notes = append(notes, fmt.Sprintf("调查未正常结束：%v；以上仅展示已收集的证据", runErr))
	}
	return notes
}

// snapshotClusterID 返回报告中使用的集群标识。
func snapshotClusterID(s snapshotResult) string {
	if id := strings.TrimSpace(s.ClusterID); id != "" {
		return id
	}
	return clusterIDUnknown
}

// snapshotHealth 返回报告中使用的健康状态。
func snapshotHealth(s snapshotResult) string {
	if h := strings.TrimSpace(s.Health); h != "" {
		return h
	}
	return snapshotHealthUnknown
}

// statusView 是从 ceph status 输出中提取的快照要点。
type statusView struct {
	fsid    string
	health  string
	summary string
}

// rawSnapshotStatus 是 ceph status -f json 中快照关心的字段。
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

// parseSnapshotStatus 解析 ceph status 输出，提取健康状态与一句话概要。
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
		summary: fmt.Sprintf("mon %d，OSD %d 总/%d up/%d in，PG %d（异常 %d），容量 %.1f%%",
			rs.Monmap.NumMons, rs.OSDMap.NumOSDs, rs.OSDMap.NumUpOSDs, rs.OSDMap.NumInOSDs,
			rs.PGMap.NumPGs, degraded, used),
	}, nil
}

// errorKindMessage 把错误归类为证据中的 ErrorKind 与 ErrorMsg。
func errorKindMessage(err error) (kind, msg string) {
	msg = err.Error()
	if te, ok := err.(*access.ToolError); ok {
		return string(te.Kind), te.Message
	}
	if k := strings.ToLower(msg); strings.Contains(k, "deadline") || strings.Contains(k, "timeout") || strings.Contains(k, "超时") {
		return string(access.KindTimeout), msg
	}
	return string(access.KindFailed), msg
}

// mergeChains 把快照证据放在最前，再接上调查证据，返回合并后的证据链与新旧 ID 映射。
//
// 证据链的 ID 由 Add 按插入顺序分配，合并后调查证据的 ID 会后移，
// 因此必须返回 old→new 映射，供结论中的 evidence_refs 与 ruled_out.ref 重写。
func mergeChains(snapshotEv []loop.Evidence, investigation *loop.Chain) (*loop.Chain, map[string]string) {
	merged := loop.NewChain()
	for _, e := range snapshotEv {
		// 轮次用负数标记「发生在调查开始之前」，与调查轮次（从 1 开始）区分开。
		e.Round = initialEvidenceRound
		merged.Add(e)
	}

	remap := make(map[string]string)
	if investigation != nil {
		for _, e := range investigation.All() {
			oldID := e.ID
			if e.Round <= 0 {
				// 0 会被 Add 视为「使用当前轮次」，显式改为第 1 轮，保持语义一致。
				e.Round = 1
			}
			remap[oldID] = merged.Add(e).ID
		}
	}
	return merged, remap
}

// remapConclusion 按映射改写结论中的证据引用。
func remapConclusion(c *loop.Conclusion, remap map[string]string) *loop.Conclusion {
	return &loop.Conclusion{
		Severity:        c.Severity,
		Headline:        c.Headline,
		Impact:          c.Impact,
		EvidenceRefs:    remapRefs(c.EvidenceRefs, remap),
		RuledOut:        remapRuledOut(c.RuledOut, remap),
		Recommendations: c.Recommendations,
		Unknowns:        c.Unknowns,
	}
}

// remapRefs 改写一组证据引用；映射中找不到的 ID 原样保留，由报告构建阶段报错。
func remapRefs(refs []string, remap map[string]string) []string {
	if refs == nil {
		return nil
	}
	out := make([]string, len(refs))
	for i, id := range refs {
		if nid, ok := remap[id]; ok {
			out[i] = nid
		} else {
			out[i] = id
		}
	}
	return out
}

// remapRuledOut 改写已排除假设的证据引用。
func remapRuledOut(items []loop.RuledOut, remap map[string]string) []loop.RuledOut {
	if items == nil {
		return nil
	}
	out := make([]loop.RuledOut, len(items))
	for i, ro := range items {
		out[i] = ro
		if nid, ok := remap[ro.Ref]; ok {
			out[i].Ref = nid
		}
	}
	return out
}

// backendName 返回后端名称，空值显示为 unknown。
func backendName(b string) string {
	if b = strings.TrimSpace(b); b != "" {
		return b
	}
	return "unknown"
}

// topoState 把拓扑可用性转为中文描述。
func topoState(ok bool) string {
	if ok {
		return "已获取"
	}
	return "未获取"
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// truncateLines 取文本前 n 个非空行，用于证据摘要。
func truncateLines(s string, n int) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			lines = append(lines, l)
		}
		if len(lines) >= n {
			break
		}
	}
	return strings.Join(lines, "；")
}

// validFormats 返回全部合法格式的展示文本。
func validFormats() string {
	names := []string{string(FormatText), string(FormatBrief), string(FormatJSON)}
	return strings.Join(names, ", ")
}

// Render 按指定格式渲染报告；空格式等价于 FormatText。
func Render(r *Report, f Format) (string, error) {
	if r == nil {
		return "", fmt.Errorf("报告为空")
	}
	switch f {
	case "", FormatText:
		return report.RenderText(r), nil
	case FormatBrief:
		return report.RenderBrief(r), nil
	case FormatJSON:
		return report.RenderJSON(r)
	default:
		return "", fmt.Errorf("未知的报告格式 %q，可选：%s", string(f), validFormats())
	}
}

// clusterRunner 把接入层 Cluster 适配为工具层所需的 Runner。
type clusterRunner struct {
	Cluster
}

// 编译期确认适配器满足工具层接口。
var _ tools.Runner = (*clusterRunner)(nil)
