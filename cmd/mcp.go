package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/microyahoo/infra-agent/internal/config"
	"github.com/microyahoo/infra-agent/internal/mcpserver"
	"github.com/microyahoo/infra-agent/internal/session"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "以 MCP server 方式暴露只读诊断工具",
	Long: `把 Ceph 诊断工具暴露为 MCP 工具。

默认走 stdio，供 Cursor / Claude Code 等客户端直接启动：
  {
    "mcpServers": {
      "infra-agent": { "command": "infra-agent", "args": ["mcp", "--cluster", "stor-01"] }
    }
  }

HTTP 模式供 Dify 等需要 URL 的客户端使用：
  infra-agent mcp --http 127.0.0.1:9100 --token <token>

一个进程只绑定一套集群；多集群请多开进程。`,
	RunE:         runMCP,
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
	f := mcpCmd.Flags()
	f.String("cluster", "", "集群名（默认 default_cluster）")
	f.String("http", "", "HTTP 监听地址，例如 127.0.0.1:9100；留空使用 stdio")
	f.String("token", "", "HTTP 模式的 Bearer token")
	f.Bool("allow-remote", false, "允许绑定非回环地址")
}

func runMCP(cmd *cobra.Command, _ []string) error {
	v := viper.GetViper()
	if err := config.BindFlags(v, cmd.Flags(), map[string]string{
		"cluster": "mcp.cluster",
		"token":   "mcp.token",
		"http":    "mcp.listen",
	}); err != nil {
		return err
	}
	cfg, err := config.Load(v, nil)
	if err != nil {
		return err
	}

	// 集群选择推迟到第一次工具调用：客户端可能只列工具、从不连接，
	// 或者集群在启动瞬间不可达。配置错误以工具错误的形式返回，
	// 而不是让整个 MCP server 起不来。
	clusterName := cfg.MCP.Cluster
	lazy := &session.Lazy{Open: func(ctx context.Context) (*session.Session, error) {
		cl, err := cfg.Pick(clusterName)
		if err != nil {
			return nil, err
		}
		return session.Open(ctx, session.Options{
			Cluster:   cl,
			Policy:    cfg.Policy,
			SourceDir: cfg.Ceph.SourceDir,
		})
	}}
	defer lazy.Close()

	srv := mcpserver.New(lazy, Version)

	if !cmd.Flags().Changed("http") {
		// stdio mode: stdout is the protocol channel, all logging must go to stderr.
		fmt.Fprintln(os.Stderr, "MCP server 已就绪（stdio）")
		return srv.Run(cmd.Context(), &mcp.StdioTransport{})
	}

	allowRemote, _ := cmd.Flags().GetBool("allow-remote")
	return mcpserver.ServeHTTP(cmd.Context(), cfg.MCP.Listen, srv, cfg.MCP.Token, allowRemote)
}
