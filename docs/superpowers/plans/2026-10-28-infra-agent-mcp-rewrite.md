# infra-agent MCP + Skill 重做 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 infra-agent 重做成「薄入口 + 分域模块 + SKILL.md 事实源」的 Go CLI，同时以 MCP stdio/HTTP 和内置 agent 两种形态暴露同一批 Ceph 只读诊断工具。

**Architecture:** `cmd` 只做参数解析与输出；`internal/config` 是唯一配置层；`internal/access` 管 SSH/只读/探测/拓扑；`internal/ceph/tools` 是唯一工具实现；`internal/ceph/snapshot` 做会话起点采集与确定性关联；`internal/agent` 是内置循环；`internal/mcpserver` 是把工具表转成 MCP 的薄适配层；`skills/ceph-doctor/` 是唯一诊断知识源，被内置 agent 通过 `go:embed` 消费。

**Tech Stack:** Go 1.23、`github.com/modelcontextprotocol/go-sdk v1.3.1`、cobra、viper、`golang.org/x/crypto/ssh`。

**Spec:** `docs/superpowers/specs/2026-10-28-infra-agent-mcp-rewrite-design.md`

---

## 文件结构总览

| 路径 | 责任 |
|---|---|
| `cmd/root.go` | cobra root、配置文件初始化、环境变量前缀 |
| `cmd/doctor.go` | `doctor` 命令：连接、快照、内置 agent、交互 |
| `cmd/mcp.go` | `mcp` 命令：stdio / HTTP |
| `cmd/serve.go` | `serve` 命令：飞书长连接 |
| `cmd/selfcheck.go` | `doctor --self-check` 的实现 |
| `internal/config/config.go` | `Config` 及其子结构、默认值、`Pick` |
| `internal/config/load.go` | viper 装配、flag 绑定、`Load` |
| `internal/access/**` | 从 `pkg/doctor/access` 搬入，逻辑不变 |
| `internal/evidence/**` | 从 `pkg/doctor/loop` 搬入，包名改为 `evidence` |
| `internal/ceph/status.go` | `ceph status -f json` 解析与摘要（snapshot 与工具共用） |
| `internal/ceph/tools/**` | 17 个只读工具 + Registry |
| `internal/ceph/snapshot/collect.go` | 会话起点采集 |
| `internal/ceph/snapshot/correlate.go` | 告警确定性关联 |
| `internal/ceph/snapshot/podhealth.go` | Rook pod 摘要 |
| `internal/agent/agent.go` | 内置调查循环 |
| `internal/agent/prompt.go` | embed SKILL.md + 动态集群段 |
| `internal/agent/skill.go` | `read_skill` 工具 |
| `internal/agent/llm/**` | OpenAI 兼容客户端 |
| `internal/session/session.go` | 一套集群的运行时装配 |
| `internal/session/lazy.go` | MCP 用的惰性连接 |
| `internal/mcpserver/server.go` | Registry → `mcp.Server`，stdio |
| `internal/mcpserver/http.go` | Streamable HTTP + Bearer |
| `internal/feishu/**` | 飞书消息收发与会话管理 |
| `skills/embed.go` | `go:embed` 入口 |
| `skills/ceph-doctor/SKILL.md` | 诊断知识主文档 |
| `skills/ceph-doctor/references/*.md` | 分主题细节 |

---

## Task 1: 把核心包搬到 `internal/`（保持测试全绿）

**Files:**
- Move: `pkg/doctor/access` → `internal/access`
- Move: `pkg/doctor/tools` → `internal/ceph/tools`
- Move: `pkg/doctor/llm` → `internal/agent/llm`
- Move: `pkg/doctor/loop` → `internal/evidence`
- Move: `pkg/feishu` → `internal/feishu`
- Modify: 所有 import 路径
- Modify: `cmd/doctor.go`（删 `snapshotRunner`）
- Modify: `pkg/doctor/snapshot.go`（删 `CollectSnapshot` 与 `snapshotCollector`）
- Modify: `pkg/doctor/agent.go`（删 `turnStart`）
- Modify: `pkg/doctor/agent_prompt.go`（删 `var _ = access.Capabilities{}`）
- Modify: `pkg/doctor/snapshot_test.go`（删 `var _ = time.Second`）

- [ ] **Step 1: 建分支**

```bash
git switch -c refactor/mcp-rewrite
```

- [ ] **Step 2: 建立新目录并移动包**

```bash
mkdir -p internal/ceph internal/agent
git mv pkg/doctor/access internal/access
git mv pkg/doctor/tools internal/ceph/tools
git mv pkg/doctor/llm internal/agent/llm
git mv pkg/doctor/loop internal/evidence
git mv pkg/feishu internal/feishu
```

- [ ] **Step 3: 改写 import 路径**

```bash
MOD=github.com/microyahoo/infra-agent
for pair in \
  "pkg/doctor/access:internal/access" \
  "pkg/doctor/tools:internal/ceph/tools" \
  "pkg/doctor/llm:internal/agent/llm" \
  "pkg/doctor/loop:internal/evidence" \
  "pkg/feishu:internal/feishu"; do
  old="$MOD/${pair%%:*}"
  new="$MOD/${pair##*:}"
  grep -rl "$old" --include='*.go' . | xargs sed -i "s|$old|$new|g"
done
```

- [ ] **Step 4: 把 `loop` 包改名为 `evidence`**

```bash
sed -i 's/^package loop$/package evidence/' internal/evidence/evidence.go internal/evidence/evidence_test.go
grep -rl '\bloop\.' --include='*.go' . | xargs sed -i 's/\bloop\./evidence./g'
sed -i 's|^// Package loop |// Package evidence |' internal/evidence/evidence.go
```

- [ ] **Step 5: 删除死代码**

删除 `cmd/doctor.go` 中这一整段：

```go
// snapshotRunner 把 access.Cluster 适配成工具层需要的 Runner。
type snapshotRunner struct{ c *access.Cluster }

func (r snapshotRunner) Run(ctx context.Context, node string, argv []string) (access.Output, error) {
	return r.c.Run(ctx, node, argv)
}
func (r snapshotRunner) Capabilities(ctx context.Context, node string) access.Capabilities {
	return r.c.Capabilities(ctx, node)
}
func (r snapshotRunner) Topology(ctx context.Context) (*access.Topology, error) {
	return r.c.Topology(ctx)
}
func (r snapshotRunner) Nodes() []string { return r.c.Nodes() }
```

删除 `pkg/doctor/snapshot.go` 中这两个声明：

```go
// CollectSnapshot 采集会话开始时需要的全部背景信息。
//
// 只做一次，之后由 Agent 复用。采集失败不阻断：缺什么写进 Failures，
// 交给模型在对话中自行补查。
func CollectSnapshot(ctx context.Context, c *access.Cluster, _ string) (Snapshot, error) {
	return CollectSnapshotFrom(ctx, snapshotCollector{c})
}
```

```go
// snapshotCollector 把 access.Cluster 适配为 Collector。
type snapshotCollector struct{ c *access.Cluster }

func (r snapshotCollector) Run(ctx context.Context, node string, argv []string) (access.Output, error) {
	return r.c.Run(ctx, node, argv)
}
func (r snapshotCollector) Capabilities(ctx context.Context, node string) access.Capabilities {
	return r.c.Capabilities(ctx, node)
}
func (r snapshotCollector) Topology(ctx context.Context) (*access.Topology, error) {
	return r.c.Topology(ctx)
}
func (r snapshotCollector) Nodes() []string { return r.c.Nodes() }
```

同时确认 `pkg/doctor/snapshot.go` 顶部的 `"github.com/microyahoo/infra-agent/internal/access"` 是否还被使用；如果不再使用，删掉该 import。

删除 `pkg/doctor/agent.go` 中这两行（分别位于 `Send` 的开头与结尾）：

```go
	turnStart := a.chain.Len()
```

```go
	_ = turnStart
```

删除 `pkg/doctor/agent_prompt.go` 末尾这一行（`access` import 仍被 `access.OSDPlacement` 使用，保留 import）：

```go
var _ = access.Capabilities{}
```

删除 `pkg/doctor/snapshot_test.go` 中这一行，并删除不再使用的 `time` import：

```go
var _ = time.Second
```

- [ ] **Step 6: 构建、vet、测试**

```bash
go build ./... && go vet ./... && go test ./...
```

Expected: 全部通过。`grep -rn "pkg/doctor/loop\|pkg/doctor/access\|pkg/doctor/tools\|pkg/doctor/llm" --include="*.go" .` 无输出。

- [ ] **Step 7: 提交**

```bash
git add -A
git commit -m "refactor: move doctor core packages under internal/"
```

---

## Task 2: 删除旧 K8s 产品线与交付残留

**Files:**
- Delete: `pkg/analyzer/**`、`pkg/ai/**`、`pkg/common/**`、`pkg/diagnosis/**`、`pkg/executor/**`、`pkg/kubernetes/**`、`pkg/notifier/**`、`pkg/patrol/**`
- Delete: `cmd/analyze.go`、`cmd/patrol.go`、`cmd/config.go`
- Delete: 根目录 4 份 `FINAL_*` / `PROJECT_SUMMARY.md`、`demo.sh`、`test.sh`、`Makefile.test`
- Delete: `config/doctor.yaml`
- Delete: `docs/AUTO_DIAGNOSIS.md`、`docs/AUTONOMOUS_DIAGNOSIS.md`、`docs/MULTI_CLUSTER_ARCHITECTURE.md`、`docs/ROOK_CEPH_MONITORING.md`、`docs/REMOTE_STORAGE_MONITORING.md`、`docs/ARCHITECTURE.md`、`docs/FEISHU_INTEGRATION.md`、`docs/GETTING_STARTED.md`
- Modify: `cmd/root.go`（删 `namespace` / `kubeconfig` 全局 flag）
- Modify: `Makefile`（删旧目标）
- Modify: `go.mod` / `go.sum`

- [ ] **Step 1: 删除旧产品线源码**

```bash
git rm -r pkg/analyzer pkg/ai pkg/common pkg/diagnosis pkg/executor pkg/kubernetes pkg/notifier pkg/patrol
git rm cmd/analyze.go cmd/patrol.go cmd/config.go
```

- [ ] **Step 2: 删除交付残留与失效脚本**

```bash
git rm FINAL_SUMMARY.md FINAL_DELIVERY.md FINAL_COMPLETE_SUMMARY.md PROJECT_SUMMARY.md
git rm demo.sh test.sh Makefile.test
git rm config/doctor.yaml
```

- [ ] **Step 3: 删除过时文档**

```bash
git rm docs/AUTO_DIAGNOSIS.md docs/AUTONOMOUS_DIAGNOSIS.md docs/MULTI_CLUSTER_ARCHITECTURE.md
git rm docs/ROOK_CEPH_MONITORING.md docs/REMOTE_STORAGE_MONITORING.md
git rm docs/ARCHITECTURE.md docs/FEISHU_INTEGRATION.md docs/GETTING_STARTED.md
```

- [ ] **Step 4: 精简 `cmd/root.go`**

把 `var` 块改成只剩 `cfgFile`：

```go
var cfgFile string
```

删除 `init()` 中的这两行：

```go
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Kubernetes 命名空间 (为空则所有命名空间)")
	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig 文件路径 (默认: $HOME/.kube/config)")
```

并删除 `rootCmd.Long` 里关于 Kubernetes 云云的整段，改成：

```go
	Long: `Infra Agent 通过 SSH 只读访问 Ceph 集群，提供对话式诊断。

可作为命令行工具、MCP server 或飞书机器人运行。`,
```

- [ ] **Step 5: 精简 `Makefile`**

删除 `run`、`test-basic`、`test-analyze`、`test-all`、`demo` 五个目标，以及 `help` 中的 `run` 一行。文件末尾保持到 `fmt` / `deps` / `docker-*` 为止。删除后最后 20 行应为：

```make
# Docker 推送
docker-push:
	@echo "Pushing Docker image..."
	docker push microyahoo/$(BINARY_NAME):$(VERSION)
	docker push microyahoo/$(BINARY_NAME):latest

# 帮助信息
help:
	@echo "Available targets:"
	@echo "  build        - Build the binary"
	@echo "  build-all    - Build for all platforms"
	@echo "  test         - Run tests"
	@echo "  coverage     - Generate test coverage report"
	@echo "  clean        - Clean build artifacts"
	@echo "  install      - Install the binary"
	@echo "  lint         - Run linter"
	@echo "  fmt          - Format code"
	@echo "  deps         - Update dependencies"
	@echo "  docker-build - Build Docker image"
	@echo "  docker-push  - Push Docker image"
```

- [ ] **Step 6: 清理依赖**

```bash
go mod tidy
```

Expected: `k8s.io/*`、`sigs.k8s.io/*` 全部从 `go.mod` 消失。

- [ ] **Step 7: 构建、vet、测试**

```bash
go build ./... && go vet ./... && go test ./...
```

- [ ] **Step 8: 提交**

```bash
git add -A
git commit -m "refactor: remove the legacy Kubernetes product line"
```

---

## Task 3: 单一配置层

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/load.go`
- Create: `internal/config/load_test.go`
- Modify: `cmd/doctor.go`（整文件重写）
- Modify: `cmd/doctor_config.go`（删除；职责并入 `internal/config`）
- Modify: `cmd/serve.go`（改为从 `config.Config` 映射）
- Modify: `cmd/doctor_selfcheck.go`（改用 `session` 装配，见 Task 5；本任务先保留签名）
- Modify: `cmd/root.go`（`initConfig` 增加 env 前缀）
- Modify: `config/doctor.example.yaml`

- [ ] **Step 1: 先写失败的测试**

创建 `internal/config/load_test.go`：

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
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
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/config/ 2>&1 | head -20
```

Expected: `undefined: New` 或 `no Go files`。

- [ ] **Step 3: 实现 `internal/config/config.go`**

```go
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
```

- [ ] **Step 4: 实现 `internal/config/load.go`**

