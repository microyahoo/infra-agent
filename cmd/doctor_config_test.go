package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// newFlagCmd 构造一个带 doctor 全部 flag 的空命令，用于测试解析逻辑。
// 直接复用真实 flag 定义会互相污染，所以这里单独注册一份。
func newFlagCmd(t *testing.T) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "test"}
	f := c.Flags()
	f.String("host", "", "")
	f.Int("port", 22, "")
	f.String("user", "root", "")
	f.String("password", "", "")
	f.String("key", "", "")
	f.String("known-hosts", "", "")
	f.String("ceph-src", "", "")
	f.String("format", "text", "")
	f.String("question", "", "")
	f.Int("timeout", 600, "")
	f.Int("max-rounds", 20, "")
	f.Int("max-tool-calls", 60, "")
	f.Int("max-per-tool", 5, "")
	f.String("model", "", "")
	f.String("base-url", "", "")
	f.String("api-key-env", "OPENAI_API_KEY", "")
	f.Int("ssh-timeout", 30, "")
	f.Int("probe-timeout", 8, "")
	f.Int("max-tokens", 4096, "")
	f.Int("llm-timeout", 120, "")
	f.Int("llm-retries", -1, "")
	f.Bool("dry-run", false, "")
	f.Bool("self-check", false, "")
	f.Bool("interactive", false, "")
	return c
}

