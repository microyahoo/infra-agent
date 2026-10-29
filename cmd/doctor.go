package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/microyahoo/infra-agent/internal/agent"
	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/internal/config"
	"github.com/microyahoo/infra-agent/internal/session"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// doctorFlagBindings 把命令行 flag 映射到配置键。
// host/port/key 等"覆盖所选集群"的 flag 由 overrideCluster 单独处理。
var doctorFlagBindings = map[string]string{
	"model":               "llm.model",
	"base-url":            "llm.base_url",
	"api-key-env":         "llm.api_key_env",
	"max-tokens":          "llm.max_tokens",
	"llm-timeout":         "llm.timeout",
	"llm-retries":         "llm.max_retries",
	"max-rounds":          "loop.max_rounds",
	"max-tool-calls":      "loop.max_tool_calls",
	"max-calls-per-round": "loop.max_calls_per_round",
	"timeout":             "run.timeout",
	"dry-run":             "run.dry_run",
	"self-check":          "run.self_check",
	"interactive":         "run.interactive",
	"ceph-src":            "ceph.source_dir",
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "通过 SSH 调查 Ceph 集群（对话式）",
	Long: `通过 SSH 只读访问 Ceph 集群，像和运维同事对话一样查问题。

交互模式（默认进入）下直接说你想查什么；助手会自己决定查什么、查多深，
然后基于查到的东西回答。可以追问、可以质疑它上一轮的结论。

全程只读：变更类命令会被拒绝。

示例:
  # 交互式调查
  infra-agent doctor --cluster stor-01

  # 问一个问题、拿一个回答，然后退出
  infra-agent doctor --cluster stor-01 --question "集群为什么 warn 了"

  # 只验证 SSH 与探测结果，不消耗模型额度
  infra-agent doctor --cluster stor-01 --dry-run
`,
	RunE:         runDoctor,
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	f := doctorCmd.Flags()

	f.String("cluster", "", "集群名（对应配置里的 clusters）")
	f.String("host", "", "首节点 IP（覆盖所选集群）")
	f.Int("port", 0, "SSH 端口（覆盖所选集群）")
	f.String("user", "", "SSH 用户（覆盖所选集群）")
	f.String("key", "", "私钥路径（覆盖所选集群）")
	f.String("password", "", "SSH 密码（覆盖所选集群）")
	f.String("known-hosts", "", "known_hosts 路径（覆盖所选集群）")
	f.Int("ssh-timeout", 0, "单条命令超时（秒，覆盖所选集群）")
	f.Int("probe-timeout", 0, "探测超时（秒，覆盖所选集群）")
	f.String("ceph-src", "", "本地 Ceph 源码目录")

	f.String("question", "", "单次提问的内容（配合 --interactive=false）")
	f.Int("timeout", 0, "总超时（秒）")
	f.Int("max-rounds", 0, "单次提问内最多的模型往返轮数")
	f.Int("max-tool-calls", 0, "单次提问内最多的工具调用数")
	f.Int("max-calls-per-round", 0, "单轮并行的工具调用上限")
	f.String("model", "", "模型名")
	f.String("base-url", "", "OpenAI 兼容地址")
	f.String("api-key-env", "", "提供 API Key 的环境变量名")
	f.Int("max-tokens", 0, "单次模型回复的最大 token 数")
	f.Int("llm-timeout", 0, "单次模型请求超时（秒）")
	f.Int("llm-retries", 0, "模型请求失败时的重试次数（-1 表示默认 2）")
	f.Bool("dry-run", false, "仅连接并探测能力与拓扑，不调用模型")
	f.Bool("self-check", false, "在真实集群上逐个运行诊断工具（不调用模型）")
	f.Bool("interactive", true, "进入交互模式")
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	v := viper.GetViper()
	if err := config.BindFlags(v, cmd.Flags(), doctorFlagBindings); err != nil {
		return err
	}
	cfg, err := config.Load(v, nil)
	if err != nil {
		return err
	}

	cl, err := cfg.Pick(stringFlag(cmd.Flags(), "cluster"))
	if err != nil {
		return err
	}
	cl = overrideCluster(cmd.Flags(), cl)

	ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(cfg.Run.Timeout)*time.Second)
	defer cancel()

	if cfg.Run.DryRun || cfg.Run.SelfCheck {
		sess, err := openSession(ctx, cfg, cl, false)
		if err != nil {
			return fmt.Errorf("接入失败: %w", err)
		}
		defer sess.Close()
		if cfg.Run.DryRun {
			return runDoctorDryRun(ctx, sess)
		}
		return runSelfCheck(ctx, sess)
	}

	fmt.Fprintf(os.Stderr, "🔌 连接 %s ...\n", cl.Host)

	if cfg.Run.Interactive {
		return runInteractive(ctx, cfg, func(c context.Context) (*agent.Agent, *session.Session, error) {
			return openAgent(c, cfg, cl)
		})
	}

	ag, sess, err := openAgent(ctx, cfg, cl)
	if err != nil {
		return err
	}
	defer sess.Close()

	question := strings.TrimSpace(stringFlag(cmd.Flags(), "question"))
	if question == "" {
		question = "简单说明一下这个集群现在的状态，有异常就说异常。"
	}
	reply, err := ag.Send(ctx, question)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, reply)
	return nil
}

