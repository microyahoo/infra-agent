package access

import (
	"context"
	"testing"
	"time"
)

func TestChainAddr(t *testing.T) {
	if got := chainAddr("10.20.30.7", 22); got != "10.20.30.7:22" {
		t.Errorf("chainAddr = %q", got)
	}
	if got := chainAddr("10.20.30.7", 2222); got != "10.20.30.7:2222" {
		t.Errorf("chainAddr = %q", got)
	}
}

func TestDialFailsFastOnBadHost(t *testing.T) {
	c := Config{
		Host:           "127.0.0.1",
		Port:           1, // 不会有服务监听
		User:           "root",
		Password:       "x",
		CommandTimeout: 2 * time.Second,
		DialTimeout:    1 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Dial(ctx, c)
	if err == nil {
		t.Fatal("expected dial error")
	}
}

func TestDialFailsWithoutAuth(t *testing.T) {
	c := Config{Host: "10.0.0.1"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Dial(ctx, c); err == nil {
		t.Fatal("expected validation error")
	}
}
