package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// version 由 -ldflags "-X main.version=..." 覆盖；默认值给本地构建用。
var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		if err := serve(); err != nil {
			fmt.Fprintf(os.Stderr, "api-bridge serve: %v\n", err)
			os.Exit(1)
		}
	case "version", "-v", "--version":
		fmt.Printf("api-bridge %s\n", version)
	case "setup-token":
		// M5 才实现：写 ~/.claude-science/.oauth-tokens/*.enc（标准库 Fernet）。
		fmt.Fprintln(os.Stderr, "api-bridge setup-token: not implemented yet (planned for M5)")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: api-bridge <serve|version|setup-token>\n")
}

func serve() error {
	cfg := newConfigFromEnv()
	for _, warning := range cfg.warnings {
		fmt.Fprintf(os.Stderr, "[bridge] WARNING: %s\n", warning)
	}

	srv := newServer(cfg, version)
	addr := net.JoinHostPort(cfg.ProxyHost, strconv.Itoa(cfg.ProxyPort))

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	srv.logf("[bridge] api-bridge %s listening on http://%s (default_backend=%s, force_model=%s)",
		version, addr, cfg.DefaultBackend, orNone(cfg.ForceModel))
	return httpServer.ListenAndServe()
}
