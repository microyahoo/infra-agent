package mcpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/microyahoo/infra-agent/internal/mcpserver"
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