```go
package config

import (
	"fmt"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// New 返回配置好的 viper 实例：默认值 + 环境变量绑定。
//
// 环境变量只用于密钥类字段。其余字段若也允许环境变量覆盖，
// 排查"为什么这个值是 X"会变得很困难。
func New() *viper.Viper {
	v := viper.New()
	v.SetEnvPrefix("INFRA_AGENT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	_ = v.BindEnv("llm.api_key", "INFRA_AGENT_LLM_API_KEY")
	_ = v.BindEnv("feishu.app_id", "FEISHU_APP_ID")
	_ = v.BindEnv("feishu.app_secret", "FEISHU_APP_SECRET")
	_ = v.BindEnv("mcp.token", "INFRA_AGENT_MCP_TOKEN")

	setDefaults(v)
	return v
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
```

- [ ] **Step 5: 修正 import**

`load.go` 用到 `strings`，把 import 块改成：

```go
import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)
```

- [ ] **Step 6: 运行测试**

```bash
go test ./internal/config/ -v
```

Expected: 5 个测试全部 PASS（`TestLoadPrecedence`、`TestPickFallsBackToDefault`、`TestAPIKeyFromEnv`、`TestDefaultAPIKeyEnvIsHonored`、`TestExplicitAPIKeyEnvWins`）。

- [ ] **Step 7: 让 `cmd/root.go` 使用 `config.New()`**

把 `initConfig` 改成：

```go
func initConfig() {
	v := viper.GetViper()
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
```

注意：`config.New()` 会创建一个**新的** viper 实例，而这里用的是全局 `viper.GetViper()`。为了让命令行与测试共用同一套默认值，`config.New` 的默认值逻辑改为导出函数：

把 `load.go` 中的

```go
func New() *viper.Viper {
	v := viper.New()
```

改为：

```go
func New() *viper.Viper {
	v := viper.New()
	Apply(v)
	return v
}

// Apply 把环境变量绑定与默认值写入 v。
func Apply(v *viper.Viper) {
```

并把原来的函数体（从 `v.SetEnvPrefix` 到 `setDefaults(v)`）整体移入 `Apply`。

然后在 `cmd/root.go` 的 `initConfig` 开头加一行：

```go
	config.Apply(v)
```

import 增加：

```go
	"github.com/microyahoo/infra-agent/internal/config"
```

- [ ] **Step 8: 重写 `cmd/serve.go`**

本步只改配置来源，不动机器人逻辑。把整个文件替换为：

```go
package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/microyahoo/infra-agent/internal/config"
	"github.com/microyahoo/infra-agent/internal/feishu"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "以飞书机器人方式运行（长连接）",
	Long: `启动飞书长连接机器人：在群里 @ 它提问，它会调查集群并回复。

长连接由本程序主动连接飞书服务器，不需要公网 IP 或回调地址。
需要在飞书开放平台为自建应用开启：
  · 机器人能力
  · 事件订阅：接收消息 im.message.receive_v1（使用长连接）
  · 权限：im:message、im:message:send_as_bot

集群与模型配置见配置文件的 clusters 与 feishu 段。`,
	RunE: runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	f := serveCmd.Flags()
	f.String("app-id", "", "飞书应用 App ID")
	f.String("app-secret", "", "飞书应用 App Secret")
	f.String("cluster", "", "默认集群名（对应配置里的 clusters）")
}

func runServe(cmd *cobra.Command, _ []string) error {
	v := viper.GetViper()
	if err := config.BindFlags(v, cmd.Flags(), map[string]string{
		"app-id":     "feishu.app_id",
		"app-secret": "feishu.app_secret",
		"cluster":    "feishu.default_cluster",
	}); err != nil {
		return err
	}
	cfg, err := config.Load(v, nil)
	if err != nil {
		return err
	}

	bot, err := feishu.New(loadBotConfig(cfg))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintln(os.Stderr, "🤖 飞书机器人启动中（长连接）...")
	err = bot.Run(ctx)
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "已停止")
		return nil
	}
	return err
}

// loadBotConfig 把统一配置映射成飞书机器人配置。
func loadBotConfig(cfg config.Config) feishu.Config {
	clusters := make(map[string]feishu.ClusterConfig, len(cfg.Clusters))
	for name, cc := range cfg.Clusters {
		clusters[name] = feishu.ClusterConfig{
			Name:           name,
			Host:           cc.Host,
			Port:           cc.Port,
			User:           cc.User,
			PrivateKeyPath: config.ExpandHome(cc.PrivateKeyPath),
			Password:       cc.Password,
			KnownHostsPath: config.ExpandHome(cc.KnownHostsPath),
		}
	}

	name := cfg.Feishu.DefaultCluster
	if name == "" {
		name = cfg.DefaultCluster
	}

	return feishu.Config{
		AppID:             cfg.Feishu.AppID,
		AppSecret:         cfg.Feishu.AppSecret,
		VerificationToken: cfg.Feishu.VerificationToken,
		EncryptKey:        cfg.Feishu.EncryptKey,
		Cluster:           clusters[name],
		Clusters:          clusters,
		ChatClusters:      cfg.Feishu.ChatClusters,
		LLM: feishu.LLMConfig{
			BaseURL:    cfg.LLM.BaseURL,
			APIKey:     cfg.LLM.APIKey,
			Model:      cfg.LLM.Model,
			MaxTokens:  cfg.LLM.MaxTokens,
			MaxRetries: cfg.LLM.MaxRetries,
		},
		SourceDir:        cfg.Ceph.SourceDir,
		MaxRounds:        cfg.Loop.MaxRounds,
		MaxCallsPerRound: cfg.Loop.MaxCallsPerRound,
		MaxCallsPerTurn:  cfg.Loop.MaxToolCalls,
		IdleTimeout:      time.Duration(cfg.Feishu.IdleTimeoutMinutes) * time.Minute,
	}
}
```

注意：`cmd/doctor_config.go` 里的 `expandHome` 暂时保留（`doctor.go` 还在用），Task 5 删除该文件时一并清理。本文件使用 `config.ExpandHome`。

- [ ] **Step 9: 重写配置示例**

把 `config/doctor.example.yaml` 整个替换为：

```yaml
# ============================================================================
# infra-agent 配置示例
#
# 优先级：命令行 > 环境变量 > 本文件 > 内置默认值
# 复制为 ~/.infra-agent.yaml 即可被自动读取。
# ============================================================================

# 默认使用哪套集群。doctor / mcp / serve 都可以用 --cluster 临时覆盖。
default_cluster: stor-01

# 可接入的集群。一套集群 = 一个 SSH 入口节点，其余节点由它跳转。
clusters:
  stor-01:
    host: 10.3.14.48
    port: 22
    user: root
    # 认证二选一，私钥优先。不要把生产私钥提交进版本库。
    private_key: ~/.ssh/id_rsa
    # password: ""
    # 配置后启用严格主机键校验；留空退回 TOFU，并在诊断结果里标注。
    known_hosts: ~/.ssh/known_hosts
    # 诊断命令超时（秒）。故障集群的 ceph -s 可能卡住，不要设得太短。
    command_timeout: 30
    # 能力探测与拓扑发现的单条命令超时（秒）。
    probe_timeout: 8

  # stor-02:
  #   host: 10.4.1.10
  #   user: root
  #   private_key: ~/.ssh/id_rsa

# 模型（OpenAI 兼容接口）
llm:
  model: gpt-4o
  # 中转站填它自己的地址，不要带 /chat/completions。
  base_url: ""
  # 建议留空，改用环境变量 INFRA_AGENT_LLM_API_KEY。
  api_key: ""
  api_key_env: OPENAI_API_KEY
  temperature: 0.2
  # 单次回复上限。多数中转站默认值很小，容易把结论截断，不建议设 0。
  max_tokens: 8192
  timeout: 120
  # -1 = 内置默认（2 次）；0 = 不重试。429/5xx 重试，4xx 不重试。
  max_retries: -1

# 内置调查循环预算。仅 doctor / serve 使用。
loop:
  # 一轮 = 一次模型调用。体检 3-5 轮，复杂性能问题 10-20 轮。
  max_rounds: 20
  max_tool_calls: 60
  # 单轮并行工具调用上限。设 1-2 才能保证"看一步走一步"。
  max_calls_per_round: 2

# 只读策略
policy:
  # 额外允许执行的只读程序。变更类命令始终被拒绝。
  extra_binaries: []

# Ceph 专项
ceph:
  # 本地 Ceph 源码目录，供 source_lookup 使用。留空则该工具不可用。
  source_dir: ""

# MCP server（infra-agent mcp）
mcp:
  # HTTP 模式监听地址；stdio 模式忽略。
  listen: 127.0.0.1:9100
  # HTTP 模式必填。等价于把集群只读权限放到网络上，不要留空。
  token: ""
  # 该进程绑定哪套集群。留空用 default_cluster。
  cluster: ""

# 飞书机器人（infra-agent serve）
feishu:
  app_id: ""
  app_secret: ""
  # 长连接模式不需要下列两项。
  verification_token: ""
  encrypt_key: ""
  # 该机器人默认查哪套集群。留空用 default_cluster。
  default_cluster: ""
  # 群 → 集群绑定。键是 chat_id。
  chat_clusters: {}
    # oc_xxxxxxxxxxxxxxxx: stor-01
  # 会话空闲多久回收 SSH 连接（分钟）。
  idle_timeout_minutes: 30

# 运行行为
run:
  # 总超时（秒），覆盖连接、快照、全部调查轮次。
  timeout: 600
  dry_run: false
  self_check: false
  interactive: true
```

- [ ] **Step 10: 构建、vet、测试**

```bash
go build ./... && go vet ./... && go test ./...
```

Expected: 全部通过。此时 `doctor` 仍走旧的 `doctorOptions` 路径（Task 5 再切），但配置层与 example 已就位。

- [ ] **Step 11: 提交**

```bash
git add -A
git commit -m "refactor(config): introduce a single configuration layer"
```

---

## Task 4: `ceph/status` 共用解析 + snapshot 拆包 + `ceph_overview`

**Files:**
- Create: `internal/ceph/status.go`
- Create: `internal/ceph/status_test.go`
- Move: `pkg/doctor/snapshot.go` → `internal/ceph/snapshot/collect.go`（拆分出 `podhealth.go`）
- Move: `pkg/doctor/correlate.go` → `internal/ceph/snapshot/correlate.go`
- Move: `pkg/doctor/snapshot_test.go` → `internal/ceph/snapshot/collect_test.go`
- Move: `pkg/doctor/correlate_test.go` → `internal/ceph/snapshot/correlate_test.go`
- Create: `internal/ceph/snapshot/podhealth.go`
- Create: `internal/ceph/tools/overview.go`
- Create: `internal/ceph/tools/overview_test.go`
- Modify: `internal/ceph/tools/registry.go`
- Modify: `internal/ceph/tools/ceph_status.go`
- Modify: `pkg/doctor/agent_prompt.go`
- Modify: `pkg/doctor/setup.go`
- Modify: `cmd/doctor_selfcheck.go`

- [ ] **Step 1: 先写 `ceph/status.go` 的失败测试**

创建 `internal/ceph/status_test.go`：

```go
package ceph

import "testing"

const sampleStatus = `{
  "fsid": "fsid-abc",
  "health": {"status": "HEALTH_WARN", "checks": {
    "OSD_DOWN": {"summary": {"message": "2 osds down"}}
  }},
  "monmap": {"num_mons": 3},
  "osdmap": {"num_osds": 10, "num_up_osds": 8, "num_in_osds": 10},
  "pgmap": {
    "num_pgs": 256,
    "pgs_by_state": [
      {"state_name": "active+clean", "count": 200},
      {"state_name": "active+undersized", "count": 56}
    ],
    "bytes_total": 2000,
    "bytes_used": 1000
  }
}`

func TestParseStatus(t *testing.T) {
	st, err := ParseStatus(sampleStatus)
	if err != nil {
		t.Fatal(err)
	}
	if st.FSID != "fsid-abc" || st.Health != "HEALTH_WARN" {
		t.Fatalf("basic fields: %+v", st)
	}
	if st.NumOSDs != 10 || st.NumOSDsUp != 8 || st.NumMons != 3 || st.NumPGs != 256 {
		t.Fatalf("counts: %+v", st)
	}
	if st.DegradedPGs != 56 {
		t.Errorf("degraded: got %d", st.DegradedPGs)
	}
	if st.UsedPercent != 50 {
		t.Errorf("used percent: got %v", st.UsedPercent)
	}
	if len(st.HealthChecks) != 1 || st.HealthChecks[0] != "OSD_DOWN" {
		t.Errorf("checks: %v", st.HealthChecks)
	}
	if len(st.HealthMessages) != 1 || st.HealthMessages[0] != "2 osds down" {
		t.Errorf("messages: %v", st.HealthMessages)
	}
}

func TestParseStatusRejectsGarbage(t *testing.T) {
	if _, err := ParseStatus("not json"); err == nil {
		t.Fatal("expected error")
	}
}

func TestStatusSummary(t *testing.T) {
	st, _ := ParseStatus(sampleStatus)
	got := st.Summary()
	want := "mon 3，OSD 10 总/8 up/10 in，PG 256（异常 56），容量 50.0%"
	if got != want {
		t.Errorf("summary:\n got %q\nwant %q", got, want)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/ceph/
```

Expected: `undefined: ParseStatus`。

- [ ] **Step 3: 实现 `internal/ceph/status.go`**

