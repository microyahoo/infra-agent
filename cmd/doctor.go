package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor"
	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor/report"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// doctorExitFindings 是在诊断发现严重问题时使用的退出码。
// 报告会先打印到 stdout，再直接以此退出码结束进程，避免错误被重复输出。
const doctorExitFindings = 2

// doctorOptions 是 doctor 命令的全部参数。
type doctorOptions struct {
	Host         string
	Port         int
	User         string
	Key          string
	Password     string
	KnownHosts   string
	CephSrc      string
	Format       string
	Question     string
	Timeout      int
	MaxRounds    int
	MaxToolCalls int
	Model        string
	BaseURL      string
	APIKeyEnv    string
	SSHTimeout   int
	LLMRetries   int
	DryRun       bool
}

var doctorOpts doctorOptions

// doctorCmd represents the doctor command
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "通过 SSH 自主诊断 Ceph 集群",
	Long: `通过 SSH 只读访问 Ceph 集群，由 AI 自主调查并给出一份可溯源的诊断报告。

命令会先探测节点的能力（宿主机 ceph CLI、rook toolbox、容器 exec 或仅日志），
发现集群拓扑并做一次只读快照，随后驱动模型按需调用诊断工具收集证据，
最终产出一份包含证据链、已排除假设与处理建议的报告。

需要提供 SSH 私钥（--key）或密码（--password）之一，并通过环境变量提供模型 API Key。

退出码:
  0  未发现 high/critical 级问题
  1  运行错误（接入失败、模型调用失败等）
  2  诊断完成且发现 critical/high 级问题（报告已打印到 stdout）

示例:
  # 全面体检
  infra-agent doctor --host 10.0.0.1 --key ~/.ssh/id_rsa

  # 指定问题与源码目录，便于结合源码定位
  infra-agent doctor --host 10.0.0.1 --key ~/.ssh/id_rsa \
    --question "为什么 PG 一直处于 active+undersized?" --ceph-src ~/ceph

  # 先验证 SSH 与探测结果，不消耗模型额度
  infra-agent doctor --host 10.0.0.1 --key ~/.ssh/id_rsa --dry-run

  # 输出 JSON 供下游处理
  infra-agent doctor --host 10.0.0.1 --key ~/.ssh/id_rsa --format json
`,
	RunE: runDoctor,
	// 运行期错误（接入失败、模型失败）只需打印错误本身；
	// 参数错误由 cobra 统一提示，用户可通过 --help 查看完整用法。
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(doctorCmd)

	f := doctorCmd.Flags()
	f.StringVar(&doctorOpts.Host, "host", "", "首节点 IP")
	f.IntVar(&doctorOpts.Port, "port", 22, "SSH 端口")
	f.StringVar(&doctorOpts.User, "user", "root", "SSH 用户")
	f.StringVar(&doctorOpts.Key, "key", "", "私钥路径")
	f.StringVar(&doctorOpts.Password, "password", "", "SSH 密码（不推荐）")
	f.StringVar(&doctorOpts.KnownHosts, "known-hosts", "", "known_hosts 路径，留空不校验")
	f.StringVar(&doctorOpts.CephSrc, "ceph-src", "", "本地 Ceph 源码目录")
	f.StringVar(&doctorOpts.Format, "format", "text", "报告格式 (text, brief, json)")
	f.StringVar(&doctorOpts.Question, "question", "", "自定义诊断问题，留空做全面体检")
	f.IntVar(&doctorOpts.Timeout, "timeout", 600, "总超时（秒）")
	f.IntVar(&doctorOpts.MaxRounds, "max-rounds", 20, "最大调查轮数")
	f.IntVar(&doctorOpts.MaxToolCalls, "max-tool-calls", 60, "最大工具调用数")
	f.StringVar(&doctorOpts.Model, "model", "", "模型名，默认读配置")
	f.StringVar(&doctorOpts.BaseURL, "base-url", "", "OpenAI 兼容地址，默认读配置")
	f.StringVar(&doctorOpts.APIKeyEnv, "api-key-env", "OPENAI_API_KEY", "提供 API Key 的环境变量名")
	f.IntVar(&doctorOpts.SSHTimeout, "ssh-timeout", 30, "单条命令超时（秒）")
	f.BoolVar(&doctorOpts.DryRun, "dry-run", false, "仅连接并探测能力与拓扑，不调用模型")
	f.IntVar(&doctorOpts.LLMRetries, "llm-retries", -1, "模型请求失败时的重试次数（-1 表示用默认值 2）")

	_ = doctorCmd.MarkFlagRequired("host")
}

