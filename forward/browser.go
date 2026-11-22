package forward

import (
	"os/exec"
	"runtime"
)

// openBrowser launches the platform's URL opener without waiting for it.
func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		cmd = "xdg-open"
		args = []string{url}
	}

	proc := exec.Command(cmd, args...)
	if err := proc.Start(); err != nil {
		return err
	}
	return proc.Process.Release()
}
