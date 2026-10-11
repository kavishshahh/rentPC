package netpolicy

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAllowIP(t *testing.T) {
	p := DefaultPolicy()
	blocked := []string{"127.0.0.1", "10.0.2.2", "192.168.1.1", "172.16.5.5", "169.254.169.254", "100.64.0.1", "::1", "fe80::1", "0.0.0.0"}
	for _, s := range blocked {
		if p.AllowIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	allowed := []string{"1.1.1.1", "8.8.8.8", "151.101.1.63", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if !p.AllowIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
	// Every address bound on this host is blocked too.
	for ip := range p.hostIPs {
		if p.AllowIP(net.ParseIP(ip)) {
			t.Errorf("host ip %s should be blocked", ip)
		}
	}
}

func TestCheckBlocksPortsAndLiteralIPs(t *testing.T) {
	p := DefaultPolicy()
	if _, _, err := p.Check(context.Background(), "1.1.1.1:25"); err == nil {
		t.Error("smtp should be blocked")
	}
	if _, _, err := p.Check(context.Background(), "192.168.0.10:443"); err == nil {
		t.Error("private ip should be blocked")
	}
	if _, _, err := p.Check(context.Background(), "localhost:8080"); err == nil {
		t.Error("localhost should be blocked")
	}
	if ip, port, err := p.Check(context.Background(), "1.1.1.1:443"); err != nil || port != 443 || ip.String() != "1.1.1.1" {
		t.Errorf("public ip should pass, got %v %d %v", ip, port, err)
	}
}

// TestProxyDeniesLoopbackTarget starts a local HTTP server (which lives on
// loopback, i.e. "the host") and verifies the proxy refuses to reach it both
// via CONNECT and via a plain absolute-URI request.
func TestProxyDeniesLoopbackTarget(t *testing.T) {
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "secret") }))
	defer victim.Close()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	px := &Proxy{Policy: DefaultPolicy(), Label: "test"}
	go px.Serve(ctx, l)

	for _, req := range []string{
		"CONNECT " + strings.TrimPrefix(victim.URL, "http://") + " HTTP/1.1\r\nHost: x\r\n\r\n",
		"GET " + victim.URL + "/ HTTP/1.1\r\nHost: x\r\n\r\n",
	} {
		c, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(c, req)
		status, _ := bufio.NewReader(c).ReadString('\n')
		c.Close()
		if !strings.Contains(status, "403") {
			t.Errorf("expected 403 for %q, got %q", strings.Fields(req)[0], strings.TrimSpace(status))
		}
	}
}
