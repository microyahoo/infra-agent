package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPrecedence(t *testing.T) {
	path := writeFile(t, `
default_cluster: from-file
clusters:
  from-file:
    host: 10.0.0.1
llm:
  model: file-model
  max_tokens: 1024
loop:
  max_rounds: 30
`)

	v := New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("read config: %v", err)
	}

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("model", "", "")
	flags.Int("max-rounds", 0, "")
	if err := flags.Parse([]string{"--model", "flag-model"}); err != nil {
		t.Fatal(err)
	}
	if err := BindFlags(v, flags, map[string]string{
		"model":      "llm.model",
		"max-rounds": "loop.max_rounds",
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(v, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.LLM.Model != "flag-model" {
		t.Errorf("flag should win: got %q", cfg.LLM.Model)
	}
	if cfg.Loop.MaxRounds != 30 {
		t.Errorf("file value should survive: got %d", cfg.Loop.MaxRounds)
	}
	if cfg.LLM.MaxTokens != 1024 {
		t.Errorf("file max_tokens: got %d", cfg.LLM.MaxTokens)
	}
	if got := cfg.Clusters["from-file"].Host; got != "10.0.0.1" {
		t.Errorf("cluster host: got %q", got)
	}
	if got := cfg.Clusters["from-file"].Port; got != 22 {
		t.Errorf("cluster port default: got %d", got)
	}
	if cfg.Run.Timeout != 600 {
		t.Errorf("run timeout default: got %d", cfg.Run.Timeout)
	}
}

func TestPickFallsBackToDefault(t *testing.T) {
	cfg := Config{
		DefaultCluster: "a",
		Clusters: map[string]Cluster{
			"a": {Host: "10.0.0.1"},
			"b": {Host: "10.0.0.2"},
		},
	}
	got, err := cfg.Pick("")
	if err != nil || got.Host != "10.0.0.1" {
		t.Fatalf("default pick: %v %+v", err, got)
	}
	got, err = cfg.Pick("b")
	if err != nil || got.Host != "10.0.0.2" {
		t.Fatalf("named pick: %v %+v", err, got)
	}
	if _, err := cfg.Pick("missing"); err == nil {
		t.Fatal("missing cluster must error")
	}
}

func TestAPIKeyFromEnv(t *testing.T) {
	t.Setenv("INFRA_AGENT_LLM_API_KEY", "sk-from-env")
	cfg, err := Load(New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.APIKey != "sk-from-env" {
		t.Errorf("api key from env: got %q", cfg.LLM.APIKey)
	}
}

func TestDefaultAPIKeyEnvIsHonored(t *testing.T) {
	// api_key_env 的默认值就是 OPENAI_API_KEY，因此它应该被读取。
	t.Setenv("OPENAI_API_KEY", "sk-from-default-env")
	cfg, err := Load(New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.APIKey != "sk-from-default-env" {
		t.Errorf("api key from api_key_env: got %q", cfg.LLM.APIKey)
	}
}

func TestExplicitAPIKeyEnvWins(t *testing.T) {
	// INFRA_AGENT_LLM_API_KEY 比 api_key_env 指向的变量优先。
	t.Setenv("OPENAI_API_KEY", "sk-default")
	t.Setenv("INFRA_AGENT_LLM_API_KEY", "sk-explicit")
	cfg, err := Load(New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.APIKey != "sk-explicit" {
		t.Errorf("explicit env should win: got %q", cfg.LLM.APIKey)
	}
}
