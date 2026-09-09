package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile   string
	namespace string
	kubeconfig string
)

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:   "infra-agent",
	Short: "智能基础设施运维 Agent",
	Long: `Infra Agent 是一个综合性的基础设施智能运维工具，
专为 Kubernetes 环境下的计算和存储服务器设计。

它提供了智能分析、自动巡检、问题诊断和告警通知等功能，
帮助 SRE 团队快速定位和解决基础设施问题。`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	// 全局标志
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "配置文件路径 (默认: $HOME/.infra-agent.yaml)")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Kubernetes 命名空间 (为空则所有命名空间)")
	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig 文件路径 (默认: $HOME/.kube/config)")
}

// initConfig 读取配置文件和环境变量
func initConfig() {
	if cfgFile != "" {
		// 使用指定的配置文件
		viper.SetConfigFile(cfgFile)
	} else {
		// 查找主目录
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		// 在主目录中搜索 .infra-agent.yaml 配置文件
		viper.AddConfigPath(home)
		viper.AddConfigPath(".")
		viper.SetConfigType("yaml")
		viper.SetConfigName(".infra-agent")
	}

	viper.AutomaticEnv() // 读取匹配的环境变量

	// 如果找到配置文件，读取它
	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "使用配置文件:", viper.ConfigFileUsed())
	}
}
