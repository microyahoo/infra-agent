package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceLookupFindsString(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "v18.2.4", "src", "osd")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "void OSD::log_slow_op() {\n  ss << \"slow request \" << duration;\n}\n"
	if err := os.WriteFile(filepath.Join(src, "OSD.cc"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tr := newFakeRunner()
	tr.caps.CephVersion = "18.2.4"
	tool := NewSourceLookup(tr, dir)

	res, raw, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"slow request"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*SourceLookupData)
	if len(d.Matches) == 0 {
		t.Fatal("expected matches")
	}
	if !strings.Contains(d.Matches[0].File, "OSD.cc") {
		t.Errorf("file = %q", d.Matches[0].File)
	}
	if d.Matches[0].Line == 0 {
		t.Error("line should be set")
	}
	if raw.Output == "" {
		t.Error("raw should carry the grep output")
	}
}

func TestSourceLookupNoMatch(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "v18.2.4"), 0o755)

	tr := newFakeRunner()
	tool := NewSourceLookup(tr, dir)
	res, _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"nonexistent_string_xyz"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Data.(*SourceLookupData).Matches) != 0 {
		t.Errorf("expected no matches")
	}
}

func TestSourceLookupUnavailableWithoutDir(t *testing.T) {
	tr := newFakeRunner()
	tool := NewSourceLookup(tr, "")
	_, _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"x"}`))
	if err == nil {
		t.Fatal("expected unavailable error when source dir not configured")
	}
}

func TestSourceLookupRejectsShortPattern(t *testing.T) {
	dir := t.TempDir()
	tr := newFakeRunner()
	tool := NewSourceLookup(tr, dir)
	_, _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"a"}`))
	if err == nil {
		t.Fatal("expected error for too-short pattern")
	}
}

func TestSourceLookupRejectsMissingVersionDir(t *testing.T) {
	dir := t.TempDir()
	tr := newFakeRunner()
	tr.caps.CephVersion = "99.0.0"
	tool := NewSourceLookup(tr, dir)
	_, _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"slow request"}`))
	if err == nil {
		t.Fatal("expected error when version dir absent")
	}
}
