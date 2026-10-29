package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/ceph/snapshot"
)

type overviewTool struct{ r Runner }

// NewOverview 构造集群概况工具。
func NewOverview(r Runner) Tool { return &overviewTool{r: r} }

func (t *overviewTool) Name() string { return "ceph_overview" }

func (t *overviewTool) Description() string {
	return "一次性查看集群概况：健康状态、告警明细、Rook pod 异常、确定性的关联结论，" +
		"以及告警涉及的 OSD 落在哪个节点、哪块盘。" +
		"开始调查一个新问题时先调用它，可以省掉 ceph_status / ceph_health / rook_pods / osd_tree 的多次往返。"
}

func (t *overviewTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

// OverviewData 是 ceph_overview 的结构化结果。
type OverviewData struct {
	Health       string             `json:"health"`
	Summary      string             `json:"summary"`
	HealthDetail string             `json:"health_detail,omitempty"`
	PodHealth    string             `json:"pod_health,omitempty"`
	Findings     []snapshot.Finding `json:"findings,omitempty"`
	OSDPlacement []OSDPlacementRow  `json:"osd_placement,omitempty"`
}

// OSDPlacementRow 是告警里提到的 OSD 的落点。
type OSDPlacementRow struct {
	OSD    int    `json:"osd"`
	Node   string `json:"node,omitempty"`
	Device string `json:"device,omitempty"`
	Status string `json:"status,omitempty"`
}

func (t *overviewTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	snap, err := snapshot.Collect(ctx, t.r)
	if err != nil {
		return nil, nil, err
	}

	data := &OverviewData{
		Health:       snap.Health,
		Summary:      snap.Summary,
		HealthDetail: snap.HealthDetail,
		PodHealth:    snap.PodHealth,
		Findings:     snap.Findings,
	}

	if snap.Topology != nil {
		place := make(map[int]access.OSDPlacement, len(snap.Topology.OSDs))
		for _, p := range snap.Topology.OSDs {
			place[p.ID] = p
		}
		for _, id := range snapshot.ReferencedOSDs(snap, 24) {
			row := OSDPlacementRow{OSD: id}
			if p, ok := place[id]; ok {
				row.Node, row.Device, row.Status = p.Host, p.Device, p.Status
			}
			data.OSDPlacement = append(data.OSDPlacement, row)
		}
	}

	summary := snap.Summary
	if snap.Health != "" && snap.Summary != "" {
		summary = snap.Health + "，" + snap.Summary
	} else if snap.Health != "" {
		summary = snap.Health
	}
	res := &Result{Summary: summary, Data: data}
	for _, f := range snap.Findings {
		res.Hints = append(res.Hints, f.Next)
	}
	if snap.Health != "HEALTH_OK" && len(snap.Findings) == 0 {
		res.Hints = append(res.Hints, "告警没有匹配到已知模式：用 ceph_health 看完整明细，再按提到的对象逐个查")
	}
	if len(snap.Failures) > 0 {
		res.Hints = append(res.Hints, fmt.Sprintf("会话起点有 %d 项没采到：%s", len(snap.Failures), snap.Failures[0]))
	}
	return res, nil, nil
}
