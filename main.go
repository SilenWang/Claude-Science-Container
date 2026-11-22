// Command claude-science-container is the single binary for this project: it
// keeps the container's proxy/setup-token commands, forwards SSH port forwards
// to a remote Claude Science, and runs the whole local stack against a
// third-party API backend. The GUI phase imports the same config, forward and
// local packages.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SilenWang/Claude-Science-Container/config"
	"github.com/SilenWang/Claude-Science-Container/forward"
	"github.com/SilenWang/Claude-Science-Container/local"
	"github.com/SilenWang/Claude-Science-Container/proxy"
)

// version can be overridden with -ldflags "-X main.version=...".
var version = "0.1.0"

const binaryName = "claude-science-container"

func main() {
	if len(os.Args) < 2 {
		runBare()
		return
	}

	args := os.Args[2:]
	switch os.Args[1] {
	case "forward":
		runForward(args)
	case "local":
		runLocal(args)
	case "serve":
		if err := proxy.ServeOptionsFromEnv(version); err != nil {
			fmt.Fprintf(os.Stderr, "%s serve: %v\n", binaryName, err)
			os.Exit(1)
		}
	case "setup-token":
		if err := proxy.SetupToken(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "%s setup-token: %v\n", binaryName, err)
			os.Exit(1)
		}
	case "config":
		runConfig(args)
	case "version", "-v", "--version":
		fmt.Printf("%s %s\n", binaryName, version)
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintf(w, `Claude Science Container — single CLI for the container, remote forwarding and local mode.

usage: %s <command> [flags]

commands:
  forward   SSH-forward a remote Claude Science and print its login URL
  local     run the Anthropic→OpenAI proxy and a local claude-science against it
  config    path | show | init — inspect or create the shared config.json
  serve     run only the API bridge, configured from environment variables
            (container compatibility, used by supervisord)
  setup-token  write the fake OAuth token from ~/.claude-science/encryption.key
  version   print the version
  help      this message

Run "%s <command> --help" for command flags.
`, binaryName, binaryName)
}

func runBare() {
	path, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n\n", err)
		usage(os.Stderr)
		return
	}
	cfg, created, err := config.LoadOrCreate(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load %s: %v\n\n", path, err)
		usage(os.Stderr)
		return
	}
	if created {
		fmt.Printf("created default config: %s\n", path)
	} else {
		fmt.Printf("config: %s\n", path)
	}
	printNextSteps(cfg, path, created)
	fmt.Println()
	usage(os.Stdout)
}

func printNextSteps(cfg *config.Config, path string, created bool) {
	if !created {
		return
	}
	fmt.Println()
	fmt.Println("next steps:")
	if t := firstTarget(cfg); t == nil || t.SSHHost == "" || t.SSHKey == "" {
		fmt.Printf("  - remote mode: edit %s (ssh_host, ssh_key) then run `%s forward`\n", path, binaryName)
	}
	if b, _ := cfg.Local.EffectiveBackend(cfg.Local.DefaultBackend); b.APIKey == "" {
		fmt.Printf("  - local mode: run `%s local --set-key <API_KEY> --backend %s` then `%s local`\n",
			binaryName, cfg.Local.DefaultBackend, binaryName)
	}
}

func runForward(args []string) {
	fs := flag.NewFlagSet("forward", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config.json (default: next to the executable)")
	targetFlag := fs.String("target", "", "target name to use (default: the only target, or an interactive menu)")
	noBrowser := fs.Bool("no-browser", false, "do not open the login URL in a browser")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `usage: %s forward [flags]

Connects to forward.targets[] over SSH, runs the remote command once to fetch
the login URL, and keeps the configured port forwards alive, reconnecting
after 3s when the connection drops.

flags:
`, binaryName)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	path, cfg, created, err := loadConfig(*configFlag)
	if err != nil {
		fatal(err)
	}
	announceConfig(path, created)

	target, err := chooseTarget(cfg.Forward.Targets, *targetFlag)
	if err != nil {
		fatal(err)
	}
	if err := validateTarget(*target); err != nil {
		fatalf("%v\n\nedit %s and set forward.targets[].ssh_host / ssh_key", err, path)
	}

	session := forward.NewSession(*target, forward.Options{AutoOpenBrowser: !*noBrowser})
	session.Start(func(status forward.Status) {
		if status.Message != "" {
			logLine(status.Message)
		}
	})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logLine("Shutting down ...")
	session.Stop()
	session.Wait()
}