// openSession 建立连接与工具表；collect 为真时再取一次会话快照。
func openSession(ctx context.Context, cfg config.Config, cl config.Cluster, collect bool) (*session.Session, error) {
	fmt.Fprintln(os.Stderr, "🔎 采集集群信息 ...")
	return session.Open(ctx, session.Options{
		Cluster:         cl,
		Policy:          cfg.Policy,
		SourceDir:       cfg.Ceph.SourceDir,
		CollectSnapshot: collect,
		OnEvent:         func(msg string) { fmt.Fprintf(os.Stderr, "  · %s\n", msg) },
	})
}

// openAgent 建好会话与模型，返回可对话的 agent。
func openAgent(ctx context.Context, cfg config.Config, cl config.Cluster) (*agent.Agent, *session.Session, error) {
	if cfg.LLM.APIKey == "" {
		return nil, nil, fmt.Errorf("未找到 API Key：请设置环境变量 %s，或在配置文件 llm.api_key 中填写", cfg.LLM.APIKeyEnv)
	}
	sess, err := openSession(ctx, cfg, cl, true)
	if err != nil {
		return nil, nil, fmt.Errorf("接入失败: %w", err)
	}
	snap := sess.Snapshot()
	fmt.Fprintf(os.Stderr, "✓ %s｜%s\n", snap.Health, snap.Summary)
	return sess.NewAgent(newModel(cfg), cfg.Loop), sess, nil
}

func newModel(cfg config.Config) llm.ChatModel {
	return llm.NewClient(llm.Config{
		BaseURL:     cfg.LLM.BaseURL,
		APIKey:      cfg.LLM.APIKey,
		Model:       cfg.LLM.Model,
		Temperature: cfg.LLM.Temperature,
		MaxTokens:   cfg.LLM.MaxTokens,
		Timeout:     time.Duration(cfg.LLM.Timeout) * time.Second,
		MaxRetries:  cfg.LLM.MaxRetries,
	})
}

func stringFlag(flags *pflag.FlagSet, name string) string {
	v, _ := flags.GetString(name)
	return v
}

// overrideCluster 用显式给出的 flag 覆盖所选集群的字段。
func overrideCluster(flags *pflag.FlagSet, cl config.Cluster) config.Cluster {
	if flags.Changed("host") {
		cl.Host, _ = flags.GetString("host")
	}
	if flags.Changed("port") {
		cl.Port, _ = flags.GetInt("port")
	}
	if flags.Changed("user") {
		cl.User, _ = flags.GetString("user")
	}
	if flags.Changed("key") {
		cl.PrivateKeyPath, _ = flags.GetString("key")
	}
	if flags.Changed("password") {
		cl.Password, _ = flags.GetString("password")
	}
	if flags.Changed("known-hosts") {
		cl.KnownHostsPath, _ = flags.GetString("known-hosts")
	}
	if flags.Changed("ssh-timeout") {
		cl.CommandTimeout, _ = flags.GetInt("ssh-timeout")
	}
	if flags.Changed("probe-timeout") {
		cl.ProbeTimeout, _ = flags.GetInt("probe-timeout")
	}
	return cl
}
