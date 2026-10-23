package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// OSDDetailData 是 osd_detail 的结构化结果。
type OSDDetailData struct {
	ID        int    `json:"id"`
	Host      string `json:"host,omitempty"`
	Device    string `json:"device,omitempty"`
	RawDevice string `json:"raw_device,omitempty"`
	Status    string `json:"status_raw,omitempty"`
}

type osdDetailTool struct{ r Runner }

// NewOSDDetail 构造 osd_detail 工具。
func NewOSDDetail(r Runner) Tool { return &osdDetailTool{r: r} }

func (t *osdDetailTool) Name() string { return "osd_detail" }

func (t *osdDetailTool) Description() string {
	return "查看单个 OSD 的运行状态，并给出它所在节点与物理盘路径。用于把 OSD 问题关联到具体宿主机和磁盘。"
}

func (t *osdDetailTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"osd_id":{"type":"integer","description":"OSD 编号，例如 7"}},"required":["osd_id"],"additionalProperties":false}`)
}

func (t *osdDetailTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		OSDID *int `json:"osd_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	if in.OSDID == nil {
		return nil, nil, fmt.Errorf("缺少必填参数 osd_id")
	}
	id := *in.OSDID

	node := primaryNode(t.r)
	if !t.r.Capabilities(ctx, node).CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI，无法执行 ceph tell")
	}

	// 先从拓扑补全「节点/磁盘」，实时状态失败时仍能把 OSD 关联到宿主机。
	d := &OSDDetailData{ID: id}
	if topo, err := t.r.Topology(ctx); err == nil && topo != nil {
		for _, p := range topo.OSDs {
			if p.ID == id {
				d.Host, d.Device, d.RawDevice = p.Host, p.Device, p.RawDevice
				break
			}
		}
	}

	argv := []string{"ceph", "tell", fmt.Sprintf("osd.%d", id), "status"}
	out, err := t.r.Run(ctx, node, argv)
	if err != nil {
		res := &Result{
			Data:    d,
			Summary: fmt.Sprintf("osd.%d 位于 %s，盘 %s（实时状态获取失败）", id, orUnknown(d.Host), orUnknown(d.Device)),
			Hints:   osdHints(d),
		}
		return res, newRaw(node, argv, out), nil
	}

	d.Status = out.Stdout
	res := &Result{
		Data:    d,
		Summary: fmt.Sprintf("osd.%d 位于节点 %s，物理盘 %s", id, orUnknown(d.Host), orUnknown(d.Device)),
		Hints:   osdHints(d),
	}
	return res, newRaw(node, argv, out), nil
}

// osdHints 根据是否已知节点/磁盘给出下一步查询。
func osdHints(d *OSDDetailData) []string {
	if d.Host == "" || d.Device == "" {
		return []string{"缺少节点/磁盘映射，可调用 osd_tree 获取，或 daemon_log 查看该 OSD 日志"}
	}
	return []string{
		fmt.Sprintf("调用 host_io(node=%q, device=%q) 查看该盘的 iostat / dmesg / smartctl", d.Host, d.Device),
		fmt.Sprintf("调用 daemon_log(node=%q, daemon=osd, id=%q) 查看该 OSD 日志", d.Host, fmt.Sprint(d.ID)),
	}
}

// orUnknown 把空值替换为可读的「未知」。
func orUnknown(s string) string {
	if s == "" {
		return "未知"
	}
	return s
}