// resetConfig 让每个用例从干净的 viper 状态开始。
func resetConfig(t *testing.T, yaml string) {
	t.Helper()
	viper.Reset()
	if yaml == "" {
		return
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(viper.Reset)
}

// 没有配置文件、没有 flag 时，应得到内置默认值。
func TestResolveDefaults(t *testing.T) {
	resetConfig(t, "")
	cmd := newFlagCmd(t)
	doctorOpts = doctorOptions{Host: "10.0.0.1"}

	opt := resolveDoctorOptions(cmd)
	if opt.Port != 22 || opt.User != "root" {
		t.Errorf("ssh defaults = %d/%s", opt.Port, opt.User)
	}
	if opt.CommandTimeout != 30 || opt.ProbeTimeout != 8 {
		t.Errorf("timeouts = %d/%d", opt.CommandTimeout, opt.ProbeTimeout)
	}
	if opt.MaxRounds != 20 || opt.MaxToolCalls != 60 || opt.MaxPerTool != 5 {
		t.Errorf("loop budget = %d/%d/%d", opt.MaxRounds, opt.MaxToolCalls, opt.MaxPerTool)
	}
	if opt.LLMRetries != -1 {
		t.Errorf("LLMRetries = %d, want -1 (use client default)", opt.LLMRetries)
	}
}

// 配置文件里的值应生效。
func TestResolveFromConfigFile(t *testing.T) {
	resetConfig(t, `
doctor:
  ssh:
    port: 2222
    user: ceph-admin
    private_key: /keys/id
    known_hosts: /keys/known
    command_timeout: 45
    probe_timeout: 5
  llm:
    model: qwen-max
    base_url: https://relay.example.com/v1
    api_key_env: RELAY_KEY
    temperature: 0.1
    max_tokens: 2048
    timeout: 90
    max_retries: 3
  loop:
    max_rounds: 40
    max_tool_calls: 120
    max_per_tool: 8
  ceph:
    source_dir: /data/ceph-src
  report:
    format: json
  timeout: 900
  interactive: true
`)
	cmd := newFlagCmd(t)
	doctorOpts = doctorOptions{Host: "10.0.0.1"}

	opt := resolveDoctorOptions(cmd)
	if opt.Port != 2222 || opt.User != "ceph-admin" {
		t.Errorf("ssh = %d/%s", opt.Port, opt.User)
	}
	if opt.PrivateKey != "/keys/id" || opt.KnownHosts != "/keys/known" {
		t.Errorf("keys = %q/%q", opt.PrivateKey, opt.KnownHosts)
	}
	if opt.CommandTimeout != 45 || opt.ProbeTimeout != 5 {
		t.Errorf("timeouts = %d/%d", opt.CommandTimeout, opt.ProbeTimeout)
	}
	if opt.Model != "qwen-max" || opt.BaseURL != "https://relay.example.com/v1" {
		t.Errorf("llm = %s/%s", opt.Model, opt.BaseURL)
	}
	if opt.APIKeyEnv != "RELAY_KEY" {
		t.Errorf("APIKeyEnv = %q", opt.APIKeyEnv)
	}
	if opt.Temperature != 0.1 || opt.MaxTokens != 2048 || opt.LLMTimeout != 90 || opt.LLMRetries != 3 {
		t.Errorf("llm tuning = %v/%d/%d/%d", opt.Temperature, opt.MaxTokens, opt.LLMTimeout, opt.LLMRetries)
	}
	if opt.MaxRounds != 40 || opt.MaxToolCalls != 120 || opt.MaxPerTool != 8 {
		t.Errorf("loop = %d/%d/%d", opt.MaxRounds, opt.MaxToolCalls, opt.MaxPerTool)
	}
	if opt.CephSrc != "/data/ceph-src" {
		t.Errorf("CephSrc = %q", opt.CephSrc)
	}
	if opt.Format != "json" || opt.Timeout != 900 || !opt.Interactive {
		t.Errorf("report/run = %s/%d/%v", opt.Format, opt.Timeout, opt.Interactive)
	}
}

// 命令行参数必须能覆盖配置文件。
func TestFlagOverridesConfig(t *testing.T) {
	resetConfig(t, `
doctor:
  ssh:
    port: 2222
    user: ceph-admin
  loop:
    max_rounds: 40
  report:
    format: json
`)
	cmd := newFlagCmd(t)
	doctorOpts = doctorOptions{Host: "10.0.0.1"}
	if err := cmd.Flags().Set("port", "2200"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("max-rounds", "7"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("format", "brief"); err != nil {
		t.Fatal(err)
	}

	opt := resolveDoctorOptions(cmd)
	if opt.Port != 2200 {
		t.Errorf("Port = %d, want flag value 2200", opt.Port)
	}
	if opt.MaxRounds != 7 {
		t.Errorf("MaxRounds = %d, want flag value 7", opt.MaxRounds)
	}
	if opt.Format != "brief" {
		t.Errorf("Format = %q, want flag value brief", opt.Format)
	}
	// 未在命令行指定的项仍取配置值
	if opt.User != "ceph-admin" {
		t.Errorf("User = %q, want config value", opt.User)
	}
}

// 认证信息：flag 优先于配置。
func TestCredentialsPreferFlag(t *testing.T) {
	resetConfig(t, `
doctor:
  ssh:
    password: from-config
`)
	cmd := newFlagCmd(t)
	doctorOpts = doctorOptions{Host: "h", Password: "from-flag"}
	if got := resolveDoctorOptions(cmd).Password; got != "from-flag" {
		t.Errorf("Password = %q, want flag value", got)
	}
}

// flag 未给出时，认证信息回落到配置。
func TestCredentialsFallBackToConfig(t *testing.T) {
	resetConfig(t, `
doctor:
  ssh:
    password: from-config
`)
	cmd := newFlagCmd(t)
	doctorOpts = doctorOptions{Host: "h"}
	if got := resolveDoctorOptions(cmd).Password; got != "from-config" {
		t.Errorf("Password = %q, want config value", got)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cases := map[string]string{
		"":              "",
		"~":             home,
		"~/x":           home + "/x",
		"/abs/path":     "/abs/path",
		"relative/path": "relative/path",
	}
	for in, want := range cases {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

// 示例配置文件必须能被解析，否则用户复制后直接报错。
func TestExampleConfigIsValid(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	path := filepath.Join("..", "config", "doctor.example.yaml")
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("example config does not parse: %v", err)
	}
	// 抽查几个关键键确实能被读到
	if got := viper.GetString("doctor.llm.model"); got == "" {
		t.Error("doctor.llm.model missing from example")
	}
	if got := viper.GetInt("doctor.loop.max_rounds"); got == 0 {
		t.Error("doctor.loop.max_rounds missing from example")
	}
	if !strings.Contains(viper.GetString("doctor.ssh.private_key"), "id_rsa") {
		t.Error("doctor.ssh.private_key missing from example")
	}
}