func runLocal(args []string) {
	fs := flag.NewFlagSet("local", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config.json (default: next to the executable)")
	setKey := fs.String("set-key", "", "store an API key for --backend and exit (use - to read stdin)")
	setKeyEnv := fs.String("set-key-env", "", "store the value of this environment variable as the API key and exit")
	setBaseURL := fs.String("set-base-url", "", "store the backend base URL and exit")
	backend := fs.String("backend", "", "backend to use/set: deepseek, openai, or custom (default: local.default_backend)")
	noBrowser := fs.Bool("no-browser", false, "do not open the login URL in a browser")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `usage: %s local [flags]

Sets up ~/.claude-science (encryption.key + fake OAuth token), starts the
Anthropic→OpenAI proxy from config.json, then runs:

  claude-science serve --no-browser --port <claude_science_port>
      with ANTHROPIC_BASE_URL=http://127.0.0.1:<proxy_port>

and prints the login URL.

config-writing flags:
  --set-key <KEY>       store an API key for --backend (use - for stdin)
  --set-key-env <VAR>   store $VAR as the API key for --backend
  --set-base-url <URL>  store the base URL for --backend
  --backend <NAME>      deepseek | openai | custom

flags:
`, binaryName)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	path, cfg, created, err := loadConfig(*configFlag)
	if err != nil {
		fatal(err)
	}
	announceConfig(path, created)

	backendName := strings.ToLower(strings.TrimSpace(*backend))
	if backendName == "" {
		backendName = strings.ToLower(strings.TrimSpace(cfg.Local.DefaultBackend))
	}
	if backendName != "deepseek" && backendName != "openai" && backendName != "custom" {
		fatalf("unknown backend %q: use deepseek, openai, or custom", backendName)
	}

	if *setKey != "" || *setKeyEnv != "" || *setBaseURL != "" {
		key := *setKey
		if key == "-" {
			data, err := os.ReadFile("/dev/stdin")
			if err != nil {
				fatal(fmt.Errorf("reading key from stdin: %w", err))
			}
			key = strings.TrimSpace(string(data))
		}
		if *setKeyEnv != "" {
			key = strings.TrimSpace(os.Getenv(*setKeyEnv))
			if key == "" {
				fatalf("environment variable %s is empty", *setKeyEnv)
			}
		}
		b := cfg.Local.Backends[backendName]
		if key != "" {
			b.APIKey = key
		}
		if *setBaseURL != "" {
			b.BaseURL = *setBaseURL
		}
		cfg.Local.Backends[backendName] = b
		if err := config.Save(path, cfg); err != nil {
			fatal(err)
		}
		fmt.Printf("saved %s backend to %s\n", backendName, path)
		fmt.Printf("run `%s local` to start\n", binaryName)
		return
	}

	cfg.Local.DefaultBackend = backendName

	runner := local.New(local.Options{
		Config:          cfg.Local,
		Version:         version,
		AutoOpenBrowser: !*noBrowser && cfg.Local.OpenBrowser,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		fatal(err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logLine("Shutting down ...")
	runner.Stop()
	_ = runner.Wait()
}

func runConfig(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: %s config <path|show|init> [flags]\n", binaryName)
		os.Exit(2)
	}
	cmd := args[0]
	rest := args[1:]

	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		printConfigUsage(os.Stdout)
		return
	}

	switch cmd {
	case "path":
		fs := flag.NewFlagSet("config path", flag.ExitOnError)
		configFlag := fs.String("config", "", "override the config path")
		_ = fs.Parse(rest)
		path, err := resolveConfigPath(*configFlag)
		if err != nil {
			fatal(err)
		}
		fmt.Println(path)
	case "show":
		fs := flag.NewFlagSet("config show", flag.ExitOnError)
		configFlag := fs.String("config", "", "override the config path")
		_ = fs.Parse(rest)
		path, cfg, created, err := loadConfig(*configFlag)
		if err != nil {
			fatal(err)
		}
		if created {
			fmt.Fprintf(os.Stderr, "created default config: %s\n", path)
		}
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			fatal(err)
		}
		fmt.Println(string(data))
	case "init":
		fs := flag.NewFlagSet("config init", flag.ExitOnError)
		configFlag := fs.String("config", "", "write to this path instead of next to the executable")
		force := fs.Bool("force", false, "overwrite an existing config")
		_ = fs.Parse(rest)
		path, err := resolveConfigPath(*configFlag)
		if err != nil {
			fatal(err)
		}
		if _, err := os.Stat(path); err == nil && !*force {
			fatalf("%s already exists; use --force to overwrite", path)
		}
		if err := config.Save(path, config.Default()); err != nil {
			fatal(err)
		}
		fmt.Printf("wrote default config: %s\n", path)
	default:
		fmt.Fprintf(os.Stderr, "unknown config subcommand %q\n", cmd)
		printConfigUsage(os.Stderr)
		os.Exit(2)
	}
}

