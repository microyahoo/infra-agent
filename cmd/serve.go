package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/microyahoo/infra-agent/internal/config"
	"github.com/microyahoo/infra-agent/internal/feishu"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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

集群与模型配置见配置文件的 clusters 与 feishu 段。`,
	RunE: runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	f := serveCmd.Flags()
	f.String("app-id", "", "飞书应用 App ID")
	f.String("app-secret", "", "飞书应用 App Secret")
	f.String("cluster", "", "默认集群名（对应配置里的 clusters）")
}

func runServe(cmd *cobra.Command, _ []string) error {
	v := viper.GetViper()
	if err := config.BindFlags(v, cmd.Flags(), map[string]string{
		"app-id":     "feishu.app_id",
		"app-secret": "feishu.app_secret",
		"cluster":    "feishu.default_cluster",
	}); err != nil {
		return err
	}
	cfg, err := config.Load(v, nil)
	if err != nil {
		return err
	}

	bot, err := feishu.New(loadBotConfig(cfg))
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

// loadBotConfig 把统一配置映射成飞书机器人配置。
func loadBotConfig(cfg config.Config) feishu.Config {
	clusters := make(map[string]feishu.ClusterConfig, len(cfg.Clusters))
	for name, cc := range cfg.Clusters {
		clusters[name] = feishu.ClusterConfig{
			Name:           name,
			Host:           cc.Host,
			Port:           cc.Port,
			User:           cc.User,
			PrivateKeyPath: config.ExpandHome(cc.PrivateKeyPath),
			Password:       cc.Password,
			KnownHostsPath: config.ExpandHome(cc.KnownHostsPath),
		}
	}

	name := cfg.Feishu.DefaultCluster
	if name == "" {
		name = cfg.DefaultCluster
	}

	return feishu.Config{
		AppID:             cfg.Feishu.AppID,
		AppSecret:         cfg.Feishu.AppSecret,
		VerificationToken: cfg.Feishu.VerificationToken,
		EncryptKey:        cfg.Feishu.EncryptKey,
		Cluster:           clusters[name],
		Clusters:          clusters,
		ChatClusters:      cfg.Feishu.ChatClusters,
		LLM: feishu.LLMConfig{
			BaseURL:    cfg.LLM.BaseURL,
			APIKey:     cfg.LLM.APIKey,
			Model:      cfg.LLM.Model,
			MaxTokens:  cfg.LLM.MaxTokens,
			MaxRetries: cfg.LLM.MaxRetries,
		},
		SourceDir:        cfg.Ceph.SourceDir,
		MaxRounds:        cfg.Loop.MaxRounds,
		MaxCallsPerRound: cfg.Loop.MaxCallsPerRound,
		MaxCallsPerTurn:  cfg.Loop.MaxToolCalls,
		IdleTimeout:      time.Duration(cfg.Feishu.IdleTimeoutMinutes) * time.Minute,
	}
}
