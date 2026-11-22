package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// DefaultVersion 是本地构建时的版本号；发布构建可用
// -ldflags "-X main.version=..." 覆盖根包的同名变量。
const DefaultVersion = "0.1.0"

// Address 返回 proxy 的监听地址。
func Address(cfg *config) string {
	return net.JoinHostPort(cfg.ProxyHost, strconv.Itoa(cfg.ProxyPort))
}

// NewServer 暴露 HTTP handler，测试与嵌入式启动（local 或下一阶段的 GUI）
// 都可以直接复用它。
func NewServer(cfg *config, version string) http.Handler {
	return newServer(cfg, version)
}

// ServeOptionsFromEnv 按容器现有语义从环境变量启动 proxy，直到进程退出。
func ServeOptionsFromEnv(version string) error {
	cfg := newConfigFromEnv()
	for _, warning := range cfg.warnings {
		fmt.Fprintf(os.Stderr, "[bridge] WARNING: %s\n", warning)
	}

	srv := newServer(cfg, version)
	addr := Address(cfg)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	srv.logf("[bridge] api-bridge %s listening on http://%s (default_backend=%s, force_model=%s)",
		version, addr, cfg.DefaultBackend, orNone(cfg.ForceModel))
	return httpServer.ListenAndServe()
}

// ServeContext 用显式 Options 启动 proxy，ctx 取消时优雅关停。
// local 包与 GUI 用它把 proxy 跑在同一个进程里。
func ServeContext(ctx context.Context, opts Options, version string, stderr io.Writer) error {
	cfg := newConfigFromOptions(opts)
	for _, warning := range cfg.warnings {
		fmt.Fprintf(stderr, "[bridge] WARNING: %s\n", warning)
	}

	srv := newServer(cfg, version)
	addr := Address(cfg)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	srv.logf("[bridge] api-bridge %s listening on http://%s (default_backend=%s, force_model=%s)",
		version, addr, cfg.DefaultBackend, orNone(cfg.ForceModel))

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			httpServer.Close()
		}
		<-errCh
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// SetupToken 写假 OAuth token，供容器入口与 local 子命令复用。
func SetupToken(w io.Writer) error {
	return runSetupToken(w)
}
