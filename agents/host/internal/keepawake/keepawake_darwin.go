//go:build darwin

package keepawake

import (
	"log/slog"
	"os/exec"
)

var caff *exec.Cmd

// start runs caffeinate: -i prevents idle sleep, -s prevents system sleep on AC.
func start() {
	caff = exec.Command("caffeinate", "-i", "-s")
	if err := caff.Start(); err != nil {
		slog.Warn("could not start caffeinate", "err", err)
		caff = nil
		return
	}
	slog.Info("sleep prevention on (caffeinate)")
}

func stop() {
	if caff != nil && caff.Process != nil {
		_ = caff.Process.Kill()
		_, _ = caff.Process.Wait()
		caff = nil
		slog.Info("sleep prevention off")
	}
}

func poke() {}
