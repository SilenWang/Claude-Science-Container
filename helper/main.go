package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	var (
		cliMode    bool
		configPath string
	)

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--cli":
			cliMode = true
		case arg == "--config" || arg == "-config":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "missing value for --config")
				os.Exit(2)
			}
			i++
			configPath = args[i]
		case arg == "-h" || arg == "-help" || arg == "--help":
			usage()
			return
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "unknown option: %s\n\n", arg)
			usage()
			os.Exit(2)
		default:
			configPath = arg
		}
	}

	if configPath == "" {
		configPath = defaultConfigPath()
	}

	if cliMode {
		runCLI(configPath)
		return
	}
	runGUI(configPath)
}

func usage() {
	fmt.Fprint(os.Stderr, `Claude Science launcher

Usage:
  launcher [--config FILE] [FILE]   open the GUI (one button per target)
  launcher --cli [--config FILE]    use the terminal prompt instead

Without FILE the launcher looks for config.json next to the executable,
then in the working directory.
`)
}

// defaultConfigPath prefers config.json next to the executable so that a
// double-clicked launcher finds it whatever its working directory is.
func defaultConfigPath() string {
	exePath, exeErr := os.Executable()
	if exeErr == nil {
		besideExe := filepath.Join(filepath.Dir(exePath), "config.json")
		if fileExists(besideExe) {
			return besideExe
		}
	}
	if fileExists("config.json") {
		return "config.json"
	}
	if exeErr == nil {
		return filepath.Join(filepath.Dir(exePath), "config.json")
	}
	return "config.json"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