```go
// Package ceph 提供 Ceph 数据结构的解析与摘要，供工具层与会话快照共用。
package ceph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Status 是 `ceph status -f json` 中与诊断相关的字段。
type Status struct {
	FSID           string    `json:"fsid"`
	Health         string    `json:"health"`
	HealthChecks   []string  `json:"health_checks,omitempty"`
	HealthMessages []string  `json:"health_messages,omitempty"`
	NumMons        int       `json:"num_mons"`
	NumOSDs        int       `json:"num_osds"`
	NumOSDsUp      int       `json:"num_osds_up"`
	NumOSDsIn      int       `json:"num_osds_in"`
	NumPGs         int       `json:"num_pgs"`
	DegradedPGs    int       `json:"degraded_pgs"`
	PGState        []PGState `json:"pg_state"`
	BytesTotal     uint64    `json:"bytes_total"`
	BytesUsed      uint64    `json:"bytes_used"`
	UsedPercent    float64   `json:"used_percent"`
}

// PGState 是单个 PG 状态及其数量。
type PGState struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

type rawStatus struct {
	FSID   string `json:"fsid"`
	Health struct {
		Status string                     `json:"status"`
		Checks map[string]json.RawMessage `json:"checks"`
	} `json:"health"`
	Monmap struct {
		NumMons int `json:"num_mons"`
	} `json:"monmap"`
	OSDMap struct {
		NumOSDs   int `json:"num_osds"`
		NumUpOSDs int `json:"num_up_osds"`
		NumInOSDs int `json:"num_in_osds"`
	} `json:"osdmap"`
	PGMap struct {
		NumPGs     int `json:"num_pgs"`
		PGsByState []struct {
			StateName string `json:"state_name"`
			Count     int    `json:"count"`
		} `json:"pgs_by_state"`
		BytesTotal uint64 `json:"bytes_total"`
		BytesUsed  uint64 `json:"bytes_used"`
	} `json:"pgmap"`
}

// ParseStatus 解析 ceph status JSON。
func ParseStatus(raw string) (Status, error) {
	var rs rawStatus
	if err := json.Unmarshal([]byte(raw), &rs); err != nil {
		return Status{}, err
	}

	out := Status{
		FSID:       strings.TrimSpace(rs.FSID),
		Health:     strings.TrimSpace(rs.Health.Status),
		NumMons:    rs.Monmap.NumMons,
		NumOSDs:    rs.OSDMap.NumOSDs,
		NumOSDsUp:  rs.OSDMap.NumUpOSDs,
		NumOSDsIn:  rs.OSDMap.NumInOSDs,
		NumPGs:     rs.PGMap.NumPGs,
		BytesTotal: rs.PGMap.BytesTotal,
		BytesUsed:  rs.PGMap.BytesUsed,
	}

	// checks 是 map，遍历顺序不确定，排序后保证摘要稳定可复现。
	for name, rawCheck := range rs.Health.Checks {
		out.HealthChecks = append(out.HealthChecks, name)
		var c struct {
			Summary struct {
				Message string `json:"message"`
			} `json:"summary"`
		}
		if json.Unmarshal(rawCheck, &c) == nil && c.Summary.Message != "" {
			out.HealthMessages = append(out.HealthMessages, c.Summary.Message)
		}
	}
	sort.Strings(out.HealthChecks)
	sort.Strings(out.HealthMessages)

	for _, s := range rs.PGMap.PGsByState {
		out.PGState = append(out.PGState, PGState{State: s.StateName, Count: s.Count})
		if s.StateName != "active+clean" {
			out.DegradedPGs += s.Count
		}
	}
	if out.BytesTotal > 0 {
		out.UsedPercent = float64(out.BytesUsed) / float64(out.BytesTotal) * 100
	}
	return out, nil
}

// Summary 返回一句紧凑的中文概览。
func (s Status) Summary() string {
	return fmt.Sprintf("mon %d，OSD %d 总/%d up/%d in，PG %d（异常 %d），容量 %s",
		s.NumMons, s.NumOSDs, s.NumOSDsUp, s.NumOSDsIn, s.NumPGs, s.DegradedPGs, formatPercent(s.UsedPercent))
}

// formatPercent 按数值大小选择有效位数：大容量集群早期使用率极低，
// 固定 %.1f 会显示成 0.0%，看起来像数据缺失。
func formatPercent(p float64) string {
	switch {
	case p == 0:
		return "0%"
	case p < 0.01:
		return fmt.Sprintf("%.4f%%", p)
	case p < 1:
		return fmt.Sprintf("%.2f%%", p)
	}
	return fmt.Sprintf("%.1f%%", p)
}
```

- [ ] **Step 4: 运行测试**

```bash
go test ./internal/ceph/ -v
```

Expected: 3 个测试 PASS。

- [ ] **Step 5: 搬迁 snapshot 与 correlate**

```bash
mkdir -p internal/ceph/snapshot
git mv pkg/doctor/snapshot.go internal/ceph/snapshot/collect.go
git mv pkg/doctor/snapshot_test.go internal/ceph/snapshot/collect_test.go
git mv pkg/doctor/correlate.go internal/ceph/snapshot/correlate.go
git mv pkg/doctor/correlate_test.go internal/ceph/snapshot/correlate_test.go
```

- [ ] **Step 6: 改包名与导入**

在 `internal/ceph/snapshot/*.go` 中：

- `package doctor` → `package snapshot`
- 删除对 `internal/evidence` 以外的 `pkg/doctor` 自引用（这些文件原本同属 `doctor` 包，搬迁后不再需要）
- `collect.go` 增加 import：

```go
import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/microyahoo/infra-agent/internal/access"
	ceph "github.com/microyahoo/infra-agent/internal/ceph"
	"github.com/microyahoo/infra-agent/internal/evidence"
)
```

- 把 `CollectSnapshotFrom(ctx context.Context, c Collector)` 改名为 `Collect(ctx context.Context, c Collector)`，更新注释
- 删除 `parseSnapshotStatus`、`statusView`、`rawSnapshotStatus`、`formatPercent`（已由 `internal/ceph/status.go` 提供）
- 把 `collect.go` 中调用 `parseSnapshotStatus(out.Stdout)` 的段落改为：

```go
	st, perr := ceph.ParseStatus(out.Stdout)
	if perr != nil {
		s.Health = "unknown"
		s.Failures = append(s.Failures, fmt.Sprintf("解析 ceph status 失败：%v", perr))
		assignEvidenceIDs(&s)
		return s, nil
	}
	s.Health = st.Health
	s.Summary = st.Summary()
	if st.FSID != "" {
		s.ClusterID = st.FSID
	}
```

- 把 `collectPodHealth`、`summarizePods` 两个函数从 `collect.go` 剪切到新建的 `internal/ceph/snapshot/podhealth.go`，文件头：

```go
package snapshot

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)
```

`collect.go` 相应删除 `sort`、`strconv` 两个不再使用的 import。

- 在 `correlate.go` 中把 `referencedOSDs` 改名为 `ReferencedOSDs`（导出），并给 `Finding` 加 JSON tag：

```go
type Finding struct {
	Summary string `json:"summary"`
	Why     string `json:"why"`
	Next    string `json:"next"`
}
```

- 把 `collect_test.go` / `correlate_test.go` 中的 `package doctor` 改为 `package snapshot`，并修正对 `Snapshot`、`Collect` 的调用（原 `CollectSnapshotFrom` → `Collect`）。
- `collect_test.go` 中 `newFakeCollector` 返回的 `fakeCollector` 若实现了 `Collector`，无需改动。

- [ ] **Step 7: 更新 `pkg/doctor` 中仍在引用 snapshot 的代码**

`pkg/doctor/agent_prompt.go`：

- import 增加 `"github.com/microyahoo/infra-agent/internal/ceph/snapshot"`
- `func AgentPrompt(s Snapshot) string` → `func AgentPrompt(s snapshot.Snapshot) string`
- 类型引用 `Finding` → `snapshot.Finding`
- `referencedOSDs(s, 24)` → `snapshot.ReferencedOSDs(s, 24)`
- `truncateLines`、`orDash` 保持在本文件（agent 专用展示函数）

`pkg/doctor/setup.go`：

- import 增加 `"github.com/microyahoo/infra-agent/internal/ceph/snapshot"`
- `snap, err := CollectSnapshotFrom(ctx, runner)` → `snap, err := snapshot.Collect(ctx, runner)`
- `func Open(...) (*Agent, Snapshot, error)` → `(*Agent, snapshot.Snapshot, error)`
- 其它 `Snapshot` 引用加 `snapshot.` 前缀

`pkg/doctor/agent.go` 不需要改（它不引用 Snapshot）。

- [ ] **Step 8: 让 `ceph_status` 工具改用共用解析**

`internal/ceph/tools/ceph_status.go`：

- 删除文件内的 `CephStatusData`、`PGState`、`rawStatus`、`formatPercent` 定义
- import 增加 `ceph "github.com/microyahoo/infra-agent/internal/ceph"`
- `Execute` 中解析与摘要改为：

```go
	st, jerr := ceph.ParseStatus(out.Stdout)
	if jerr != nil {
		return nil, nil, fmt.Errorf("解析 ceph status 输出失败: %w", jerr)
	}
	d := &st
```

- 后续引用 `d.Health`、`d.HealthMessages` 等保持不变
- `Result.Data` 仍为 `d`（类型变为 `*ceph.Status`）
- 清理不再使用的 import：解析逻辑搬走后 `sort`、`strconv` 不再需要；`json` 仍被 `Schema()` 使用，保留。修改完先跑 `go build ./internal/ceph/tools/`，按报错逐个删 import

- [ ] **Step 9: 先写 `ceph_overview` 的失败测试**

创建 `internal/ceph/tools/overview_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOverviewReportsFindingsAndPlacement(t *testing.T) {
	r := newFakeRunner()
	r.on("node-3", []string{"ceph", "status", "-f", "json"}, `{
      "fsid": "fsid-abc",
      "health": {"status": "HEALTH_WARN"},
      "monmap": {"num_mons": 3},
      "osdmap": {"num_osds": 3, "num_up_osds": 1, "num_in_osds": 3},
      "pgmap": {"num_pgs": 128, "pgs_by_state": [
        {"state_name": "active+clean", "count": 100},
        {"state_name": "active+undersized", "count": 28}
      ]}
    }`)
	r.on("node-3", []string{"ceph", "health", "detail"},
		"HEALTH_WARN 2 osds down\n"+
			"osd.7 (root=default,host=node-3) is down\n"+
			"osd.8 (root=default,host=node-3) is down\n")

	tool := NewOverview(r)
	res, _, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Summary, "HEALTH_WARN") {
		t.Errorf("summary: %q", res.Summary)
	}
	data, ok := res.Data.(*OverviewData)
	if !ok {
		t.Fatalf("data type: %T", res.Data)
	}
	if len(data.Findings) == 0 {
		t.Fatal("expected deterministic findings for two OSDs down on one node")
	}
	if len(data.OSDPlacement) != 2 {
		t.Fatalf("expected placement for osd.7 and osd.8, got %+v", data.OSDPlacement)
	}
	if data.OSDPlacement[0].Node != "node-3" {
		t.Errorf("placement node: %+v", data.OSDPlacement[0])
	}
}

func TestOverviewIncludesPodHealthWhenKubectlExists(t *testing.T) {
	r := newFakeRunner()
	r.caps.Kubectl = true
	r.caps.RookNamespace = "rook-ceph"
	r.on("node-3", []string{"ceph", "status", "-f", "json"}, `{"health":{"status":"HEALTH_OK"},"fsid":"fsid-abc"}`)
	r.on("node-3", []string{"ceph", "health", "detail"}, "")
	r.on("node-3", []string{"ceph", "log", "last", "500"}, "")

	tool := NewOverview(r)
	res, _, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	data := res.Data.(*OverviewData)
	if data.PodHealth == "" {
		t.Fatal("expected pod health section when kubectl is available")
	}
}
```

注意：第二个用例依赖 `fakeRunner` 支持为 kubectl 命令注册输出。若 `newFakeRunner` 默认未注册 kubectl，`podhealth.collectPodHealth` 会返回空字符串，测试会失败——这属于预期失败，Step 10 会在 `internal/ceph/snapshot/podhealth.go` 中把 kubectl 调用改为通过 `Capabilities().Kubectl` 判断后执行，测试里需要注册对应的 `kubectl ... jsonpath ...` 输出。注册命令的 argv 必须与 `podhealth.go` 中完全一致：

```go
	argv := []string{"kubectl", "-n", "rook-ceph", "get", "pods", "-o",
		"jsonpath={range .items[*]}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\t\"}{.status.phase}{\"\\t\"}" +
			"{.status.containerStatuses[*].ready}{\"\\t\"}{.status.containerStatuses[*].restartCount}{\"\\t\"}" +
			"{.status.containerStatuses[*].state.waiting.reason}{\"\\n\"}{end}"}
```

在第二个用例开头补一行（参数与 `podhealth.go` 一致）：

```go
	r.on("node-3", []string{"kubectl", "-n", "rook-ceph", "get", "pods", "-o",
		"jsonpath={range .items[*]}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\t\"}{.status.phase}{\"\\t\"}" +
			"{.status.containerStatuses[*].ready}{\"\\t\"}{.status.containerStatuses[*].restartCount}{\"\\t\"}" +
			"{.status.containerStatuses[*].state.waiting.reason}{\"\\n\"}{end}"},
		"rook-ceph-osd-0\tnode-3\tRunning\tfalse\t5\tCrashLoopBackOff\n")
```

- [ ] **Step 10: 实现 `internal/ceph/tools/overview.go`**

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/ceph/snapshot"
)

type overviewTool struct{ r Runner }

// NewOverview 构造集群概况工具。
func NewOverview(r Runner) Tool { return &overviewTool{r: r} }

func (t *overviewTool) Name() string { return "ceph_overview" }

func (t *overviewTool) Description() string {
	return "一次性查看集群概况：健康状态、告警明细、Rook pod 异常、确定性的关联结论，" +
		"以及告警涉及的 OSD 落在哪个节点、哪块盘。" +
		"开始调查一个新问题时先调用它，可以省掉 ceph_status / ceph_health / rook_pods / osd_tree 的多次往返。"
}

