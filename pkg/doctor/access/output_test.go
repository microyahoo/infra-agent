package access

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTruncateOutput(t *testing.T) {
	big := strings.Repeat("a", 1000) + strings.Repeat("b", 1000)
	got, truncated := truncateOutput(big, 600)
	if !truncated {
		t.Fatal("expected truncated")
	}
	if len(got) > 700 {
		t.Errorf("truncated output too large: %d", len(got))
	}
	if !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, "b") {
		t.Error("expected head and tail preserved")
	}
	if !strings.Contains(got, "truncated") {
		t.Error("expected truncation marker")
	}
}

func TestTruncateOutputUnderLimit(t *testing.T) {
	got, truncated := truncateOutput("short", 100)
	if truncated {
		t.Fatal("did not expect truncation")
	}
	if got != "short" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyTimeout(t *testing.T) {
	err := classifyError(errDeadline, "", 0)
	if err.Kind != KindTimeout {
		t.Errorf("kind = %v, want timeout", err.Kind)
	}
	if !strings.Contains(err.Message, "无响应") {
		t.Errorf("message should explain timeout in domain terms: %q", err.Message)
	}
}

func TestClassifyExitCode(t *testing.T) {
	err := classifyError(nil, "Error: monclient hunting", 1)
	if err.Kind != KindFailed {
		t.Errorf("kind = %v, want failed", err.Kind)
	}
	if !strings.Contains(err.Message, "monclient hunting") {
		t.Errorf("message should include stderr: %q", err.Message)
	}
}

func TestClassifyUnreachable(t *testing.T) {
	err := classifyError(errors.New("dial tcp 10.0.0.1:22: connect: connection refused"), "", 0)
	if err.Kind != KindUnreachable {
		t.Errorf("kind = %v, want unreachable", err.Kind)
	}
}

func TestClassifyEmptyIsNotError(t *testing.T) {
	if err := classifyError(nil, "", 0); err != nil {
		t.Fatalf("empty successful output should not be an error, got %v", err)
	}
}

func TestOutputCarriesDuration(t *testing.T) {
	o := Output{Stdout: "x", Duration: 3 * time.Second}
	if o.Duration != 3*time.Second {
		t.Error("duration not preserved")
	}
}

func TestToolErrorKindsDistinct(t *testing.T) {
	kinds := []ErrorKind{KindTimeout, KindUnreachable, KindUnavailable, KindFailed}
	seen := map[ErrorKind]bool{}
	for _, k := range kinds {
		if seen[k] {
			t.Errorf("duplicate kind %v", k)
		}
		seen[k] = true
	}
}
