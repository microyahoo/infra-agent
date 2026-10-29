package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	ceph "github.com/microyahoo/infra-agent/internal/ceph"
)

type cephStatusTool struct{ r Runner }

// NewCephStatus 构造 ceph status 工具。
func NewCephStatus(r Runner) Tool { return &cephStatusTool{r: r} }

func (t *cephStatusTool) Name() string { return "ceph_status" }

func (t *cephStatusTool) Description() string {
	return "获取集群整体状态：健康码、健康检查明细、mon quorum、OSD up/in 数量、PG 状态分布、容量使用率。" +
		"注意：会话开始时已自动采集过一次，结果在系统提示词的集群背景里；" +
		"除非用户问的是当前状态是否已经变化，否则不必重复调用。"
}

func (t *cephStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
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
	st, jerr := ceph.ParseStatus(out.Stdout)
	if jerr != nil {
		return nil, nil, fmt.Errorf("解析 ceph status 输出失败: %w", jerr)
	}
	d := &st

	raw := newRaw(node, argv, out)

	res := &Result{
		Summary: fmt.Sprintf("%s，mon %d，OSD %d 总/%d up/%d in，PG %d（异常 %d），容量 %s",
			d.Health, d.NumMons, d.NumOSDs, d.NumOSDsUp, d.NumOSDsIn, d.NumPGs, d.DegradedPGs, ceph.FormatPercent(d.UsedPercent)),
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

// primaryNode 返回诊断入口节点。
func primaryNode(r Runner) string {
	nodes := r.Nodes()
	if len(nodes) > 0 {
		return nodes[0]
	}
	return ""
}
