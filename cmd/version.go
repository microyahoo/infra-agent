package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	// Version 版本号，由编译时注入
	Version = "dev"
	// GitCommit Git 提交哈希
	GitCommit = "none"
	// BuildTime 构建时间
	BuildTime = "unknown"
)

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "显示版本信息",
	Long:  `显示 infra-agent 的版本信息，包括版本号、Git 提交和构建时间。`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("infra-agent version %s\n", Version)
		fmt.Printf("Git commit: %s\n", GitCommit)
		fmt.Printf("Build time: %s\n", BuildTime)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