func (t *overviewTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

// OverviewData 是 ceph_overview 的结构化结果。
type OverviewData struct {
	Health       string             `json:"health"`
	Summary      string             `json:"summary"`
	HealthDetail string             `json:"health_detail,omitempty"`
	PodHealth    string             `json:"pod_health,omitempty"`
	Findings     []snapshot.Finding `json:"findings,omitempty"`
	OSDPlacement []OSDPlacementRow  `json:"osd_placement,omitempty"`
}

// OSDPlacementRow 是告警里提到的 OSD 的落点。
type OSDPlacementRow struct {
	OSD    int    `json:"osd"`
	Node   string `json:"node,omitempty"`
	Device string `json:"device,omitempty"`
	Status string `json:"status,omitempty"`
}

func (t *overviewTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	snap, err := snapshot.Collect(ctx, t.r)
	if err != nil {
		return nil, nil, err
	}

	data := &OverviewData{
		Health:       snap.Health,
		Summary:      snap.Summary,
		HealthDetail: snap.HealthDetail,
		PodHealth:    snap.PodHealth,
		Findings:     snap.Findings,
	}

	if snap.Topology != nil {
		place := make(map[int]access.OSDPlacement, len(snap.Topology.OSDs))
		for _, p := range snap.Topology.OSDs {
			place[p.ID] = p
		}
		for _, id := range snapshot.ReferencedOSDs(snap, 24) {
			row := OSDPlacementRow{OSD: id}
			if p, ok := place[id]; ok {
				row.Node, row.Device, row.Status = p.Host, p.Device, p.Status
			}
			data.OSDPlacement = append(data.OSDPlacement, row)
		}
	}

	res := &Result{Summary: snap.Summary, Data: data}
	for _, f := range snap.Findings {
		res.Hints = append(res.Hints, f.Next)
	}
	if snap.Health != "HEALTH_OK" && len(snap.Findings) == 0 {
		res.Hints = append(res.Hints, "告警没有匹配到已知模式：用 ceph_health 看完整明细，再按提到的对象逐个查")
	}
	if len(snap.Failures) > 0 {
		res.Hints = append(res.Hints, fmt.Sprintf("会话起点有 %d 项没采到：%s", len(snap.Failures), snap.Failures[0]))
	}
	return res, nil, nil
}
```

- [ ] **Step 11: 把 `ceph_overview` 注册进工具表**

`internal/ceph/tools/registry.go` 的 `All` 中，在 `NewCephHealth(r)` 之后插入：

```go
		NewOverview(r),
```

同时把 `DefaultRegistry` 的注释从「构建全部 12 个工具」改为「构建全部 17 个工具」。

- [ ] **Step 12: 自检加入 `ceph_overview`**

`cmd/doctor_selfcheck.go` 的 `checks` 列表开头插入：

```go
		{"ceph_overview", nil},
```

- [ ] **Step 13: 构建、vet、测试**

```bash
go build ./... && go vet ./... && go test ./...
```

Expected: 全部通过。工具总数断言（`registry_test.go` 中的 `TestDefaultRegistryHasAllTools`）如果写死了名字列表，需要在其中加入 `ceph_overview`。

- [ ] **Step 14: 提交**

```bash
git add -A
git commit -m "refactor(ceph): share status parsing and add ceph_overview"
```

---

## Task 5: Skill 事实源 + agent 搬迁 + session + CLI 接线

**Files:**
- Create: `skills/embed.go`
- Create: `skills/ceph-doctor/SKILL.md`
- Create: `skills/ceph-doctor/references/{osd,pg,performance,rook}.md`
- Create: `skills/drift_test.go`
- Move: `pkg/doctor/agent.go` → `internal/agent/agent.go`
- Move: `pkg/doctor/agent_test.go` → `internal/agent/agent_test.go`
- Create: `internal/agent/prompt.go`（由 `pkg/doctor/agent_prompt.go` 改写）
- Create: `internal/agent/prompt_test.go`
- Create: `internal/agent/skill.go`
- Create: `internal/session/session.go`
- Create: `internal/session/lazy.go`
- Modify: `internal/agent/agent.go`（接入 `read_skill`）
- Modify: `cmd/doctor.go`（整文件重写）
- Modify: `cmd/doctor_interactive.go`
- Modify: `cmd/doctor_selfcheck.go`
- Delete: `pkg/doctor/setup.go`、`pkg/doctor/agent_prompt.go`、`cmd/doctor_config.go`、`cmd/doctor_config_test.go`
- Delete: `pkg/doctor/`（清理后应为空）

- [ ] **Step 1: 创建 `skills/embed.go`**

```go
// Package skills 内嵌 Ceph 诊断知识。
//
// 同一份文档有两个消费者：
//   - 内置 agent 通过 SystemPrompt 读取 SKILL.md
//   - 外部 agent（Cursor / Dify / Claude）直接读磁盘上的 skills/ceph-doctor/
//
// 因此这里只做读取，不写任何"只对内置 agent 成立"的指令。
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed ceph-doctor
var FS embed.FS

const skillName = "ceph-doctor"

// MustRead 读取技能目录下的文件，供内置 agent 使用。
func MustRead(name string) string {
	b, err := FS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("skills: read %s: %v", name, err))
	}
	return string(b)
}

// ReadReference 按名字读取 references/<name>.md。
func ReadReference(name string) (string, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".md")
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("无效的文档名 %q", name)
	}
	b, err := FS.ReadFile(path.Join(skillName, "references", name+".md"))
	if err != nil {
		return "", fmt.Errorf("没有名为 %q 的文档", name)
	}
	return string(b), nil
}

