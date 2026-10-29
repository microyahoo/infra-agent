package config

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// New 返回配置好的 viper 实例：默认值 + 环境变量绑定。
//
// 环境变量只用于密钥类字段。其余字段若也允许环境变量覆盖，
// 排查"为什么这个值是 X"会变得很困难。
func New() *viper.Viper {
	v := viper.New()
	Apply(v)
	return v
}

// Apply 把环境变量绑定与默认值写入 v。
func Apply(v *viper.Viper) {
	v.SetEnvPrefix("INFRA_AGENT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	_ = v.BindEnv("llm.api_key", "INFRA_AGENT_LLM_API_KEY")
	_ = v.BindEnv("feishu.app_id", "FEISHU_APP_ID")
	_ = v.BindEnv("feishu.app_secret", "FEISHU_APP_SECRET")
	_ = v.BindEnv("mcp.token", "INFRA_AGENT_MCP_TOKEN")

	setDefaults(v)
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("default_cluster", "")
	v.SetDefault("clusters", map[string]any{})

	v.SetDefault("llm.model", "gpt-4o")
	v.SetDefault("llm.api_key_env", "OPENAI_API_KEY")
	v.SetDefault("llm.temperature", 0.2)
	v.SetDefault("llm.max_tokens", 8192)
	v.SetDefault("llm.timeout", 120)
	v.SetDefault("llm.max_retries", -1)

	v.SetDefault("loop.max_rounds", 20)
	v.SetDefault("loop.max_tool_calls", 60)
	v.SetDefault("loop.max_calls_per_round", 2)

	v.SetDefault("policy.extra_binaries", []string{})
	v.SetDefault("ceph.source_dir", "")

	v.SetDefault("mcp.listen", "127.0.0.1:9100")
	v.SetDefault("feishu.idle_timeout_minutes", 30)

	v.SetDefault("run.timeout", 600)
	v.SetDefault("run.dry_run", false)
	v.SetDefault("run.self_check", false)
	v.SetDefault("run.interactive", true)
}

// BindFlags 把命令行 flag 名字映射到配置键。
//
// viper 的 BindPFlags 按 flag 名做键，而配置是分层的（llm.model），
// 因此必须显式给出映射，不能依赖默认行为。
func BindFlags(v *viper.Viper, flags *pflag.FlagSet, pairs map[string]string) error {
	for flagName, key := range pairs {
		f := flags.Lookup(flagName)
		if f == nil {
			return fmt.Errorf("unknown flag %q", flagName)
		}
		if err := v.BindPFlag(key, f); err != nil {
			return fmt.Errorf("bind %s -> %s: %w", flagName, key, err)
		}
	}
	return nil
}

// Load 把 viper 中已有的值（默认值/文件/环境变量/已绑定的 flag）解成一个 Config。
func Load(v *viper.Viper, flags *pflag.FlagSet) (Config, error) {
	if flags != nil {
		if err := v.BindPFlags(flags); err != nil {
			return Config{}, fmt.Errorf("bind flags: %w", err)
		}
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}
	c.applyDefaults()
	return c, nil
}
