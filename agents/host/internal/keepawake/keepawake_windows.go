//go:build windows

package keepawake

import (
	"log/slog"
	"syscall"
)

var (
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
)

const (
	esSystemRequired = 0x00000001
	esAwayModeReq    = 0x00000040
)

// Without ES_CONTINUOUS the call just resets the idle timer, so we re-call it
// from the supervisor's ticker; that avoids pinning an OS thread.
func start() { poke(); slog.Info("sleep prevention on (SetThreadExecutionState)") }
func stop()  { slog.Info("sleep prevention off") }
func poke()  { procSetThreadExecutionState.Call(uintptr(esSystemRequired | esAwayModeReq)) }
