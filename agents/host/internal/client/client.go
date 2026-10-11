// Package client maintains the agent's single outbound connection to the
// backend: WebSocket → yamux → control stream + backend-opened data streams.
// It reconnects with exponential backoff and never listens on any port.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"github.com/cadencz/rentpc/packages/protocol"
)

// Handler is what the session supervisor implements.
type Handler interface {
	// Hello builds the hello payload (hardware, driver, version).
	Hello(ctx context.Context) protocol.Hello
	// Heartbeat builds the periodic heartbeat payload.
	Heartbeat(ctx context.Context) protocol.Heartbeat
	// StateReport is sent right after connecting.
	StateReport(ctx context.Context) protocol.StateReport
	// Control handles a backend → agent message. Replies are sent via Send.
	Control(ctx context.Context, env Envelope)
	// Stream handles a backend-opened data stream; it must send a StreamAck
	// line first and close the stream when done.
	Stream(ctx context.Context, hdr protocol.StreamHeader, st net.Conn)
}

type Envelope struct {
	Type    string
	ID      string
	Payload json.RawMessage
}

type Client struct {
	Server string // https://api.example.com
	Token  string
	H      Handler

	mu   sync.Mutex
	ctrl net.Conn
	enc  *json.Encoder
}

// Send writes a control message to the backend (no-op when disconnected).
func (c *Client) Send(typ, id string, payload any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.enc == nil {
		return errors.New("not connected")
	}
	_ = c.ctrl.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.enc.Encode(protocol.Envelope{Type: typ, ID: id, Payload: payload})
}

// Run connects and keeps reconnecting until ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("disconnected from backend", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func wsURL(server string) string {
	u := strings.TrimRight(server, "/")
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u + "/v1/agent/connect"
}

func (c *Client) runOnce(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, resp, err := websocket.Dial(dialCtx, wsURL(c.Server), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}},
	})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errors.New("agent token rejected (401): re-run `rentpc-host register`")
		}
		return err
	}
	ws.SetReadLimit(8 << 20)
	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()
	nc := websocket.NetConn(connCtx, ws, websocket.MessageBinary)

	ycfg := yamux.DefaultConfig()
	ycfg.EnableKeepAlive = true
	ycfg.KeepAliveInterval = 20 * time.Second
	ycfg.ConnectionWriteTimeout = 15 * time.Second
	ycfg.LogOutput = io.Discard
	sess, err := yamux.Client(nc, ycfg)
	if err != nil {
		return err
	}
	defer sess.Close()

	ctrl, err := sess.OpenStream()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.ctrl, c.enc = ctrl, json.NewEncoder(ctrl)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.ctrl, c.enc = nil, nil
		c.mu.Unlock()
	}()

	if err := c.Send(protocol.TypeHello, "", c.H.Hello(ctx)); err != nil {
		return err
	}
	dec := json.NewDecoder(bufio.NewReaderSize(ctrl, 1<<20))
	var first Envelope
	_ = ctrl.SetReadDeadline(time.Now().Add(20 * time.Second))
	if err := dec.Decode(&first); err != nil {
		return err
	}
	_ = ctrl.SetReadDeadline(time.Time{})
	if first.Type == protocol.TypeError {
		var e protocol.ErrorMsg
		_ = json.Unmarshal(first.Payload, &e)
		return errors.New("backend rejected agent: " + e.Code + ": " + e.Message)
	}
	var welcome protocol.Welcome
	_ = json.Unmarshal(first.Payload, &welcome)
	slog.Info("connected to backend", "host_id", welcome.HostID)
	_ = c.Send(protocol.TypeStateReport, "", c.H.StateReport(ctx))

	// Heartbeats.
	every := time.Duration(welcome.HeartbeatEvery) * time.Second
	if every <= 0 {
		every = 15 * time.Second
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-t.C:
				if err := c.Send(protocol.TypeHeartbeat, "", c.H.Heartbeat(connCtx)); err != nil {
					return
				}
			}
		}
	}()

	// Backend-opened data streams.
	go func() {
		for {
			st, err := sess.AcceptStream()
			if err != nil {
				cancelConn()
				return
			}
			go c.handleStream(connCtx, st)
		}
	}()

	// Control loop.
	for {
		var env Envelope
		if err := dec.Decode(&env); err != nil {
			return err
		}
		go c.H.Control(connCtx, env)
	}
}

func (c *Client) handleStream(ctx context.Context, st net.Conn) {
	_ = st.SetReadDeadline(time.Now().Add(20 * time.Second))
	line, err := readLine(st, 4096)
	if err != nil {
		st.Close()
		return
	}
	_ = st.SetReadDeadline(time.Time{})
	var hdr protocol.StreamHeader
	if err := json.Unmarshal(line, &hdr); err != nil {
		_ = json.NewEncoder(st).Encode(protocol.StreamAck{OK: false, Error: "bad header"})
		st.Close()
		return
	}
	c.H.Stream(ctx, hdr, st)
}

func readLine(r io.Reader, max int) ([]byte, error) {
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

// Register exchanges a one-time registration token for an agent token.
func Register(ctx context.Context, server, regToken string) (hostID, agentToken string, err error) {
	body, _ := json.Marshal(map[string]string{"token": regToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+"/v1/agent/register", strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var out struct {
		HostID     string `json:"host_id"`
		AgentToken string `json:"agent_token"`
		Error      string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	if resp.StatusCode != 200 {
		return "", "", errors.New(out.Error)
	}
	return out.HostID, out.AgentToken, nil
}
