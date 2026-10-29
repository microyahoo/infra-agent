package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor"
	"github.com/spf13/cobra"
)

// doctorOptions 是 doctor 命令的全部参数。
type doctorOptions struct {
	Host             string
	Port             int
	User             string
	Key              string
	Password         string
	KnownHosts       string
	CephSrc          string
	Question         string
	Timeout          int
	MaxRounds        int
	MaxToolCalls     int
	MaxPerTool       int
	MaxCallsPerRound int
	Model            string
	BaseURL          string
	APIKeyEnv        string
	SSHTimeout       int
	ProbeTimeout     int
	MaxTokens        int
	LLMTimeout       int
	LLMRetries       int
	DryRun           bool
	SelfCheck        bool
	Interactive      bool
}

var doctorOpts doctorOptions

// doctorCmd represents the doctor command
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "通过 SSH 调查 Ceph 集群（对话式）",
	Long: `通过 SSH 只读访问 Ceph 集群，像和运维同事对话一样查问题。

交互模式（默认进入）下直接说你想查什么；助手会自己决定查什么、查多深，
然后基于查到的东西回答。可以追问、可以质疑它上一轮的结论。

全程只读：变更类命令会被拒绝。

示例:
  # 交互式调查
  infra-agent doctor --host 10.0.0.1 --key ~/.ssh/id_rsa --interactive

  # 问一个问题、拿一个回答，然后退出
  infra-agent doctor --host 10.0.0.1 --key ~/.ssh/id_rsa \
    --question "集群为什么 warn 了"

  # 只验证 SSH 与探测结果，不消耗模型额度
  infra-agent doctor --host 10.0.0.1 --key ~/.ssh/id_rsa --dry-run
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

	f.StringVar(&doctorOpts.Question, "question", "", "单次提问的内容（配合 --interactive=false）")
	f.IntVar(&doctorOpts.Timeout, "timeout", 600, "总超时（秒）")
	f.IntVar(&doctorOpts.MaxRounds, "max-rounds", 12, "单次提问内最多的模型往返轮数")
	f.IntVar(&doctorOpts.MaxToolCalls, "max-tool-calls", 20, "单次提问内最多的工具调用数")
	f.IntVar(&doctorOpts.MaxPerTool, "max-per-tool", 5, "单个工具的最大调用次数")
	f.IntVar(&doctorOpts.MaxCallsPerRound, "max-calls-per-round", 2, "单轮并行的工具调用上限")
	f.StringVar(&doctorOpts.Model, "model", "", "模型名，默认读配置")
	f.StringVar(&doctorOpts.BaseURL, "base-url", "", "OpenAI 兼容地址，默认读配置")
	f.StringVar(&doctorOpts.APIKeyEnv, "api-key-env", "OPENAI_API_KEY", "提供 API Key 的环境变量名")
	f.IntVar(&doctorOpts.SSHTimeout, "ssh-timeout", 30, "单条命令超时（秒）")
	f.BoolVar(&doctorOpts.DryRun, "dry-run", false, "仅连接并探测能力与拓扑，不调用模型")
	f.BoolVar(&doctorOpts.SelfCheck, "self-check", false, "在真实集群上逐个运行诊断工具（不调用模型），用于验证解析是否正确")
	f.BoolVar(&doctorOpts.Interactive, "interactive", true, "进入交互模式（用 --interactive=false 改为单次提问）")
	f.IntVar(&doctorOpts.LLMRetries, "llm-retries", -1, "模型请求失败时的重试次数（-1 表示用默认值 2）")
	f.IntVar(&doctorOpts.MaxTokens, "max-tokens", 8192, "单次模型回复的最大 token 数（0 表示交给服务端默认值）")
	f.IntVar(&doctorOpts.LLMTimeout, "llm-timeout", 120, "单次模型请求超时（秒）")
	f.IntVar(&doctorOpts.ProbeTimeout, "probe-timeout", 8, "能力探测与拓扑发现的单条命令超时（秒）")

	_ = doctorCmd.MarkFlagRequired("host")
}

func runDoctor(cmd *cobra.Command, args []string) error {
	// 0. 合并配置：命令行 > 配置文件 > 内置默认值。
	opt := resolveDoctorOptions(cmd)

	// 1. 认证方式检查放在最前，避免连上之后再报错。
	if opt.PrivateKey == "" && opt.Password == "" {
		return fmt.Errorf("必须提供 SSH 认证方式：--key（私钥路径）和 --password（密码）均未设置，" +
			"可在配置文件的 doctor.ssh 段中设置")
	}

	// 2. 非 --dry-run 时读取 API Key；放在建连之前，参数错误无需等待网络。
	//    交互模式下延迟到第一次提问再校验：用户可能只是进去看看 :help 就退出，
	//    不该因为没配 key 连界面都进不去。
	apiKey := ""
	needKey := !opt.DryRun && !opt.SelfCheck
	if needKey {
		apiKey = opt.APIKey
		if apiKey == "" {
			apiKey = os.Getenv(opt.APIKeyEnv)
		}
		if apiKey == "" && !opt.Interactive {
			return fmt.Errorf("未找到 API Key：请设置环境变量 %s，或在配置文件 doctor.llm.api_key 中填写", opt.APIKeyEnv)
		}
	}
	model := opt.Model
	baseURL := opt.BaseURL

	// 4. 连接阶段用总超时；交互模式下每轮另设新预算，见 runInteractive。
	//    不能整个进程共用一个会过期的 context：首轮一旦跑满，
	//    后续每次追问都会立刻 context deadline exceeded。
	ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(opt.Timeout)*time.Second)
	defer cancel()

	// 5. 连接接入层。
	fmt.Fprintf(os.Stderr, "🔌 连接 %s ...\n", opt.Host)
	cluster, err := access.Dial(ctx, access.Config{
		Host:           opt.Host,
		Port:           opt.Port,
		User:           opt.User,
		Password:       opt.Password,
		PrivateKeyPath: opt.PrivateKey,
		KnownHostsPath: opt.KnownHosts,
		CommandTimeout: time.Duration(opt.CommandTimeout) * time.Second,
		ExtraBinaries:  opt.ExtraBinaries,
		ProbeTimeout:   time.Duration(opt.ProbeTimeout) * time.Second,
		// 探测阶段的逐条日志只在 --dry-run 时有用；正常诊断里刷七行
		// 会让人以为在重复做初始化。
	})
	if err != nil {
		return fmt.Errorf("接入失败: %w", err)
	}
	defer cluster.Close()
	fmt.Fprintf(os.Stderr, "✓ SSH 连接已建立\n")

	if opt.DryRun {
		return runDoctorDryRun(ctx, cluster)
	}
	if opt.SelfCheck {
		return runSelfCheck(ctx, cluster)
	}

	// 6. 组装调查助手。
	//    交互模式下延迟到用户真正提问时才建立，避免"进来什么都没问"
	//    就消耗一次探测、拓扑与模型客户端构造。
	newAgent := func(key string) (*doctor.Agent, error) {
		return openAgent(ctx, cluster, llm.NewClient(llm.Config{
			BaseURL:     baseURL,
			APIKey:      key,
			Model:       model,
			Temperature: opt.Temperature,
			MaxTokens:   opt.MaxTokens,
			Timeout:     time.Duration(opt.LLMTimeout) * time.Second,
			MaxRetries:  opt.LLMRetries,
		}), opt)
	}

	if opt.Interactive {
		return runInteractive(cmd.Context(), newAgent, apiKey, needKey, opt)
	}

	// 非交互：一条问题、一次回答，然后退出。
	if needKey && apiKey == "" {
		return fmt.Errorf("未找到 API Key：请设置环境变量 %s，或在配置文件 doctor.llm.api_key 中填写", opt.APIKeyEnv)
	}
	a, err := newAgent(apiKey)
	if err != nil {
		return err
	}
	question := strings.TrimSpace(opt.Question)
	if question == "" {
		question = "简单说明一下这个集群现在的状态，有异常就说异常。"
	}
	reply, err := a.Send(ctx, question)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, reply)
	return nil
}

// openAgent 建立会话：探测能力、发现拓扑、取一次健康快照，然后构造助手。
func openAgent(ctx context.Context, cluster *access.Cluster, model llm.ChatModel, opt resolvedDoctorOptions) (*doctor.Agent, error) {
	fmt.Fprintln(os.Stderr, "🔎 采集集群信息 ...")

	agent, snap, err := doctor.Open(ctx, doctor.OpenConfig{
		Cluster:          cluster,
		Model:            model,
		SourceDir:        opt.CephSrc,
		MaxRounds:        opt.MaxRounds,
		MaxCallsPerRound: opt.MaxCallsPerRound,
		MaxCallsPerTurn:  opt.MaxToolCalls,
		OnEvent:          func(msg string) { fmt.Fprintf(os.Stderr, "  · %s\n", msg) },
	})
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "✓ %s｜%s\n", snap.Health, snap.Summary)
	if snap.PodHealth != "" {
		fmt.Fprintf(os.Stderr, "  pod：%s\n", firstLine(snap.PodHealth))
	}
	if len(snap.Findings) > 0 {
		fmt.Fprintf(os.Stderr, "  初步判断：%s\n", snap.Findings[0].Summary)
	}
	return agent, nil
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
	fmt.Fprintf(os.Stderr, "  → 后端 %s，ceph CLI %s\n", orUnknown(caps.Backend), yesNo(caps.CephCLI))

	fmt.Fprintf(os.Stderr, "🔎 发现集群拓扑 ...\n")
	topo, topoErr := cluster.Topology(ctx)
	if topoErr != nil {
		fmt.Fprintf(os.Stderr, "  → 拓扑发现失败：%v\n", topoErr)
	} else if topo != nil {
		fmt.Fprintf(os.Stderr, "  → %d 个节点，%d 个 OSD\n", len(topo.Nodes), len(topo.OSDs))
	}

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
			fmt.Fprintf(&b, "  - %s（主机 %s，OSD %d）\n",
				n.Name, orUnknown(n.Host), len(n.OSDIDs))
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

// firstLine 取多行文本的第一行，用于紧凑的进度显示。
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
