package doctor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
)

// Snapshot 是诊断开始前对集群的只读快照，用于拼装系统提示词。
//
// 快照只提供「起点信息」，不替代证据：模型若要引用集群状态，仍需自行调用工具
// 取得证据 ID，从而保证最终报告里的命令与输出都能溯源。
type Snapshot struct {
	ClusterID string
	Health    string
	Summary   string
	// HealthDetail 是 ceph health detail 的原文（非健康状态下采集）。
	// 必须进提示词：模型只有知道"到底哪条告警、数值多大"，
	// 才能直接朝那个方向查，而不是把所有工具跑一遍去碰。
	HealthDetail string
	// RecentHealthEvents 是最近日志中的健康变更记录。
	// 当前采样为 HEALTH_OK 但用户报告异常时，这是唯一的症状来源。
	RecentHealthEvents string
	Capabilities       access.Capabilities
	Topology           *access.Topology
	Failures           []string
}

// SystemPrompt 根据集群快照与用户问题拼装系统提示词。
func SystemPrompt(s Snapshot, question string) string {
	var b strings.Builder

	// 顺序很重要：弱模型只认真读前面几百个 token。
	// 用户问题与症状放最前，规则与背景在后。
	b.WriteString(promptRole)
	b.WriteString("\n\n")
	b.WriteString(renderMission(s, question))
	b.WriteString("\n\n")
	b.WriteString(promptRules)
	b.WriteString("\n\n")
	b.WriteString(renderCluster(s))
	b.WriteString("\n\n")
	b.WriteString(renderCapabilities(s.Capabilities))
	b.WriteString("\n\n")
	b.WriteString(renderTopology(s.Topology))
	b.WriteString("\n\n")
	if len(s.Failures) > 0 {
		b.WriteString(renderFailures(s.Failures))
		b.WriteString("\n\n")
	}
	q := strings.TrimSpace(question)
	if q == "" {
		q = defaultQuestion
	}
	b.WriteString("## 用户问题\n\n")
	b.WriteString(q)
	b.WriteString("\n\n")
	b.WriteString("按上面的调查协议开始：")
	b.WriteString("\n\n1. 先确认「哪里不对」——集群不是 HEALTH_OK 时必须先调 ceph_health 看具体告警；")
	b.WriteString("\n2. 针对告警形成具体假设，再用最少的工具调用逐个验证；")
	b.WriteString("\n3. 用工具返回的 data 与 next_hints 决定下一步，不要盲目跑全套检查；")
	b.WriteString("\n4. 调查完成后调用 " + loop.ConcludeToolName + " 提交结论。")

	return b.String()
}

const promptRole = `# 角色

你是一名资深的 Ceph 存储集群诊断专家，精通 RADOS、MON、OSD、MGR、MDS 各组件的运行机制，
以及底层磁盘、网络与主机层面的常见故障模式。
你的任务是在只读调查的前提下，基于真实证据定位问题的根因，并给出可执行的处理建议。`

