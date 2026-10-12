//go:build windows

package qemu

import (
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess     = kernel32.NewProc("OpenProcess")
	procGetExitCodeProc = kernel32.NewProc("GetExitCodeProcess")
	procCloseHandle     = kernel32.NewProc("CloseHandle")
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

// processAlive reports whether pid is a live process.
func processAlive(pid int) bool {
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return false
	}
	defer procCloseHandle.Call(h)
	var code uint32
	r, _, _ := procGetExitCodeProc.Call(h, uintptr(unsafe.Pointer(&code)))
	return r != 0 && code == stillActive
}

const belowNormalPriorityClass = 0x00004000

// lowerPriority keeps the owner's desktop responsive while a rental runs.
func lowerPriority(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: belowNormalPriorityClass | syscall.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}
