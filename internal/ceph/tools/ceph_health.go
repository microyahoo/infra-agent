package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// HealthCheck 是一条健康告警。
type HealthCheck struct {
	// Code 是告警代码，例如 OSD_SLOW_HEARTBEAT、PG_DEGRADED。
	Code string `json:"code"`
	// Severity 是严重度：HEALTH_WARN / HEALTH_ERR。
	Severity string `json:"severity"`
	// Summary 是告警的一句话说明，通常含关键数值（如 longest 1237ms）。
	Summary string `json:"summary"`
	// Details 是展开后的明细行，指明受影响的具体对象。
	Details []string `json:"details,omitempty"`
}

// CephHealthData 是 ceph_health 的结构化结果。
type CephHealthData struct {
	Status string        `json:"status"`
	Checks []HealthCheck `json:"checks"`
}

type cephHealthTool struct{ r Runner }

// NewCephHealth 构造 ceph_health 工具。
func NewCephHealth(r Runner) Tool { return &cephHealthTool{r: r} }

func (t *cephHealthTool) Name() string { return "ceph_health" }

func (t *cephHealthTool) Description() string {
	return "读取集群健康告警的完整明细（ceph health detail）：每条告警的代码、严重度、说明与受影响对象。" +
		"注意：会话开始时已自动采集过一次，结果在系统提示词里；" +
		"当用户问的是「现在有没有新告警」或怀疑状态已变化时才需要重新调用。"
}

func (t *cephHealthTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

// rawHealthDetail 对应 ceph health detail -f json。
//
// 形态：
//
//	{"status":"HEALTH_WARN",
//	 "checks":{"OSD_SLOW_HEARTBEAT":{"severity":"HEALTH_WARN",
//	    "summary":{"message":"Slow OSD heartbeats on back (longest 1237ms)"},
//	    "detail":[{"message":"osd.13 is experiencing slow heartbeats"}]}}}
//
// detail 的元素在不同版本里既可能是对象也可能是字符串，这里统一按 RawMessage 收再逐个判断。
type rawHealthDetail struct {
	Status string                    `json:"status"`
	Checks map[string]rawHealthCheck `json:"checks"`
}

type rawHealthCheck struct {
	Severity string `json:"severity"`
	Summary  struct {
		Message string `json:"message"`
	} `json:"summary"`
	Detail []json.RawMessage `json:"detail"`
}

func (t *cephHealthTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	node := primaryNode(t.r)
	caps := t.r.Capabilities(ctx, node)
	if !caps.CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI（后端 %s），无法执行 ceph health detail；可改用 daemon_log 从日志分析", caps.Backend)
	}

	argv := []string{"ceph", "health", "detail", "-f", "json"}
	out, err := t.r.Run(ctx, node, argv)
	if err != nil {
		return nil, nil, err
	}

	var raw rawHealthDetail
	if jerr := json.Unmarshal([]byte(out.Stdout), &raw); jerr != nil {
		return nil, newRaw(node, argv, out), fmt.Errorf("解析 ceph health detail 失败: %w", jerr)
	}

	d := &CephHealthData{Status: raw.Status}
	codes := make([]string, 0, len(raw.Checks))
	for code := range raw.Checks {
		codes = append(codes, code)
	}
	// map 顺序不确定，排序保证输出可复现。
	sort.Strings(codes)

	for _, code := range codes {
		c := raw.Checks[code]
		hc := HealthCheck{
			Code:     code,
			Severity: c.Severity,
			Summary:  strings.TrimSpace(c.Summary.Message),
		}
		for _, det := range c.Detail {
			if msg := detailMessage(det); msg != "" {
				hc.Details = append(hc.Details, msg)
			}
		}
		d.Checks = append(d.Checks, hc)
	}

	res := &Result{Data: d}

	if len(d.Checks) == 0 {
		res.Summary = fmt.Sprintf("%s，没有健康告警", d.Status)
		return res, newRaw(node, argv, out), nil
	}

	// 摘要按严重度排序，ERR 在前，让模型先看到最要紧的。
	sort.SliceStable(d.Checks, func(i, j int) bool {
		return d.Checks[i].Severity > d.Checks[j].Severity
	})

	var parts []string
	for _, c := range d.Checks {
		parts = append(parts, fmt.Sprintf("%s（%s）", c.Summary, c.Code))
	}
	res.Summary = fmt.Sprintf("%s，%d 条告警：%s", d.Status, len(d.Checks), strings.Join(parts, "；"))

	// 依据告警代码给出下一步方向。经验固化在工具里，而不是让模型自己猜。
	res.Hints = healthHints(d)
	return res, newRaw(node, argv, out), nil
}

