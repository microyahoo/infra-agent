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
	ClusterID    string
	Health       string
	Summary      string
	Capabilities access.Capabilities
	Topology     *access.Topology
	Failures     []string
}

// SystemPrompt 根据集群快照与用户问题拼装系统提示词。
func SystemPrompt(s Snapshot, question string) string {
	var b strings.Builder

	b.WriteString(promptRole)
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
