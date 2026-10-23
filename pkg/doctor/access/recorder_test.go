package access

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecorderWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec.Record("node-3", []string{"ceph", "-s"}, Output{Stdout: "HEALTH_OK", ExitCode: 0})
	rec.Record("node-4", []string{"ceph", "df"}, Output{Stdout: "data"})
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	var first ReplayEntry
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Node != "node-3" || first.Stdout != "HEALTH_OK" {
		t.Errorf("entry = %+v", first)
	}
	if first.Argv[0] != "ceph" || first.Argv[1] != "-s" {
		t.Errorf("argv = %v", first.Argv)
	}
}

func TestReplayReturnsRecordedOutput(t *testing.T) {
	dir := t.TempDir()
	rec, _ := NewRecorder(dir)
	rec.Record("node-3", []string{"ceph", "-s"}, Output{Stdout: "HEALTH_OK", Duration: 2 * time.Second})
	rec.Close()

	rp, err := NewReplay(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := rp.Run(context.Background(), "node-3", []string{"ceph", "-s"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Stdout != "HEALTH_OK" {
		t.Errorf("stdout = %q", out.Stdout)
	}
	if out.Duration != 2*time.Second {
		t.Errorf("duration = %v", out.Duration)
	}
	if out.Command == "" {
		t.Error("command should be reconstructed")
	}
}

func TestReplayFailsOnUnrecordedCall(t *testing.T) {
	dir := t.TempDir()
	rec, _ := NewRecorder(dir)
	rec.Record("node-3", []string{"ceph", "-s"}, Output{Stdout: "x"})
	rec.Close()

	rp, _ := NewReplay(dir)
	_, err := rp.Run(context.Background(), "node-3", []string{"ceph", "df"})
	if err == nil {
		t.Fatal("expected error for unrecorded call")
	}
	if !strings.Contains(err.Error(), "未录制") {
		t.Errorf("error = %v", err)
	}
}

func TestReplayRejectsNodeMismatch(t *testing.T) {
	dir := t.TempDir()
	rec, _ := NewRecorder(dir)
	rec.Record("node-3", []string{"ceph", "-s"}, Output{Stdout: "x"})
	rec.Close()

	rp, _ := NewReplay(dir)
	if _, err := rp.Run(context.Background(), "node-9", []string{"ceph", "-s"}); err == nil {
		t.Fatal("expected error for wrong node")
	}
}

func TestReplayPreservesErrors(t *testing.T) {
	dir := t.TempDir()
	rec, _ := NewRecorder(dir)
	rec.RecordError("node-3", []string{"ceph", "-s"}, Output{
		Stderr: "boom", ExitCode: 1,
	}, &ToolError{Kind: KindFailed, Message: "failed: boom"})
	rec.Close()

	rp, _ := NewReplay(dir)
	_, err := rp.Run(context.Background(), "node-3", []string{"ceph", "-s"})
	if err == nil {
		t.Fatal("expected replay to reproduce the error")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
	if te.Kind != KindFailed {
		t.Errorf("kind = %v", te.Kind)
	}
}

func TestRecorderIsConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	rec, _ := NewRecorder(dir)
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(i int) {
			rec.Record("node-3", []string{"ceph", "-s"}, Output{Stdout: "x"})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "session.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 10 {
		t.Fatalf("got %d lines, want 10", len(lines))
	}
	for _, l := range lines {
		if !json.Valid([]byte(l)) {
			t.Errorf("invalid JSON line under concurrency: %q", l)
		}
	}
}
