// rentpc-host is the host agent: it registers the machine, reports
// hardware, keeps the outbound connection to the backend and runs isolated
// sandboxes for rentals.
//
//	rentpc-host register --server https://api.rentpc.dev --token <one-time token>
//	rentpc-host run [--driver qemu|mock]
//	rentpc-host doctor
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/cadencz/rentpc/agents/host/internal/client"
	"github.com/cadencz/rentpc/agents/host/internal/hwdetect"
	"github.com/cadencz/rentpc/agents/host/internal/sandbox"
	"github.com/cadencz/rentpc/agents/host/internal/sandbox/mock"
	"github.com/cadencz/rentpc/agents/host/internal/sandbox/qemu"
	"github.com/cadencz/rentpc/agents/host/internal/session"
)

var version = "0.1.0-dev"

type credentials struct {
	Server     string `json:"server"`
	HostID     string `json:"host_id"`
	AgentToken string `json:"agent_token"`
}

func defaultDataDir() string {
	if v := os.Getenv("RENTPC_DATA_DIR"); v != "" {
		return v
	}
	base, err := os.UserConfigDir()
	if err != nil || runtime.GOOS == "linux" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "rentpc-host")
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "register":
		cmdRegister(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "doctor":
		cmdDoctor(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `rentpc-host — rentPC host agent

  rentpc-host register --server <url> --token <one-time token>
  rentpc-host run [--driver qemu|mock] [--data-dir <dir>] [--allow-tcg]
  rentpc-host doctor [--data-dir <dir>]
  rentpc-host version`)
}

func credsPath(dataDir string) string { return filepath.Join(dataDir, "credentials.json") }

func cmdRegister(args []string) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	server := fs.String("server", "", "backend URL, e.g. https://api.rentpc.dev")
	token := fs.String("token", "", "one-time registration token from the web dashboard")
	dataDir := fs.String("data-dir", defaultDataDir(), "agent data directory")
	_ = fs.Parse(args)
	if *server == "" || *token == "" {
		fs.Usage()
		os.Exit(2)
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		fatal(err)
	}
	hostID, agentTok, err := client.Register(context.Background(), *server, *token)
	if err != nil {
		fatal(fmt.Errorf("registration failed: %w", err))
	}
	b, _ := json.MarshalIndent(credentials{Server: *server, HostID: hostID, AgentToken: agentTok}, "", "  ")
	if err := os.WriteFile(credsPath(*dataDir), b, 0o600); err != nil {
		fatal(err)
	}
	fmt.Printf("Registered host %s with %s.\nCredentials saved to %s\nNext: rentpc-host run\n", hostID, *server, credsPath(*dataDir))
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	driverName := fs.String("driver", "qemu", "sandbox driver: qemu (VM isolation) or mock (development only, NO isolation)")
	dataDir := fs.String("data-dir", defaultDataDir(), "agent data directory")
	allowTCG := fs.Bool("allow-tcg", false, "allow software emulation when no accelerator is available (very slow)")
	accel := fs.String("accel", "", "override QEMU accelerator (whpx|hvf|kvm|tcg)")
	_ = fs.Parse(args)

	raw, err := os.ReadFile(credsPath(*dataDir))
	if err != nil {
		fatal(fmt.Errorf("not registered yet (%s missing): run `rentpc-host register` first", credsPath(*dataDir)))
	}
	var creds credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		fatal(err)
	}

	var drv sandbox.Driver
	switch *driverName {
	case "mock":
		slog.Warn("using the MOCK driver: rentals get NO isolation. Development only.")
		drv = mock.New()
	case "qemu":
		d, err := qemu.New(qemu.Options{DataDir: *dataDir, Accel: *accel, AllowTCG: *allowTCG})
		if err != nil {
			fatal(err)
		}
		drv = d
	default:
		fatal(fmt.Errorf("unknown driver %q", *driverName))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sup := session.New(drv, version, *dataDir)
	cl := &client.Client{Server: creds.Server, Token: creds.AgentToken, H: sup}
	sup.Client = cl
	if q, ok := drv.(*qemu.Driver); ok {
		slog.Info("preparing base image")
		if _, err := q.EnsureBaseImage(ctx); err != nil {
			slog.Error("base image", "err", err)
		}
	}
	go sup.RunExpiryEnforcer(ctx)
	slog.Info("rentpc-host starting", "version", version, "driver", drv.Name(), "server", creds.Server, "host_id", creds.HostID)
	cl.Run(ctx)
	// Shutdown: destroy nothing automatically — live rentals survive an agent
	// restart (qemu driver re-adopts them). Users stop rentals via the backend.
	slog.Info("rentpc-host stopped")
}

func cmdDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "agent data directory")
	_ = fs.Parse(args)
	hw := hwdetect.Detect(context.Background(), *dataDir)
	b, _ := json.MarshalIndent(hw, "", "  ")
	fmt.Println(string(b))
	fmt.Println()
	ok := func(c bool, label, fix string) {
		mark := "✗"
		if c {
			mark = "✓"
		}
		fmt.Printf("  %s %s", mark, label)
		if !c && fix != "" {
			fmt.Printf("  → %s", fix)
		}
		fmt.Println()
	}
	fmt.Println("Checks:")
	ok(hw.Virt.QEMU, "QEMU installed", "install from https://www.qemu.org/download/ and add it to PATH")
	switch runtime.GOOS {
	case "windows":
		ok(hw.Virt.WHPX, "Windows Hypervisor Platform enabled", `run as admin: DISM /online /Enable-Feature /FeatureName:HypervisorPlatform /All  (then reboot)`)
		fmt.Printf("  %s Hyper-V management: %v (optional; Pro/Enterprise only)\n", "·", hw.Virt.HyperV)
	case "darwin":
		ok(hw.Virt.HVF, "Hypervisor.framework available", "macOS 11+ on supported hardware required")
	default:
		ok(hw.Virt.KVM, "/dev/kvm available", "enable VT-x/AMD-V in BIOS and add your user to the kvm group")
	}
	ok(hw.DiskFreeGB >= 40, fmt.Sprintf("≥ 40 GB free in data dir (%d GB)", hw.DiskFreeGB), "free up space or set --data-dir")
	ok(hw.RAMMB >= 8192, fmt.Sprintf("≥ 8 GB RAM (%d MB)", hw.RAMMB), "")
	fmt.Println("\nGPU access in rentals: not available in this version (CPU-only VM tier). See docs/ARCHITECTURE.md §1.2.")
	if _, err := os.Stat(credsPath(*dataDir)); err == nil {
		fmt.Println("Registered: yes (" + credsPath(*dataDir) + ")")
	} else {
		fmt.Println("Registered: no — run `rentpc-host register`")
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
