//go:build !windows

package qemu

import (
	"os/exec"
	"syscall"
)

// lowerPriority keeps the owner's desktop responsive while a rental runs.
func lowerPriority(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Nice level is applied after start (see provision) via setpriority; on
	// unix we simply wrap through "nice" when available.
	if nice, err := exec.LookPath("nice"); err == nil {
		args := append([]string{"-n", "10", cmd.Path}, cmd.Args[1:]...)
		cmd.Path = nice
		cmd.Args = append([]string{nice}, args...)
	}
}
