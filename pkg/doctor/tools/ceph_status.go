package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CephStatusData 是 ceph_status 的结构化结果。
type CephStatusData struct {
	FSID           string    `json:"fsid"`
	Health         string    `json:"health"`
	HealthChecks   []string  `json:"health_checks,omitempty"`
	HealthMessages []string  `json:"health_messages,omitempty"`
	NumMons        int       `json:"num_mons"`
	NumOSDs        int       `json:"num_osds"`
	NumOSDsUp      int       `json:"num_osds_up"`
	NumOSDsIn      int       `json:"num_osds_in"`
	NumPGs         int       `json:"num_pgs"`
	DegradedPGs    int       `json:"degraded_pgs"`
	PGState        []PGState `json:"pg_state"`
	BytesTotal     uint64    `json:"bytes_total"`
	BytesUsed      uint64    `json:"bytes_used"`
	UsedPercent    float64   `json:"used_percent"`
}

// PGState 是单个 PG 状态及其数量。
type PGState struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

type cephStatusTool struct{ r Runner }

// NewCephStatus 构造 ceph status 工具。
func NewCephStatus(r Runner) Tool { return &cephStatusTool{r: r} }

func (t *cephStatusTool) Name() string { return "ceph_status" }

func (t *cephStatusTool) Description() string {
	return "获取集群整体状态：健康码、健康检查明细、mon quorum、OSD up/in 数量、PG 状态分布、容量使用率。这是每次诊断的起点。"
}

func (t *cephStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

type rawStatus struct {
	FSID   string `json:"fsid"`
	Health struct {
		Status string                     `json:"status"`
		Checks map[string]json.RawMessage `json:"checks"`
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
		BytesAvail uint64 `json:"bytes_avail"`
	} `json:"pgmap"`
}

func (t *cephStatusTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	node := primaryNode(t.r)
	caps := t.r.Capabilities(ctx, node)
	if !caps.CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI（后端 %s），无法执行 ceph status；可改用 daemon_log 或 read_log 从日志分析", caps.Backend)
	}

	argv := []string{"ceph", "status", "-f", "json"}
	out, err := t.r.Run(ctx, node, argv)
	if err != nil {
		return nil, nil, err
	}
	var st rawStatus
	if jerr := json.Unmarshal([]byte(out.Stdout), &st); jerr != nil {
		return nil, nil, fmt.Errorf("解析 ceph status 输出失败: %w", jerr)
	}

	d := &CephStatusData{
		FSID:       st.FSID,
		Health:     st.Health.Status,
		NumMons:    st.Monmap.NumMons,
		NumOSDs:    st.OSDMap.NumOSDs,
		NumOSDsUp:  st.OSDMap.NumUpOSDs,
		NumOSDsIn:  st.OSDMap.NumInOSDs,
		NumPGs:     st.PGMap.NumPGs,
		BytesTotal: st.PGMap.BytesTotal,
		BytesUsed:  st.PGMap.BytesUsed,
	}
	// checks 是 map，遍历顺序不确定，排序后保证摘要稳定可复现。
	//
	// 除了代码名，还要取出 summary.message：只给 HEALTH_WARN + 代码名，
	// 模型仍然不知道"慢在哪、多慢"，只能全量排查。
	for name, raw := range st.Health.Checks {
		d.HealthChecks = append(d.HealthChecks, name)
		var c struct {
			Summary struct {
				Message string `json:"message"`
			} `json:"summary"`
		}
		if json.Unmarshal(raw, &c) == nil && c.Summary.Message != "" {
			d.HealthMessages = append(d.HealthMessages, c.Summary.Message)
		}
	}
	sort.Strings(d.HealthChecks)
	sort.Strings(d.HealthMessages)
	for _, s := range st.PGMap.PGsByState {
		d.PGState = append(d.PGState, PGState{State: s.StateName, Count: s.Count})
		if s.StateName != "active+clean" {
			d.DegradedPGs += s.Count
		}
	}
	if d.BytesTotal > 0 {
		d.UsedPercent = float64(d.BytesUsed) / float64(d.BytesTotal) * 100
	}

	raw := newRaw(node, argv, out)

	res := &Result{
		Summary: fmt.Sprintf("%s，mon %d，OSD %d 总/%d up/%d in，PG %d（异常 %d），容量 %s",
			d.Health, d.NumMons, d.NumOSDs, d.NumOSDsUp, d.NumOSDsIn, d.NumPGs, d.DegradedPGs, formatPercent(d.UsedPercent)),
		Data: d,
	}
	if d.Health != "HEALTH_OK" {
		// 把告警原文放进摘要，模型才知道该往哪个方向查。
		if len(d.HealthMessages) > 0 {
			res.Summary += "；告警：" + strings.Join(d.HealthMessages, "；")
			res.Hints = append(res.Hints, "根据上面的告警原文定向排查；如果告警提到具体 OSD/节点，先 osd_detail 再 host_check/host_io")
		} else {
			res.Hints = append(res.Hints, fmt.Sprintf("集群非健康（%s），可调用 ceph_health 查看完整告警明细", strings.Join(d.HealthChecks, ",")))
		}
	}
	if d.NumOSDsUp < d.NumOSDs {
		res.Hints = append(res.Hints, fmt.Sprintf("有 %d 个 OSD down，可调用 osd_tree 定位，再用 osd_detail 查看具体 OSD", d.NumOSDs-d.NumOSDsUp))
	}
	if d.UsedPercent >= 75 {
		res.Hints = append(res.Hints, fmt.Sprintf("容量使用率 %.1f%% 偏高，可调用 pool_info 查看是哪个池占满", d.UsedPercent))
	}
	if d.DegradedPGs > 100 {
		res.Hints = append(res.Hints, "异常 PG 数量较多，优先调用 pg_detail 而非逐个查询")
	}
	return res, raw, nil
}

// formatPercent 按数值大小选择有效位数。
// 大容量集群早期使用率极低（例如 2PB 用 148GB ≈ 0.007%），
// 固定 %.1f 会显示成 0.0%，让读者误以为数据缺失。
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

// primaryNode 返回诊断入口节点。
func primaryNode(r Runner) string {
	nodes := r.Nodes()
	if len(nodes) > 0 {
		return nodes[0]
	}
	return ""
}
