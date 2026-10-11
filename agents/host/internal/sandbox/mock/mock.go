// Package mock is an in-process "sandbox" for development and CI. It gives
// the whole rental pipeline (reserve → provision → terminal → terminate)
// something to run against without a hypervisor. It provides NO isolation
// and refuses to run unless explicitly selected with --driver mock.
package mock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/cadencz/rentpc/agents/host/internal/sandbox"
	"github.com/cadencz/rentpc/packages/protocol"
)

type Driver struct {
	mu    sync.Mutex
	boxes map[string]*box
}

type box struct {
	st sandbox.Status
}

func New() *Driver { return &Driver{boxes: map[string]*box{}} }

func (d *Driver) Name() string { return "mock" }

func (d *Driver) Capabilities(ctx context.Context) (sandbox.Capabilities, error) {
	return sandbox.Capabilities{
		Driver: "mock", Isolation: sandbox.IsolationNone, GPU: protocol.GPUNone, MaxCPUs: 64, MaxRAMMB: 1 << 20,
		Notes: []string{"mock driver: NO isolation, development only"},
	}, nil
}

func (d *Driver) Provision(ctx context.Context, spec sandbox.Spec) (sandbox.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if b, ok := d.boxes[spec.RentalID]; ok {
		return b.st, nil
	}
	time.Sleep(500 * time.Millisecond) // pretend to boot
	st := sandbox.Status{ID: "mock-" + spec.RentalID[:8], RentalID: spec.RentalID, State: protocol.SandboxRunning,
		CreatedAt: time.Now(), ExpiresAt: spec.ExpiresAt}
	d.boxes[spec.RentalID] = &box{st: st}
	return st, nil
}

func (d *Driver) Status(ctx context.Context, rentalID string) (sandbox.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.boxes[rentalID]
	if !ok {
		return sandbox.Status{}, sandbox.ErrNotFound
	}
	return b.st, nil
}

func (d *Driver) List(ctx context.Context) ([]sandbox.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]sandbox.Status, 0, len(d.boxes))
	for _, b := range d.boxes {
		out = append(out, b.st)
	}
	return out, nil
}

func (d *Driver) Dial(ctx context.Context, rentalID string, port int) (net.Conn, error) {
	if _, err := d.Status(ctx, rentalID); err != nil {
		return nil, err
	}
	return nil, errors.New("mock sandbox has no network services; use the web terminal")
}

func (d *Driver) Destroy(ctx context.Context, rentalID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.boxes, rentalID)
	return nil
}

func (d *Driver) SetExpiry(ctx context.Context, rentalID string, at time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if b, ok := d.boxes[rentalID]; ok {
		b.st.ExpiresAt = at
	}
	return nil
}

// DialPTY implements sandbox.PTYDialer with a tiny fake shell so the browser
// terminal can be exercised end-to-end.
func (d *Driver) DialPTY(ctx context.Context, rentalID string, cols, rows int) (sandbox.PTY, error) {
	if _, err := d.Status(ctx, rentalID); err != nil {
		return nil, err
	}
	client, server := net.Pipe()
	go fakeShell(server, rentalID)
	return &pty{Conn: client}, nil
}

type pty struct{ net.Conn }

func (p *pty) Resize(cols, rows int) error { return nil }

func fakeShell(c net.Conn, rentalID string) {
	defer c.Close()
	w := func(s string) { _, _ = io.WriteString(c, strings.ReplaceAll(s, "\n", "\r\n")) }
	w("rentPC mock sandbox (NO isolation — development driver)\n")
	w("rental " + rentalID + "\n\n")
	prompt := func() { w("renter@mock:~$ ") }
	prompt()
	buf := make([]byte, 1024)
	var line []byte
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		for _, b := range buf[:n] {
			switch b {
			case '\r', '\n':
				w("\n")
				cmd := strings.TrimSpace(string(line))
				line = line[:0]
				switch {
				case cmd == "":
				case cmd == "exit":
					w("logout\n")
					return
				case cmd == "whoami":
					w("renter\n")
				case cmd == "uname -a":
					w("Linux mock-sandbox 6.8.0 #1 SMP x86_64 GNU/Linux\n")
				case cmd == "help":
					w("mock shell: try whoami, uname -a, echo <text>, hostname, exit\n")
				case cmd == "hostname":
					w("mock-sandbox\n")
				case strings.HasPrefix(cmd, "echo "):
					w(strings.TrimPrefix(cmd, "echo ") + "\n")
				default:
					w(fmt.Sprintf("mock: %s: command not found (this is the mock driver; a real VM runs a full Ubuntu)\n", strings.Fields(cmd)[0]))
				}
				prompt()
			case 0x7f, 0x08: // backspace
				if len(line) > 0 {
					line = line[:len(line)-1]
					w("\b \b")
				}
			case 0x03: // ctrl-c
				line = line[:0]
				w("^C\n")
				prompt()
			default:
				line = append(line, b)
				_, _ = c.Write([]byte{b})
			}
		}
	}
}
