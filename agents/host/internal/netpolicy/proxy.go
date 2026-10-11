// Package netpolicy provides the sandbox's only path to the internet: an
// HTTP/1.1 forward proxy (CONNECT + absolute-URI requests) that enforces the
// egress policy from docs/SECURITY.md. The guest itself runs with QEMU
// `restrict=on`, i.e. it cannot reach the host, the LAN or the internet
// directly; everything goes through here, where destinations are resolved
// and checked before any connection is made.
package netpolicy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Policy decides which destinations are reachable.
type Policy struct {
	// BlockedPorts are refused regardless of destination (25 = SMTP spam).
	BlockedPorts map[int]bool
	// ExtraBlocked lets a host owner add their own ranges.
	ExtraBlocked []*net.IPNet
	// hostIPs are the addresses currently bound on this host.
	hostIPs map[string]bool
}

var privateRanges = mustCIDRs(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

// DefaultPolicy blocks private/link-local/loopback ranges, every IP bound on
// this host, and SMTP.
func DefaultPolicy() *Policy {
	p := &Policy{BlockedPorts: map[int]bool{25: true, 465: true, 587: true}, hostIPs: map[string]bool{}}
	p.RefreshHostIPs()
	return p
}

// RefreshHostIPs re-reads the host's interface addresses.
func (p *Policy) RefreshHostIPs() {
	ips := map[string]bool{}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			ips[ipn.IP.String()] = true
		}
	}
	p.hostIPs = ips
}

// AllowIP reports whether a resolved destination IP may be contacted.
func (p *Policy) AllowIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4 // normalise IPv4-mapped IPv6 so the v4 ranges match
	}
	if p.hostIPs[ip.String()] {
		return false
	}
	for _, n := range privateRanges {
		if n.Contains(ip) {
			return false
		}
	}
	for _, n := range p.ExtraBlocked {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// Check resolves host:port and returns the first permitted IP, or an error.
// A name that resolves to ANY blocked address is refused outright (defeats
// DNS-rebinding style tricks where a public name maps to a private IP).
func (p *Policy) Check(ctx context.Context, hostport string) (net.IP, int, error) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, 0, fmt.Errorf("bad destination %q", hostport)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil || port < 1 || port > 65535 {
		return nil, 0, fmt.Errorf("bad port %q", portStr)
	}
	if p.BlockedPorts[port] {
		return nil, 0, fmt.Errorf("port %d is blocked by policy", port)
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if !p.AllowIP(ip) {
			return nil, 0, fmt.Errorf("destination %s is not reachable from a sandbox (private/host/link-local)", ip)
		}
		return ip, port, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(rctx, "ip", host)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, 0, fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, ip := range ips {
		if !p.AllowIP(ip) {
			return nil, 0, fmt.Errorf("%s resolves to %s which is not reachable from a sandbox", host, ip)
		}
	}
	// Prefer IPv4 for broad compatibility.
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip, port, nil
		}
	}
	return ips[0], port, nil
}

// Proxy is the egress proxy for one sandbox.
type Proxy struct {
	Policy *Policy
	Label  string // for logs (rental id)

	TxBytes atomic.Int64 // sandbox → internet
	RxBytes atomic.Int64 // internet → sandbox
}

// Serve accepts connections from l (typically an SSH remote-forward
// listener living inside the guest) until ctx is done.
func (p *Proxy) Serve(ctx context.Context, l net.Listener) error {
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go p.handle(ctx, c)
	}
}

func (p *Proxy) handle(ctx context.Context, c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	if req.Method == http.MethodConnect {
		p.handleConnect(ctx, c, req)
		return
	}
	p.handlePlain(ctx, c, br, req)
}

func (p *Proxy) deny(c net.Conn, req *http.Request, reason string) {
	slog.Info("egress denied", "sandbox", p.Label, "method", req.Method, "dest", req.Host, "reason", reason)
	body := "rentPC egress policy: " + reason + "\n"
	fmt.Fprintf(c, "HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func (p *Proxy) dial(ctx context.Context, hostport string) (net.Conn, error) {
	ip, port, err := p.Policy.Check(ctx, hostport)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 20 * time.Second}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), fmt.Sprint(port)))
}

func (p *Proxy) handleConnect(ctx context.Context, c net.Conn, req *http.Request) {
	target := req.Host
	if !strings.Contains(target, ":") {
		target += ":443"
	}
	up, err := p.dial(ctx, target)
	if err != nil {
		p.deny(c, req, err.Error())
		return
	}
	defer up.Close()
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	p.pipe(c, up)
}

func (p *Proxy) handlePlain(ctx context.Context, c net.Conn, br *bufio.Reader, req *http.Request) {
	if !req.URL.IsAbs() {
		p.deny(c, req, "only absolute-URI or CONNECT requests are accepted")
		return
	}
	if req.URL.Scheme != "http" {
		p.deny(c, req, "only http:// may be proxied in plain mode; use CONNECT for TLS")
		return
	}
	hostport := req.URL.Host
	if !strings.Contains(hostport, ":") {
		hostport += ":80"
	}
	up, err := p.dial(ctx, hostport)
	if err != nil {
		p.deny(c, req, err.Error())
		return
	}
	defer up.Close()
	// Rewrite to origin-form and forward; keep it simple: one request per connection.
	req.RequestURI = ""
	req.Header.Del("Proxy-Connection")
	req.Header.Set("Connection", "close")
	outReq := req.Clone(ctx)
	outReq.URL.Scheme, outReq.URL.Host = "", ""
	outReq.RequestURI = ""
	if err := outReq.Write(up); err != nil {
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(up), req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	resp.Close = true
	_ = resp.Write(&countWriter{w: c, n: &p.RxBytes})
}

type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (cw *countWriter) Write(b []byte) (int, error) {
	n, err := cw.w.Write(b)
	cw.n.Add(int64(n))
	return n, err
}

func (p *Proxy) pipe(guest, up net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		n, _ := io.Copy(up, guest)
		p.TxBytes.Add(n)
		if cw, ok := up.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(guest, up)
		p.RxBytes.Add(n)
		done <- struct{}{}
	}()
	<-done
	// Give the other direction a moment to drain, then tear down.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}
