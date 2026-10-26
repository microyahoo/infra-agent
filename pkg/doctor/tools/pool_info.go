package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PoolInfo 单个池的容量信息。
type PoolInfo struct {
	Name          string  `json:"name"`
	BytesUsed     uint64  `json:"bytes_used"`
	MaxAvail      uint64  `json:"max_avail"`
	UsedPercent   float64 `json:"used_percent"` // 0-1
	PGNum         int     `json:"pg_num"`
	PGNumTarget   int     `json:"pg_num_target,omitempty"`
	AutoscaleMode string  `json:"autoscale_mode,omitempty"`
	Size          int     `json:"size"`
	MinSize       int     `json:"min_size"`
}

// PoolInfoData 是 pool_info 的结构化结果。
type PoolInfoData struct {
	Pools []PoolInfo `json:"pools"`
}

type poolInfoTool struct{ r Runner }

// NewPoolInfo 构造 pool_info 工具。
func NewPoolInfo(r Runner) Tool { return &poolInfoTool{r: r} }

func (t *poolInfoTool) Name() string { return "pool_info" }

func (t *poolInfoTool) Description() string {
	return "获取各存储池的容量使用、副本数、PG 数量及 autoscale 目标值。用于定位是哪个池接近写满，以及 PG 数量是否严重偏离目标。"
}

func (t *poolInfoTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

type rawPoolDF struct {
	Pools []struct {
		Name  string `json:"name"`
		Stats struct {
			BytesUsed   uint64  `json:"bytes_used"`
			MaxAvail    uint64  `json:"max_avail"`
			PercentUsed float64 `json:"percent_used"`
		} `json:"stats"`
		PGNum   int `json:"pg_num"`
		Size    int `json:"size"`
		MinSize int `json:"min_size"`
	} `json:"pools"`
}

type rawAutoscale struct {
	Pools []struct {
		PoolName        string `json:"pool_name"`
		PGNumTarget     int    `json:"pg_num_target"`
		PGNum           int    `json:"pg_num"`
		PGAutoscaleMode string `json:"pg_autoscale_mode"`
	} `json:"pools"`
}

type autoscaleEntry struct {
	pgNum  int
	target int
	mode   string
}

func (t *poolInfoTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	node := primaryNode(t.r)
	caps := t.r.Capabilities(ctx, node)
	if !caps.CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI（后端 %s），无法执行 ceph df detail；可改用 ceph_status 查看整体容量", caps.Backend)
	}

	argv := []string{"ceph", "df", "detail", "-f", "json"}
	out, err := t.r.Run(ctx, node, argv)
	if err != nil {
		return nil, nil, err
	}
	var df rawPoolDF
	if jerr := json.Unmarshal([]byte(out.Stdout), &df); jerr != nil {
		return nil, nil, fmt.Errorf("解析 ceph df detail 失败: %w", jerr)
	}

	// autoscale-status 是补充信息：失败或解析失败时退化为只有容量数据。
	autoMap := map[string]autoscaleEntry{}
	if aout, aerr := t.r.Run(ctx, node, []string{"ceph", "osd", "pool", "autoscale-status", "-f", "json"}); aerr == nil {
		var au rawAutoscale
		if json.Unmarshal([]byte(aout.Stdout), &au) == nil {
			for _, p := range au.Pools {
				autoMap[p.PoolName] = autoscaleEntry{pgNum: p.PGNum, target: p.PGNumTarget, mode: p.PGAutoscaleMode}
			}
		}
	}

	d := &PoolInfoData{}
	for _, p := range df.Pools {
		pi := PoolInfo{
			Name: p.Name, BytesUsed: p.Stats.BytesUsed, MaxAvail: p.Stats.MaxAvail,
			UsedPercent: p.Stats.PercentUsed, PGNum: p.PGNum, Size: p.Size, MinSize: p.MinSize,
		}
		if a, ok := autoMap[p.Name]; ok {
			pi.PGNumTarget, pi.AutoscaleMode = a.target, a.mode
			// df 与 autoscale-status 可能来自不同时刻：pg_num 在分裂过程中
			// 先体现在 autoscale-status 上，后者更新时以它为准。
			if a.pgNum > pi.PGNum {
				pi.PGNum = a.pgNum
			}
		}
		d.Pools = append(d.Pools, pi)
	}

	raw := newRaw(node, argv, out)

	res := &Result{Data: d, Summary: fmt.Sprintf("%d 个池", len(d.Pools))}
	var hot []string
	for _, p := range d.Pools {
		if p.UsedPercent >= 0.8 {
			hot = append(hot, fmt.Sprintf("%s(%s)", p.Name, formatPercent(p.UsedPercent*100)))
		}
		if p.PGNumTarget > 0 && p.PGNumTarget >= p.PGNum*2 {
			res.Hints = append(res.Hints, fmt.Sprintf("池 %s 的 PG 数 %d 远低于目标 %d，可以建议调整 pg_num（需人工确认）", p.Name, p.PGNum, p.PGNumTarget))
		}
	}
	if len(hot) > 0 {
		res.Summary += "；接近写满：" + strings.Join(hot, "、")
		res.Hints = append(res.Hints, "存在接近写满的池，可调用 osd_tree 确认是否个别 OSD 先满")
	}
	return res, raw, nil
}
