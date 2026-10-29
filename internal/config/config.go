// Package config 把默认值、配置文件、环境变量与命令行参数合并成一个 Config。
// 整个程序只在这里做一次合并，其它包只接收已经解析好的结构体。
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config 是三个入口（doctor / mcp / serve）共用的配置。
type Config struct {
	DefaultCluster string             `mapstructure:"default_cluster"`
	Clusters       map[string]Cluster `mapstructure:"clusters"`

	LLM    LLM    `mapstructure:"llm"`
	Loop   Loop   `mapstructure:"loop"`
	Policy Policy `mapstructure:"policy"`
	Ceph   Ceph   `mapstructure:"ceph"`
	MCP    MCP    `mapstructure:"mcp"`
	Feishu Feishu `mapstructure:"feishu"`
	Run    Run    `mapstructure:"run"`
}

// Cluster 是一套可被 SSH 接入的 Ceph 集群。
type Cluster struct {
	Host           string `mapstructure:"host"`
	Port           int    `mapstructure:"port"`
	User           string `mapstructure:"user"`
	Password       string `mapstructure:"password"`
	PrivateKeyPath string `mapstructure:"private_key"`
	KnownHostsPath string `mapstructure:"known_hosts"`
	CommandTimeout int    `mapstructure:"command_timeout"` // 秒
	ProbeTimeout   int    `mapstructure:"probe_timeout"`   // 秒
}

// LLM 是 OpenAI 兼容接口的设置。
type LLM struct {
	BaseURL     string  `mapstructure:"base_url"`
	APIKey      string  `mapstructure:"api_key"`
	APIKeyEnv   string  `mapstructure:"api_key_env"`
	Model       string  `mapstructure:"model"`
	Temperature float64 `mapstructure:"temperature"`
	MaxTokens   int     `mapstructure:"max_tokens"`
	Timeout     int     `mapstructure:"timeout"` // 秒
	MaxRetries  int     `mapstructure:"max_retries"`
}

// Loop 是内置调查循环的预算。
type Loop struct {
	MaxRounds        int `mapstructure:"max_rounds"`
	MaxToolCalls     int `mapstructure:"max_tool_calls"`
	MaxCallsPerRound int `mapstructure:"max_calls_per_round"`
}

// Policy 是只读策略的扩展项。
type Policy struct {
	ExtraBinaries []string `mapstructure:"extra_binaries"`
}

// Ceph 是 Ceph 专项配置。
type Ceph struct {
	SourceDir string `mapstructure:"source_dir"`
}

// MCP 是 MCP server 的配置。
type MCP struct {
	Listen  string `mapstructure:"listen"`
	Token   string `mapstructure:"token"`
	Cluster string `mapstructure:"cluster"`
}

// Feishu 是飞书机器人配置。
type Feishu struct {
	AppID              string            `mapstructure:"app_id"`
	AppSecret          string            `mapstructure:"app_secret"`
	VerificationToken  string            `mapstructure:"verification_token"`
	EncryptKey         string            `mapstructure:"encrypt_key"`
	DefaultCluster     string            `mapstructure:"default_cluster"`
	ChatClusters       map[string]string `mapstructure:"chat_clusters"`
	IdleTimeoutMinutes int               `mapstructure:"idle_timeout_minutes"`
}

// Run 是运行行为。
type Run struct {
	Timeout     int  `mapstructure:"timeout"` // 秒
	DryRun      bool `mapstructure:"dry_run"`
	SelfCheck   bool `mapstructure:"self_check"`
	Interactive bool `mapstructure:"interactive"`
}

// applyDefaults 补齐无法用 viper 默认值表达的部分。
func (c *Config) applyDefaults() {
	if c.LLM.APIKey == "" && c.LLM.APIKeyEnv != "" {
		c.LLM.APIKey = os.Getenv(c.LLM.APIKeyEnv)
	}
	if c.DefaultCluster == "" && len(c.Clusters) == 1 {
		for name := range c.Clusters {
			c.DefaultCluster = name
		}
	}
	for name, cl := range c.Clusters {
		if cl.Port == 0 {
			cl.Port = 22
		}
		if cl.User == "" {
			cl.User = "root"
		}
		if cl.CommandTimeout == 0 {
			cl.CommandTimeout = 30
		}
		if cl.ProbeTimeout == 0 {
			cl.ProbeTimeout = 8
		}
		c.Clusters[name] = cl
	}
}

// Pick 返回指定集群；name 为空时用 default_cluster。
func (c Config) Pick(name string) (Cluster, error) {
	if strings.TrimSpace(name) == "" {
		name = c.DefaultCluster
	}
	if name == "" {
		return Cluster{}, fmt.Errorf("未指定集群：请设置 default_cluster，或用 --cluster 指定")
	}
	cl, ok := c.Clusters[name]
	if !ok {
		return Cluster{}, fmt.Errorf("没有名为 %q 的集群配置", name)
	}
	if cl.Host == "" {
		return Cluster{}, fmt.Errorf("集群 %q 缺少 host", name)
	}
	return cl, nil
}

// ExpandHome 把配置里的 ~ 展开成用户主目录。
func ExpandHome(path string) string {
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
	}
	return path
}