func runDoctor(cmd *cobra.Command, args []string) error {
	// 1. 认证方式检查放在最前，避免连上之后再报错。
	if doctorOpts.Key == "" && doctorOpts.Password == "" {
		return fmt.Errorf("必须提供 SSH 认证方式：--key（私钥路径）和 --password（密码）均未设置")
	}

	// 2. 非 --dry-run 时读取 API Key；放在建连之前，参数错误无需等待网络。
	apiKey := ""
	if !doctorOpts.DryRun {
		apiKey = os.Getenv(doctorOpts.APIKeyEnv)
		if apiKey == "" {
			return fmt.Errorf("未找到 API Key：请设置环境变量 %s", doctorOpts.APIKeyEnv)
		}
	}

	// 3. 模型与地址优先取命令行，其次读配置。
	model := doctorOpts.Model
	if model == "" {
		model = viper.GetString("doctor.llm.model")
	}
	baseURL := doctorOpts.BaseURL
	if baseURL == "" {
		baseURL = viper.GetString("doctor.llm.base_url")
	}

	// 格式在联网前校验，参数写错时立即报错；dry-run 不使用报告格式。
	if !doctorOpts.DryRun {
		if _, err := doctor.Render(&report.Report{}, doctor.Format(doctorOpts.Format)); err != nil {
			return err
		}
	}

	// 4. 超时以命令上下文为父，保证 Ctrl-C 能立即中断。
	ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(doctorOpts.Timeout)*time.Second)
	defer cancel()

	// 5. 连接接入层。
	fmt.Fprintf(os.Stderr, "🔌 连接 %s ...\n", doctorOpts.Host)
	cluster, err := access.Dial(ctx, access.Config{
		Host:           doctorOpts.Host,
		Port:           doctorOpts.Port,
		User:           doctorOpts.User,
		Password:       doctorOpts.Password,
		PrivateKeyPath: doctorOpts.Key,
		KnownHostsPath: doctorOpts.KnownHosts,
		CommandTimeout: time.Duration(doctorOpts.SSHTimeout) * time.Second,
	})
	if err != nil {
		return fmt.Errorf("接入失败: %w", err)
	}
	defer cluster.Close()
	fmt.Fprintf(os.Stderr, "✓ SSH 连接已建立\n")

	if doctorOpts.DryRun {
		return runDoctorDryRun(ctx, cluster)
	}

	// 6. 组装并运行诊断。
	modelName := model
	if modelName == "" {
		modelName = "默认模型"
	}
	fmt.Fprintf(os.Stderr, "🤖 开始诊断（模型 %s，最多 %d 轮 / %d 次工具调用）\n",
		modelName, doctorOpts.MaxRounds, doctorOpts.MaxToolCalls)

	d := doctor.New(doctor.Config{
		Cluster: cluster,
		// MaxRetries 用负数表示「采用默认值」。零值意味着不重试，
		// 一次 5xx 或限流就会让整次诊断白跑。--llm-retries 可覆盖。
		Model: llm.NewClient(llm.Config{
			BaseURL: baseURL, APIKey: apiKey, Model: model,
			MaxRetries: doctorOpts.LLMRetries,
		}),
		SourceDir:    doctorOpts.CephSrc,
		MaxRounds:    doctorOpts.MaxRounds,
		MaxToolCalls: doctorOpts.MaxToolCalls,
		OnProgress: func(msg string) {
			fmt.Fprintf(os.Stderr, "  · %s\n", msg)
		},
	})

	rep, err := d.Run(ctx, doctor.Task{Question: doctorOpts.Question})
	if err != nil {
		return err
	}

	// 7. 报告写 stdout，进度与提示写 stderr。
	formatted, err := doctor.Render(rep, doctor.Format(doctorOpts.Format))
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, formatted)

	// 8. 有严重问题时直接以退出码 2 结束：报告已打印，不再返回错误避免重复输出。
	if rank := report.SeverityRank(rep.Severity); rank >= report.SeverityRank("high") {
		fmt.Fprintf(os.Stderr, "\n⚠ 发现 %s 级问题（退出码 %d）\n", rep.Severity, doctorExitFindings)
		os.Exit(doctorExitFindings)
	}
	return nil
}

