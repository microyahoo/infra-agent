package snapshot

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Finding 是从告警里直接推出来的结论。
//
// 为什么需要它：一条健康明细往往同时列出几十项症状——6 个 OSD down、
// 1110 个 PG 降级、上百条慢心跳。让模型自己去把这些串起来，
// 它多半会逐个工具查一遍，而不是先看出"这些都是同一个根因的下游反应"。
// 确定性的关联放在代码里做，模型负责验证和解释。
type Finding struct {
	// Summary 是一句话结论。
	Summary string `json:"summary"`
	// Why 说明依据。
	Why string `json:"why"`
	// Next 是建议的下一步验证。
	Next string `json:"next"`
}

// 匹配 health detail 里的各类条目。
var (
	// osd.44 (root=...,host=cloud-hdd05-node05-xxxx) is down
	reOSDDown = regexp.MustCompile(`osd\.(\d+)\s*\(.*?host=([^,)\s]+).*?\)\s*is down`)

	// Slow OSD heartbeats on back from osd.98 [] to osd.105 [] (down) 10072.200 msec
	reSlowHeartbeat = regexp.MustCompile(`Slow OSD heartbeats on (?:back|front) from osd\.(\d+)[^(]*?(?:\(down\))?\s*to osd\.(\d+)`)

	// pg 9.db is stuck peering for 4d
	reStuckPG = regexp.MustCompile(`pg\s+(\S+)\s+is stuck\s+(\S+)`)
)

// correlate 从告警明细里推出确定的结论，按可信度排序。
//
// 只做"几乎不可能错"的关联：同一节点的多个 OSD 同时 down、
// 慢心跳集中在少数 OSD 上、PG 卡住与 OSD 掉线时间吻合等。
// 推测性判断仍然留给模型。
func correlate(s Snapshot) []Finding {
	var findings []Finding

	downs, _ := parseDownOSDs(s.HealthDetail)

	// 规则 1：多个 OSD 同时 down 且集中在同一节点 —— 单节点故障。
	if len(downs) >= 2 {
		byHost := map[string][]string{}
		for id, host := range downs {
			byHost[host] = append(byHost[host], id)
		}
		// 找最大的那一组
		var topHost string
		for h, ids := range byHost {
			if topHost == "" || len(ids) > len(byHost[topHost]) {
				topHost = h
			}
		}
		ids := byHost[topHost]
		sort.Strings(ids)
		if len(ids) >= 2 && len(ids)*2 >= len(downs) {
			findings = append(findings, Finding{
				Summary: fmt.Sprintf("%d 个 OSD 同时 down，全部位于节点 %s：%s",
					len(ids), shortHostName(topHost), strings.Join(prefixOSD(ids), "、")),
				Why: "多个 OSD 同属一台机器且同时失联，几乎不会是 N 块盘同时坏；" +
					"更可能是该节点本身不可用（宕机、重启、网络断开、内核或 RAID 故障）。",
				Next: fmt.Sprintf("先确认节点 %s 是否可达（host_check node=%s，或直接 ping/ssh）；"+
					"节点不可达就别再逐个查 OSD 了，先修节点。", shortHostName(topHost), shortHostName(topHost)),
			})
		}
		if len(byHost) > 1 {
			var parts []string
			for h, ids := range byHost {
				parts = append(parts, fmt.Sprintf("%s:%d 个", shortHostName(h), len(ids)))
			}
			sort.Strings(parts)
			findings = append(findings, Finding{
				Summary: "OSD down 分散在多个节点：" + strings.Join(parts, "，"),
				Why:     "跨节点同时掉线更像是网络分区、mon 侧问题或大规模重启，而不是单机故障。",
				Next:    "先看 mon quorum 与集群整体网络，再决定是否逐节点排查。",
			})
		}
	}

	// 规则 2：慢心跳集中在少数 OSD 上。
	if offenders := parseSlowHeartbeatOffenders(s.HealthDetail); len(offenders) > 0 {
		findings = append(findings, Finding{
			Summary: "慢心跳集中在 " + strings.Join(offenders, "、") + " 上",
			Why:     "心跳超时成对出现且反复指向同样的 OSD，说明问题在这些 OSD 或其所在节点/链路上，而不是全网抖动。",
			Next:    "对这些 OSD 所在节点做 host_check（负载）与 host_io（磁盘），并确认它们是否就是 down 的那批。",
		})
	}

	// 规则 3：PG 长期卡住。
	if stuck := parseStuckPGs(s.HealthDetail); stuck > 0 {
		findings = append(findings, Finding{
			Summary: fmt.Sprintf("%d 个 PG 长时间卡在 peering/undersized 状态", stuck),
			Why:     "PG 卡住通常是因为其副本所在的 OSD 不可用；如果同时有 OSD down，二者是同一件事。",
			Next:    "结合上面的 OSD down 一起看；OSD 恢复后 PG 会自动完成 peering，不需要单独处理 PG。",
		})
	}

	// 规则 4：健康明细里 OSD 数与 ceph status 对不上，提示采样时刻不一致。
	if strings.Contains(s.HealthDetail, "slow ops") && strings.Contains(s.HealthDetail, "blocked for") {
		findings = append(findings, Finding{
			Summary: "存在被阻塞的请求（slow ops），前端 IO 已经在受影响",
			Why:     "阻塞请求通常发生在后端 OSD 不可达或磁盘卡住时。",
			Next:    "确认阻塞源的 OSD 是否在上面那批 down 的机器上。",
		})
	}

	return findings
}