func printConfigUsage(w *os.File) {
	fmt.Fprintf(w, `usage: %s config <path|show|init> [flags]

  path   print the config.json path (next to the executable)
  show   print the effective config with defaults filled in
  init   write a default config (fails if it exists unless --force)

flags:
  -config string  override the config path
  -force          (init only) overwrite an existing config
`, binaryName)
}

func loadConfig(flagPath string) (string, *config.Config, bool, error) {
	path, err := resolveConfigPath(flagPath)
	if err != nil {
		return "", nil, false, err
	}
	cfg, created, err := config.LoadOrCreate(path)
	if err != nil {
		return "", nil, false, err
	}
	return path, cfg, created, nil
}

func resolveConfigPath(flagPath string) (string, error) {
	if strings.TrimSpace(flagPath) != "" {
		abs, err := filepathAbs(flagPath)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	return config.DefaultPath()
}

func announceConfig(path string, created bool) {
	if created {
		fmt.Printf("created default config: %s\n", path)
		fmt.Println("edit it to point at your SSH target or API key")
	} else {
		fmt.Printf("config: %s\n", path)
	}
}

func chooseTarget(targets []config.Target, name string) (*config.Target, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("no forward.targets in config")
	}
	if name != "" {
		for i := range targets {
			if targets[i].Name == name {
				return &targets[i], nil
			}
		}
		return nil, fmt.Errorf("target %q not found", name)
	}
	if len(targets) == 1 {
		return &targets[0], nil
	}

	fmt.Println("\nAvailable targets:")
	for i, t := range targets {
		label := t.Name
		if label == "" {
			label = fmt.Sprintf("%s@%s:%d", t.SSHUser, t.SSHHost, t.SSHPort)
		}
		containerInfo := ""
		if t.ContainerID != "" {
			containerInfo = fmt.Sprintf(" (container: %s)", t.ContainerID)
		}
		fmt.Printf("  [%d] %s — %s%s", i+1, label, t.SSHHost, containerInfo)
		if len(t.PortForwards) > 0 {
			fmt.Print(" (forwards: ")
			for j, pf := range t.PortForwards {
				if j > 0 {
					fmt.Print(", ")
				}
				fmt.Printf(":%d→:%d", pf.Local, pf.Remote)
			}
			fmt.Print(")")
		}
		fmt.Println()
	}

	fmt.Printf("\nSelect target [1-%d]: ", len(targets))
	var choice int
	for {
		if _, err := fmt.Scanf("%d", &choice); err != nil {
			fmt.Printf("Invalid input. Enter 1-%d: ", len(targets))
			continue
		}
		if choice < 1 || choice > len(targets) {
			fmt.Printf("Invalid choice. Enter 1-%d: ", len(targets))
			continue
		}
		break
	}
	return &targets[choice-1], nil
}

func validateTarget(t config.Target) error {
	if t.SSHHost == "" {
		return fmt.Errorf("target %q: ssh_host is required", t.Name)
	}
	if t.SSHKey == "" {
		return fmt.Errorf("target %q: ssh_key is required", t.Name)
	}
	return nil
}

func firstTarget(cfg *config.Config) *config.Target {
	if len(cfg.Forward.Targets) == 0 {
		return nil
	}
	return &cfg.Forward.Targets[0]
}

func filepathAbs(path string) (string, error) {
	return filepath.Abs(path)
}

func logLine(message string) {
	fmt.Fprintln(os.Stderr, message)
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", binaryName, err)
	}
	os.Exit(1)
}

func fatalf(format string, args ...any) {
	fatal(fmt.Errorf(format, args...))
}
