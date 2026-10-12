// Package session supervises rentals on this host: it turns backend control
// messages into driver calls, reports results, bridges data streams into
// sandboxes and enforces expiry locally.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/cadencz/rentpc/agents/host/internal/client"
	"github.com/cadencz/rentpc/agents/host/internal/hwdetect"
	"github.com/cadencz/rentpc/agents/host/internal/keepawake"
	"github.com/cadencz/rentpc/agents/host/internal/sandbox"
	"github.com/cadencz/rentpc/packages/protocol"
)

// AgentKeyProvider is implemented by VM drivers: the agent injects its own
// SSH key into every sandbox (alongside the renter's) so it can open the
// web-terminal PTY without ever seeing the renter's private key.
type AgentKeyProvider interface {
	AgentSigner(rentalID string) (ssh.Signer, error)
}

type Supervisor struct {
	Driver  sandbox.Driver
	Version string
	DataDir string
	Client  *client.Client

	mu       sync.Mutex
	hardware protocol.Hardware
	hwAt     time.Time
	// provisioning guards against duplicate provision requests for one rental.
	provisioning map[string]bool
}

func New(d sandbox.Driver, version, dataDir string) *Supervisor {
	return &Supervisor{Driver: d, Version: version, DataDir: dataDir, provisioning: map[string]bool{}}
}

func (s *Supervisor) Hello(ctx context.Context) protocol.Hello {
	s.mu.Lock()
	if time.Since(s.hwAt) > 10*time.Minute {
		s.hardware = hwdetect.Detect(ctx, s.DataDir)
		s.hwAt = time.Now()
	}
	hw := s.hardware
	s.mu.Unlock()
	return protocol.Hello{ProtocolVersion: protocol.Version, AgentVersion: s.Version, Hardware: hw, Driver: s.Driver.Name()}
}

func (s *Supervisor) Heartbeat(ctx context.Context) protocol.Heartbeat {
	hb := protocol.Heartbeat{}
	list, _ := s.Driver.List(ctx)
	for _, st := range list {
		hb.Sandboxes = append(hb.Sandboxes, toStatus(st))
	}
	return hb
}

func (s *Supervisor) StateReport(ctx context.Context) protocol.StateReport {
	rep := protocol.StateReport{Sandboxes: []protocol.SandboxStatus{}}
	list, _ := s.Driver.List(ctx)
	for _, st := range list {
		rep.Sandboxes = append(rep.Sandboxes, toStatus(st))
	}
	return rep
}

func toStatus(st sandbox.Status) protocol.SandboxStatus {
	return protocol.SandboxStatus{RentalID: st.RentalID, SandboxID: st.ID, State: st.State, CPUPct: st.CPUPct,
		MemMB: st.MemMB, NetTxB: st.NetTxB, NetRxB: st.NetRxB, Error: st.Error}
}

// ---- control ---------------------------------------------------------------------

func (s *Supervisor) Control(ctx context.Context, env client.Envelope) {
	switch env.Type {
	case protocol.TypeProvision:
		var spec protocol.SandboxSpec
		if err := json.Unmarshal(env.Payload, &spec); err != nil {
			return
		}
		s.provision(ctx, env.ID, spec)
	case protocol.TypeTerminate:
		var req protocol.TerminateRequest
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			return
		}
		slog.Info("terminating sandbox", "rental", req.RentalID, "reason", req.Reason)
		err := s.Driver.Destroy(ctx, req.RentalID)
		res := protocol.TerminateResult{RentalID: req.RentalID, OK: err == nil}
		if err != nil {
			res.Error = err.Error()
		}
		_ = s.Client.Send(protocol.TypeTerminateResult, env.ID, res)
	case protocol.TypeExtend:
		var req protocol.ExtendRequest
		if err := json.Unmarshal(env.Payload, &req); err != nil {
			return
		}
		_ = s.Driver.SetExpiry(ctx, req.RentalID, req.ExpiresAt)
	case protocol.TypeError:
		var e protocol.ErrorMsg
		_ = json.Unmarshal(env.Payload, &e)
		slog.Error("backend error", "code", e.Code, "msg", e.Message)
	}
}

func (s *Supervisor) provision(ctx context.Context, id string, spec protocol.SandboxSpec) {
	s.mu.Lock()
	if s.provisioning[spec.RentalID] {
		s.mu.Unlock()
		return
	}
	s.provisioning[spec.RentalID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.provisioning, spec.RentalID)
		s.mu.Unlock()
	}()

	slog.Info("provisioning sandbox", "rental", spec.RentalID, "cpus", spec.CPUs, "ram_mb", spec.RAMMB, "disk_gb", spec.DiskGB)
	pctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	st, err := s.Driver.Provision(pctx, spec)
	res := protocol.ProvisionResult{RentalID: spec.RentalID, OK: err == nil, SandboxID: st.ID}
	if err != nil {
		res.Error = err.Error()
		slog.Error("provision failed", "rental", spec.RentalID, "err", err)
		_ = s.Driver.Destroy(context.Background(), spec.RentalID)
	} else {
		slog.Info("sandbox ready", "rental", spec.RentalID, "sandbox", st.ID)
		keepawake.Set(true)
	}
	_ = s.Client.Send(protocol.TypeProvisionResult, id, res)
}

