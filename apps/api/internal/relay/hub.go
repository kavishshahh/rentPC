// Package relay keeps the live connections to host agents and lets the rest
// of the backend (a) send control messages to an agent and (b) open data
// streams (terminals, TCP tunnels) towards a sandbox on that host.
//
// Scaling note: this hub is in-process. One backend instance == one hub. To
// run several instances, streams must be routed via Redis pub/sub or a
// consistent-hash of host IDs; see docs/ARCHITECTURE.md §13.
package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"github.com/cadencz/rentpc/packages/protocol"
)

var ErrAgentOffline = errors.New("host agent is offline")

// Events are the hub's callbacks into the domain layer. All are optional.
type Events struct {
	OnHello           func(ctx context.Context, hostID string, h protocol.Hello) error
	OnHeartbeat       func(ctx context.Context, hostID string, hb protocol.Heartbeat)
	OnProvisionResult func(ctx context.Context, hostID string, r protocol.ProvisionResult)
	OnTerminateResult func(ctx context.Context, hostID string, r protocol.TerminateResult)
	OnSandboxStatus   func(ctx context.Context, hostID string, s protocol.SandboxStatus)
	OnStateReport     func(ctx context.Context, hostID string, s protocol.StateReport)
	OnDisconnect      func(ctx context.Context, hostID string)
}

type Hub struct {
	events   Events
	minProto int

	mu     sync.RWMutex
	agents map[string]*agentConn
}

func New(events Events, minProto int) *Hub {
	return &Hub{events: events, minProto: minProto, agents: map[string]*agentConn{}}
}

type agentConn struct {
	hostID string
	sess   *yamux.Session
	ctrl   net.Conn
	wmu    sync.Mutex
	enc    *json.Encoder
	hello  protocol.Hello
	since  time.Time
}

// Online reports whether an agent for hostID is connected.
func (h *Hub) Online(hostID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.agents[hostID]
	return ok
}

func (h *Hub) OnlineHosts() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.agents))
	for id := range h.agents {
		ids = append(ids, id)
	}
	return ids
}

// Send writes a control envelope to the agent.
func (h *Hub) Send(hostID string, typ, id string, payload any) error {
	h.mu.RLock()
	a := h.agents[hostID]
	h.mu.RUnlock()
	if a == nil {
		return ErrAgentOffline
	}
	return a.send(protocol.Envelope{Type: typ, ID: id, Payload: payload})
}

func (a *agentConn) send(e protocol.Envelope) error {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	_ = a.ctrl.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return a.enc.Encode(e)
}

// OpenStream opens a yamux stream to the agent, sends the StreamHeader and
// waits for the agent's ack. The returned conn carries raw bytes (TCP kind)
// or framed records (PTY kind).
func (h *Hub) OpenStream(ctx context.Context, hostID string, hdr protocol.StreamHeader) (net.Conn, error) {
	h.mu.RLock()
	a := h.agents[hostID]
	h.mu.RUnlock()
	if a == nil {
		return nil, ErrAgentOffline
	}
	st, err := a.sess.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	_ = st.SetDeadline(time.Now().Add(20 * time.Second))
	if err := json.NewEncoder(st).Encode(hdr); err != nil {
		st.Close()
		return nil, err
	}
	// The ack is a single JSON line. Read it byte-wise so that no bytes
	// belonging to the data phase are buffered away from the caller.
	line, err := ReadLine(st, 4096)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("read stream ack: %w", err)
	}
	var ack protocol.StreamAck
	if err := json.Unmarshal(line, &ack); err != nil {
		st.Close()
		return nil, fmt.Errorf("decode stream ack: %w", err)
	}
	if !ack.OK {
		st.Close()
		return nil, fmt.Errorf("agent refused stream: %s", ack.Error)
	}
	_ = st.SetDeadline(time.Time{})
	return st, nil
}

