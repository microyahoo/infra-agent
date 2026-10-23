package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// commit 平均延迟超过该阈值（毫秒）视为偏高：commit 已进入 journal/落盘路径，
// 偏高通常指向底层磁盘或网络。
const perfCommitLatencyWarnMS = 50

// PerfCountersData 是 perf_counters 的结构化结果。
type PerfCountersData struct {
	OSD int                `json:"osd"`
	Avg map[string]float64 `json:"avg"` // 计数器名 → 平均值（扁平化自各 section）
}

type perfCountersTool struct{ r Runner }

// NewPerfCounters 构造 perf_counters 工具。
func NewPerfCounters(r Runner) Tool { return &perfCountersTool{r: r} }

func (t *perfCountersTool) Name() string { return "perf_counters" }

func (t *perfCountersTool) Description() string {
	return "采集单个 OSD 的 perf counters 平均值（op 读写延迟、commit/apply 延迟、缓存命中等内部计数器）。用于在 slow_ops 之外做更细的性能判断，例如 commit_latency_ms 偏高说明落盘慢。"
}

func (t *perfCountersTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"osd_id":{"type":"integer","description":"OSD 编号，例如 7"}},"required":["osd_id"],"additionalProperties":false}`)
}

// rawPerfDump 是 perf dump 的形态：section → 计数器名 → 统计值。
// 普通计数器只有 value，带延迟统计的计数器才有 avgcount/sum/avg。
type rawPerfDump map[string]map[string]struct {
	Value    *float64 `json:"value"`
	AvgCount float64  `json:"avgcount"`
	Sum      float64  `json:"sum"`
	Avg      float64  `json:"avg"`
}

func (t *perfCountersTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		OSDID *int `json:"osd_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	if in.OSDID == nil || *in.OSDID < 0 {
		return nil, nil, fmt.Errorf("缺少必填参数 osd_id（非负整数）")
	}
	id := *in.OSDID

	node := primaryNode(t.r)
	caps := t.r.Capabilities(ctx, node)
	if !caps.CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI（后端 %s），无法执行 ceph tell perf dump", caps.Backend)
	}

	argv := []string{"ceph", "tell", fmt.Sprintf("osd.%d", id), "perf", "dump"}
	out, err := t.r.Run(ctx, node, argv)
	if err != nil {
		return nil, newRaw(node, argv, out), err
	}
	var dump rawPerfDump
	if jerr := json.Unmarshal([]byte(out.Stdout), &dump); jerr != nil {
		return nil, newRaw(node, argv, out), fmt.Errorf("解析 perf dump 输出失败: %w", jerr)
	}

	// 同一计数器名可能出现在多个 section，后写的覆盖先读的（如 osd 段最权威）。
	d := &PerfCountersData{OSD: id, Avg: map[string]float64{}}
	for _, section := range dump {
		for name, c := range section {
			if c.Value != nil {
				d.Avg[name] = *c.Value
				continue
			}
			d.Avg[name] = c.Avg
		}
	}

	// 摘要只列出关键指标（顺序固定，保证可复现），其余通过 Data 提供给模型。
	var parts []string
	for _, k := range []string{"op_w_latency", "op_r_latency", "commit_latency_ms", "apply_latency_ms"} {
		if v, ok := d.Avg[k]; ok {
			parts = append(parts, fmt.Sprintf("%s=%.4g", k, v))
		}
	}
	res := &Result{
		Data:    d,
		Summary: fmt.Sprintf("osd.%d 共 %d 个 perf counter", id, len(d.Avg)),
	}
	if len(parts) > 0 {
		res.Summary += "：" + strings.Join(parts, "，")
	}

	if v, ok := d.Avg["commit_latency_ms"]; ok && v > perfCommitLatencyWarnMS {
		res.Hints = append(res.Hints,
			fmt.Sprintf("osd.%d 的 commit_latency_ms=%.2f 偏高（>%dms），commit 路径已卡在落盘，可调用 osd_detail(osd_id=%d) 定位节点与磁盘，再调用 host_io 确认", id, v, perfCommitLatencyWarnMS, id))
	}
	if v, ok := d.Avg["op_w_latency"]; ok && v > 1 {
		res.Hints = append(res.Hints, fmt.Sprintf("写 op 平均延迟 %.2fs 偏高，可调用 slow_ops(osd_ids=[%d]) 查看卡在哪个阶段", v, id))
	}
	return res, newRaw(node, argv, out), nil
}
