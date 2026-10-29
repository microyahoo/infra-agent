package cmd

import (
	"fmt"
	"os"

	"github.com/microyahoo/infra-agent/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:   "infra-agent",
	Short: "智能基础设施运维 Agent",
	Long: `Infra Agent 通过 SSH 只读访问 Ceph 集群，提供对话式诊断。

可作为命令行工具、MCP server 或飞书机器人运行。`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	// 全局标志
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "配置文件路径 (默认: $HOME/.infra-agent.yaml)")
}

// initConfig 读取配置文件和环境变量
func initConfig() {
	v := viper.GetViper()
	config.Apply(v)
	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)
		v.AddConfigPath(home)
		v.AddConfigPath(".")
		v.SetConfigType("yaml")
		v.SetConfigName(".infra-agent")
	}
	if err := v.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "使用配置文件:", v.ConfigFileUsed())
	}
}