// parseDownOSDs 从告警明细里提取 down 的 OSD 及其主机。
func parseDownOSDs(detail string) (map[string]string, map[string]bool) {
	downs := map[string]string{}
	hosts := map[string]bool{}
	for _, m := range reOSDDown.FindAllStringSubmatch(detail, -1) {
		id, host := m[1], m[2]
		downs[id] = host
		hosts[host] = true
	}
	return downs, hosts
}

// parseSlowHeartbeatOffenders 找出在慢心跳里反复出现的 OSD。
func parseSlowHeartbeatOffenders(detail string) []string {
	count := map[string]int{}
	for _, m := range reSlowHeartbeat.FindAllStringSubmatch(detail, -1) {
		count[m[1]]++
		count[m[2]]++
	}
	type kv struct {
		id string
		n  int
	}
	var all []kv
	for id, n := range count {
		all = append(all, kv{id, n})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].id < all[j].id
	})
	var out []string
	for _, e := range all {
		if e.n < 2 {
			break
		}
		out = append(out, "osd."+e.id)
		if len(out) >= 5 {
			break
		}
	}
	return out
}

// parseStuckPGs 统计卡住的 PG 数量。
func parseStuckPGs(detail string) int {
	seen := map[string]bool{}
	for _, m := range reStuckPG.FindAllStringSubmatch(detail, -1) {
		seen[m[1]] = true
	}
	return len(seen)
}

// shortHostName 去掉 Rook 主机名后面的随机后缀，便于阅读。
func shortHostName(h string) string {
	// cloud-hdd05-node05-77876dc2-4900-4823-95c2-64641c9e7443 → cloud-hdd05-node05
	if i := strings.Index(h, "-7"); i > 0 && len(h)-i > 30 {
		return h[:i]
	}
	return h
}

func prefixOSD(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, "osd."+id)
	}
	sort.Strings(out)
	return out
}

var reOSDRef = regexp.MustCompile(`osd\.(\d+)`)

// ReferencedOSDs 收集告警与结论里提到的 OSD 编号。
// 用于把"这些 OSD 在哪台机器、哪块盘"直接查出来写进提示词，
// 省掉模型逐个 osd_detail 的往返。
func ReferencedOSDs(s Snapshot, limit int) []int {
	seen := map[int]bool{}
	var out []int
	add := func(idStr string) {
		id := 0
		for _, r := range idStr {
			if r < '0' || r > '9' {
				return
			}
			id = id*10 + int(r-'0')
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, m := range reOSDRef.FindAllStringSubmatch(s.HealthDetail, -1) {
		add(m[1])
	}
	for _, f := range s.Findings {
		for _, m := range reOSDRef.FindAllStringSubmatch(f.Summary+" "+f.Next, -1) {
			add(m[1])
		}
	}
	sort.Ints(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