// runDoctorDryRun 只做能力探测与拓扑发现，打印摘要后返回，不调用模型。
func runDoctorDryRun(ctx context.Context, cluster *access.Cluster) error {
	nodes := cluster.Nodes()
	if len(nodes) == 0 {
		return fmt.Errorf("未能确定任何节点")
	}
	primary := nodes[0]

	fmt.Fprintf(os.Stderr, "🔎 探测节点能力 ...\n")
	caps := cluster.Capabilities(ctx, primary)

	fmt.Fprintf(os.Stderr, "🔎 发现集群拓扑 ...\n")
	topo, topoErr := cluster.Topology(ctx)

	var b strings.Builder
	fmt.Fprintf(&b, "SSH 接入验证（dry-run）\n")
	fmt.Fprintf(&b, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(&b, "入口节点: %s\n", primary)
	fmt.Fprintf(&b, "诊断后端: %s\n", orUnknown(caps.Backend))
	fmt.Fprintf(&b, "ceph CLI: %s\n", yesNo(caps.CephCLI))
	if caps.CephVersion != "" {
		fmt.Fprintf(&b, "ceph 版本: %s\n", caps.CephVersion)
	}
	fmt.Fprintf(&b, "kubectl: %s\n", yesNo(caps.Kubectl))
	if caps.ContainerRT != "" && caps.ContainerRT != "none" {
		fmt.Fprintf(&b, "容器运行时: %s\n", caps.ContainerRT)
	}
	fmt.Fprintf(&b, "日志目录: %s\n", listOrNone(caps.LogDirs))
	tools := make([]string, 0, len(caps.HostTools))
	for name, ok := range caps.HostTools {
		if ok {
			tools = append(tools, name)
		}
	}
	sort.Strings(tools)
	fmt.Fprintf(&b, "宿主机工具: %s\n", listOrNone(tools))

	if topoErr != nil {
		fmt.Fprintf(&b, "集群拓扑: 发现失败：%v\n", topoErr)
	} else {
		fmt.Fprintf(&b, "集群拓扑: fsid %s，节点 %d，OSD %d\n",
			orUnknown(topo.FSID), len(topo.Nodes), len(topo.OSDs))
		for _, n := range topo.Nodes {
			fmt.Fprintf(&b, "  - %s（主机 %s，OSD %d，角色 %s）\n",
				n.Name, orUnknown(n.Host), len(n.OSDIDs), listOrNone(n.Roles))
		}
	}
	fmt.Fprintf(&b, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(&b, "dry-run 结束：SSH 与探测已完成，未调用模型\n")

	fmt.Fprint(os.Stdout, b.String())
	return nil
}

// orUnknown 把空字符串显示为 unknown。
func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// yesNo 把布尔值转为中文可读文本。
func yesNo(ok bool) string {
	if ok {
		return "可用"
	}
	return "不可用"
}

// listOrNone 把列表拼接为逗号分隔文本，空列表显示为「无」。
func listOrNone(items []string) string {
	if len(items) == 0 {
		return "无"
	}
	return strings.Join(items, ", ")
}
