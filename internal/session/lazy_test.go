package session

import (
	"context"
	"errors"
	"testing"

	"github.com/microyahoo/infra-agent/internal/ceph/tools"
)

func TestLazyOpensOnce(t *testing.T) {
	opens := 0
	l := &Lazy{Open: func(context.Context) (*Session, error) {
		opens++
		return &Session{registry: tools.NewRegistry()}, nil
	}}
	for i := 0; i < 3; i++ {
		if _, err := l.Registry(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if opens != 1 {
		t.Fatalf("expected one open, got %d", opens)
	}
}

func TestLazyRetriesAfterFailure(t *testing.T) {
	calls := 0
	l := &Lazy{Open: func(context.Context) (*Session, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("dial failed")
		}
		return &Session{registry: tools.NewRegistry()}, nil
	}}
	if _, err := l.Registry(context.Background()); err == nil {
		t.Fatal("expected first call to fail")
	}
	if _, err := l.Registry(context.Background()); err != nil {
		t.Fatalf("second call should retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected two attempts, got %d", calls)
	}
}

func TestLazyResetReopens(t *testing.T) {
	opens := 0
	l := &Lazy{Open: func(context.Context) (*Session, error) {
		opens++
		return &Session{registry: tools.NewRegistry()}, nil
	}}
	if _, err := l.Registry(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.Reset()
	if _, err := l.Registry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if opens != 2 {
		t.Fatalf("reset should force a reopen, got %d opens", opens)
	}
}