// ServeAgent upgrades the request to a WebSocket and runs the agent session
// until it disconnects. hostID must already be authenticated by the caller.
func (h *Hub) ServeAgent(w http.ResponseWriter, r *http.Request, hostID string) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  []string{"*"}, // agents are not browsers; auth is the host token
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		slog.Warn("agent ws accept failed", "err", err)
		return
	}
	ws.SetReadLimit(8 << 20)
	ctx := context.Background()
	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)

	ycfg := yamux.DefaultConfig()
	ycfg.EnableKeepAlive = true
	ycfg.KeepAliveInterval = 20 * time.Second
	ycfg.ConnectionWriteTimeout = 15 * time.Second
	ycfg.LogOutput = io.Discard
	sess, err := yamux.Server(nc, ycfg)
	if err != nil {
		slog.Warn("yamux server failed", "err", err)
		ws.Close(websocket.StatusInternalError, "yamux")
		return
	}
	defer sess.Close()

	// Stream 0: control channel, opened by the agent.
	ctrl, err := sess.AcceptStream()
	if err != nil {
		slog.Warn("agent did not open control stream", "host", hostID, "err", err)
		return
	}
	a := &agentConn{hostID: hostID, sess: sess, ctrl: ctrl, enc: json.NewEncoder(ctrl), since: time.Now()}

	dec := json.NewDecoder(bufio.NewReaderSize(ctrl, 1<<20))
	// First message must be hello.
	var first rawEnvelope
	_ = ctrl.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err := dec.Decode(&first); err != nil || first.Type != protocol.TypeHello {
		_ = a.send(protocol.Envelope{Type: protocol.TypeError, Payload: protocol.ErrorMsg{Code: "bad_hello", Message: "first message must be hello"}})
		return
	}
	_ = ctrl.SetReadDeadline(time.Time{})
	if err := json.Unmarshal(first.Payload, &a.hello); err != nil {
		return
	}
	if a.hello.ProtocolVersion < h.minProto {
		_ = a.send(protocol.Envelope{Type: protocol.TypeError, Payload: protocol.ErrorMsg{Code: "protocol_too_old", Message: "please update the host agent"}})
		return
	}
	if h.events.OnHello != nil {
		if err := h.events.OnHello(ctx, hostID, a.hello); err != nil {
			_ = a.send(protocol.Envelope{Type: protocol.TypeError, Payload: protocol.ErrorMsg{Code: "rejected", Message: err.Error()}})
			return
		}
	}

	// Replace any previous connection for this host.
	h.mu.Lock()
	if old := h.agents[hostID]; old != nil {
		old.sess.Close()
	}
	h.agents[hostID] = a
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.agents[hostID] == a {
			delete(h.agents, hostID)
		}
		h.mu.Unlock()
		if h.events.OnDisconnect != nil {
			h.events.OnDisconnect(ctx, hostID)
		}
		slog.Info("agent disconnected", "host", hostID)
	}()

	_ = a.send(protocol.Envelope{Type: protocol.TypeWelcome, Payload: protocol.Welcome{HostID: hostID, ServerTime: time.Now().UTC(), HeartbeatEvery: 15}})
	slog.Info("agent connected", "host", hostID, "driver", a.hello.Driver, "version", a.hello.AgentVersion)

	for {
		var env rawEnvelope
		if err := dec.Decode(&env); err != nil {
			return
		}
		h.dispatch(ctx, hostID, env)
	}
}

type rawEnvelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (h *Hub) dispatch(ctx context.Context, hostID string, env rawEnvelope) {
	switch env.Type {
	case protocol.TypeHeartbeat:
		var hb protocol.Heartbeat
		if json.Unmarshal(env.Payload, &hb) == nil && h.events.OnHeartbeat != nil {
			h.events.OnHeartbeat(ctx, hostID, hb)
		}
	case protocol.TypeProvisionResult:
		var r protocol.ProvisionResult
		if json.Unmarshal(env.Payload, &r) == nil && h.events.OnProvisionResult != nil {
			h.events.OnProvisionResult(ctx, hostID, r)
		}
	case protocol.TypeTerminateResult:
		var r protocol.TerminateResult
		if json.Unmarshal(env.Payload, &r) == nil && h.events.OnTerminateResult != nil {
			h.events.OnTerminateResult(ctx, hostID, r)
		}
	case protocol.TypeSandboxStatus:
		var s protocol.SandboxStatus
		if json.Unmarshal(env.Payload, &s) == nil && h.events.OnSandboxStatus != nil {
			h.events.OnSandboxStatus(ctx, hostID, s)
		}
	case protocol.TypeStateReport:
		var s protocol.StateReport
		if json.Unmarshal(env.Payload, &s) == nil && h.events.OnStateReport != nil {
			h.events.OnStateReport(ctx, hostID, s)
		}
	default:
		slog.Debug("unknown agent message", "type", env.Type, "host", hostID)
	}
}

// ReadLine reads up to and including '\n' one byte at a time (so that nothing
// after the line is consumed) and returns the line without the newline.
func ReadLine(r io.Reader, max int) ([]byte, error) {
	buf := make([]byte, 0, 256)
	b := make([]byte, 1)
	for len(buf) < max {
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		if b[0] == '\n' {
			return buf, nil
		}
		buf = append(buf, b[0])
	}
	return nil, errors.New("line too long")
}
