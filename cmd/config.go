package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	setAIProvider string
	setAIToken    string
	setAIModel    string
	listAll       bool
)

// configCmd represents the config command
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "管理配置",
	Long:  `管理 infra-agent 的配置，包括 AI 提供商、通知渠道等。`,
}

// configListCmd represents the config list command
var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出当前配置",
	RunE:  runConfigList,
}

// configSetAICmd represents the config set-ai command
var configSetAICmd = &cobra.Command{
	Use:   "set-ai",
	Short: "配置 AI 提供商",
	Long: `配置 AI 提供商的认证信息和模型。

示例:
  # 配置 OpenAI
  infra-agent config set-ai --provider openai --token YOUR_TOKEN --model gpt-4

  # 配置 Anthropic
  infra-agent config set-ai --provider anthropic --token YOUR_TOKEN --model claude-3-sonnet
`,
	RunE: runConfigSetAI,
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configSetAICmd)

	// config list flags
	configListCmd.Flags().BoolVar(&listAll, "all", false, "显示所有配置（包括敏感信息）")

	// config set-ai flags
	configSetAICmd.Flags().StringVar(&setAIProvider, "provider", "", "AI 提供商 (openai, anthropic, qwen)")
	configSetAICmd.Flags().StringVar(&setAIToken, "token", "", "API Token")
	configSetAICmd.Flags().StringVar(&setAIModel, "model", "", "模型名称")
	configSetAICmd.MarkFlagRequired("provider")
	configSetAICmd.MarkFlagRequired("token")
}

func runConfigList(cmd *cobra.Command, args []string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	defer w.Flush()

	fmt.Fprintln(w, "配置项\t当前值")
	fmt.Fprintln(w, "------\t------")

	// Kubernetes 配置
	fmt.Fprintf(w, "kubernetes.namespace\t%s\n", viper.GetString("kubernetes.namespace"))
	fmt.Fprintf(w, "kubernetes.timeout\t%d\n", viper.GetInt("kubernetes.timeout"))

	// AI 配置
	aiProvider := viper.GetString("ai.provider")
	fmt.Fprintf(w, "ai.provider\t%s\n", aiProvider)
	fmt.Fprintf(w, "ai.enabled\t%v\n", viper.GetBool("ai.enabled"))
	fmt.Fprintf(w, "ai.anonymize\t%v\n", viper.GetBool("ai.anonymize"))

	if aiProvider != "" {
		if listAll {
			fmt.Fprintf(w, "ai.%s.apiKey\t%s\n", aiProvider, viper.GetString(fmt.Sprintf("ai.%s.apiKey", aiProvider)))
		} else {
			fmt.Fprintf(w, "ai.%s.apiKey\t***\n", aiProvider)
		}
		fmt.Fprintf(w, "ai.%s.model\t%s\n", aiProvider, viper.GetString(fmt.Sprintf("ai.%s.model", aiProvider)))
	}

	// 巡检配置
	fmt.Fprintf(w, "patrol.enabled\t%v\n", viper.GetBool("patrol.enabled"))
	fmt.Fprintf(w, "patrol.interval\t%d\n", viper.GetInt("patrol.interval"))

	// 通知配置
	fmt.Fprintf(w, "notifiers.default\t%s\n", viper.GetString("notifiers.default"))
	fmt.Fprintf(w, "notifiers.slack.enabled\t%v\n", viper.GetBool("notifiers.slack.enabled"))
	fmt.Fprintf(w, "notifiers.dingtalk.enabled\t%v\n", viper.GetBool("notifiers.dingtalk.enabled"))
	fmt.Fprintf(w, "notifiers.wechat.enabled\t%v\n", viper.GetBool("notifiers.wechat.enabled"))

	return nil
}

func runConfigSetAI(cmd *cobra.Command, args []string) error {
	// 验证提供商
	validProviders := map[string]bool{
		"openai":    true,
		"anthropic": true,
		"qwen":      true,
	}

	if !validProviders[setAIProvider] {
		return fmt.Errorf("invalid provider: %s, valid providers: openai, anthropic, qwen", setAIProvider)
	}

	// 设置配置
	viper.Set("ai.provider", setAIProvider)
	viper.Set(fmt.Sprintf("ai.%s.apiKey", setAIProvider), setAIToken)

	if setAIModel != "" {
		viper.Set(fmt.Sprintf("ai.%s.model", setAIProvider), setAIModel)
	}

	// 保存配置
	if err := viper.WriteConfig(); err != nil {
		// 如果配置文件不存在，创建它
		home, _ := os.UserHomeDir()
		configFile := filepath.Join(home, ".infra-agent.yaml")
		if err := viper.SafeWriteConfigAs(configFile); err != nil {
			return fmt.Errorf("failed to write config: %w", err)
		}
	}

	fmt.Printf("✓ AI 提供商配置成功: %s\n", setAIProvider)
	return nil
}