// healthHints 把告警代码翻译成具体的下一步查询。
func healthHints(d *CephHealthData) []string {
	var hints []string
	seen := map[string]bool{}
	add := func(h string) {
		if h != "" && !seen[h] {
			seen[h] = true
			hints = append(hints, h)
		}
	}

	for _, c := range d.Checks {
		code := strings.ToUpper(c.Code)
		switch {
		case strings.Contains(code, "OSD_SLOW_HEARTBEAT"),
			strings.Contains(code, "OSD_HEARTBEAT"),
			strings.Contains(code, "SLOW_HEARTBEAT"):
			add("心跳慢通常来自网络抖动或节点负载，而不是磁盘：" +
				"先用 host_check 看受影响节点的负载，再用 osd_detail 确认 OSD 所在节点")

		case strings.Contains(code, "PG_DEGRADED"), strings.Contains(code, "PG_UNDERSIZED"),
			strings.Contains(code, "PG_INACTIVE"), strings.Contains(code, "PG_UNAVAILABLE"):
			add("PG 降级：用 pg_detail 查看卡住的 PG，再用 osd_detail 定位其主 OSD")

		case strings.Contains(code, "OSD_DOWN"), strings.Contains(code, "OSD_HOST_DOWN"):
			add("OSD 掉线：用 osd_tree 确认哪些 OSD down，再用 osd_detail 与 daemon_log 看原因")

		case strings.Contains(code, "POOL_NEAR_FULL"), strings.Contains(code, "POOL_FULL"),
			strings.Contains(code, "NEARFULL"), strings.Contains(code, "BACKFILL_TO_FULL"):
			add("容量告警：用 pool_info 看是哪个池占满、用 osd_tree 看是否个别 OSD 先满")

		case strings.Contains(code, "SLOW_OPS"), strings.Contains(code, "REQUEST_SLOW"):
			add("存在慢请求：用 slow_ops 查看 op 时间线，判断卡在哪个阶段")

		case strings.Contains(code, "MON_CLOCK_SKEW"), strings.Contains(code, "TIME_SKEW"):
			add("时钟偏移：检查受影响 mon 节点的时间同步状态")
		}

		// 告警明细里点名了具体 OSD 的，直接给出可执行的下一步。
		for _, detail := range c.Details {
			if id, ok := extractOSDID(detail); ok {
				add(fmt.Sprintf("告警明细提到 osd.%d，可用 osd_detail(osd_id=%d) 获取其节点与磁盘", id, id))
			}
		}
	}

	if len(hints) == 0 {
		add("按告警代码在本集群语义下逐项排查；若工具无法覆盖，请在 unknowns 中说明缺少什么")
	}
	return hints
}

// detailMessage 从一条明细里取出可读文本。
// 不同 Ceph 版本里 detail 可能是 {"message": "..."} 也可能是裸字符串。
func detailMessage(raw json.RawMessage) string {
	var obj struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Message != "" {
		return strings.TrimSpace(obj.Message)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

// extractOSDID 从文本中找出形如 osd.13 的 OSD 编号。
func extractOSDID(s string) (int, bool) {
	idx := strings.Index(s, "osd.")
	if idx < 0 {
		return 0, false
	}
	rest := s[idx+len("osd."):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	id := 0
	for _, r := range rest[:end] {
		id = id*10 + int(r-'0')
	}
	return id, true
}
