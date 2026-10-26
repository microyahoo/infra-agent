package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// Runner 是工具层对集群的全部依赖。
type Runner interface {
	Run(ctx context.Context, node string, argv []string) (access.Output, error)
	Capabilities(ctx context.Context, node string) access.Capabilities
	Topology(ctx context.Context) (*access.Topology, error)
	Nodes() []string
}

// Result 是工具返回给模型的内容。
type Result struct {
	Summary string   `json:"summary"`         // 一句话结论
	Data    any      `json:"data,omitempty"`  // 结构化细节
	Hints   []string `json:"hints,omitempty"` // 下一步可执行的查询
}

// Raw 是工具留下的一手材料，由 loop 写入证据链。
type Raw struct {
	Node      string
	Command   string
	Output    string
	Stderr    string
	Duration  time.Duration
	ExitCode  int
	Truncated bool
}

// Tool 是一个诊断工具。
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error)
}

// Spec 是发给模型的工具描述。
type Spec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Registry 工具注册表。
type Registry struct {
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) error {
	name := t.Name()
	if name == "" {
		return fmt.Errorf("tool has empty name")
	}
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("duplicate tool %q", name)
	}
	r.tools[name] = t
	r.order = append(r.order, name)
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Has 判断工具是否已注册。
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Names 返回按字典序排序的工具名，保证给模型的工具列表稳定。
func (r *Registry) Names() []string {
	out := append([]string{}, r.order...)
	sort.Strings(out)
	return out
}

// Specs 返回全部工具描述，顺序与 Names 一致。
func (r *Registry) Specs() []Spec {
	out := make([]Spec, 0, len(r.order))
	for _, n := range r.Names() {
		t := r.tools[n]
		out = append(out, Spec{Name: t.Name(), Description: t.Description(), Schema: t.Schema()})
	}
	return out
}

// Execute 按名字分发到工具。
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (*Result, *Raw, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, nil, fmt.Errorf("unknown tool %q", name)
	}
	return t.Execute(ctx, args)
}

// All 返回全部工具实例（用于需要注入源码目录等参数的场景）。
func All(r Runner, srcDir string) []Tool {
	return []Tool{
		NewCephStatus(r),
		NewCephHealth(r),
		NewOSDTree(r),
		NewPoolInfo(r),
		NewPGDetail(r),
		NewOSDDetail(r),
		NewDaemonLog(r),
		NewSlowOps(r),
		NewPerfCounters(r),
		NewHostIO(r),
		NewReadLog(r),
		NewExplore(r),
		NewRunCommand(r),
		NewHostCheck(r),
		NewSourceLookup(r, srcDir),
	}
}

// DefaultRegistry 构建全部 12 个工具。
func DefaultRegistry(r Runner) *Registry {
	return RegistryFrom(All(r, ""))
}

// RegistryFrom 由工具列表构建注册表。
func RegistryFrom(list []Tool) *Registry {
	reg := NewRegistry()
	for _, t := range list {
		if err := reg.Register(t); err != nil {
			panic(err)
		}
	}
	return reg
}
