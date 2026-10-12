// Package qemu is the MVP isolation driver: one Linux microVM per rental,
// run by qemu-system-* with the host's native accelerator (WHPX on Windows,
// HVF on macOS, KVM on Linux).
//
// Isolation properties (see docs/SECURITY.md):
//   - own kernel, own qcow2 overlay disk under the agent data dir, nothing
//     from the host filesystem is ever mounted or shared;
//   - `-nodefaults`, no USB/PCI passthrough, no display, serial to a log file;
//   - networking is `-netdev user,restrict=on`: the guest cannot reach the
//     host, the LAN or the internet directly. The only inbound path is the
//     loopback hostfwd to guest:22 used by the agent; the only outbound path
//     is the policy-enforcing egress proxy (netpolicy) that the agent
//     publishes inside the guest as 127.0.0.1:3128 via an SSH remote forward.
package qemu

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kdomanski/iso9660"
	"golang.org/x/crypto/ssh"

	"github.com/cadencz/rentpc/agents/host/internal/hwdetect"
	"github.com/cadencz/rentpc/agents/host/internal/netpolicy"
	"github.com/cadencz/rentpc/agents/host/internal/sandbox"
	"github.com/cadencz/rentpc/packages/protocol"
)

const (
	ubuntuRelease  = "noble" // 24.04 LTS
	imageBaseURL   = "https://cloud-images.ubuntu.com/" + ubuntuRelease + "/current/"
	guestProxyPort = 3128
)

type Options struct {
	DataDir string
	// Accel overrides the accelerator (whpx|hvf|kvm|tcg). Empty = auto.
	Accel string
	// AllowTCG lets the driver fall back to software emulation (very slow;
	// only for development on machines without virtualization).
	AllowTCG bool
	Policy   *netpolicy.Policy
}

type Driver struct {
	opt   Options
	qemu  string
	accel string

	mu    sync.Mutex
	boxes map[string]*vm // by rental id
}

