package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 单次最多采集的 OSD 数量，避免大集群下把上下文与时间全耗在逐 OSD 查询上。
const slowOpsMaxOSDs = 64

// 返回给模型的最慢 op 条数。
const slowOpsTopN = 10

// SlowOp 单条慢操作及其阶段时间线。
type SlowOp struct {
	OSD         int      `json:"osd"`
	Description string   `json:"description"`
	Duration    float64  `json:"duration_sec"`
	Events      []string `json:"events,omitempty"`
}

// SlowOpsData 是 slow_ops 的结构化结果。
type SlowOpsData struct {
	TotalOps int         `json:"total_ops"`
	PerOSD   map[int]int `json:"per_osd"` // 每个被查询 OSD 的 num_ops（含 0）
	TopOps   []SlowOp    `json:"top_ops"` // 按耗时降序，最多 10 条
}

type slowOpsTool struct{ r Runner }

// NewSlowOps 构造 slow_ops 工具。
func NewSlowOps(r Runner) Tool { return &slowOpsTool{r: r} }

func (t *slowOpsTool) Name() string { return "slow_ops" }

func (t *slowOpsTool) Description() string {
	return "采集各 OSD 的 historic ops（慢操作），给出耗时最长的操作及其内部阶段时间线。waiting_for_ondisk 表示 op 已完成但卡在磁盘 fsync，通常意味着磁盘慢。用于性能问题定位，是判断「是否磁盘慢」的首选信号。"
}

func (t *slowOpsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"osd_ids":{"type":"array","items":{"type":"integer"},"description":"可选，只查这些 OSD；默认查全部"}},"additionalProperties":false}`)
}

type rawHistoricOps struct {
	NumOps int `json:"num_ops"`
	Ops    []struct {
		Description string  `json:"description"`
		Age         float64 `json:"age"`
		Events      []struct {
			Event string `json:"event"`
			Time  string `json:"time"`
		} `json:"events"`
	} `json:"ops"`
}

func (t *slowOpsTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		OSDIDs []int `json:"osd_ids"`
	}
	// 参数全部可选：没有参数（nil/空）时按默认范围查询全部 OSD；
	// 但只要给了内容，就必须是合法 JSON，避免参数写错却静默退回全量扫描。
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, nil, fmt.Errorf("参数解析失败: %w", err)
		}
	}

	node := primaryNode(t.r)
	caps := t.r.Capabilities(ctx, node)
	if !caps.CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI（后端 %s），无法执行 ceph tell dump_historic_ops；可改用 daemon_log 从日志分析", caps.Backend)
	}

	ids := in.OSDIDs
	if len(ids) == 0 {
		topo, err := t.r.Topology(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("获取拓扑以确定 OSD 列表失败: %w", err)
		}
		if topo != nil {
			for _, o := range topo.OSDs {
				ids = append(ids, o.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil, fmt.Errorf("没有可查询的 OSD（拓扑为空），可显式传入 osd_ids")
	}
	if len(ids) > slowOpsMaxOSDs {
		ids = ids[:slowOpsMaxOSDs]
	}

	d := &SlowOpsData{PerOSD: map[int]int{}}
	var lastRaw *Raw
	var firstErr error
	queried := 0
	for _, id := range ids {
		argv := []string{"ceph", "tell", fmt.Sprintf("osd.%d", id), "dump_historic_ops"}
		out, err := t.r.Run(ctx, node, argv)
		if err != nil {
			// 单个 OSD 超时/无响应不应让整次采集失败，记下首个错误，继续下一个。
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		queried++
		lastRaw = newRaw(node, argv, out)

		var h rawHistoricOps
		if json.Unmarshal([]byte(out.Stdout), &h) != nil {
			// 输出不可解析（可能是被截断）时仍计为一次成功查询，但不贡献 op。
			continue
		}
		d.PerOSD[id] = h.NumOps
		d.TotalOps += h.NumOps
		for _, op := range h.Ops {
			so := SlowOp{OSD: id, Description: op.Description, Duration: op.Age}
			for _, e := range op.Events {
				so.Events = append(so.Events, e.Event)
			}
			d.TopOps = append(d.TopOps, so)
		}
	}
	// 只有全部 OSD 都失败才报错；此时把 OSD 无响应的原始语义交给上层。
	if queried == 0 && firstErr != nil {
		return nil, lastRaw, firstErr
	}

	sort.SliceStable(d.TopOps, func(i, j int) bool { return d.TopOps[i].Duration > d.TopOps[j].Duration })
	if len(d.TopOps) > slowOpsTopN {
		d.TopOps = d.TopOps[:slowOpsTopN]
	}

	res := &Result{
		Data:    d,
		Summary: fmt.Sprintf("检查 %d 个 OSD，共 %d 个 slow op", queried, d.TotalOps),
	}
	if d.TotalOps > 0 {
		res.Summary += "；分布：" + slowOpsDistribution(d.PerOSD)
	}
	if len(d.TopOps) == 0 {
		return res, lastRaw, nil
	}
	top := d.TopOps[0]
	res.Summary += fmt.Sprintf("；最长 %.1fs（osd.%d）", top.Duration, top.OSD)

	// waiting_for_ondisk：op 逻辑上已完成，卡在磁盘 fsync，
	// 这是「磁盘慢」最直接的证据，必须把模型导向宿主机 IO 检查。
	if containsEvent(top.Events, "waiting_for_ondisk") {
		res.Hints = append(res.Hints,
			fmt.Sprintf("最慢的 op 卡在 waiting_for_ondisk（等待磁盘 fsync），说明瓶颈在磁盘而非 CPU/网络：先调用 osd_detail(osd_id=%d) 获取其节点与磁盘，再调用 host_io 检查该盘", top.OSD),
			"若多个 OSD 同时出现 waiting_for_ondisk，可能是共享的后端存储（如 Ceph 之上的 SAN）变慢，可对首个 OSD 的节点调用 host_check")
	} else {
		res.Hints = append(res.Hints,
			fmt.Sprintf("最慢的 op 未卡在磁盘阶段，可调用 osd_detail(osd_id=%d) 查看其状态，并用 perf_counters(osd_id=%d) 查看 op 读写延迟", top.OSD, top.OSD),
			"若多个 OSD 同时变慢，可调用 host_check 检查对应节点的整体负载")
	}
	return res, lastRaw, nil
}

// slowOpsDistribution 把 per-OSD 计数拼成 "osd.7(2)、osd.8(1)"，按 OSD 编号升序保证摘要稳定。
func slowOpsDistribution(perOSD map[int]int) string {
	ids := make([]int, 0, len(perOSD))
	for id, n := range perOSD {
		if n > 0 {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("osd.%d(%d)", id, perOSD[id]))
	}
	if len(parts) == 0 {
		return "无"
	}
	return strings.Join(parts, "、")
}

// containsEvent 判断阶段时间线里是否出现指定事件。
func containsEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}
