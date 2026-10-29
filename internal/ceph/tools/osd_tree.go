package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// OSDInfo 单个 OSD 的精简视图。
type OSDInfo struct {
	ID          int     `json:"id"`
	Host        string  `json:"host,omitempty"`
	Device      string  `json:"device,omitempty"`
	Status      string  `json:"status"`
	DeviceClass string  `json:"device_class,omitempty"`
	Utilization float64 `json:"utilization"`
	Var         float64 `json:"var"`
	PGs         int     `json:"pgs"`
}

// OSDTreeData 是 osd_tree 的结构化结果。
type OSDTreeData struct {
	OSDs     []OSDInfo `json:"osds"`
	Hotspots []OSDInfo `json:"hotspots,omitempty"`
	DownOSDs []OSDInfo `json:"down_osds,omitempty"`
	MaxUtil  float64   `json:"max_util"`
	AvgUtil  float64   `json:"avg_util"`
}

type osdTreeTool struct{ r Runner }

// NewOSDTree 构造 osd df 工具。
func NewOSDTree(r Runner) Tool { return &osdTreeTool{r: r} }

func (t *osdTreeTool) Name() string { return "osd_tree" }

func (t *osdTreeTool) Description() string {
	return "获取所有 OSD 的状态、使用率、PG 数量，以及每个 OSD 所在的节点与物理盘。用于发现使用率异常偏高的 OSD 和 down 的 OSD。"
}

func (t *osdTreeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

type rawOSDDF struct {
	Nodes []rawOSDDFNode `json:"nodes"`
}

// rawOSDDFNode 兼容不同 Ceph 版本的 ceph osd df 输出。
//
// 实测（Reef/Quincy）：使用率并非总能直接读到 utilization 字段，
// 有时只有 kb / kb_used，需要自行计算。这里两路都收，取值时兜底。
type rawOSDDFNode struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Status      string  `json:"status"`
	DeviceClass string  `json:"device_class"`
	Utilization float64 `json:"utilization"`
	Var         float64 `json:"var"`
	PGs         int     `json:"pgs"`
	KB          float64 `json:"kb"`
	KBUsed      float64 `json:"kb_used"`
}

// utilization 返回该 OSD 的使用率（百分比 0-100）。
//
// 重要：Ceph 的 ceph osd df -f json 里 utilization 字段**已经是百分比**。
// 实测 Pacific 16.2：utilization=0.00531，而 kb_used/kb*100=0.005315%——
// 两者完全一致，说明字段值就是百分数。曾经错误地按"0-1 小数"再乘 100，
// 把 0.005% 显示成 0.5%，进而让模型以为容量紧张。
func (n rawOSDDFNode) utilization() float64 {
	if n.Utilization > 0 || n.KBUsed > 0 {
		return n.Utilization
	}
	return 0
}

func (t *osdTreeTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	node := primaryNode(t.r)
	caps := t.r.Capabilities(ctx, node)
	if !caps.CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI（后端 %s），无法执行 ceph osd df", caps.Backend)
	}

	argv := []string{"ceph", "osd", "df", "-f", "json"}
	out, err := t.r.Run(ctx, node, argv)
	if err != nil {
		return nil, nil, err
	}
	var df rawOSDDF
	if jerr := json.Unmarshal([]byte(out.Stdout), &df); jerr != nil {
		return nil, nil, fmt.Errorf("解析 ceph osd df 输出失败: %w", jerr)
	}

	// 拓扑用于把 OSD 补全为「节点/物理盘」，方便直接调用 host_io。
	topo, _ := t.r.Topology(ctx)
	place := map[int]struct{ host, dev string }{}
	if topo != nil {
		for _, p := range topo.OSDs {
			place[p.ID] = struct{ host, dev string }{p.Host, p.Device}
		}
	}

	d := &OSDTreeData{}
	var sumUtil float64
	for _, n := range df.Nodes {
		if n.Type != "osd" {
			continue
		}
		util := n.utilization()
		info := OSDInfo{
			ID: n.ID, Status: n.Status, DeviceClass: n.DeviceClass,
			Utilization: util, Var: n.Var, PGs: n.PGs,
		}
		if p, ok := place[n.ID]; ok {
			info.Host, info.Device = p.host, p.dev
		}
		d.OSDs = append(d.OSDs, info)
		sumUtil += util
		if n.Status != "up" {
			d.DownOSDs = append(d.DownOSDs, info)
		}
	}
	if len(d.OSDs) > 0 {
		d.AvgUtil = sumUtil / float64(len(d.OSDs))
	}

	// 热点：使用率超过均值 15 个百分点，或超过 85%
	for _, o := range d.OSDs {
		if o.Utilization > d.AvgUtil+15 || o.Utilization >= 85 {
			d.Hotspots = append(d.Hotspots, o)
		}
		if o.Utilization > d.MaxUtil {
			d.MaxUtil = o.Utilization
		}
	}
	sort.Slice(d.Hotspots, func(i, j int) bool { return d.Hotspots[i].Utilization > d.Hotspots[j].Utilization })

	raw := newRaw(node, argv, out)

	res := &Result{
		Data: d,
		Summary: fmt.Sprintf("%d 个 OSD，平均使用率 %.1f%%，最高 %.1f%%（%s）",
			len(d.OSDs), d.AvgUtil, d.MaxUtil, utilLeader(d)),
	}
	if len(d.Hotspots) > 0 {
		var names []string
		for _, h := range d.Hotspots {
			names = append(names, fmt.Sprintf("osd.%d(%.1f%%, %s/%s)", h.ID, h.Utilization, h.Host, h.Device))
		}
		res.Summary += "；异常偏高：" + strings.Join(names, "、")
		res.Hints = append(res.Hints,
			"使用率异常的 OSD 对应盘可能写入变慢，可调用 host_io 查看 iostat/dmesg/smartctl",
			"也可调用 osd_detail 查看该 OSD 的运行状态")
	}
	if len(d.DownOSDs) > 0 {
		var ids []string
		for _, o := range d.DownOSDs {
			ids = append(ids, fmt.Sprintf("osd.%d", o.ID))
		}
		res.Hints = append(res.Hints, "存在 down 的 OSD："+strings.Join(ids, "、")+"，调用 osd_detail 查看其状态")
	}
	return res, raw, nil
}

// utilLeader 返回使用率最高的 OSD 名字，用于摘要。
func utilLeader(d *OSDTreeData) string {
	if len(d.OSDs) == 0 {
		return "无"
	}
	best := d.OSDs[0]
	for _, o := range d.OSDs {
		if o.Utilization > best.Utilization {
			best = o
		}
	}
	return fmt.Sprintf("osd.%d", best.ID)
}