// ReferenceNames 返回全部细节文档名（不含 .md）。
func ReferenceNames() []string {
	entries, err := fs.ReadDir(FS, path.Join(skillName, "references"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 2: 写 `skills/ceph-doctor/SKILL.md`**

```markdown
---
name: ceph-doctor
description: 通过只读工具诊断 Ceph / Rook 存储集群：定位 OSD、PG、容量、性能和 Rook 组件问题。当用户询问集群为什么 warn、某个 OSD 是否正常、写入延迟、容量告警或组件重启时使用。
---

# Ceph 集群诊断

你通过一组只读工具访问一套 Ceph 集群。工具返回的事实可以直接引用；不要编造没有查到的数据。

## 第一步

新问题先调用 `ceph_overview`：它一次性给出健康状态、告警明细、Rook pod 异常、确定性的关联结论，以及告警涉及的 OSD 落在哪台机器、哪块盘。

如果问题与告警无关（例如用户只问某个池的容量、某个 OSD 的延迟），可以直接跳到对应的工具。

## 工具路由

| 想确认的事 | 工具 |
|---|---|
| 整体健康、告警、关联结论 | `ceph_overview` |
| 健康码与告警明细原文 | `ceph_health` |
| 集群计数、容量、PG 分布 | `ceph_status` |
| OSD 拓扑、哪个 OSD 在哪台机器 | `osd_tree` |
| 单个 OSD 的状态与设备 | `osd_detail` |
| 池的容量、副本、使用率 | `pool_info` |
| PG 状态分布与异常 PG | `pg_detail` |
| 慢请求、阻塞请求 | `slow_ops` |
| OSD 内部计数器（延迟、队列） | `perf_counters` |
| 宿主机磁盘 I/O、SMART | `host_io` |
| 宿主机负载、内存、网络 | `host_check` |
| Rook 组件 pod 状态 | `rook_pods` |
| 某个 daemon 的日志 | `daemon_log` |
| 任意日志文件 | `read_log` |
| 目录浏览与按模式查找 | `explore` |
| 受只读策略约束的任意命令 | `run_command` |
| 按版本查 Ceph 源码 | `ceph_source_lookup` |

## 调查原则

- **一次查一件事**：调用一个工具，看完结果再决定下一步。不要一轮把所有想查的都发出去。
- **先假设，再定点验证**：从症状推出一个最可能的原因，用最少的查询证实或推翻它。
- **只读**：变更类命令会被拒绝，不要尝试。
- **工具报错不等于没有问题**：它意味着"没有拿到数据"，要在结论里说明缺了什么。
- **"可能是"不算结论**：用"可能 / 大概率 / 看起来"描述根因时，说明还没查实，继续用工具验证。
- **把关联留给代码，把解释留给你**：`ceph_overview` 的 findings 是确定性的关联，事实本身不需要重复验证。

## 常见故障模式

- 同一节点多个 OSD 同时 down → 先确认节点是否可达，不要逐个查 OSD。见 `references/osd.md`。
- 慢心跳集中在少数 OSD → 查这些 OSD 所在节点的负载与磁盘。见 `references/osd.md`。
- PG 长时间 peering / undersized → 通常与其副本所在 OSD 不可用是同一件事。见 `references/pg.md`。
- slow ops / 请求阻塞 → 后端 OSD 不可达或磁盘卡住。见 `references/performance.md`。
- daemon 反复重启 / CrashLoopBackOff → 先看 pod 状态与退出码。见 `references/rook.md`。
- 容量告警 → 直接找占满的池，不要逐池遍历。见 `references/pg.md`。

## 输出要求

- 中文，简短具体。有数字就说数字，有对象就点名（osd.13、node03、/dev/sdb）。
- 不要输出模板化的多级标题报告（结论／影响／建议这类固定结构）。
- 不要罗列你查了哪些命令，除非用户问"你是怎么查的"。
- 不确定的地方直接说不确定，不要用"建议进一步排查"充数。

## 细节文档

- `references/osd.md`：OSD down、慢心跳、磁盘故障
- `references/pg.md`：PG 降级、peering、容量
- `references/performance.md`：slow ops、延迟、宿主机 I/O
- `references/rook.md`：Rook pod、daemon 日志、退出码
```

- [ ] **Step 3: 写四份 references**

`skills/ceph-doctor/references/osd.md`：

```markdown
# OSD 故障

## OSD down

1. `ceph_overview` 已给出 down 的 OSD 及其节点。先看是否多个 OSD 落在同一节点。
2. 同一节点 ≥2 个 OSD down：先 `host_check node=<节点>` 确认节点是否可达、负载是否异常。
   节点不可达时不要逐个查 OSD。
3. 单个 OSD down：`osd_detail osd_id=<id>` 看状态与设备，再 `host_io node=<节点> device=<盘>`
   看磁盘是否报错。需要时用 `read_log` 读该 OSD 的日志。

## 慢心跳

- `ceph_health` 的 Slow OSD heartbeats 条目会给出成对 OSD。
- 对反复出现的 OSD，查其所在节点的 `host_check`（负载、网络）与 `host_io`（磁盘延迟）。
- 如果慢心跳对象正是 down 的那批 OSD，它们是同一件事，不要拆成两个问题。

## 判断顺序

节点可达性 → 磁盘健康 → OSD 进程状态 → 网络。先用 `host_check` / `host_io` 排除硬件与节点，
再进 `osd_detail`，最后才看 `daemon_log`。
```

`skills/ceph-doctor/references/pg.md`：

```markdown
# PG 与容量

## 异常 PG

- `pg_detail` 返回各状态数量与异常 PG 列表。先看状态分布，不要逐条读。
- active+undersized / degraded / peering 同时出现且伴随 OSD down：与 OSD 问题是同一根因，
  OSD 恢复后 PG 会自动收敛，不要单独处理 PG。
- 只有 PG 异常、没有 OSD down：查 `pool_info` 的副本数与 `osd_tree` 的可用 OSD 数是否匹配。

## 容量

- `pool_info` 给出各池的容量与使用率。容量告警时直接找使用率最高的池。
- 需要确认物理容量时用 `ceph_status` 的 bytes_total / bytes_used。
- 不要逐个 PG 查容量；池级数据足够回答"哪个池占满"。

## 不要做的事

- 不要试图通过重复查询等待 PG 恢复；用一次 `pg_detail` 记录当下状态即可。
```

`skills/ceph-doctor/references/performance.md`：

```markdown
# 性能

## slow ops / 阻塞请求

1. `slow_ops` 找出被阻塞的 OSD 与请求年龄。
2. 这些 OSD 若同时出现在 down / 慢心跳列表里，根因就是它们，不必另查。
3. 否则对每个 OSD：`perf_counters osd_id=<id>` 看 op latency 与队列深度，
   再 `host_io node=<节点> device=<盘>` 看磁盘 util / await。

## 延迟高但无 slow ops

- `perf_counters` 对比各 OSD 的 apply / commit latency，找出离群值。
- `host_check` 检查 CPU steal、内存压力、网络重传。
- 单块盘慢会拖累整个池；定位到盘后，问题就变成"替换或隔离这块盘"。

## 不要做的事

- 不要在未定位到具体 OSD 前对所有 OSD 跑 `perf_counters`。
- 不要用 `run_command` 执行 `ceph osd perf` 之类的重复查询——`perf_counters` 已经覆盖。
```

`skills/ceph-doctor/references/rook.md`：

```markdown
# Rook 层

## 先看 pod，再看日志

- `rook_pods` 给出 pod 状态、所在节点、重启次数与 waiting reason。
- 重启次数 ≥3 或 CrashLoopBackOff：`daemon_log daemon=<mon|osd|mgr|mds> id=<id> lines=200`
  看退出前的错误。
- 需要确认退出码时，`run_command` 执行 `kubectl -n <ns> get pod <pod> -o jsonpath=...`。

## 日志位置

- Rook 部署下日志通常在容器内，`read_log` 读不到；`daemon_log` 会自动选择宿主机或容器来源。
- `explore` 可以列出 `/var/lib/rook` 下的目录，确认实际布局。

## 判断顺序

pod 状态 → 重启次数与原因 → 该 daemon 的日志 → 对应 Ceph 的健康条目。
不要跳过 pod 状态直接翻日志。
```

- [ ] **Step 4: 写漂移测试（先失败）**

创建 `skills/drift_test.go`：

```go
package skills_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/internal/ceph/tools"
	"github.com/microyahoo/infra-agent/skills"
)

func referenceDocs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range skills.ReferenceNames() {
		doc, err := skills.ReadReference(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out = append(out, doc)
	}
	return out
}

func TestEveryToolIsDocumented(t *testing.T) {
	reg := tools.RegistryFrom(tools.All(nil, ""))
	doc := skills.MustRead("ceph-doctor/SKILL.md")
	for _, name := range reg.Names() {
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("tool %q 没有出现在 SKILL.md 的工具路由里", name)
		}
	}
}

func TestSkillDoesNotMentionUnknownTools(t *testing.T) {
	reg := tools.RegistryFrom(tools.All(nil, ""))
	re := regexp.MustCompile("`([a-z][a-z0-9_]+)`")
	prefixes := []string{
		"ceph_", "osd_", "pg_", "pool_", "host_", "rook_", "read_",
		"run_", "daemon_", "perf_", "slow_ops", "explore",
	}
	docs := append([]string{skills.MustRead("ceph-doctor/SKILL.md")}, referenceDocs(t)...)
	for _, doc := range docs {
		for _, m := range re.FindAllStringSubmatch(doc, -1) {
			name := m[1]
			matched := false
			for _, p := range prefixes {
				if strings.HasPrefix(name, p) {
					matched = true
					break
				}
			}
			if matched && !reg.Has(name) {
				t.Errorf("文档提到了不存在的工具 %q", name)
			}
		}
	}
}

func TestReferenceLinksExist(t *testing.T) {
	doc := skills.MustRead("ceph-doctor/SKILL.md")
	re := regexp.MustCompile(`references/([a-z0-9_]+)\.md`)
	for _, m := range re.FindAllStringSubmatch(doc, -1) {
		if _, err := skills.ReadReference(m[1]); err != nil {
			t.Errorf("失效的文档链接 %q: %v", m[0], err)
		}
	}
}
```

- [ ] **Step 5: 运行漂移测试**

```bash
go test ./skills/
```

Expected: 3 个测试 PASS。

- [ ] **Step 6: 创建 `internal/session/session.go`**

```go
// Package session 把连接、工具表、快照与内置 agent 装配成一次可用会话。
package session

import (
	"context"
	"time"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/agent"
	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/internal/ceph/snapshot"
	"github.com/microyahoo/infra-agent/internal/ceph/tools"
	"github.com/microyahoo/infra-agent/internal/config"
)

// Options 是建立一次会话所需的参数。
type Options struct {
	Cluster         config.Cluster
	Policy          config.Policy
	SourceDir       string
	CollectSnapshot bool
	OnEvent         func(string)
}

// Session 是一套集群上的运行时。
type Session struct {
	cluster  *access.Cluster
	registry *tools.Registry
	snap     snapshot.Snapshot
	opts     Options
}

// Open 建立 SSH 连接，可选地采集快照，并装配工具表。
func Open(ctx context.Context, opts Options) (*Session, error) {
	cluster, err := access.Dial(ctx, access.Config{
		Host:           opts.Cluster.Host,
		Port:           opts.Cluster.Port,
		User:           opts.Cluster.User,
		Password:       opts.Cluster.Password,
		PrivateKeyPath: config.ExpandHome(opts.Cluster.PrivateKeyPath),
		KnownHostsPath: config.ExpandHome(opts.Cluster.KnownHostsPath),
		CommandTimeout: time.Duration(opts.Cluster.CommandTimeout) * time.Second,
		ProbeTimeout:   time.Duration(opts.Cluster.ProbeTimeout) * time.Second,
		ExtraBinaries:  opts.Policy.ExtraBinaries,
		OnProgress:     opts.OnEvent,
	})
	if err != nil {
		return nil, err
	}

	s := &Session{cluster: cluster, opts: opts}
	if opts.CollectSnapshot {
		s.snap, err = snapshot.Collect(ctx, cluster)
		if err != nil {
			cluster.Close()
			return nil, err
		}
	}
	// access.Cluster 已经实现了工具层需要的 Runner 接口：
	// Run / Capabilities / Topology / Nodes。不再需要额外的适配器类型。
	s.registry = tools.RegistryFrom(tools.All(cluster, opts.SourceDir))
	return s, nil
}

// Cluster 返回底层连接，供 dry-run 与 self-check 使用。
func (s *Session) Cluster() *access.Cluster { return s.cluster }

// Registry 返回工具表。
func (s *Session) Registry() *tools.Registry { return s.registry }

// Snapshot 返回会话起点采集到的快照。
func (s *Session) Snapshot() snapshot.Snapshot { return s.snap }

// NewAgent 用当前会话的工具表与快照构造内置 agent。
func (s *Session) NewAgent(model llm.ChatModel, loop config.Loop) *agent.Agent {
	return agent.NewAgent(agent.Config{
		Model:            model,
		Registry:         s.registry,
		SystemPrompt:     agent.SystemPrompt(s.snap),
		SeedEvidence:     s.snap.Evidence,
		OnEvent:          s.opts.OnEvent,
		MaxRounds:        loop.MaxRounds,
		MaxCallsPerRound: loop.MaxCallsPerRound,
		MaxCallsPerTurn:  loop.MaxToolCalls,
	})
}

// Close 释放 SSH 连接。
func (s *Session) Close() error {
	if s.cluster == nil {
		return nil
	}
	return s.cluster.Close()
}
```

- [ ] **Step 7: 创建 `internal/session/lazy.go`**

```go
package session

import (
	"context"
	"sync"

	"github.com/microyahoo/infra-agent/internal/ceph/tools"
)

// Lazy 在第一次被需要时才建立会话，并在失败后允许下次调用重试。
//
// MCP 客户端可能只列出工具、从不调用；集群也可能在启动瞬间不可达。
// 因此不在进程启动时连接。
type Lazy struct {
	Open func(context.Context) (*Session, error)

	mu  sync.Mutex
	cur *Session
}

// Registry 实现 mcpserver.Provider。
func (l *Lazy) Registry(ctx context.Context) (*tools.Registry, error) {
	s, err := l.get(ctx)
	if err != nil {
		return nil, err
	}
	return s.Registry(), nil
}

func (l *Lazy) get(ctx context.Context) (*Session, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur != nil {
		return l.cur, nil
	}
	s, err := l.Open(ctx)
	if err != nil {
		return nil, err
	}
	l.cur = s
	return s, nil
}

// Reset 丢弃当前会话；下一次调用会重新连接。
func (l *Lazy) Reset() {
	l.mu.Lock()
	s := l.cur
	l.cur = nil
	l.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

// Close 关闭当前会话。
func (l *Lazy) Close() error {
	l.mu.Lock()
	s := l.cur
	l.cur = nil
	l.mu.Unlock()
	if s != nil {
		return s.Close()
	}
	return nil
}
```

- [ ] **Step 8: 搬迁 agent 并改写 prompt**

```bash
git mv pkg/doctor/agent.go internal/agent/agent.go
git mv pkg/doctor/agent_test.go internal/agent/agent_test.go
git mv pkg/doctor/agent_prompt.go internal/agent/prompt.go
```

在 `internal/agent/agent.go` 与 `agent_test.go` 中：

- `package doctor` → `package agent`
- 更新 import：`internal/agent/llm`、`internal/ceph/tools`、`internal/evidence`
- `AgentConfig.SeedEvidence []evidence.Evidence`
- 新增 `toolSpecs()` 尾部追加 `readSkillSpec()`
- 在 `Send` 的工具执行循环里，`SeenCall` 判断之前插入：

```go
			if tc.Name == readSkillToolName {
				a.msgs = append(a.msgs, llm.ToolResultMessage(tc.ID, skillContent(parseArgs(tc.Args))))
				continue
			}
```

把 `internal/agent/prompt.go` 整个替换为：

```go
package agent

import (
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/ceph/snapshot"
	"github.com/microyahoo/infra-agent/skills"
)

// SystemPrompt 由两部分组成：
//  1. skills/ceph-doctor/SKILL.md —— 与外部 agent 共用的领域知识
//  2. 本次会话的动态事实 —— 集群 ID、健康、快照、证据 ID
//
// 静态知识放在 SKILL.md 里，是为了让内置 agent 与外部 agent
// 读到的内容完全一致；动态部分只能在这里拼。
func SystemPrompt(s snapshot.Snapshot) string {
	return strings.TrimSpace(skills.MustRead("ceph-doctor/SKILL.md")) +
		"\n\n---\n\n# 本次会话\n\n" + clusterSection(s)
}

func clusterSection(s snapshot.Snapshot) string {
	var b strings.Builder

	b.WriteString("下面是会话开始时已经采集到的事实。**除非用户问「现在变了吗」，否则不要重复执行这些查询。**\n\n")
	fmt.Fprintf(&b, "- 集群 ID：%s\n", orDash(s.ClusterID))
	fmt.Fprintf(&b, "- 当前健康：%s\n", orDash(s.Health))
	if s.Summary != "" {
		fmt.Fprintf(&b, "- 概要：%s\n", s.Summary)
	}

	if ph := strings.TrimSpace(s.PodHealth); ph != "" {
		b.WriteString("\n## Pod 状态（会话开始时采集）\n\n")
		b.WriteString(ph)
		b.WriteString("\n")
	}

	if len(s.Findings) > 0 {
		b.WriteString("\n## 已经能看出来的（确定性分析，直接据此回答）\n\n")
		for i, f := range s.Findings {
			fmt.Fprintf(&b, "%d. **%s**\n   - 依据：%s\n   - 下一步：%s\n", i+1, f.Summary, f.Why, f.Next)
		}
		b.WriteString("\n这些是代码从告警里直接算出来的，事实本身不需要再验证。" +
			"你只需要判断它们是否足以回答用户的问题。\n")
	}

	if detail := strings.TrimSpace(s.HealthDetail); detail != "" && s.Health != "HEALTH_OK" {
		b.WriteString("\n## 告警明细（节选）\n\n```\n")
		b.WriteString(truncateLines(detail, 12))
		b.WriteString("\n```\n")
	} else if ev := strings.TrimSpace(s.RecentHealthEvents); ev != "" {
		b.WriteString("\n## 最近的健康变更（会话开始时从日志里查到）\n\n```\n")
		b.WriteString(ev)
		b.WriteString("\n```\n")
	}

	if s.Topology != nil {
		if rows := placementRows(s); len(rows) > 0 {
			b.WriteString("\n## 相关 OSD 的节点与磁盘（已从拓扑查出，不必再调 osd_detail）\n\n")
			b.WriteString("| OSD | 节点 | 磁盘 | 状态 |\n|---|---|---|---|\n")
			for _, r := range rows {
				fmt.Fprintf(&b, "| osd.%d | %s | %s | %s |\n", r.OSD, orDash(r.Node), orDash(r.Device), orDash(r.Status))
			}
			b.WriteString("\n")
		}
	}

	if len(s.Evidence) > 0 {
		b.WriteString("\n## 会话开始时已采集（已有证据 ID，可直接引用）\n\n")
		for _, ev := range s.Evidence {
			status := ""
			if ev.ErrorKind != "" {
				status = "（失败：" + ev.ErrorMsg + "）"
			}
			fmt.Fprintf(&b, "- `%s` → %s%s\n", ev.Tool, ev.ID, status)
		}
	}

	b.WriteString("\n## 环境\n\n")
	fmt.Fprintf(&b, "- 执行后端：%s\n", orDash(s.Capabilities.Backend))
	if s.Capabilities.CephVersion != "" {
		fmt.Fprintf(&b, "- Ceph 版本：%s\n", s.Capabilities.CephVersion)
	}
	if ns := s.Capabilities.RookNamespace; ns != "" {
		fmt.Fprintf(&b, "- Rook 命名空间：%s\n", ns)
	}
	if len(s.Capabilities.LogDirs) > 0 {
		fmt.Fprintf(&b, "- 日志目录：%s\n", strings.Join(s.Capabilities.LogDirs, "、"))
	}
	if s.Topology != nil && len(s.Topology.Nodes) > 0 {
		var names []string
		for _, n := range s.Topology.Nodes {
			names = append(names, n.Name)
		}
		fmt.Fprintf(&b, "- 节点（%d 个）：%s\n", len(names), strings.Join(names, "、"))
	}

	if len(s.Failures) > 0 {
		b.WriteString("\n### 会话开始时未能采集到的信息\n\n")
		for _, f := range s.Failures {
			b.WriteString("- " + f + "\n")
		}
	}
	return b.String()
}

type placementRow = access.OSDPlacement

func placementRows(s snapshot.Snapshot) []placementRow {
	place := make(map[int]access.OSDPlacement, len(s.Topology.OSDs))
	for _, p := range s.Topology.OSDs {
		place[p.ID] = p
	}
	var rows []placementRow
	for _, id := range snapshot.ReferencedOSDs(s, 24) {
		if p, ok := place[id]; ok {
			rows = append(rows, p)
		} else {
			rows = append(rows, access.OSDPlacement{ID: id})
		}
	}
	return rows
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未知"
	}
	return s
}

func truncateLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:n], "\n")
}
```

- [ ] **Step 9: 创建 `internal/agent/skill.go`**

```go
package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/skills"
)

// readSkillToolName 是内置 agent 专用的工具：按需展开 SKILL.md 的细节文档。
// 它不在 tools.Registry 里，因此不会通过 MCP 暴露给外部 agent
// ——外部 agent 直接读磁盘上的 references/*.md。
const readSkillToolName = "read_skill"

func readSkillSpec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        readSkillToolName,
		Description: "读取 Ceph 诊断的细节文档。当需要更具体的排查步骤时调用。可选：" + strings.Join(skills.ReferenceNames(), "、"),
		Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"文档名，例如 osd"}},"required":["name"]}`),
	}
}

func skillContent(args map[string]any) string {
	name, _ := args["name"].(string)
	if strings.TrimSpace(name) == "" {
		return `{"error":"缺少 name 参数"}`
	}
	content, err := skills.ReadReference(name)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return content
}
```

- [ ] **Step 10: 写 prompt 的回归测试**

创建 `internal/agent/prompt_test.go`：

```go
package agent

import (
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/internal/ceph/snapshot"
)

func TestSystemPromptEmbedsSkillAndClusterFacts(t *testing.T) {
	p := SystemPrompt(snapshot.Snapshot{
		ClusterID: "fsid-x",
		Health:    "HEALTH_OK",
		Summary:   "mon 3，OSD 1",
	})
	if !strings.Contains(p, "name: ceph-doctor") {
		t.Error("SKILL.md frontmatter missing from prompt")
	}
	if !strings.Contains(p, "fsid-x") {
		t.Error("dynamic cluster facts missing from prompt")
	}
}
```

- [ ] **Step 11: 重写 `cmd/doctor.go`**

整个文件替换为：

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/microyahoo/infra-agent/internal/agent"
	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/internal/config"
	"github.com/microyahoo/infra-agent/internal/session"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// doctorFlagBindings 把命令行 flag 映射到配置键。
