package access

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Topology 是集群的节点与 OSD→设备映射。
type Topology struct {
	FSID  string
	Nodes []TopoNode
	OSDs  []OSDPlacement
}

// TopoNode 是集群中的一个节点。
type TopoNode struct {
	Name   string
	Host   string
	OSDIDs []int
	Roles  []string
}

// OSDPlacement 描述单个 OSD 的物理位置。
type OSDPlacement struct {
	ID        int
	Host      string
	Device    string // 解析后的物理盘，如 sdc
	RawDevice string // 原始值，可能是 LVM 路径
	DBDevice  string
	Status    string
}

// osdNode 是 ceph osd tree 里的 OSD 条目。
type osdNode struct {
	ID     int
	Host   string
	Status string
}

type rawOSDTree struct {
	Nodes []struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		Status string `json:"status"`
		Host   string `json:"host"`
	} `json:"nodes"`
}

// parseOSDTree 从 ceph osd tree -f json 提取 OSD 节点。
func parseOSDTree(raw string) ([]osdNode, error) {
	var tree rawOSDTree
	if err := json.Unmarshal([]byte(raw), &tree); err != nil {
		return nil, fmt.Errorf("parse osd tree: %w", err)
	}
	var out []osdNode
	for _, n := range tree.Nodes {
		if n.Type != "osd" {
			continue
		}
		out = append(out, osdNode{ID: n.ID, Host: n.Host, Status: n.Status})
	}
	return out, nil
}

type rawOSDMetadata struct {
	ID             int    `json:"id"`
	Hostname       string `json:"hostname"`
	BlueStoreBDevs string `json:"bluestore_bdev_devices"`
	Devices        string `json:"devices"`
	ObjectStore    string `json:"osd_objectstore"`
}

// parseOSDMetadata 从 ceph osd metadata <id> -f json 提取物理位置。
// 设备是 LVM 逻辑卷时不设置 Device，只保留 RawDevice，交由后续用 lsblk 解析。
func parseOSDMetadata(id int, raw string) (OSDPlacement, error) {
	var m rawOSDMetadata
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return OSDPlacement{}, fmt.Errorf("parse osd metadata %d: %w", id, err)
	}
	dev := m.BlueStoreBDevs
	if dev == "" {
		dev = m.Devices
	}
	p := OSDPlacement{
		ID:        id,
		Host:      m.Hostname,
		RawDevice: dev,
	}
	if !isLVMPath(dev) {
		p.Device = strings.TrimSpace(dev)
	}
	return p, nil
}

// isLVMPath 判断设备路径是否为 LVM 逻辑卷。
func isLVMPath(dev string) bool {
	return strings.HasPrefix(dev, "/dev/mapper/") ||
		(strings.HasPrefix(dev, "/dev/ceph-") && strings.Contains(dev, "/"))
}

// parseLsblkParent 解析 lsblk -no PKNAME 的输出，取第一行。
func parseLsblkParent(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	return ""
}

// buildTopology 拉取所有 OSD 的 metadata，构建映射表。
// 节点按首次出现顺序排列（即 osd tree 顺序），保证入口节点稳定在 Nodes[0]。
func buildTopology(ctx context.Context, tr transport, fsid string, nodes []osdNode) Topology {
	topo := Topology{FSID: fsid}
	byHost := map[string]*TopoNode{}
	var order []string

	// 一次拉全部 metadata（ceph osd metadata -f json 返回数组）。
	// 逐个 OSD 查在 120 个 OSD 的集群上要 120 次往返，实测比批量慢两个数量级。
	bulk := fetchAllMetadata(ctx, tr)

	for _, n := range nodes {
		out, ok := bulk[n.ID]
		if !ok {
			// 批量失败时退回单个查询；仍失败则保留最小信息。
			var code int
			out, _, code = tr.exec(ctx, []string{"ceph", "osd", "metadata", fmt.Sprint(n.ID), "-f", "json"})
			if code != 0 {
				topo.OSDs = append(topo.OSDs, OSDPlacement{ID: n.ID, Host: n.Host, Status: n.Status})
				order = appendNode(byHost, order, n.Host, n.ID)
				continue
			}
		}
		p, err := parseOSDMetadata(n.ID, out)
		if err != nil {
			p = OSDPlacement{ID: n.ID, Host: n.Host}
		}
		p.Status = n.Status
		if p.Host == "" {
			p.Host = n.Host
		}
		topo.OSDs = append(topo.OSDs, p)

		order = appendNode(byHost, order, p.Host, n.ID)
	}

	for _, host := range order {
		topo.Nodes = append(topo.Nodes, *byHost[host])
	}
	return topo
}

// fetchAllMetadata 用一条命令取回所有 OSD 的 metadata。
// 返回 map[osdID]原始JSON；失败时返回空 map，由调用方回退到逐个查询。
func fetchAllMetadata(ctx context.Context, tr transport) map[int]string {
	out, _, code := tr.exec(ctx, []string{"ceph", "osd", "metadata", "-f", "json"})
	if code != 0 || strings.TrimSpace(out) == "" {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		return nil
	}
	res := make(map[int]string, len(arr))
	for _, raw := range arr {
		var head struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}
		res[head.ID] = string(raw)
	}
	return res
}

// appendNode 把 OSD 挂到对应节点上；节点首次出现时记录其位置以保持顺序。
func appendNode(m map[string]*TopoNode, order []string, host string, osdID int) []string {
	nd, ok := m[host]
	if !ok {
		nd = &TopoNode{Name: host, Host: host}
		m[host] = nd
		order = append(order, host)
	}
	nd.OSDIDs = append(nd.OSDIDs, osdID)
	return order
}