const promptRules = `## 调查协议（必须按此执行）

你不是一个"跑一遍标准检查然后输出模板"的脚本。你是一个调查者：
从症状出发，形成假设，用工具验证，直到能解释症状或确认无法解释。

### 第 0 步：确定"到底哪里不对"

- 集群不是 HEALTH_OK 时，**第一件事必须调用 ceph_health**。
  ceph_status 只告诉你健康码和告警代码；具体是什么问题、影响哪些 OSD/PG/池，
  只有 ceph_health 能看到。**不调用 ceph_health 就得出的"一切正常"结论一律无效。**
- 用户描述了具体现象（延迟高、pod 重启、某块盘报错等）时，先把现象转成一个可验证的问题。
- 如果用户没有说明现象、集群又是 HEALTH_OK，你可以做一轮全面体检，
  但必须在 unknowns 里写明"用户未提供具体症状，本次为无目标的常规检查"。

### 第 1 步：形成假设

基于第 0 步看到的具体告警，列出 2-4 个可能的原因。例如：
- 告警是 Slow OSD heartbeat → 假设可能是：网络抖动 / 节点负载高 / OSD 进程卡顿 /
  磁盘响应慢导致心跳延迟。**注意心跳慢通常不是磁盘问题，不要下意识去查 iostat。**
- 告警是 PG degraded → 假设可能是：OSD down / 网络分区 / 磁盘故障。

把你正在验证的假设写进 submitted conclusion 的 ruled_out（排除的）或结论主体（确认的）。

### 第 1.5 步：工具只是起点，不是边界

预设工具覆盖的是常见路径。**遇到预设工具覆盖不到的情况，用 run_command 自己构造查询**：

- 不知道日志/数据在哪 → 先 explore 探目录，不要猜路径
- 需要 Ceph 的其它视图 → run_command(["ceph","pg","dump","pools","-f","json"])、
  quorum_status、osd blocklist ls、fs status、auth list……
- 需要 RGW / CephFS / 具体子系统 → radosgw-admin bucket list、ceph fs status 等
- 需要主机层细节 → run_command 跑 ethtool -S、chronyc tracking、pidstat、ss -s、journalctl
- 任何你怀疑相关的只读命令都可以试。**被策略拒绝会告诉你原因，不会导致故障。**

不要因为"没有对应的预设工具"就放弃这条线索，也不要反复用同一个预设工具查同样的东西。

### 第 2 步：用最少的调用验证假设

- 按假设选择工具，而不是把所有工具都跑一遍。
- 工具结果的 next_hints 字段给出了下一步方向，优先参考它。
- 工具结果的 data 字段包含结构化细节（OSD 编号、节点、磁盘、数值），
  用它来构造后续调用的精确参数，而不是猜。
- 一次工具调用能回答的问题，不要拆成三次。但也不要为了省钱跳过关键验证。

### 第 3 步：收尾

- 能解释症状 → 写下根因、证据、影响、建议。
- 不能解释 → 明确说明"已排除 X、Y、Z，怀疑 A 但缺少 B 证据"，并写入 unknowns。
  **这比编造一个结论有价值得多。**

## 硬性规则（必须遵守）

1. **只读**：你只能执行查询类工具。请**不能变更**集群状态；
   需要变更时，只能写在 recommendations 里交由人工执行。
2. **只用提供的工具**：所有事实必须来自工具返回的结果。不要臆测命令输出。
3. **结论必须走 submit_conclusion**，不要用自由文本直接回答。
4. **引用必须真实**：evidence_refs 必须是本次调查中真实返回的证据 ID（形如 ev-003），
   不得编造。ruled_out 中的 ref 同理。
5. **证据不足时写入 unknowns**，而不是凭经验猜测。
6. **不要重复调用**：相同工具、相同参数已调用过时结果不会变化。
7. **不要用"未发现异常"掩盖"没能观测"**：某个工具调用失败时，
   它是"没有数据"，不是"没有问题"。必须在结论里区分这两种情况。`

// renderMission 把「这次要解决什么」放在提示词最前面。
//
// 弱模型对中后段的指令遵循很差：健康明细如果排在第 4000 个字符之后，
// 基本等于没给。这里把用户问题与已知告警提到最前，让第一轮就有明确靶子。
func renderMission(s Snapshot, question string) string {
	var b strings.Builder
	b.WriteString("## 本次任务\n\n")

	q := strings.TrimSpace(question)
	if q == "" {
		q = defaultQuestion
	}
	b.WriteString(q)
	b.WriteString("\n\n")

	health := strings.TrimSpace(s.Health)
	detail := strings.TrimSpace(s.HealthDetail)

	switch {
	case detail != "":
		b.WriteString("## 已知症状（快照已采集，直接据此定向排查）\n\n```\n")
		b.WriteString(detail)
		b.WriteString("\n```\n\n")
		b.WriteString("**第一个动作就应该验证上面这条告警**：确认它涉及哪些 OSD/节点，" +
			"再查这些对象的相关指标。不要先把 ceph_status / osd_tree / pool_info 之类全跑一遍。\n")

	case health == "HEALTH_OK" && strings.TrimSpace(s.RecentHealthEvents) != "":
		b.WriteString("## 已知症状（来自最近的集群日志）\n\n")
		b.WriteString("快照时刻健康码为 HEALTH_OK，但最近日志里出现了健康变更——说明告警是**间歇性**的，刚刚发生过：\n\n```\n")
		b.WriteString(s.RecentHealthEvents)
		b.WriteString("\n```\n\n")
		b.WriteString("**从这些历史记录里的 OSD 编号/节点名出发**：先 osd_detail 确认位置，" +
			"再 host_check / host_io 看该节点的负载与磁盘。不要忽略它们转而做全面体检。\n")

	case health == "HEALTH_OK":
		// 关键场景：用户说有问题，但快照这一刻是 OK。
		// 慢心跳、短时抖动这类告警是间歇性的，采样瞬间抓不到。
		// 不点破这一点，模型会认为"集群没事"然后漫无目的地全量查询。
		b.WriteString("## 已知症状\n\n")
		b.WriteString("快照时刻健康码为 **HEALTH_OK**。\n\n")
		if mentionsSymptom(q) {
			b.WriteString("但用户报告了异常。这类告警（如 Slow OSD heartbeats、短时 slow ops）通常是**间歇性**的，" +
				"采样瞬间可能已经恢复。\n\n")
			b.WriteString("**不要因为当前是 OK 就判断一切正常。** 先查历史：\n")
			b.WriteString("- `run_command([\"ceph\",\"log\",\"last\",\"300\"])` 找最近的 health 变更与告警记录\n")
			b.WriteString("- `daemon_log` 看相关 OSD/mon 最近日志中的慢心跳/慢请求\n")
			b.WriteString("- 告警里常带 OSD 编号或节点名，据此 `osd_detail` → `host_check`/`host_io` 定向下钻\n")
		}

	default:
		b.WriteString("## 已知症状\n\n")
		b.WriteString(fmt.Sprintf("快照时刻健康码为 `%s`，但未取到健康明细。\n\n", orUnknown(health)))
		if mentionsSymptom(q) {
			b.WriteString("先调用 `ceph_health` 拿到具体告警项，再定向排查。\n")
		}
	}
	return b.String()
}