// host/port/key 等"覆盖所选集群"的 flag 由 overrideCluster 单独处理。
var doctorFlagBindings = map[string]string{
	"model":               "llm.model",
	"base-url":            "llm.base_url",
	"api-key-env":         "llm.api_key_env",
	"max-tokens":          "llm.max_tokens",
	"llm-timeout":         "llm.timeout",
	"llm-retries":         "llm.max_retries",
	"max-rounds":          "loop.max_rounds",
	"max-tool-calls":      "loop.max_tool_calls",
	"max-calls-per-round": "loop.max_calls_per_round",
	"timeout":             "run.timeout",
	"dry-run":             "run.dry_run",
	"self-check":          "run.self_check",
	"interactive":         "run.interactive",
	"ceph-src":            "ceph.source_dir",
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "通过 SSH 调查 Ceph 集群（对话式）",
	Long: `通过 SSH 只读访问 Ceph 集群，像和运维同事对话一样查问题。

交互模式（默认进入）下直接说你想查什么；助手会自己决定查什么、查多深，
然后基于查到的东西回答。可以追问、可以质疑它上一轮的结论。

全程只读：变更类命令会被拒绝。

示例:
  # 交互式调查
  infra-agent doctor --cluster stor-01

  # 问一个问题、拿一个回答，然后退出
  infra-agent doctor --cluster stor-01 --question "集群为什么 warn 了"

  # 只验证 SSH 与探测结果，不消耗模型额度
  infra-agent doctor --cluster stor-01 --dry-run
`,
	RunE:         runDoctor,
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	f := doctorCmd.Flags()

	f.String("cluster", "", "集群名（对应配置里的 clusters）")
	f.String("host", "", "首节点 IP（覆盖所选集群）")
	f.Int("port", 0, "SSH 端口（覆盖所选集群）")
	f.String("user", "", "SSH 用户（覆盖所选集群）")
	f.String("key", "", "私钥路径（覆盖所选集群）")
	f.String("password", "", "SSH 密码（覆盖所选集群）")
	f.String("known-hosts", "", "known_hosts 路径（覆盖所选集群）")
	f.Int("ssh-timeout", 0, "单条命令超时（秒，覆盖所选集群）")
	f.Int("probe-timeout", 0, "探测超时（秒，覆盖所选集群）")
	f.String("ceph-src", "", "本地 Ceph 源码目录")

	f.String("question", "", "单次提问的内容（配合 --interactive=false）")
	f.Int("timeout", 0, "总超时（秒）")
	f.Int("max-rounds", 0, "单次提问内最多的模型往返轮数")
	f.Int("max-tool-calls", 0, "单次提问内最多的工具调用数")
	f.Int("max-calls-per-round", 0, "单轮并行的工具调用上限")
	f.String("model", "", "模型名")
	f.String("base-url", "", "OpenAI 兼容地址")
	f.String("api-key-env", "", "提供 API Key 的环境变量名")
	f.Int("max-tokens", 0, "单次模型回复的最大 token 数")
	f.Int("llm-timeout", 0, "单次模型请求超时（秒）")
	f.Int("llm-retries", 0, "模型请求失败时的重试次数（-1 表示默认 2）")
	f.Bool("dry-run", false, "仅连接并探测能力与拓扑，不调用模型")
	f.Bool("self-check", false, "在真实集群上逐个运行诊断工具（不调用模型）")
	f.Bool("interactive", true, "进入交互模式")
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	v := viper.GetViper()
	if err := config.BindFlags(v, cmd.Flags(), doctorFlagBindings); err != nil {
		return err
	}
	cfg, err := config.Load(v, nil)
	if err != nil {
		return err
	}

	cl, err := cfg.Pick(stringFlag(cmd.Flags(), "cluster"))
	if err != nil {
		return err
	}
	cl = overrideCluster(cmd.Flags(), cl)

	ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(cfg.Run.Timeout)*time.Second)
	defer cancel()

	if cfg.Run.DryRun || cfg.Run.SelfCheck {
		sess, err := openSession(ctx, cfg, cl, false)
		if err != nil {
			return fmt.Errorf("接入失败: %w", err)
		}
		defer sess.Close()
		if cfg.Run.DryRun {
			return runDoctorDryRun(ctx, sess)
		}
		return runSelfCheck(ctx, sess)
	}

	fmt.Fprintf(os.Stderr, "🔌 连接 %s ...\n", cl.Host)

	if cfg.Run.Interactive {
		return runInteractive(ctx, cfg, func(c context.Context) (*agent.Agent, *session.Session, error) {
			return openAgent(c, cfg, cl)
		})
	}

	ag, sess, err := openAgent(ctx, cfg, cl)
	if err != nil {
		return err
	}
	defer sess.Close()

	question := strings.TrimSpace(stringFlag(cmd.Flags(), "question"))
	if question == "" {
		question = "简单说明一下这个集群现在的状态，有异常就说异常。"
	}
	reply, err := ag.Send(ctx, question)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, reply)
	return nil
}

// openSession 建立连接与工具表；collect 为真时再取一次会话快照。
func openSession(ctx context.Context, cfg config.Config, cl config.Cluster, collect bool) (*session.Session, error) {
	fmt.Fprintln(os.Stderr, "🔎 采集集群信息 ...")
	return session.Open(ctx, session.Options{
		Cluster:         cl,
		Policy:          cfg.Policy,
		SourceDir:       cfg.Ceph.SourceDir,
		CollectSnapshot: collect,
		OnEvent:         func(msg string) { fmt.Fprintf(os.Stderr, "  · %s\n", msg) },
	})
}

// openAgent 建好会话与模型，返回可对话的 agent。
func openAgent(ctx context.Context, cfg config.Config, cl config.Cluster) (*agent.Agent, *session.Session, error) {
	if cfg.LLM.APIKey == "" {
		return nil, nil, fmt.Errorf("未找到 API Key：请设置环境变量 %s，或在配置文件 llm.api_key 中填写", cfg.LLM.APIKeyEnv)
	}
	sess, err := openSession(ctx, cfg, cl, true)
	if err != nil {
		return nil, nil, fmt.Errorf("接入失败: %w", err)
	}
	snap := sess.Snapshot()
	fmt.Fprintf(os.Stderr, "✓ %s｜%s\n", snap.Health, snap.Summary)
	return sess.NewAgent(newModel(cfg), cfg.Loop), sess, nil
}

func newModel(cfg config.Config) llm.ChatModel {
	return llm.NewClient(llm.Config{
		BaseURL:     cfg.LLM.BaseURL,
		APIKey:      cfg.LLM.APIKey,
		Model:       cfg.LLM.Model,
		Temperature: cfg.LLM.Temperature,
		MaxTokens:   cfg.LLM.MaxTokens,
		Timeout:     time.Duration(cfg.LLM.Timeout) * time.Second,
		MaxRetries:  cfg.LLM.MaxRetries,
	})
}

func stringFlag(flags *pflag.FlagSet, name string) string {
	v, _ := flags.GetString(name)
	return v
}

// overrideCluster 用显式给出的 flag 覆盖所选集群的字段。
func overrideCluster(flags *pflag.FlagSet, cl config.Cluster) config.Cluster {
	if flags.Changed("host") {
		cl.Host, _ = flags.GetString("host")
	}
	if flags.Changed("port") {
		cl.Port, _ = flags.GetInt("port")
	}
	if flags.Changed("user") {
		cl.User, _ = flags.GetString("user")
	}
	if flags.Changed("key") {
		cl.PrivateKeyPath, _ = flags.GetString("key")
	}
	if flags.Changed("password") {
		cl.Password, _ = flags.GetString("password")
	}
	if flags.Changed("known-hosts") {
		cl.KnownHostsPath, _ = flags.GetString("known-hosts")
	}
	if flags.Changed("ssh-timeout") {
		cl.CommandTimeout, _ = flags.GetInt("ssh-timeout")
	}
	if flags.Changed("probe-timeout") {
		cl.ProbeTimeout, _ = flags.GetInt("probe-timeout")
	}
	return cl
}
```

- [ ] **Step 11.1: 创建 `cmd/doctor_dryrun.go`**

`runDoctorDryRun` 与它的展示辅助函数从旧 `cmd/doctor.go` 拆出来，单独成文件：

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/microyahoo/infra-agent/internal/session"
)

// runDoctorDryRun 只做能力探测与拓扑发现，打印摘要后返回，不调用模型。
func runDoctorDryRun(ctx context.Context, sess *session.Session) error {
	cluster := sess.Cluster()
	nodes := cluster.Nodes()
	if len(nodes) == 0 {
		return fmt.Errorf("未能确定任何节点")
	}
	primary := nodes[0]

	fmt.Fprintf(os.Stderr, "🔎 探测节点能力 ...\n")
	caps := cluster.Capabilities(ctx, primary)
	fmt.Fprintf(os.Stderr, "  → 后端 %s，ceph CLI %s\n", listOrNone([]string{caps.Backend}), yesNo(caps.CephCLI))

	fmt.Fprintf(os.Stderr, "🔎 发现集群拓扑 ...\n")
	topo, topoErr := cluster.Topology(ctx)
	if topoErr != nil {
		fmt.Fprintf(os.Stderr, "  → 拓扑发现失败：%v\n", topoErr)
	} else if topo != nil {
		fmt.Fprintf(os.Stderr, "  → %d 个节点，%d 个 OSD\n", len(topo.Nodes), len(topo.OSDs))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SSH 接入验证（dry-run）\n")
	fmt.Fprintf(&b, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(&b, "入口节点: %s\n", primary)
	fmt.Fprintf(&b, "诊断后端: %s\n", orUnknown(caps.Backend))
	fmt.Fprintf(&b, "ceph CLI: %s\n", yesNo(caps.CephCLI))
	if caps.CephVersion != "" {
		fmt.Fprintf(&b, "ceph 版本: %s\n", caps.CephVersion)
	}
	fmt.Fprintf(&b, "kubectl: %s\n", yesNo(caps.Kubectl))
	if caps.ContainerRT != "" && caps.ContainerRT != "none" {
		fmt.Fprintf(&b, "容器运行时: %s\n", caps.ContainerRT)
	}
	fmt.Fprintf(&b, "日志目录: %s\n", listOrNone(caps.LogDirs))
	tools := make([]string, 0, len(caps.HostTools))
	for name, ok := range caps.HostTools {
		if ok {
			tools = append(tools, name)
		}
	}
	sort.Strings(tools)
	fmt.Fprintf(&b, "宿主机工具: %s\n", listOrNone(tools))

	if topoErr != nil {
		fmt.Fprintf(&b, "集群拓扑: 发现失败：%v\n", topoErr)
	} else {
		fmt.Fprintf(&b, "集群拓扑: fsid %s，节点 %d，OSD %d\n",
			orUnknown(topo.FSID), len(topo.Nodes), len(topo.OSDs))
		for _, n := range topo.Nodes {
			fmt.Fprintf(&b, "  - %s（主机 %s，OSD %d）\n",
				n.Name, orUnknown(n.Host), len(n.OSDIDs))
		}
	}
	fmt.Fprintf(&b, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(&b, "dry-run 结束：SSH 与探测已完成，未调用模型\n")

	fmt.Fprint(os.Stdout, b.String())
	return nil
}

// orUnknown 把空字符串显示为 unknown。
func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// yesNo 把布尔值转为中文可读文本。
func yesNo(ok bool) string {
	if ok {
		return "可用"
	}
	return "不可用"
}

// listOrNone 把列表拼接为逗号分隔文本，空列表显示为「无」。
func listOrNone(items []string) string {
	if len(items) == 0 {
		return "无"
	}
	return strings.Join(items, ", ")
}
```

注意：`firstLine`（旧 `doctor.go` 中仅被 `openAgent` 用于打印 pod 与 findings 摘要）不再需要，因为新的 `openAgent` 只打印健康与概要；删除它。

- [ ] **Step 12: 改写 `cmd/doctor_interactive.go`**

`runInteractive` 的签名与建立会话的部分改为：

```go
func runInteractive(ctx context.Context, cfg config.Config, open func(context.Context) (*agent.Agent, *session.Session, error)) error {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fmt.Fprint(os.Stderr, interactiveBanner)

	type opened struct {
		agent   *agent.Agent
		session *session.Session
	}

	var ag *agent.Agent
	var sess *session.Session
	defer func() {
		if sess != nil {
			sess.Close()
		}
	}()

	for {
		fmt.Fprint(os.Stderr, "\n❯ ")
		text, ok := readInput(in)
		if !ok {
			return nil
		}
		if text == "" {
			continue
		}

		switch text {
		case ":quit", ":exit", ":q":
			return nil
		case ":help", ":h":
			fmt.Fprint(os.Stderr, interactiveHelp)
			continue
		case ":evidence":
			printEvidence(ag)
			continue
		}

		if ag == nil {
			r, err := withTimeout(ctx, cfg.Run.Timeout, func(c context.Context) (opened, error) {
				a, s, err := open(c)
				return opened{a, s}, err
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "✗ 初始化失败：%v\n", err)
				continue
			}
			ag, sess = r.agent, r.session
		}

		reply, err := withTimeout(ctx, cfg.Run.Timeout, func(c context.Context) (string, error) {
			return ag.Send(c, text)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %s\n", describeError(err, cfg.Run.Timeout))
			continue
		}
		if strings.TrimSpace(reply) == "" {
			fmt.Fprintln(os.Stderr, "（模型没有返回内容）")
			continue
		}
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintln(os.Stdout, reply)
	}
}
```

`printEvidence(a *doctor.Agent)` 改为 `printEvidence(a *agent.Agent)`；`Evidence() *evidence.Chain`。import 增加 `internal/agent`、`internal/config`、`internal/session`，删除 `internal/doctor`。

`describeError` 保持不变。

- [ ] **Step 13: 改写 `cmd/doctor_selfcheck.go`**

`runSelfCheck` 改为接收会话：

```go
func runSelfCheck(ctx context.Context, sess *session.Session) error {
	registry := sess.Registry()
	cluster := sess.Cluster()
	...
}
```

删除 `selfCheckRunner` 类型（`access.Cluster` 已直接满足 `tools.Runner`）。其余逻辑不变；`checks` 列表已在 Task 4 加入 `ceph_overview`。

