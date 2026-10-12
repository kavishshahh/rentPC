//go:build !darwin && !windows

package keepawake

import (
	"log/slog"
	"os/exec"
)

var inhibit *exec.Cmd

// Linux: systemd-inhibit holds a sleep/idle lock while the child sleeps.
func start() {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		return
	}
	inhibit = exec.Command("systemd-inhibit", "--what=sleep:idle", "--who=rentpc-host", "--why=rental in progress", "sleep", "infinity")
	if err := inhibit.Start(); err != nil {
		inhibit = nil
		return
	}
	slog.Info("sleep prevention on (systemd-inhibit)")
}

func stop() {
	if inhibit != nil && inhibit.Process != nil {
		_ = inhibit.Process.Kill()
		_, _ = inhibit.Process.Wait()
		inhibit = nil
	}
}

func poke() {}