// meta is persisted per sandbox so a restarted agent can find/kill it.
type meta struct {
	RentalID  string    `json:"rental_id"`
	SandboxID string    `json:"sandbox_id"`
	PID       int       `json:"pid"`
	SSHPort   int       `json:"ssh_port"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Spec      protocol.SandboxSpec
}

type vm struct {
	meta   meta
	dir    string
	cmd    *exec.Cmd
	state  protocol.SandboxState
	err    string
	cancel context.CancelFunc // stops the network supervisor
	proxy  *netpolicy.Proxy
}

func New(opt Options) (*Driver, error) {
	if opt.DataDir == "" {
		return nil, errors.New("qemu: DataDir required")
	}
	if opt.Policy == nil {
		opt.Policy = netpolicy.DefaultPolicy()
	}
	d := &Driver{opt: opt, boxes: map[string]*vm{}}
	d.qemu = hwdetect.QEMUBinary()
	if d.qemu == "" {
		return nil, errors.New("qemu-system binary not found; install QEMU (https://www.qemu.org/download/) and make sure it is on PATH")
	}
	d.accel = opt.Accel
	if d.accel == "" {
		d.accel = defaultAccel()
	}
	if d.accel == "tcg" && !opt.AllowTCG {
		return nil, errors.New("no hardware accelerator available (enable Windows Hypervisor Platform / check VT-x / /dev/kvm); pass --allow-tcg for slow emulation")
	}
	for _, sub := range []string{"images", "sandboxes"} {
		if err := os.MkdirAll(filepath.Join(opt.DataDir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	d.recover()
	return d, nil
}

func defaultAccel() string {
	switch runtime.GOOS {
	case "windows":
		return "whpx"
	case "darwin":
		return "hvf"
	default:
		if _, err := os.Stat("/dev/kvm"); err == nil {
			return "kvm"
		}
		return "tcg"
	}
}

func (d *Driver) Name() string { return "qemu" }

func (d *Driver) Capabilities(ctx context.Context) (sandbox.Capabilities, error) {
	c := sandbox.Capabilities{Driver: "qemu", Isolation: sandbox.IsolationVM, GPU: protocol.GPUNone,
		MaxCPUs: runtime.NumCPU() - 1, Notes: []string{"accelerator: " + d.accel, "qemu: " + d.qemu}}
	if d.accel == "tcg" {
		c.Notes = append(c.Notes, "WARNING: software emulation (tcg) is very slow")
	}
	return c, nil
}

// ---- recovery -------------------------------------------------------------------

// recover reloads meta files from a previous agent run. Any VM whose process
// is still alive is adopted; dead ones are cleaned up.
func (d *Driver) recover() {
	entries, _ := os.ReadDir(filepath.Join(d.opt.DataDir, "sandboxes"))
	for _, e := range entries {
		dir := filepath.Join(d.opt.DataDir, "sandboxes", e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			continue
		}
		var m meta
		if json.Unmarshal(b, &m) != nil || m.RentalID == "" {
			_ = os.RemoveAll(dir)
			continue
		}
		if m.PID > 0 && processAlive(m.PID) {
			slog.Info("adopting running sandbox from previous run", "rental", m.RentalID, "pid", m.PID)
			v := &vm{meta: m, dir: dir, state: protocol.SandboxRunning}
			d.boxes[m.RentalID] = v
			d.startNetwork(v)
			go d.watchAdopted(v)
		} else {
			slog.Info("cleaning stale sandbox dir", "rental", m.RentalID)
			_ = os.RemoveAll(dir)
		}
	}
}

// processAlive is implemented per OS (prio_unix.go / prio_windows.go).

// watchAdopted polls an adopted VM (we have no Cmd to Wait on) and marks it
// failed if its process disappears.
func (d *Driver) watchAdopted(v *vm) {
	for {
		time.Sleep(10 * time.Second)
		d.mu.Lock()
		cur := d.boxes[v.meta.RentalID]
		d.mu.Unlock()
		if cur != v {
			return
		}
		if !processAlive(v.meta.PID) {
			d.mu.Lock()
			if v.state != protocol.SandboxStopping {
				v.state, v.err = protocol.SandboxFailed, "qemu process disappeared"
			}
			d.mu.Unlock()
			return
		}
	}
}

// ---- base image -----------------------------------------------------------------

func imageName() string {
	arch := "amd64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	return ubuntuRelease + "-server-cloudimg-" + arch + ".img"
}

// EnsureBaseImage downloads and verifies the Ubuntu cloud image once.
func (d *Driver) EnsureBaseImage(ctx context.Context) (string, error) {
	path := filepath.Join(d.opt.DataDir, "images", imageName())
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	slog.Info("downloading base image (one time)", "image", imageName())
	sums, err := httpGet(ctx, imageBaseURL+"SHA256SUMS")
	if err != nil {
		return "", fmt.Errorf("fetch checksums: %w", err)
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == imageName() {
			want = f[0]
		}
	}
	if want == "" {
		return "", errors.New("image not listed in SHA256SUMS")
	}
	tmp := path + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, imageBaseURL+imageName(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		out.Close()
		return "", err
	}
	defer resp.Body.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), resp.Body); err != nil {
		out.Close()
		return "", err
	}
	out.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("image checksum mismatch: got %s want %s", got, want)
	}
	return path, os.Rename(tmp, path)
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// ---- provisioning --------------------------------------------------------------

func (d *Driver) Provision(ctx context.Context, spec sandbox.Spec) (sandbox.Status, error) {
	d.mu.Lock()
	if v, ok := d.boxes[spec.RentalID]; ok {
		d.mu.Unlock()
		return d.status(v), nil
	}
	v := &vm{dir: filepath.Join(d.opt.DataDir, "sandboxes", spec.RentalID), state: protocol.SandboxCreating,
		meta: meta{RentalID: spec.RentalID, SandboxID: "qemu-" + spec.RentalID[:8], CreatedAt: time.Now(), ExpiresAt: spec.ExpiresAt, Spec: spec}}
	d.boxes[spec.RentalID] = v
	d.mu.Unlock()

	fail := func(err error) (sandbox.Status, error) {
		v.state, v.err = protocol.SandboxFailed, err.Error()
		return d.status(v), err
	}
	base, err := d.EnsureBaseImage(ctx)
	if err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return fail(err)
	}
	// Agent key for the web-terminal / network supervisor path.
	agentPub, err := writeAgentKey(v.dir)
	if err != nil {
		return fail(err)
	}
	// Overlay disk.
	disk := filepath.Join(v.dir, "disk.qcow2")
	if out, err := exec.CommandContext(ctx, qemuImg(d.qemu), "create", "-f", "qcow2", "-F", "qcow2", "-b", base, disk,
		fmt.Sprintf("%dG", spec.DiskGB)).CombinedOutput(); err != nil {
		return fail(fmt.Errorf("qemu-img create: %v: %s", err, out))
	}
	// cloud-init seed.
	seed := filepath.Join(v.dir, "seed.iso")
	if err := writeSeedISO(seed, spec, agentPub); err != nil {
		return fail(err)
	}
	port, err := freePort()
	if err != nil {
		return fail(err)
	}
	v.meta.SSHPort = port

	args := d.qemuArgs(v, disk, seed, spec)
	cmd := exec.Command(d.qemu, args...)
	cmd.Dir = v.dir
	logf, _ := os.Create(filepath.Join(v.dir, "qemu.log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	lowerPriority(cmd)
	if err := cmd.Start(); err != nil {
		return fail(fmt.Errorf("start qemu: %w", err))
	}
	v.cmd = cmd
	v.meta.PID = cmd.Process.Pid
	_ = d.saveMeta(v)
	go func() {
		_ = cmd.Wait()
		d.mu.Lock()
		if cur := d.boxes[spec.RentalID]; cur == v && v.state != protocol.SandboxStopping {
			v.state, v.err = protocol.SandboxFailed, "qemu exited unexpectedly"
		}
		d.mu.Unlock()
	}()

	// Wait for sshd.
	if err := d.waitSSH(ctx, v, 5*time.Minute); err != nil {
		return fail(err)
	}
	v.state = protocol.SandboxRunning
	d.startNetwork(v)
	return d.status(v), nil
}

func qemuImg(qemuPath string) string {
	name := "qemu-img"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(qemuPath), name)
}

func (d *Driver) qemuArgs(v *vm, disk, seed string, spec sandbox.Spec) []string {
	cpu := "host"
	if d.accel == "whpx" || d.accel == "tcg" {
		cpu = "max"
	}
	args := []string{
		"-name", "rentpc-" + spec.RentalID[:8],
		"-accel", d.accel,
		"-cpu", cpu,
		"-smp", strconv.Itoa(spec.CPUs),
		"-m", strconv.FormatInt(spec.RAMMB, 10),
		"-nodefaults", "-nographic", "-monitor", "none",
		"-serial", "file:" + filepath.Join(v.dir, "serial.log"),
		"-drive", "file=" + disk + ",if=virtio,format=qcow2,cache=writeback,discard=unmap",
		"-drive", "file=" + seed + ",if=virtio,format=raw,readonly=on",
		"-device", "virtio-rng-pci",
		// Isolated user-mode network: nothing in or out except the explicit ssh hostfwd on loopback.
		"-netdev", fmt.Sprintf("user,id=n0,restrict=on,hostfwd=tcp:127.0.0.1:%d-:22", v.meta.SSHPort),
		"-device", "virtio-net-pci,netdev=n0",
	}
	if runtime.GOARCH == "arm64" {
		args = append(args, "-machine", "virt,highmem=on")
		if fw := findAArch64Firmware(d.qemu); fw != "" {
			args = append(args, "-bios", fw)
		}
	} else {
		args = append(args, "-machine", "q35")
	}
	return args
}

func findAArch64Firmware(qemuPath string) string {
	candidates := []string{
		filepath.Join(filepath.Dir(qemuPath), "..", "share", "qemu", "edk2-aarch64-code.fd"),
		filepath.Join(filepath.Dir(qemuPath), "share", "edk2-aarch64-code.fd"),
		"/opt/homebrew/share/qemu/edk2-aarch64-code.fd",
		"/usr/share/qemu/edk2-aarch64-code.fd",
		"/usr/share/AAVMF/AAVMF_CODE.fd",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func (d *Driver) saveMeta(v *vm) error {
	b, _ := json.MarshalIndent(v.meta, "", "  ")
	return os.WriteFile(filepath.Join(v.dir, "meta.json"), b, 0o600)
}

// waitSSH polls the hostfwd port until sshd answers with a banner.
func (d *Driver) waitSSH(ctx context.Context, v *vm, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if v.state == protocol.SandboxFailed {
			return errors.New(v.err)
		}
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", v.meta.SSHPort), 2*time.Second)
		if err == nil {
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, 4)
			n, _ := c.Read(buf)
			c.Close()
			if n >= 4 && string(buf[:4]) == "SSH-" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New("timed out waiting for the sandbox to boot (see serial.log)")
}

// ---- network supervisor: egress proxy via SSH remote forward ----------------------

func (d *Driver) startNetwork(v *vm) {
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	v.proxy = &netpolicy.Proxy{Policy: d.opt.Policy, Label: v.meta.RentalID}
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			err := d.serveProxyOnce(ctx, v)
			if ctx.Err() != nil {
				return
			}
			slog.Warn("sandbox egress link dropped; reconnecting", "rental", v.meta.RentalID, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
		}
	}()
}

func (d *Driver) serveProxyOnce(ctx context.Context, v *vm) error {
	signer, err := d.AgentSigner(v.meta.RentalID)
	if err != nil {
		return err
	}
	raw, err := d.Dial(ctx, v.meta.RentalID, 22)
	if err != nil {
		return err
	}
	cfg := &ssh.ClientConfig{User: "renter", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 15 * time.Second}
	cc, chans, reqs, err := ssh.NewClientConn(raw, "sandbox:22", cfg)
	if err != nil {
		raw.Close()
		return err
	}
	cl := ssh.NewClient(cc, chans, reqs)
	defer cl.Close()
	l, err := cl.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", guestProxyPort))
	if err != nil {
		return fmt.Errorf("remote forward: %w", err)
	}
	slog.Info("sandbox egress proxy up", "rental", v.meta.RentalID)
	return v.proxy.Serve(ctx, l)
}

// ---- status / dial / destroy -----------------------------------------------------

func (d *Driver) status(v *vm) sandbox.Status {
	st := sandbox.Status{ID: v.meta.SandboxID, RentalID: v.meta.RentalID, State: v.state, Error: v.err,
		CreatedAt: v.meta.CreatedAt, ExpiresAt: v.meta.ExpiresAt}
	if v.proxy != nil {
		st.NetTxB, st.NetRxB = v.proxy.TxBytes.Load(), v.proxy.RxBytes.Load()
	}
	return st
}

func (d *Driver) Status(ctx context.Context, rentalID string) (sandbox.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.boxes[rentalID]
	if !ok {
		return sandbox.Status{}, sandbox.ErrNotFound
	}
	return d.status(v), nil
}

func (d *Driver) List(ctx context.Context) ([]sandbox.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]sandbox.Status, 0, len(d.boxes))
	for _, v := range d.boxes {
		out = append(out, d.status(v))
	}
	return out, nil
}

func (d *Driver) Dial(ctx context.Context, rentalID string, port int) (net.Conn, error) {
	d.mu.Lock()
	v, ok := d.boxes[rentalID]
	d.mu.Unlock()
	if !ok {
		return nil, sandbox.ErrNotFound
	}
	if port != 22 {
		// Only sshd is forwarded. Other guest ports can be reached by the
		// renter through ssh -L; keeping a single hole keeps the model simple.
		return nil, fmt.Errorf("port %d is not forwarded; use ssh port forwarding", port)
	}
	var dl net.Dialer
	return dl.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", v.meta.SSHPort))
}

func (d *Driver) Destroy(ctx context.Context, rentalID string) error {
	d.mu.Lock()
	v, ok := d.boxes[rentalID]
	if ok {
		delete(d.boxes, rentalID)
	}
	d.mu.Unlock()
	if !ok {
		// Maybe a leftover directory without a live process.
		return os.RemoveAll(filepath.Join(d.opt.DataDir, "sandboxes", rentalID))
	}
	v.state = protocol.SandboxStopping
	if v.cancel != nil {
		v.cancel()
	}
	if v.cmd != nil && v.cmd.Process != nil {
		_ = v.cmd.Process.Kill()
	} else if v.meta.PID > 0 {
		if p, err := os.FindProcess(v.meta.PID); err == nil {
			_ = p.Kill()
		}
	}
	// Give the process a moment to release file handles (Windows), then shred.
	time.Sleep(500 * time.Millisecond)
	for _, f := range []string{"disk.qcow2", "seed.iso", "agent_key"} {
		_ = shred(filepath.Join(v.dir, f))
	}
	var last error
	for i := 0; i < 10; i++ {
		if last = os.RemoveAll(v.dir); last == nil {
			break
		}
		time.Sleep(time.Second)
	}
	return last
}

// shred overwrites the first few MB (where cloud-init data, keys and the
// most recent filesystem metadata live) before deletion. Full-disk
// overwrites would take minutes on large overlays; the overlay is deleted
// anyway and the host's disk is the owner's own.
func shred(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	zero := make([]byte, 1<<20)
	for i := 0; i < 8; i++ {
		if _, err := f.Write(zero); err != nil {
			break
		}
	}
	return f.Sync()
}

func (d *Driver) SetExpiry(ctx context.Context, rentalID string, at time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.boxes[rentalID]
	if !ok {
		return sandbox.ErrNotFound
	}
	v.meta.ExpiresAt = at
	return d.saveMeta(v)
}

// AgentSigner implements session.AgentKeyProvider.
func (d *Driver) AgentSigner(rentalID string) (ssh.Signer, error) {
	b, err := os.ReadFile(filepath.Join(d.opt.DataDir, "sandboxes", rentalID, "agent_key"))
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(b)
}

func writeAgentKey(dir string) (string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "rentpc agent")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "agent_key"), pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " rentpc-agent", nil
}

// ---- cloud-init -------------------------------------------------------------------

// writeSeedISO builds a NoCloud seed (volume label "cidata") in pure Go.
func writeSeedISO(path string, spec sandbox.Spec, agentPub string) error {
	w, err := iso9660.NewWriter()
	if err != nil {
		return err
	}
	defer w.Cleanup()
	userData := cloudConfig(spec, agentPub)
	metaData := "instance-id: rentpc-" + spec.RentalID + "\nlocal-hostname: sandbox\n"
	if err := w.AddFile(strings.NewReader(userData), "user-data"); err != nil {
		return err
	}
	if err := w.AddFile(strings.NewReader(metaData), "meta-data"); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return w.WriteTo(f, "cidata")
}

func cloudConfig(spec sandbox.Spec, agentPub string) string {
	proxy := fmt.Sprintf("http://127.0.0.1:%d", guestProxyPort)
	return fmt.Sprintf(`#cloud-config
hostname: sandbox
users:
  - name: %[1]s
    groups: [sudo]
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: true
    ssh_authorized_keys:
      - %[2]s
      - %[3]s
ssh_pwauth: false
disable_root: true
package_update: false
write_files:
  - path: /etc/profile.d/rentpc-proxy.sh
    permissions: "0644"
    content: |
      export http_proxy=%[4]s https_proxy=%[4]s HTTP_PROXY=%[4]s HTTPS_PROXY=%[4]s
      export no_proxy=localhost,127.0.0.1 NO_PROXY=localhost,127.0.0.1
  - path: /etc/environment
    permissions: "0644"
    content: |
      PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin"
      http_proxy=%[4]s
      https_proxy=%[4]s
      HTTP_PROXY=%[4]s
      HTTPS_PROXY=%[4]s
      no_proxy=localhost,127.0.0.1
  - path: /etc/systemd/system.conf.d/10-rentpc-proxy.conf
    permissions: "0644"
    content: |
      [Manager]
      DefaultEnvironment="http_proxy=%[4]s" "https_proxy=%[4]s" "HTTP_PROXY=%[4]s" "HTTPS_PROXY=%[4]s" "no_proxy=localhost,127.0.0.1"
  - path: /etc/apt/apt.conf.d/90rentpc-proxy
    permissions: "0644"
    content: |
      Acquire::http::Proxy "%[4]s";
      Acquire::https::Proxy "%[4]s";
  - path: /etc/motd
    permissions: "0644"
    content: |
      ────────────────────────────────────────────────────────────
       rentPC sandbox  ·  rental %[5]s
       Isolated VM. Internet access goes through the egress proxy
       (http_proxy is preset). Everything here is destroyed when
       the rental ends. Do not store anything you want to keep.
      ────────────────────────────────────────────────────────────
runcmd:
  - [ sh, -c, "systemctl daemon-reexec || true" ]
  - [ sh, -c, "systemctl disable --now snapd.service snapd.socket snapd.seeded.service 2>/dev/null || true" ]
  - [ sh, -c, "systemctl disable --now unattended-upgrades 2>/dev/null || true" ]
  - [ sh, -c, "growpart /dev/vda 1 2>/dev/null; resize2fs /dev/vda1 2>/dev/null || true" ]
`, spec.Username, spec.SSHPubKey, agentPub, proxy, spec.RentalID)
}
