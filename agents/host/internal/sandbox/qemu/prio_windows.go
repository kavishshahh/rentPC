//go:build windows

package qemu

import (
	"os/exec"
	"syscall"
)

const belowNormalPriorityClass = 0x00004000

// lowerPriority keeps the owner's desktop responsive while a rental runs.
func lowerPriority(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: belowNormalPriorityClass | syscall.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}