// ---- data streams ------------------------------------------------------------------

func (s *Supervisor) Stream(ctx context.Context, hdr protocol.StreamHeader, st net.Conn) {
	defer st.Close()
	ack := func(err error) bool {
		a := protocol.StreamAck{OK: err == nil}
		if err != nil {
			a.Error = err.Error()
		}
		b, _ := json.Marshal(a)
		_, werr := st.Write(append(b, '\n'))
		return err == nil && werr == nil
	}
	if _, err := s.Driver.Status(ctx, hdr.RentalID); err != nil {
		ack(fmt.Errorf("no sandbox for rental: %w", err))
		return
	}
	switch hdr.Kind {
	case protocol.StreamTCP:
		port := hdr.Port
		if port == 0 {
			port = 22
		}
		dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		conn, err := s.Driver.Dial(dctx, hdr.RentalID, port)
		cancel()
		if !ack(err) {
			return
		}
		defer conn.Close()
		pipe(st, conn)
	case protocol.StreamPTY:
		pty, err := s.openPTY(ctx, hdr)
		if !ack(err) {
			return
		}
		defer pty.Close()
		s.pumpPTY(st, pty)
	default:
		ack(errors.New("unknown stream kind"))
	}
}

func pipe(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}

func (s *Supervisor) openPTY(ctx context.Context, hdr protocol.StreamHeader) (sandbox.PTY, error) {
	cols, rows := hdr.Cols, hdr.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if pd, ok := s.Driver.(sandbox.PTYDialer); ok {
		return pd.DialPTY(ctx, hdr.RentalID, cols, rows)
	}
	kp, ok := s.Driver.(AgentKeyProvider)
	if !ok {
		return nil, errors.New("driver cannot open terminals")
	}
	signer, err := kp.AgentSigner(hdr.RentalID)
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := s.Driver.Dial(dctx, hdr.RentalID, 22)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            "renter",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // private virtual network, fresh host key per VM
		Timeout:         15 * time.Second,
	}
	cc, chans, reqs, err := ssh.NewClientConn(raw, "sandbox:22", cfg)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("ssh into sandbox: %w", err)
	}
	cl := ssh.NewClient(cc, chans, reqs)
	sess, err := cl.NewSession()
	if err != nil {
		cl.Close()
		return nil, err
	}
	if err := sess.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		sess.Close()
		cl.Close()
		return nil, err
	}
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	sess.Stderr = sess.Stdout
	if err := sess.Shell(); err != nil {
		sess.Close()
		cl.Close()
		return nil, err
	}
	return &sshPTY{sess: sess, cl: cl, in: stdin, out: stdout}, nil
}

type sshPTY struct {
	sess *ssh.Session
	cl   *ssh.Client
	in   io.WriteCloser
	out  io.Reader
}

func (p *sshPTY) Read(b []byte) (int, error)       { return p.out.Read(b) }
func (p *sshPTY) Write(b []byte) (int, error)      { return p.in.Write(b) }
func (p *sshPTY) Close() error                     { p.sess.Close(); return p.cl.Close() }
func (p *sshPTY) Resize(cols, rows int) error      { return p.sess.WindowChange(rows, cols) }
func (p *sshPTY) LocalAddr() net.Addr              { return dummyAddr{} }
func (p *sshPTY) RemoteAddr() net.Addr             { return dummyAddr{} }
func (p *sshPTY) SetDeadline(time.Time) error      { return nil }
func (p *sshPTY) SetReadDeadline(time.Time) error  { return nil }
func (p *sshPTY) SetWriteDeadline(time.Time) error { return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "ssh" }
func (dummyAddr) String() string  { return "sandbox" }

// pumpPTY moves framed records between the relay stream and the PTY.
func (s *Supervisor) pumpPTY(st net.Conn, pty sandbox.PTY) {
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				if werr := protocol.WriteFrame(st, protocol.FrameData, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			kind, payload, err := protocol.ReadFrame(st)
			if err != nil {
				return
			}
			switch kind {
			case protocol.FrameData:
				if _, err := pty.Write(payload); err != nil {
					return
				}
			case protocol.FrameResize:
				if c, r, ok := protocol.DecodeResize(payload); ok {
					_ = pty.Resize(c, r)
				}
			}
		}
	}()
	<-done
}

// RunExpiryEnforcer destroys sandboxes that outlive their expiry even if the
// backend never says so (defence in depth for a lost connection).
func (s *Supervisor) RunExpiryEnforcer(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			list, _ := s.Driver.List(ctx)
			keepawake.Set(len(list) > 0)
			for _, st := range list {
				if !st.ExpiresAt.IsZero() && time.Now().After(st.ExpiresAt.Add(5*time.Minute)) {
					slog.Warn("sandbox past expiry; destroying locally", "rental", st.RentalID)
					_ = s.Driver.Destroy(ctx, st.RentalID)
					_ = s.Client.Send(protocol.TypeTerminateResult, "", protocol.TerminateResult{RentalID: st.RentalID, OK: true})
				}
			}
		}
	}
}
