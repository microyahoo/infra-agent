package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/microyahoo/infra-agent/pkg/feishu"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	serveAppID     string
	serveAppSecret string
	serveCluster   string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "以飞书机器人方式运行（长连接）",
	Long: `启动飞书长连接机器人：在群里 @ 它提问，它会调查集群并回复。

长连接由本程序主动连接飞书服务器，不需要公网 IP 或回调地址。
需要在飞书开放平台为自建应用开启：
  · 机器人能力
  · 事件订阅：接收消息 im.message.receive_v1（使用长连接）
  · 权限：im:message、im:message:send_as_bot

集群与模型配置见配置文件的 feishu.bot 段。`,
	RunE: runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	f := serveCmd.Flags()
	f.StringVar(&serveAppID, "app-id", "", "飞书应用 App ID")
	f.StringVar(&serveAppSecret, "app-secret", "", "飞书应用 App Secret")
	f.StringVar(&serveCluster, "cluster", "", "默认集群名（对应配置里的 clusters）")
}

func runServe(cmd *cobra.Command, _ []string) error {
	cfg, err := loadBotConfig()
	if err != nil {
		return err
	}
	bot, err := feishu.New(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintln(os.Stderr, "🤖 飞书机器人启动中（长连接）...")
	err = bot.Run(ctx)
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "已停止")
		return nil
	}
	return err
}

// loadBotConfig 合并命令行与配置文件。
func loadBotConfig() (feishu.Config, error) {
	if serveAppID != "" {
		viper.Set("feishu.bot.app_id", serveAppID)
	}
	if serveAppSecret != "" {
		viper.Set("feishu.bot.app_secret", serveAppSecret)
	}
	// 也支持环境变量，避免把密钥写进文件
	if v := os.Getenv("FEISHU_APP_ID"); v != "" && serveAppID == "" {
		viper.Set("feishu.bot.app_id", v)
	}
	if v := os.Getenv("FEISHU_APP_SECRET"); v != "" && serveAppSecret == "" {
		viper.Set("feishu.bot.app_secret", v)
	}

	cfg := feishu.Config{
		AppID:             viper.GetString("feishu.bot.app_id"),
		AppSecret:         viper.GetString("feishu.bot.app_secret"),
		VerificationToken: viper.GetString("feishu.bot.verification_token"),
		EncryptKey:        viper.GetString("feishu.bot.encrypt_key"),
		Clusters:          map[string]feishu.ClusterConfig{},
		ChatClusters:      map[string]string{},
		SourceDir:         expandHome(viper.GetString("doctor.ceph.source_dir")),
		MaxRounds:         viper.GetInt("doctor.loop.max_rounds"),
		MaxCallsPerRound:  viper.GetInt("doctor.loop.max_calls_per_round"),
		MaxCallsPerTurn:   viper.GetInt("doctor.loop.max_tool_calls"),
		IdleTimeout:       time.Duration(viper.GetInt("feishu.bot.idle_timeout_minutes")) * time.Minute,
	}

	cfg.LLM = feishu.LLMConfig{
		BaseURL:    viper.GetString("doctor.llm.base_url"),
		APIKey:     viper.GetString("doctor.llm.api_key"),
		Model:      viper.GetString("doctor.llm.model"),
		MaxTokens:  viper.GetInt("doctor.llm.max_tokens"),
		MaxRetries: viper.GetInt("doctor.llm.max_retries"),
	}
	if cfg.LLM.APIKey == "" {
		cfg.LLM.APIKey = os.Getenv(viper.GetString("doctor.llm.api_key_env"))
	}

	// 默认集群
	if serveCluster != "" {
		viper.Set("feishu.bot.default_cluster", serveCluster)
	}
	defaultName := viper.GetString("feishu.bot.default_cluster")

	clusters := viper.GetStringMap("feishu.bot.clusters")
	for name := range clusters {
		prefix := "feishu.bot.clusters." + name + "."
		cc := feishu.ClusterConfig{
			Name:           name,
			Host:           viper.GetString(prefix + "host"),
			Port:           viper.GetInt(prefix + "port"),
			User:           viper.GetString(prefix + "user"),
			PrivateKeyPath: expandHome(viper.GetString(prefix + "private_key")),
			Password:       viper.GetString(prefix + "password"),
			KnownHostsPath: expandHome(viper.GetString(prefix + "known_hosts")),
		}
		if cc.Host == "" {
			continue
		}
		cfg.Clusters[name] = cc
		if defaultName == "" {
			defaultName = name
		}
	}
	if defaultName != "" {
		if cc, ok := cfg.Clusters[defaultName]; ok {
			cfg.Cluster = cc
		}
	}
	if cfg.Cluster.Host == "" && len(cfg.Clusters) == 0 {
		// 退回到 doctor.ssh 段，单集群场景常这么配
		cfg.Cluster = feishu.ClusterConfig{
			Name:           "default",
			Host:           viper.GetString("doctor.ssh.host"),
			Port:           viper.GetInt("doctor.ssh.port"),
			User:           viper.GetString("doctor.ssh.user"),
			PrivateKeyPath: expandHome(viper.GetString("doctor.ssh.private_key")),
			KnownHostsPath: expandHome(viper.GetString("doctor.ssh.known_hosts")),
		}
	}

	chatMap := viper.GetStringMapString("feishu.bot.chat_clusters")
	for chat, cluster := range chatMap {
		cfg.ChatClusters[chat] = cluster
	}

	return cfg, nil
}