// mentionsSymptom 判断用户问题里是否描述了异常。
// 只用于决定提示词里要不要强调"间歇性告警"这类引导。
func mentionsSymptom(q string) bool {
	lower := strings.ToLower(q)
	for _, kw := range []string{
		"warn", "error", "err", "异常", "问题", "故障", "慢", "卡", "告警", "失败",
		"down", "slow", "degraded", "不健康", "检查", "排查", "怎么回事", "为啥",
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// orUnknown 返回占位值。
func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// renderCluster 输出集群基础信息块。
func renderCluster(s Snapshot) string {
	var b strings.Builder
	b.WriteString("## 集群快照\n\n")

	fsid := strings.TrimSpace(s.ClusterID)
	if fsid == "" {
		fsid = "未知"
	}
	health := strings.TrimSpace(s.Health)
	if health == "" {
		health = "unknown"
	}

	b.WriteString(fmt.Sprintf("- FSID：%s\n", fsid))
	b.WriteString(fmt.Sprintf("- 健康状态：%s\n", health))
	if summary := strings.TrimSpace(s.Summary); summary != "" {
		b.WriteString(fmt.Sprintf("- 概要：%s\n", summary))
	}

	// 健康明细是调查的起点。把它直接给出，模型才能针对具体告警下钻，
	// 而不是先跑一遍所有工具去猜问题在哪。
	if detail := strings.TrimSpace(s.HealthDetail); detail != "" {
		b.WriteString("\n### 健康告警明细（已采集，不要再调用 ceph_health 重复获取）\n\n")
		b.WriteString("```\n")
		b.WriteString(detail)
		b.WriteString("\n```\n")
		b.WriteString("\n**先针对上面这条告警形成假设并定向验证**，不要一上来就把所有诊断工具都跑一遍。\n")
	}

	b.WriteString("\n快照仅供参考，正式结论必须引用你自己调用工具后产生的证据 ID。")

	return b.String()
}

// renderCapabilities 输出能力清单，明确告诉模型「能做什么、不能做什么」。
func renderCapabilities(c access.Capabilities) string {
	var b strings.Builder
	b.WriteString("## 环境能力\n\n")

	backend := strings.TrimSpace(c.Backend)
	if backend == "" {
		backend = "unknown"
	}
	version := strings.TrimSpace(c.CephVersion)
	if version == "" {
		version = "未知"
	}
	cephCLI := "无"
	if c.CephCLI {
		cephCLI = "有"
	}
	b.WriteString(fmt.Sprintf("- ceph CLI：%s（后端 %s，版本 %s）\n", cephCLI, backend, version))
	b.WriteString(fmt.Sprintf("- kubectl：%s\n", yesNo(c.Kubectl)))

	rt := strings.TrimSpace(c.ContainerRT)
	if rt == "" {
		rt = "none"
	}
	b.WriteString(fmt.Sprintf("- 容器运行时：%s\n", rt))

	if len(c.LogDirs) > 0 {
		dirs := append([]string{}, c.LogDirs...)
		sort.Strings(dirs)
		b.WriteString(fmt.Sprintf("- 日志目录：%s\n", strings.Join(dirs, "、")))
	} else {
		b.WriteString("- 日志目录：无\n")
	}

	if len(c.HostTools) > 0 {
		var tools []string
		for name, ok := range c.HostTools {
			if ok {
				tools = append(tools, name)
			}
		}
		sort.Strings(tools)
		if len(tools) > 0 {
			b.WriteString(fmt.Sprintf("- 可用主机工具：%s\n", strings.Join(tools, "、")))
		} else {
			b.WriteString("- 可用主机工具：无\n")
		}
	} else {
		b.WriteString("- 可用主机工具：无\n")
	}

	if !c.CephCLI {
		b.WriteString("\n注意：当前没有可用的 ceph CLI，ceph 类查询工具不可用。" +
			"请以日志（daemon_log、read_log）与主机检查（host_check）为主要数据来源，" +
			"无法覆盖的部分请写入 unknowns。")
	}

	return b.String()
}

// renderTopology 输出节点与 OSD 分布。
func renderTopology(t *access.Topology) string {
	var b strings.Builder
	b.WriteString("## 节点与 OSD\n\n")

	if t == nil || (len(t.Nodes) == 0 && len(t.OSDs) == 0) {
		b.WriteString("拓扑未知（发现失败或暂不支持），可用 osd_tree 工具尝试获取。")
		return b.String()
	}

	if len(t.Nodes) > 0 {
		names := make([]string, 0, len(t.Nodes))
		for _, n := range t.Nodes {
			name := n.Name
			if name == "" {
				name = n.Host
			}
			if name == "" {
				name = "未命名节点"
			}
			names = append(names, name)
		}
		b.WriteString(fmt.Sprintf("- 节点（%d 个）：%s\n", len(names), strings.Join(names, "、")))
	}

	if len(t.OSDs) > 0 {
		perHost := make(map[string]int)
		var order []string
		for _, o := range t.OSDs {
			host := o.Host
			if host == "" {
				host = "未知主机"
			}
			if _, ok := perHost[host]; !ok {
				order = append(order, host)
			}
			perHost[host]++
		}
		var parts []string
		for _, h := range order {
			parts = append(parts, fmt.Sprintf("%s(%d)", h, perHost[h]))
		}
		b.WriteString(fmt.Sprintf("- OSD（%d 个）：%s\n", len(t.OSDs), strings.Join(parts, "、")))

		// 只列出前若干条 OSD→设备映射，避免提示词过长。
		const maxListed = 20
		listed := t.OSDs
		truncated := false
		if len(listed) > maxListed {
			listed = listed[:maxListed]
			truncated = true
		}
		var osdParts []string
		for _, o := range listed {
			dev := o.Device
			if dev == "" {
				dev = o.RawDevice
			}
			if dev == "" {
				dev = "未知设备"
			}
			status := o.Status
			if status == "" {
				status = "unknown"
			}
			osdParts = append(osdParts, fmt.Sprintf("osd.%d@%s:%s(%s)", o.ID, o.Host, dev, status))
		}
		b.WriteString(fmt.Sprintf("- OSD 位置：%s", strings.Join(osdParts, "，")))
		if truncated {
			b.WriteString(fmt.Sprintf(" …（共 %d 个，仅列前 %d 个）", len(t.OSDs), maxListed))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// renderFailures 输出快照阶段的失败项，提醒模型相关数据缺失。
func renderFailures(failures []string) string {
	var b strings.Builder
	b.WriteString("## 快照阶段的失败\n\n")
	b.WriteString("以下信息在诊断开始前未能获取，相关判断可能缺少依据，请优先补查或写入 unknowns：\n\n")
	for _, f := range failures {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(f))
		b.WriteString("\n")
	}
	return b.String()
}

// yesNo 把布尔值转为中文。
func yesNo(v bool) string {
	if v {
		return "有"
	}
	return "无"
}
