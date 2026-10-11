// rentpc is the renter CLI.
//
//	rentpc tunnel [--server URL] <rental-id>   stdin/stdout ⇄ guest:22 (for ssh ProxyCommand)
//	rentpc term   [--server URL] <rental-id>   interactive terminal in the current console
//	rentpc ssh    [--server URL] <rental-id> [-i key]   convenience wrapper around ssh
//
// Authentication: RENTPC_API_KEY (create one in the web app under Account → API keys)
// or RENTPC_SESSION (the session cookie value) for development.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/coder/websocket"
	"golang.org/x/term"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "tunnel":
		cmdTunnel(os.Args[2:])
	case "term":
		cmdTerm(os.Args[2:])
	case "ssh":
		cmdSSH(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `rentpc — renter CLI
  rentpc tunnel [--server URL] <rental-id>          raw tunnel to guest:22 (ssh ProxyCommand)
  rentpc term   [--server URL] <rental-id>          browser-style terminal in this console
  rentpc ssh    [--server URL] [-i key] <rental-id> run ssh through the tunnel
env: RENTPC_API_KEY=rpk_...  (or RENTPC_SESSION=<cookie> for development)`)
}

func serverFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("RENTPC_SERVER")
	if def == "" {
		def = "http://localhost:8080"
	}
	return fs.String("server", def, "backend URL")
}

func authHeader() http.Header {
	h := http.Header{}
	if k := os.Getenv("RENTPC_API_KEY"); k != "" {
		h.Set("Authorization", "Bearer "+k)
	} else if s := os.Getenv("RENTPC_SESSION"); s != "" {
		h.Set("Cookie", "rentpc_session="+s)
	} else {
		fmt.Fprintln(os.Stderr, "error: set RENTPC_API_KEY (or RENTPC_SESSION)")
		os.Exit(1)
	}
	return h
}

func wsDial(ctx context.Context, server, path string) (*websocket.Conn, error) {
	u := strings.TrimRight(server, "/")
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	ws, resp, err := websocket.Dial(ctx, u+path, &websocket.DialOptions{HTTPHeader: authHeader()})
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		return nil, err
	}
	ws.SetReadLimit(8 << 20)
	return ws, nil
}

func cmdTunnel(args []string) {
	fs := flag.NewFlagSet("tunnel", flag.ExitOnError)
	server := serverFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws, err := wsDial(ctx, *server, "/v1/rentals/"+fs.Arg(0)+"/tunnel")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tunnel:", err)
		os.Exit(1)
	}
	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	go func() { _, _ = io.Copy(nc, os.Stdin); cancel() }()
	_, _ = io.Copy(os.Stdout, nc)
}

func cmdSSH(args []string) {
	fs := flag.NewFlagSet("ssh", flag.ExitOnError)
	server := serverFlag(fs)
	key := fs.String("i", "", "private key file (from the rental page)")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(2)
	}
	self, _ := os.Executable()
	sshArgs := []string{"-o", fmt.Sprintf("ProxyCommand=%q tunnel --server %s %s", self, *server, fs.Arg(0)),
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"}
	if *key != "" {
		sshArgs = append(sshArgs, "-i", *key)
	}
	sshArgs = append(sshArgs, "renter@rentpc")
	sshArgs = append(sshArgs, fs.Args()[1:]...)
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "RENTPC_API_KEY="+os.Getenv("RENTPC_API_KEY"), "RENTPC_SESSION="+os.Getenv("RENTPC_SESSION"))
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func cmdTerm(args []string) {
	fs := flag.NewFlagSet("term", flag.ExitOnError)
	server := serverFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cols, rows := 80, 24
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		cols, rows = w, h
	}
	ws, err := wsDial(ctx, *server, fmt.Sprintf("/v1/rentals/%s/terminal?cols=%d&rows=%d", fs.Arg(0), cols, rows))
	if err != nil {
		fmt.Fprintln(os.Stderr, "terminal:", err)
		os.Exit(1)
	}
	defer ws.CloseNow()
	var restore func()
	if term.IsTerminal(int(os.Stdin.Fd())) {
		old, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			restore = func() { _ = term.Restore(int(os.Stdin.Fd()), old) }
			defer restore()
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	go func() {
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			if typ == websocket.MessageBinary {
				_, _ = os.Stdout.Write(data)
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if werr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()
	_ = json.Marshal // (resize messages: {"type":"resize","cols":..,"rows":..}, sent by the web UI)
	select {
	case <-ctx.Done():
	case <-sig:
	}
	if restore != nil {
		restore()
	}
	fmt.Println()
}
