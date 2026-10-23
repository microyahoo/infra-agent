package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultRegistryHasTwelveTools(t *testing.T) {
	r := DefaultRegistry(nil)
	names := r.Names()
	if len(names) != 12 {
		t.Fatalf("got %d tools, want 12: %v", len(names), names)
	}
	want := []string{
		"ceph_status", "osd_tree", "pool_info", "pg_detail", "osd_detail",
		"daemon_log", "slow_ops", "perf_counters", "host_io", "read_log",
		"host_check", "ceph_source_lookup",
	}
	for _, w := range want {
		if _, ok := r.Get(w); !ok {
			t.Errorf("missing tool %q", w)
		}
	}
}

func TestRegistrySpecsHaveSchema(t *testing.T) {
	r := DefaultRegistry(nil)
	for _, spec := range r.Specs() {
		if spec.Name == "" {
			t.Error("empty tool name")
		}
		if spec.Description == "" {
			t.Errorf("%s: empty description", spec.Name)
		}
		if len(spec.Schema) == 0 {
			t.Errorf("%s: empty schema", spec.Name)
		}
		var v map[string]any
		if err := json.Unmarshal(spec.Schema, &v); err != nil {
			t.Errorf("%s: schema is not valid JSON: %v", spec.Name, err)
		}
	}
}

func TestRegistryRejectsDuplicateName(t *testing.T) {
	r := NewRegistry()
	a := &stubTool{name: "x"}
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&stubTool{name: "x"}); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestRegistryDispatch(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{name: "x", out: "hello"})
	res, _, err := r.Execute(context.Background(), "x", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "hello" {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestRegistryExecuteUnknownTool(t *testing.T) {
	r := NewRegistry()
	_, _, err := r.Execute(context.Background(), "nope", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error = %v", err)
	}
}

func TestRegistryHas(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{name: "x"})
	if !r.Has("x") {
		t.Error("expected Has(x) true")
	}
	if r.Has("y") {
		t.Error("expected Has(y) false")
	}
}

type stubTool struct {
	name string
	out  string
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub" }
func (s *stubTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	return &Result{Summary: s.out}, &Raw{}, nil
}
