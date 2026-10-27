package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// Profile 描述一种存储系统需要怎样诊断。
//
// 核心循环（SSH 接入、只读策略、工具调用、证据链、对话）与具体存储无关；
// 与 Ceph 强相关的只有四件事：怎么判断集群类型、启动时采集什么、
// 提供哪些工具、给模型什么领域提示。把这四件事抽出来，
// 换一套存储系统只需要新增一个 Profile，而不是改核心。
type Profile interface {
	// Name 是 profile 名，用于 --profile 与日志。
	Name() string
	// Detect 判断目标集群是否属于本类型。只做只读探测。
	Detect(ctx context.Context, c Collector) bool
	// Snapshot 采集会话开始时的背景信息与证据。
	Snapshot(ctx context.Context, c Collector, srcDir string) (Snapshot, error)
	// Tools 返回该存储系统的诊断工具。
	Tools(r tools.Runner, srcDir string) []tools.Tool
	// Hints 返回追加到提示词末尾的领域说明。
	Hints(s Snapshot) string
}

// profileRegistry 保存已注册的 profile，按注册顺序尝试检测。
var profileRegistry []Profile

// RegisterProfile 注册一个 profile。重复注册同名会覆盖。
func RegisterProfile(p Profile) {
	for i, existing := range profileRegistry {
		if existing.Name() == p.Name() {
			profileRegistry[i] = p
			return
		}
	}
	profileRegistry = append(profileRegistry, p)
}

// ResolveProfile 按名称或自动检测选择 profile。
//
// name 为空或 "auto" 时按注册顺序检测；都不匹配则使用 generic。
func ResolveProfile(ctx context.Context, name string, c Collector) (Profile, error) {
	name = strings.TrimSpace(name)
	if name != "" && name != "auto" {
		for _, p := range profileRegistry {
			if p.Name() == name {
				return p, nil
			}
		}
		return nil, fmt.Errorf("未知的 profile %q，可选：%s", name, strings.Join(profileNames(), "、"))
	}

	for _, p := range profileRegistry {
		if p.Name() == "generic" {
			continue // 兜底，最后再用
		}
		if p.Detect(ctx, c) {
			return p, nil
		}
	}
	for _, p := range profileRegistry {
		if p.Name() == "generic" {
			return p, nil
		}
	}
	return nil, fmt.Errorf("没有可用的 profile")
}

func profileNames() []string {
	out := make([]string, 0, len(profileRegistry))
	for _, p := range profileRegistry {
		out = append(out, p.Name())
	}
	sort.Strings(out)
	return out
}

// GenericProfile 是"不认识的存储系统"。
//
// 它的存在本身就是答案：遇到 JuiceFS / CPFS / 自研文件系统这类
// 没有专用 Profile 的集群，仍然能接入、仍然能做只读探索——
// 只是没有领域预判，需要模型自己先摸清系统里有什么。
type GenericProfile struct{}

func (GenericProfile) Name() string { return "generic" }

func (GenericProfile) Detect(context.Context, Collector) bool { return true }

// Snapshot 只做通用探索：能力探测 + 当前运行的进程。
//
// 不猜测任何存储系统的命令；把"系统上跑着什么"交给模型自己判断。
func (GenericProfile) Snapshot(ctx context.Context, c Collector, _ string) (Snapshot, error) {
	var s Snapshot
	nodes := c.Nodes()
	if len(nodes) == 0 {
		return s, fmt.Errorf("未能确定任何节点")
	}
	entry := nodes[0]
	s.Capabilities = c.Capabilities(ctx, entry)

	// 进程列表是判断"这是什么存储系统"最直接的线索。
	argv := []string{"ps", "-eo", "comm="}
	if out, err := c.Run(ctx, entry, argv); err == nil {
		s.RunningProcesses = summarizeProcesses(out.Stdout)
		s.Evidence = append(s.Evidence, evidenceFrom(c, entry, argv, out, nil, "snapshot:ps"))
	}
	s.Summary = "未能自动识别存储系统类型，以下为通用探测结果"
	assignEvidenceIDs(&s)
	return s, nil
}

func (GenericProfile) Tools(r tools.Runner, _ string) []tools.Tool {
	// 只给不依赖具体存储系统的工具：宿主机、日志、目录、任意只读命令。
	return []tools.Tool{
		tools.NewHostCheck(r),
		tools.NewHostIO(r),
		tools.NewReadLog(r),
		tools.NewExplore(r),
		tools.NewRunCommand(r),
	}
}

func (GenericProfile) Hints(Snapshot) string {
	return `## 关于这套系统

没有识别出这是哪种存储系统。不要套用 Ceph / 其他系统的经验去猜它的状态。

先摸清楚环境：运行着什么进程（上面已列出）、有哪些数据目录、服务怎么管理，
再从用户描述的症状出发，用 run_command 查真实的输出。

如果用户在问一个具体的存储系统（如 JuiceFS、CPFS），先确认相关进程/服务是否存在，
再查它自己的状态命令。`
}

// RunningProcesses 之外，profile 也可往 Snapshot 里补充字段。
func summarizeProcesses(out string) string {
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	const max = 60
	if len(names) > max {
		names = names[:max]
	}
	return strings.Join(names, " ")
}

var _ = access.Capabilities{}
