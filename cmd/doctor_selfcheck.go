package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// selfCheckResult 记录单个工具的调用结果。
type selfCheckResult struct {
	Tool    string
	OK      bool
	Summary string
	Err     string
	Ms      int64
}

// runSelfCheck 在真实集群上把每个诊断工具各跑一次。
//
// 目的是在消耗模型额度之前，先确认「工具能不能真的把集群的数据读出来」。
// 工具解析失败在 dry-run 阶段看不出来——dry-run 只验证 SSH 与探测——
// 却会在完整诊断时变成模型拿不到证据。
func runSelfCheck(ctx context.Context, cluster *access.Cluster) error {
	runner := &selfCheckRunner{cluster: cluster}
	registry := tools.DefaultRegistry(runner)

	node := ""
	if nodes := cluster.Nodes(); len(nodes) > 0 {
		node = nodes[0]
	}

	// 每个工具的参数：需要节点/OSD 的先从拓扑里取一个真实值。
	topo, topoErr := cluster.Topology(ctx)
	osdID := 0
	if topoErr == nil && topo != nil && len(topo.OSDs) > 0 {
		osdID = topo.OSDs[0].ID
	}

	fmt.Fprintf(os.Stderr, "🧪 在真实集群上逐个验证工具 ...\n\n")

	checks := []selfCheck{
		{"ceph_status", nil},
		{"ceph_health", nil},
		{"osd_tree", nil},
		{"pool_info", nil},
		{"pg_detail", nil},
		{"slow_ops", map[string]any{"osd_ids": firstOSDs(topo, 3)}},
		{"host_check", map[string]any{"node": node}},
	}

	// 拓扑相关工具依赖真实 OSD
	if osdID > 0 {
		checks = append(checks,
			selfCheck{"osd_detail", map[string]any{"osd_id": osdID}},
			selfCheck{"perf_counters", map[string]any{"osd_id": osdID}},
		)
		if dev, host := placementOf(topo, osdID); dev != "" && host != "" {
			checks = append(checks,
				selfCheck{"host_io", map[string]any{"node": host, "device": dev}},
			)
		}
	}
	// 日志类工具：优先验证 daemon_log（它会自行选择宿主机/容器来源），
	// 只有宿主机确实存在日志文件时才验证 read_log。
	if osdID > 0 {
		checks = append(checks, selfCheck{"daemon_log", map[string]any{
			"daemon": "osd", "id": fmt.Sprint(osdID), "lines": 50,
		}})
	}
	if caps := cluster.Capabilities(ctx, node); len(caps.LogDirs) > 0 && osdID > 0 {
		checks = append(checks, selfCheck{"read_log", map[string]any{
			"node": node, "path": fmt.Sprintf("%s/ceph-osd.%d.log", caps.LogDirs[0], osdID),
		}})
	} else {
		fmt.Fprintf(os.Stderr, "  （跳过 read_log：该节点宿主机没有日志目录，日志在容器内）\n")
	}

	var results []selfCheckResult
	for _, c := range checks {
		args := c.args
		if args == nil {
			args = map[string]any{}
		}
		raw, _ := json.Marshal(args)
		start := nowMillis()
		res, _, err := registry.Execute(ctx, c.name, raw)
		elapsed := nowMillis() - start

		r := selfCheckResult{Tool: c.name, Ms: elapsed}
		switch {
		case err != nil:
			r.Err = err.Error()
		case res == nil:
			r.Err = "工具返回空结果"
		default:
			r.OK = true
			r.Summary = res.Summary
		}
		results = append(results, r)

		if r.OK {
			fmt.Fprintf(os.Stderr, "  ✓ %-16s %5dms  %s\n", r.Tool, r.Ms, truncateLine(r.Summary, 90))
		} else {
			fmt.Fprintf(os.Stderr, "  ✗ %-16s %5dms  %s\n", r.Tool, r.Ms, truncateLine(r.Err, 90))
		}
	}

	okCount, failCount := 0, 0
	for _, r := range results {
		if r.OK {
			okCount++
		} else {
			failCount++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n工具自检结果：%d 通过 / %d 失败（共 %d）\n", okCount, failCount, len(results))
	if failCount > 0 {
		b.WriteString("\n失败明细：\n")
		for _, r := range results {
			if !r.OK {
				fmt.Fprintf(&b, "  - %s: %s\n", r.Tool, r.Err)
			}
		}
		b.WriteString("\n这些工具在完整诊断时会让模型拿不到相应证据，建议先修复。\n")
	} else {
		b.WriteString("全部工具都能正常读取集群数据，可以运行完整诊断。\n")
	}

	if !hasCheck(checks, "ceph_source_lookup") {
		fmt.Fprintf(&b, "\n未验证：ceph_source_lookup（需通过 --ceph-src 配置本地源码目录）\n")
	}

	fmt.Fprint(os.Stdout, b.String())
	return nil
}

// selfCheck 是一条待验证的工具调用。
type selfCheck struct {
	name string
	args map[string]any
}

// hasCheck 判断某个工具是否已纳入自检。
func hasCheck(checks []selfCheck, name string) bool {
	for _, c := range checks {
		if c.name == name {
			return true
		}
	}
	return false
}

// selfCheckRunner 把 Cluster 适配成 tools.Runner。
type selfCheckRunner struct{ cluster *access.Cluster }

func (r *selfCheckRunner) Run(ctx context.Context, node string, argv []string) (access.Output, error) {
	return r.cluster.Run(ctx, node, argv)
}
func (r *selfCheckRunner) Capabilities(ctx context.Context, node string) access.Capabilities {
	return r.cluster.Capabilities(ctx, node)
}
func (r *selfCheckRunner) Topology(ctx context.Context) (*access.Topology, error) {
	return r.cluster.Topology(ctx)
}
func (r *selfCheckRunner) Nodes() []string { return r.cluster.Nodes() }

// firstOSDs 取前 n 个 OSD 编号，供 slow_ops 这类需要采样参数的工具使用。
func firstOSDs(topo *access.Topology, n int) []int {
	if topo == nil {
		return nil
	}
	var ids []int
	for _, o := range topo.OSDs {
		ids = append(ids, o.ID)
		if len(ids) >= n {
			break
		}
	}
	sort.Ints(ids)
	return ids
}

// placementOf 返回某个 OSD 的节点与磁盘。
func placementOf(topo *access.Topology, osdID int) (device, host string) {
	if topo == nil {
		return "", ""
	}
	for _, p := range topo.OSDs {
		if p.ID == osdID {
			return p.Device, p.Host
		}
	}
	return "", ""
}

// firstLogPath 从拓扑里推出一个真实存在的日志文件路径，用于 read_log 自检。
func firstLogPath(topo *access.Topology) string {
	if topo == nil || len(topo.OSDs) == 0 {
		return ""
	}
	return fmt.Sprintf("/var/log/ceph/ceph-osd.%d.log", topo.OSDs[0].ID)
}

// nowMillis 返回当前毫秒时间戳，用于统计单个工具的耗时。
func nowMillis() int64 { return time.Now().UnixMilli() }

func truncateLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
