// Package hwdetect reports the host's hardware and virtualization
// capabilities. Everything is best-effort: a detection failure yields an
// empty field, never an error that stops the agent.
package hwdetect

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/cadencz/rentpc/packages/protocol"
)

// Detect gathers everything. dataDir is where sandboxes will live; the
// free-disk figure is measured there.
func Detect(ctx context.Context, dataDir string) protocol.Hardware {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	hw := protocol.Hardware{OSFamily: runtime.GOOS, Arch: runtime.GOARCH}
	if hi, err := host.InfoWithContext(ctx); err == nil {
		hw.Hostname = hi.Hostname
		hw.OSVersion = strings.TrimSpace(hi.Platform + " " + hi.PlatformVersion)
	}
	if hw.Hostname == "" {
		hw.Hostname, _ = os.Hostname()
	}
	if infos, err := cpu.InfoWithContext(ctx); err == nil && len(infos) > 0 {
		hw.CPUModel = strings.TrimSpace(infos[0].ModelName)
	}
	if n, err := cpu.CountsWithContext(ctx, false); err == nil {
		hw.CPUCores = n
	}
	if n, err := cpu.CountsWithContext(ctx, true); err == nil {
		hw.CPUThreads = n
	}
	if hw.CPUCores == 0 {
		hw.CPUCores = runtime.NumCPU()
	}
	if hw.CPUThreads == 0 {
		hw.CPUThreads = runtime.NumCPU()
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		hw.RAMMB = int64(vm.Total / (1024 * 1024))
	}
	if dataDir == "" {
		dataDir, _ = os.Getwd()
	}
	_ = os.MkdirAll(dataDir, 0o700)
	if du, err := disk.UsageWithContext(ctx, dataDir); err == nil {
		hw.DiskFreeGB = int64(du.Free / (1024 * 1024 * 1024))
	}
	hw.GPUs = detectGPUs(ctx)
	hw.Virt = detectVirt(ctx)
	return hw
}

// ---- GPUs ------------------------------------------------------------------------

func detectGPUs(ctx context.Context) []protocol.GPU {
	var gpus []protocol.GPU
	// NVIDIA first: nvidia-smi is the most reliable source of VRAM on every OS.
	if out, err := run(ctx, "nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			parts := strings.Split(line, ",")
			if len(parts) == 2 {
				vram, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
				gpus = append(gpus, protocol.GPU{Vendor: "NVIDIA", Model: strings.TrimSpace(parts[0]), VRAMMB: vram})
			}
		}
	}
	if len(gpus) > 0 {
		return gpus
	}
	switch runtime.GOOS {
	case "windows":
		out, err := run(ctx, "powershell", "-NoProfile", "-Command",
			`Get-CimInstance Win32_VideoController | Select-Object Name,AdapterRAM | ConvertTo-Json -Compress`)
		if err == nil {
			out = strings.TrimSpace(out)
			if strings.HasPrefix(out, "{") {
				out = "[" + out + "]"
			}
			var rows []struct {
				Name       string `json:"Name"`
				AdapterRAM int64  `json:"AdapterRAM"`
			}
			if json.Unmarshal([]byte(out), &rows) == nil {
				for _, r := range rows {
					if r.Name == "" || strings.Contains(strings.ToLower(r.Name), "remote") {
						continue
					}
					gpus = append(gpus, protocol.GPU{Vendor: vendorOf(r.Name), Model: r.Name, VRAMMB: r.AdapterRAM / (1024 * 1024)})
				}
			}
		}
	case "darwin":
		out, err := run(ctx, "system_profiler", "SPDisplaysDataType", "-json")
		if err == nil {
			var sp struct {
				Items []struct {
					Model string `json:"sppci_model"`
					VRAM  string `json:"spdisplays_vram"`
					Cores string `json:"sppci_cores"`
				} `json:"SPDisplaysDataType"`
			}
			if json.Unmarshal([]byte(out), &sp) == nil {
				for _, it := range sp.Items {
					g := protocol.GPU{Vendor: vendorOf(it.Model), Model: it.Model, VRAMMB: parseMB(it.VRAM)}
					if it.Cores != "" {
						g.Model += " (" + it.Cores + " cores)"
					}
					gpus = append(gpus, g)
				}
			}
		}
	default: // linux
		out, err := run(ctx, "lspci")
		if err == nil {
			re := regexp.MustCompile(`(?i)(VGA compatible controller|3D controller|Display controller): (.*)`)
			for _, line := range strings.Split(out, "\n") {
				if m := re.FindStringSubmatch(line); m != nil {
					gpus = append(gpus, protocol.GPU{Vendor: vendorOf(m[2]), Model: strings.TrimSpace(m[2])})
				}
			}
		}
	}
	return gpus
}

func vendorOf(model string) string {
	l := strings.ToLower(model)
	switch {
	case strings.Contains(l, "nvidia"), strings.Contains(l, "geforce"), strings.Contains(l, "rtx"), strings.Contains(l, "quadro"):
		return "NVIDIA"
	case strings.Contains(l, "amd"), strings.Contains(l, "radeon"):
		return "AMD"
	case strings.Contains(l, "intel"), strings.Contains(l, "iris"), strings.Contains(l, "uhd"):
		return "Intel"
	case strings.Contains(l, "apple"):
		return "Apple"
	}
	return "Other"
}

func parseMB(s string) int64 {
	s = strings.ToUpper(strings.TrimSpace(s))
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0
	}
	n, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	if strings.HasPrefix(f[1], "GB") {
		return int64(n * 1024)
	}
	return int64(n)
}

// ---- Virtualization ------------------------------------------------------------

func detectVirt(ctx context.Context) protocol.VirtCapability {
	var v protocol.VirtCapability
	v.QEMU = QEMUBinary() != ""
	switch runtime.GOOS {
	case "windows":
		sys := os.Getenv("SystemRoot")
		if sys == "" {
			sys = `C:\Windows`
		}
		// WinHvPlatform.dll is present once the "Windows Hypervisor Platform" feature is on.
		if _, err := os.Stat(filepath.Join(sys, "System32", "WinHvPlatform.dll")); err == nil {
			v.WHPX = true
		}
		if out, err := run(ctx, "powershell", "-NoProfile", "-Command", `(Get-Service vmms -ErrorAction SilentlyContinue).Status`); err == nil && strings.Contains(out, "Running") {
			v.HyperV = true
		}
	case "darwin":
		if out, err := run(ctx, "sysctl", "-n", "kern.hv_support"); err == nil && strings.TrimSpace(out) == "1" {
			v.HVF = true
			v.VZ = true // Virtualization.framework rides on the same support (macOS 11+)
		}
	default:
		if _, err := os.Stat("/dev/kvm"); err == nil {
			v.KVM = true
		}
	}
	return v
}

// QEMUBinary returns the path of the qemu-system binary for this
// architecture, searching PATH and the usual install locations.
func QEMUBinary() string {
	name := "qemu-system-x86_64"
	if runtime.GOARCH == "arm64" {
		name = "qemu-system-aarch64"
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	candidates := []string{
		`C:\Program Files\qemu\` + name,
		"/opt/homebrew/bin/" + name,
		"/usr/local/bin/" + name,
		"/usr/bin/" + name,
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	return string(out), err
}
