package mcpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewHTTPHandler returns a Bearer-authenticated Streamable HTTP handler.
// /health does not require authentication and is suitable for liveness probes.
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

// ServeHTTP starts an MCP HTTP server on addr until ctx is cancelled.
//
// This service is equivalent to putting cluster read access on the network.
// Therefore:
//   - An empty token is refused at startup
//   - Only loopback addresses are allowed by default; allowRemote must be
//     set explicitly to expose on any other interface
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
		if !secureEqual(r.Header.Get("Authorization"), "Bearer "+token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="infra-agent"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