- [ ] **Step 14: 删除旧文件**

```bash
git rm pkg/doctor/setup.go
git rm cmd/doctor_config.go cmd/doctor_config_test.go
rmdir pkg/doctor 2>/dev/null || true
```

`runDoctorDryRun` 已在 Step 11.1 拆到 `cmd/doctor_dryrun.go`，本步不需要再处理。

- [ ] **Step 15: 构建、vet、测试**

```bash
go build ./... && go vet ./... && go test ./...
```

Expected: 全部通过。`grep -rn "pkg/doctor" --include="*.go" .` 无输出；`ls pkg` 不存在。

- [ ] **Step 16: 提交**

```bash
git add -A
git commit -m "refactor(agent): make SKILL.md the single source of diagnostic knowledge"
```

---

## Task 6: MCP server（stdio）+ `mcp` 命令

**Files:**
- Create: `internal/mcpserver/server.go`
- Create: `internal/mcpserver/server_test.go`
- Create: `cmd/mcp.go`
- Modify: `go.mod`（引入 MCP SDK）

- [ ] **Step 1: 引入 SDK 并提升 Go 版本**

```bash
go mod edit -go=1.23.0
go get github.com/modelcontextprotocol/go-sdk@v1.3.1
```

Expected: `go.mod` 出现 `github.com/modelcontextprotocol/go-sdk v1.3.1`，`go` 指令为 `1.23.0`。

- [ ] **Step 2: 先写 MCP 端到端测试**

创建 `internal/mcpserver/server_test.go`：

```go
package mcpserver_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/ceph/tools"
	"github.com/microyahoo/infra-agent/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stubRunner struct{}

func (stubRunner) Run(context.Context, string, []string) (access.Output, error) {
	return access.Output{Stdout: `{"health":{"status":"HEALTH_OK"},"monmap":{"num_mons":3},"osdmap":{"num_osds":1,"num_up_osds":1,"num_in_osds":1},"pgmap":{"num_pgs":4}}`}, nil
}
func (stubRunner) Capabilities(context.Context, string) access.Capabilities {
	return access.Capabilities{CephCLI: true}
}
func (stubRunner) Topology(context.Context) (*access.Topology, error) {
	return &access.Topology{}, nil
}
func (stubRunner) Nodes() []string { return []string{"node-1"} }

type fakeProvider struct {
	reg *tools.Registry
	err error
}

func (p *fakeProvider) Registry(context.Context) (*tools.Registry, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.reg, nil
}
func (p *fakeProvider) Reset() {}

func connect(t *testing.T, p mcpserver.Provider) *mcp.ClientSession {
	t.Helper()
	srv := mcpserver.New(p, "test")
	st1, st2 := mcp.NewInMemoryTransports()

	ctx := context.Background()
	if _, err := srv.Connect(ctx, st1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, st2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return cs
}

func TestListsAllTools(t *testing.T) {
	cs := connect(t, &fakeProvider{reg: tools.RegistryFrom(tools.All(nil, ""))})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 17 {
		t.Fatalf("expected 17 tools, got %d", len(res.Tools))
	}
	for _, tool := range res.Tools {
		if tool.Name == "" || tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("incomplete tool definition: %+v", tool)
		}
	}
}

func TestCallsToolThroughProvider(t *testing.T) {
	reg := tools.RegistryFrom([]tools.Tool{tools.NewCephStatus(stubRunner{})})
	cs := connect(t, &fakeProvider{reg: reg})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ceph_status"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}
	body, _ := json.Marshal(res.Content)
	if !strings.Contains(string(body), "HEALTH_OK") {
		t.Errorf("result should contain health: %s", body)
	}
}

func TestProviderFailureIsToolError(t *testing.T) {
	cs := connect(t, &fakeProvider{err: context.DeadlineExceeded})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ceph_status"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError when the provider fails")
	}
}
```

- [ ] **Step 3: 运行测试，确认失败**

```bash
go test ./internal/mcpserver/
```

Expected: `undefined: mcpserver.New`。

- [ ] **Step 4: 实现 `internal/mcpserver/server.go`**

```go
// Package mcpserver 把工具表暴露为 MCP 工具。
//
// 它不参与任何诊断决策：只做参数转发、结果封装与错误分类。
// 工具表在首次调用时才建立，因此客户端可以只列工具、从不连接集群。
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/ceph/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Provider 按需提供工具表。
type Provider interface {
	Registry(ctx context.Context) (*tools.Registry, error)
	// Reset 表示当前连接已不可用，下次调用应重新建立。
	Reset()
}

// New 构造一个注册了全部工具的 MCP server。
//
// 工具清单来自一个不依赖连接的静态 Registry：工具的 Name/Description/Schema
// 在构造时就已确定，只有 Execute 才需要集群。
func New(p Provider, version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "infra-agent", Version: version}, nil)
	static := tools.RegistryFrom(tools.All(nil, ""))
	for _, spec := range static.Specs() {
		srv.AddTool(&mcp.Tool{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: spec.Schema,
		}, handlerFor(p, spec.Name))
	}
	return srv
}

func handlerFor(p Provider, name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reg, err := p.Registry(ctx)
		if err != nil {
			return errorResult(fmt.Errorf("接入集群失败: %w", err)), nil
		}
		res, _, err := reg.Execute(ctx, name, req.Params.Arguments)
		if err != nil {
			if isConnError(err) {
				p.Reset()
			}
			return errorResult(err), nil
		}

		payload := map[string]any{"summary": res.Summary}
		if res.Data != nil {
			payload["data"] = res.Data
		}
		if len(res.Hints) > 0 {
			payload["facts"] = res.Hints
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return errorResult(err), nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
		}, nil
	}
}

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

// isConnError 判断是否值得重连。解析错误不该导致丢掉一条好连接。
func isConnError(err error) bool {
	var te *access.ToolError
	if errors.As(err, &te) {
		return te.Kind == access.KindUnreachable || te.Kind == access.KindTimeout
	}
	return false
}
```

- [ ] **Step 5: 运行测试**

```bash
go test ./internal/mcpserver/ -v
```

Expected: 3 个测试 PASS。

- [ ] **Step 6: 创建 `cmd/mcp.go`**

```go
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

	cl, err := cfg.Pick(stringFlag(cmd.Flags(), "cluster"))
	if err != nil {
		return err
	}

	lazy := &session.Lazy{Open: func(ctx context.Context) (*session.Session, error) {
		return session.Open(ctx, session.Options{
			Cluster:   cl,
			Policy:    cfg.Policy,
			SourceDir: cfg.Ceph.SourceDir,
		})
	}}
	defer lazy.Close()

	srv := mcpserver.New(lazy, Version)

	if !cmd.Flags().Changed("http") {
		// stdio 模式下 stdout 是协议通道，任何日志都必须走 stderr。
		fmt.Fprintln(os.Stderr, "MCP server 已就绪（stdio）")
		return srv.Run(cmd.Context(), &mcp.StdioTransport{})
	}

	allowRemote, _ := cmd.Flags().GetBool("allow-remote")
	return mcpserver.ServeHTTP(cmd.Context(), cfg.MCP.Listen, srv, cfg.MCP.Token, allowRemote)
}
```

- [ ] **Step 7: 构建、vet、测试**

```bash
go build ./... && go vet ./... && go test ./...
```

Expected: 全部通过。`infra-agent mcp --help` 可用。

- [ ] **Step 8: 手工验证 stdio**

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | go run . mcp --cluster stor-01 2>/dev/null | tail -1 | head -c 300
```

Expected: 返回 `"tools":[` 且长度大于 0。若本地没有配置集群，`initialize` 与 `tools/list` 仍然成功（不触发连接）。

- [ ] **Step 9: 提交**

```bash
git add -A
git commit -m "feat(mcp): expose diagnostic tools over stdio"
```

---

## Task 7: MCP over Streamable HTTP（供 Dify）

**Files:**
- Create: `internal/mcpserver/http.go`
- Create: `internal/mcpserver/http_test.go`

- [ ] **Step 1: 先写失败测试**

创建 `internal/mcpserver/http_test.go`：

```go
package mcpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPHealthNeedsNoToken(t *testing.T) {
	srv := mcpserver.New(&fakeProvider{reg: toolsRegistry()}, "test")
	h := mcpserver.NewHTTPHandler(srv, "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status: %d", rec.Code)
	}
}

func TestHTTPRejectsMissingToken(t *testing.T) {
	srv := mcpserver.New(&fakeProvider{reg: toolsRegistry()}, "test")
	h := mcpserver.NewHTTPHandler(srv, "secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHTTPAcceptsCorrectToken(t *testing.T) {
	srv := mcpserver.New(&fakeProvider{reg: toolsRegistry()}, "test")
	h := mcpserver.NewHTTPHandler(srv, "secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("token was rejected")
	}
}

func TestServeHTTPRequiresToken(t *testing.T) {
	srv := mcpserver.New(&fakeProvider{reg: toolsRegistry()}, "test")
	err := mcpserver.ServeHTTP(context.Background(), "127.0.0.1:0", srv, "", false)
	if err == nil {
		t.Fatal("empty token must be rejected")
	}
}
```

在同目录的测试辅助文件中（追加到 `server_test.go` 末尾）增加：

```go
func toolsRegistry() *tools.Registry {
	return tools.RegistryFrom(tools.All(nil, ""))
}
```

并把 `http_test.go` 的 import 补上：

```go
import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/microyahoo/infra-agent/internal/ceph/tools"
	"github.com/microyahoo/infra-agent/internal/mcpserver"
)
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/mcpserver/
```

Expected: `undefined: mcpserver.NewHTTPHandler`。

- [ ] **Step 3: 实现 `internal/mcpserver/http.go`**

```go
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewHTTPHandler 返回一个带 Bearer 鉴权的 Streamable HTTP handler。
// /health 不需要鉴权，供探活使用。
func NewHTTPHandler(srv *mcp.Server, token string) http.Handler {
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.Handle("/", bearerAuth(token, streamable))
	return mux
}

