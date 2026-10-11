// Package sandbox defines the isolation driver interface. Every rental runs
// in a Sandbox created by a Driver; the rest of the agent never touches a
// hypervisor directly. See docs/ARCHITECTURE.md §8 and docs/SECURITY.md.
package sandbox

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/cadencz/rentpc/packages/protocol"
)

var ErrNotFound = errors.New("sandbox not found")

// IsolationLevel is advertised to the backend and shown to renters. Drivers
// must be honest: a container is not a VM.
type IsolationLevel string

const (
	IsolationVM        IsolationLevel = "vm"        // hardware virtualization, own kernel
	IsolationContainer IsolationLevel = "container" // shared kernel; compute-only tier
	IsolationNone      IsolationLevel = "none"      // mock / development only
)

type Capabilities struct {
	Driver    string
	Isolation IsolationLevel
	GPU       protocol.GPUAccess // best GPU tier this driver can offer on this host
	MaxCPUs   int
	MaxRAMMB  int64
	Notes     []string // human-readable caveats surfaced by `rentpc-host doctor`
}

type Spec = protocol.SandboxSpec

type Status struct {
	ID        string
	RentalID  string
	State     protocol.SandboxState
	CPUPct    float64
	MemMB     int64
	NetTxB    int64
	NetRxB    int64
	Error     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Driver creates and destroys sandboxes.
type Driver interface {
	Name() string
	Capabilities(ctx context.Context) (Capabilities, error)
	// Provision creates the sandbox and returns once a renter could connect
	// (sshd answering). It must be idempotent for the same RentalID.
	Provision(ctx context.Context, spec Spec) (Status, error)
	Status(ctx context.Context, rentalID string) (Status, error)
	List(ctx context.Context) ([]Status, error)
	// Dial connects to a TCP port inside the sandbox over the private
	// virtual network. It must never reach anything outside the sandbox.
	Dial(ctx context.Context, rentalID string, port int) (net.Conn, error)
	// Destroy stops the sandbox and securely removes its disk and any
	// renter data. Idempotent.
	Destroy(ctx context.Context, rentalID string) error
	// SetExpiry lets the driver enforce a hard deadline locally even if the
	// backend goes away.
	SetExpiry(ctx context.Context, rentalID string, at time.Time) error
}

// PTY is an interactive terminal attached to a sandbox.
type PTY interface {
	net.Conn
	Resize(cols, rows int) error
}

// PTYDialer is implemented by drivers that can open a terminal without SSH
// (the mock driver). VM drivers do not implement it; the session package
// falls back to SSH over Dial(22) using the rental key.
type PTYDialer interface {
	DialPTY(ctx context.Context, rentalID string, cols, rows int) (PTY, error)
}
