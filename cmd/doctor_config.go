package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// resolvedDoctorOptions 是合并后的最终参数。
//
// 优先级：命令行显式给出 > 配置文件 > 内置默认值。
// 每项先判断对应 flag 是否被显式改过（cmd.Flags().Changed），
// 只有没改过时才回落到配置，配置缺省时才用默认值。
type resolvedDoctorOptions struct {
	Host     string
	Port     int
	User     string
	Password string

	PrivateKey string
	KnownHosts string

	CommandTimeout int // 诊断命令超时（秒）
	ProbeTimeout   int // 探测/拓扑超时（秒）

	Model       string
	BaseURL     string
	APIKey      string
	APIKeyEnv   string
	Temperature float64
	MaxTokens   int
	LLMTimeout  int
	LLMRetries  int

	MaxRounds        int
	MaxToolCalls     int
	MaxPerTool       int
	MaxCallsPerRound int

	Timeout     int
	Format      string
	Question    string
	CephSrc     string
	DryRun      bool
	SelfCheck   bool
	Interactive bool
}

// resolveDoctorOptions 合并 flag、配置与默认值。
func resolveDoctorOptions(cmd *cobra.Command) resolvedDoctorOptions {
	f := cmd.Flags()
	changed := func(name string) bool { return f.Changed(name) }

	// flagString 在 flag 被显式设置时取 flag 值，否则取配置值，再否则取默认值。
	flagString := func(name, cfgKey, def string) string {
		if changed(name) {
			v, _ := f.GetString(name)
			return v
		}
		if v := viper.GetString(cfgKey); v != "" {
			return v
		}
		return def
	}
	flagInt := func(name, cfgKey string, def int) int {
		if changed(name) {
			v, _ := f.GetInt(name)
			return v
		}
		if v := viper.GetInt(cfgKey); v != 0 {
			return v
		}
		return def
	}
	flagBool := func(name, cfgKey string) bool {
		if changed(name) {
			v, _ := f.GetBool(name)
			return v
		}
		return viper.GetBool(cfgKey)
	}

	// 认证信息同样支持配置，但 flag 显式值时优先。
	pickCredential := func(flagVal, cfgKey string) string {
		if flagVal != "" {
			return flagVal
		}
		return viper.GetString(cfgKey)
	}

	return resolvedDoctorOptions{
		Host:     doctorOpts.Host,
		Port:     flagInt("port", "doctor.ssh.port", 22),
		User:     flagString("user", "doctor.ssh.user", "root"),
		Password: pickCredential(doctorOpts.Password, "doctor.ssh.password"),

		PrivateKey: expandHome(pickCredential(doctorOpts.Key, "doctor.ssh.private_key")),
		KnownHosts: expandHome(pickCredential(doctorOpts.KnownHosts, "doctor.ssh.known_hosts")),

		CommandTimeout: flagInt("ssh-timeout", "doctor.ssh.command_timeout", 30),
		ProbeTimeout:   flagInt("probe-timeout", "doctor.ssh.probe_timeout", 8),

		Model:       flagString("model", "doctor.llm.model", "gpt-4o"),
		BaseURL:     flagString("base-url", "doctor.llm.base_url", ""),
		APIKey:      viper.GetString("doctor.llm.api_key"),
		APIKeyEnv:   flagString("api-key-env", "doctor.llm.api_key_env", "OPENAI_API_KEY"),
		Temperature: viper.GetFloat64("doctor.llm.temperature"),
		MaxTokens:   flagInt("max-tokens", "doctor.llm.max_tokens", 8192),
		LLMTimeout:  flagInt("llm-timeout", "doctor.llm.timeout", 120),
		LLMRetries:  flagInt("llm-retries", "doctor.llm.max_retries", -1),

		MaxRounds:        flagInt("max-rounds", "doctor.loop.max_rounds", 20),
		MaxToolCalls:     flagInt("max-tool-calls", "doctor.loop.max_tool_calls", 60),
		MaxPerTool:       flagInt("max-per-tool", "doctor.loop.max_per_tool", 5),
		MaxCallsPerRound: flagInt("max-calls-per-round", "doctor.loop.max_calls_per_round", 2),

		Timeout:     flagInt("timeout", "doctor.timeout", 600),
		Format:      flagString("format", "doctor.report.format", "text"),
		Question:    doctorOpts.Question,
		CephSrc:     expandHome(flagString("ceph-src", "doctor.ceph.source_dir", "")),
		DryRun:      flagBool("dry-run", "doctor.dry_run"),
		SelfCheck:   flagBool("self-check", "doctor.self_check"),
		Interactive: flagBool("interactive", "doctor.interactive"),
	}
}

// expandHome 把配置里的 ~ 展开成用户主目录。
// viper 不做这件事，而配置文件里写 ~/.ssh/id_rsa 是最自然的写法。
func expandHome(path string) string {
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