// ServeHTTP 在 addr 上提供 MCP HTTP 服务，直到 ctx 结束。
//
// 这个服务等价于把集群的只读权限放到网络上，因此：
//   - token 为空直接拒绝启动
//   - 默认只允许回环地址，绑定其它地址必须显式 allowRemote
func ServeHTTP(ctx context.Context, addr string, srv *mcp.Server, token string, allowRemote bool) error {
	if token == "" {
		return fmt.Errorf("HTTP 模式必须配置 mcp.token：该接口暴露集群只读权限")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("监听地址 %q 无效: %w", addr, err)
	}
	if !allowRemote && !isLoopback(host) {
		return fmt.Errorf("地址 %s 不是回环地址；如确需对外暴露，加 --allow-remote", addr)
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           NewHTTPHandler(srv, token),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("MCP HTTP server 监听 %s", addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func bearerAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("WWW-Authenticate", `Bearer realm="infra-agent"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
```

- [ ] **Step 4: 运行测试**

```bash
go test ./internal/mcpserver/ -v
```

Expected: 7 个测试全部 PASS。

- [ ] **Step 5: 手工验证 HTTP**

```bash
go run . mcp --cluster stor-01 --http 127.0.0.1:19100 --token secret &
sleep 1
curl -s http://127.0.0.1:19100/health
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:19100/
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Authorization: Bearer secret' http://127.0.0.1:19100/
kill %1
```

Expected: `{"status":"ok"}`、`401`、一个非 401 的状态码。

- [ ] **Step 6: 构建、vet、测试**

```bash
go build ./... && go vet ./... && go test ./...
```

- [ ] **Step 7: 提交**

```bash
git add -A
git commit -m "feat(mcp): add streamable HTTP transport with bearer auth"
```

---

## Task 8: 飞书机器人改用 `session`

**Files:**
- Modify: `internal/feishu/session.go`
- Modify: `internal/feishu/bot.go`
- Modify: `cmd/serve.go`
- Modify: `internal/feishu/bot_test.go`（仅当类型变化影响编译）

- [ ] **Step 1: 重写 `internal/feishu/session.go`**

```go
package feishu

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/microyahoo/infra-agent/internal/agent"
	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/internal/config"
	"github.com/microyahoo/infra-agent/internal/session"
)

// SessionManager 按群（chat_id）维护诊断会话。
//
// 群里的追问要能看到上一轮查过什么，因此会话必须留存；
// 但 SSH 连接和模型客户端不能无限累积，所以带空闲回收。
type SessionManager struct {
	cfg     Config
	mu      sync.Mutex
	entries map[string]*sessionEntry
}

type sessionEntry struct {
	agent     *agent.Agent
	session   *session.Session
	clusterID string
	lastUsed  time.Time
	mu        sync.Mutex // 同一个群串行处理，避免两条消息交叉
}

func newSessionManager(cfg Config) *SessionManager {
	return &SessionManager{cfg: cfg, entries: make(map[string]*sessionEntry)}
}

// get 返回该群的会话，必要时新建。
func (m *SessionManager) get(ctx context.Context, chatID, clusterName string) (*sessionEntry, error) {
	m.mu.Lock()
	if e, ok := m.entries[chatID]; ok && e.clusterID == clusterName {
		e.lastUsed = time.Now()
		m.mu.Unlock()
		return e, nil
	}
	m.mu.Unlock()

	cc, err := m.cfg.cluster(clusterName)
	if err != nil {
		return nil, err
	}

	sess, err := session.Open(ctx, session.Options{
		Cluster:         cc,
		Policy:          m.cfg.Policy,
		SourceDir:       m.cfg.SourceDir,
		CollectSnapshot: true,
	})
	if err != nil {
		return nil, fmt.Errorf("接入集群 %s 失败: %w", clusterName, err)
	}

	e := &sessionEntry{
		agent:     sess.NewAgent(m.cfg.newModel(), m.cfg.Loop),
		session:   sess,
		clusterID: clusterName,
		lastUsed:  time.Now(),
	}

	m.mu.Lock()
	if old, ok := m.entries[chatID]; ok && old.clusterID == clusterName {
		m.mu.Unlock()
		_ = sess.Close()
		return old, nil
	}
	if old, ok := m.entries[chatID]; ok {
		_ = old.session.Close()
	}
	m.entries[chatID] = e
	m.mu.Unlock()
	return e, nil
}

// cleanupIdle 关闭长时间未使用的会话。
func (m *SessionManager) cleanupIdle(idle time.Duration) {
	if idle <= 0 {
		return
	}
	cutoff := time.Now().Add(-idle)
	m.mu.Lock()
	var dead []*sessionEntry
	for id, e := range m.entries {
		if e.lastUsed.Before(cutoff) {
			dead = append(dead, e)
			delete(m.entries, id)
		}
	}
	m.mu.Unlock()
	for _, e := range dead {
		_ = e.session.Close()
	}
}

// Close 关闭全部会话。
func (m *SessionManager) Close() {
	m.mu.Lock()
	all := make([]*sessionEntry, 0, len(m.entries))
	for _, e := range m.entries {
		all = append(all, e)
	}
	m.entries = map[string]*sessionEntry{}
	m.mu.Unlock()
	for _, e := range all {
		_ = e.session.Close()
	}
}

// newModel 便于测试替换。
func (c Config) newModel() llm.ChatModel {
	if c.NewModel != nil {
		return c.NewModel()
	}
	return llm.NewClient(llm.Config{
		BaseURL:    c.LLM.BaseURL,
		APIKey:     c.LLM.APIKey,
		Model:      c.LLM.Model,
		MaxTokens:  c.LLM.MaxTokens,
		MaxRetries: c.LLM.MaxRetries,
	})
}

// cluster 返回指定集群，空名时用默认集群。
func (c Config) cluster(name string) (config.Cluster, error) {
	if name == "" {
		name = c.DefaultCluster
	}
	if name == "" {
		return config.Cluster{}, fmt.Errorf("未配置默认集群")
	}
	cc, ok := c.Clusters[name]
	if !ok {
		return config.Cluster{}, fmt.Errorf("没有名为 %q 的集群配置", name)
	}
	return cc, nil
}
```

- [ ] **Step 2: 重写 `internal/feishu/bot.go` 的 `Config`**

把 `Config` 结构体改为：

```go
// Config 是飞书机器人的配置。
type Config struct {
	AppID     string
	AppSecret string
	// VerificationToken / EncryptKey 在长连接模式下可留空。
	VerificationToken string
	EncryptKey        string

	// Clusters 是可选的集群集合；DefaultCluster 决定默认查哪套。
	Clusters       map[string]config.Cluster
	DefaultCluster string

	// ChatClusters 指定某个群默认查哪套集群（chat_id → 集群名）。
	ChatClusters map[string]string

	LLM      LLMConfig
	NewModel func() llm.ChatModel // 测试注入

	Policy    config.Policy
	SourceDir string
	Loop      config.Loop
	IdleTimeout time.Duration
}
```

`New()` 的校验改为：

```go
	if len(cfg.Clusters) == 0 {
		return nil, fmt.Errorf("没有任何集群配置")
	}
```

`handle` 中调用 `entry.session` 的地方改为 `entry.agent.Send`（字段名已从 `agent` 变为 `agent`，保持不变）；`cleanup` 中 `entry.cluster.Close()` 改为 `entry.session.Close()`（在上述重写中已统一）。

`clusterForChat` / `stripClusterPrefix` / `markSeen` / `reply` / `extractText` / `deref` / `clip` 全部保持不变。

- [ ] **Step 3: 简化 `cmd/serve.go` 的映射**

`loadBotConfig` 改为直接传递集群与策略：

```go
func loadBotConfig(cfg config.Config) feishu.Config {
	name := cfg.Feishu.DefaultCluster
	if name == "" {
		name = cfg.DefaultCluster
	}
	return feishu.Config{
		AppID:             cfg.Feishu.AppID,
		AppSecret:         cfg.Feishu.AppSecret,
		VerificationToken: cfg.Feishu.VerificationToken,
		EncryptKey:        cfg.Feishu.EncryptKey,
		Clusters:          cfg.Clusters,
		DefaultCluster:    name,
		ChatClusters:      cfg.Feishu.ChatClusters,
		LLM: feishu.LLMConfig{
			BaseURL:    cfg.LLM.BaseURL,
			APIKey:     cfg.LLM.APIKey,
			Model:      cfg.LLM.Model,
			MaxTokens:  cfg.LLM.MaxTokens,
			MaxRetries: cfg.LLM.MaxRetries,
		},
		Policy:      cfg.Policy,
		SourceDir:   cfg.Ceph.SourceDir,
		Loop:        cfg.Loop,
		IdleTimeout: time.Duration(cfg.Feishu.IdleTimeoutMinutes) * time.Minute,
	}
}
```

删除 `feishu.ClusterConfig` 与 `feishu.LLMConfig` 中不再使用的字段（`ClusterConfig` 类型整个删除）。

- [ ] **Step 4: 运行飞书测试**

```bash
go test ./internal/feishu/ -v
```

Expected: 现有测试全部 PASS（它们只测纯函数）。

- [ ] **Step 5: 构建、vet、测试**

```bash
go build ./... && go vet ./... && go test ./...
```

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "refactor(feishu): build bot sessions on top of internal/session"
```

---

## Task 9: 文档、配置示例与最终验收

**Files:**
- Create: `README.md`（重写）
- Create: `docs/ARCHITECTURE.md`
- Create: `docs/MCP.md`
- Create: `docs/GETTING_STARTED.md`
- Modify: `config/doctor.example.yaml`（如 Task 3 已写则核对）
- Modify: `Dockerfile`（如果它引用了旧命令）

- [ ] **Step 1: 重写 `README.md`**

必须包含以下小节，内容与代码一致：

```markdown
# infra-agent

通过 SSH 只读诊断 Ceph / Rook 集群。同一套工具可以三种方式使用：

| 形态 | 命令 | 适用 |
|---|---|---|
| 命令行 | `infra-agent doctor` | 人工排查、追问 |
| MCP server | `infra-agent mcp` | Cursor / Claude Code / Dify |
| 飞书机器人 | `infra-agent serve` | 群里 @ 提问 |

## 它是什么

- 只读：变更类命令在执行层被拒绝，模型没有绕过手段
- 证据可追溯：每次工具调用都记录命令、输出与耗时
- 知识只有一份：`skills/ceph-doctor/SKILL.md`，内置与外部 agent 共用

## 快速开始

见 [docs/GETTING_STARTED.md](docs/GETTING_STARTED.md)。

## 文档

- [架构](docs/ARCHITECTURE.md)
- [MCP 接入](docs/MCP.md)
- [配置示例](config/doctor.example.yaml)

## 目录

（列出 cmd/ internal/ skills/ docs/ 五个目录及各自职责）

## 开发

make build / make test
```

删除所有「与其他项目对比」「k8sgpt / sre-agent / storage-bot」类宣传内容。

- [ ] **Step 2: 写 `docs/ARCHITECTURE.md`**

必须包含：

1. 一张 ASCII 数据流图：

```
cmd (doctor/mcp/serve)
   │
   ├─ doctor/serve ─→ session ─→ agent ─→ ceph/tools ─→ access ─→ SSH
   │                     │          │          │
   │                     │          └→ evidence│
   │                     └→ ceph/snapshot ─────┘
   └─ mcp ─→ mcpserver ─→ ceph/tools ─→ access
```

2. 分层规则（与 spec 第 3.2 节相同）
3. 双消费说明：工具唯一、知识唯一、循环可两份
4. 只读策略在 `access` 层的执行点
5. 证据链的用途

- [ ] **Step 3: 写 `docs/MCP.md`**

必须包含：

1. Cursor / Claude Code 的配置片段：

```json
{
  "mcpServers": {
    "infra-agent": {
      "command": "/path/to/infra-agent",
      "args": ["mcp", "--cluster", "stor-01"]
    }
  }
}
```

2. HTTP 启动方式与 curl 示例：

```bash
infra-agent mcp --cluster stor-01 --http 127.0.0.1:9100 --token "$INFRA_AGENT_MCP_TOKEN"
curl -s http://127.0.0.1:9100/health
```

3. 工具清单（17 个）与每个工具的一句话用途
4. 安全说明：只读、Bearer 必填、默认回环
5. 如何让客户端读到 `skills/ceph-doctor/SKILL.md`

- [ ] **Step 4: 写 `docs/GETTING_STARTED.md`**

必须包含：

1. 构建：`make build`
2. 写配置：复制 `config/doctor.example.yaml` 到 `~/.infra-agent.yaml`，填 `clusters`
3. 验证连接：`infra-agent doctor --cluster stor-01 --dry-run`
4. 验证工具：`infra-agent doctor --cluster stor-01 --self-check`
5. 开始提问：`infra-agent doctor --cluster stor-01`
6. MCP 与飞书的最小配置
7. 常见错误：API Key 未设置、known_hosts 不存在、集群只配了 host 没配 key

- [ ] **Step 5: 核对配置示例**

确认 `config/doctor.example.yaml` 与 `internal/config/config.go` 的字段一一对应，特别是：

```bash
grep -o 'mapstructure:"[^"]*"' internal/config/config.go | sort -u
grep -E '^  [a-z_]+:|^[a-z_]+:' config/doctor.example.yaml
```

两者列出的键集合必须一致（`run.*`、`mcp.*`、`feishu.*` 等）。

- [ ] **Step 6: 检查 Dockerfile**

```bash
grep -n "analyze\|patrol\|doctor\|ENTRYPOINT\|CMD" Dockerfile
```

如果 `CMD` 指向已删除命令，改成 `["doctor"]` 或 `["mcp"]` 并在文档中说明。

- [ ] **Step 7: 最终验收**

```bash
go build ./...
go vet ./...
go test ./...

echo "--- 命令面 ---"
go run . --help

echo "--- 残留检查（应为空）---"
grep -rn "Profile\|MaxPerTool\|snapshotRunner\|pkg/doctor" --include="*.go" . || true

echo "--- 目录面（应只有 cmd internal skills config docs images）---"
find . -maxdepth 1 -type d -not -name '.git' -not -name '.' | sort

echo "--- 密钥检查（应无输出）---"
grep -rn "sk-[a-zA-Z0-9]\{20,\}" config/ || true
```

Expected:

- `--help` 只列出 `doctor`、`mcp`、`serve`、`version`
- 残留检查无输出
- 目录只有 `cmd internal skills config docs images bin`
- 密钥检查无输出

- [ ] **Step 8: 提交**

```bash
git add -A
git commit -m "docs: rewrite documentation for the MCP + skill architecture"
```

---

## Self-Review

**Spec coverage**

| Spec 章节 | 对应任务 |
|---|---|
| 3 目录结构 | Task 1（搬包）、Task 4（snapshot）、Task 5（agent/skills/session）、Task 6（mcpserver） |
| 3.1 依赖方向 | Task 1/5/6 的包边界；Task 5 Step 6 明确删除适配器类型 |
| 3.2 包职责 | 文件结构总览 + 各 Task 的 Files 列表 |
| 4 双消费 | Task 5（SKILL.md embed）、Task 6（MCP 工具表） |
| 4.1 工具描述中立性 | Task 5 Step 11 的 SKILL.md 与工具描述改写；drift 测试兜底 |
| 4.2 `ceph_overview` | Task 4 Step 9–12 |
| 5 配置层 | Task 3 |
| 5.3 统一 clusters | Task 3 Step 3/9 |
| 5.4 失效项 | Task 3（不定义 `Profile`/`MaxPerTool`）、Task 5（删除旧 options） |
| 6.1 工具暴露 | Task 6 Step 4 |
| 6.2 传输 | Task 6（stdio）、Task 7（HTTP） |
| 6.3 集群绑定 | Task 6 Step 6（`--cluster`） |
| 6.4 连接生命周期 | Task 6（`Lazy`） |
| 7.1 知识边界 | Task 5 Step 2 |
| 7.2 embed | Task 5 Step 1/8 |
| 7.3 漂移测试 | Task 5 Step 4 |
| 7.4 references 划分 | Task 5 Step 3 |
| 8 内置循环 | Task 5 Step 8 |
| 9 删除清单 | Task 1/2/5 |
| 10 测试策略 | 各 Task 的测试步骤 |
| 11 迁移顺序 | Task 1–9 |
| 12 验收标准 | Task 9 Step 7 |

**Type consistency check**

- `session.Options` 字段：`Cluster config.Cluster`、`Policy config.Policy`、`SourceDir string`、`CollectSnapshot bool`、`OnEvent func(string)` —— 在 Task 3 的 serve 映射与 Task 5/6 的使用中一致
- `agent.Config` 字段：`Model`、`Registry`、`SystemPrompt`、`OnEvent`、`MaxRounds`、`MaxCallsPerRound`、`MaxCallsPerTurn`、`SeedEvidence` —— 在 Task 5 中搬运时保持原名
- `mcpserver.Provider`：`Registry(ctx) (*tools.Registry, error)` + `Reset()` —— `session.Lazy` 与测试 `fakeProvider` 均实现
- `snapshot.Collect(ctx, Collector)` —— Task 4 改名后，Task 5 的 session 与 overview 均使用该名
- `snapshot.ReferencedOSDs(s, limit)` —— Task 4 导出，Task 4 的 overview 与 Task 5 的 prompt 使用
- `config.BindFlags(v, flags, pairs)` —— Task 3 定义，Task 5 的 doctor、Task 6 的 mcp、Task 8 之前的 serve 使用

**已知顺序依赖**

- Task 3 只创建 `internal/config` 并改 `serve.go` 的映射；`doctor.go` 的接线在 Task 5，因此 Task 3 结束时 `doctor` 仍走旧代码路径，但可编译、可测试
- Task 5 Step 6 创建的 `internal/session` 依赖 Task 4 的 `snapshot.Collect` 与 Task 5 Step 8 的 `agent.SystemPrompt`，因此 Task 5 的步骤顺序不能调换
- Task 6 的 `mcpserver.New` 使用 `tools.All(nil, "")` 生成清单；如果 Task 4 漏了 `NewOverview`，`TestListsAllTools` 的 17 会失败——这是有意的
