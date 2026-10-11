// Package protocol is the single source of truth for messages exchanged
// between the rentPC backend and host agents, and for the shared domain
// enums used by both sides.
//
// Transport: the agent opens ONE outbound WebSocket to the backend
// (/v1/agent/connect) authenticated with its host token. Inside it a yamux
// session is established with the agent as the yamux *client* and the
// backend as the *server* so that the backend can open additional streams
// towards the agent (renter tunnels, terminals) without the agent ever
// listening on a port.
//
// Stream 0 (the first stream, opened by the agent) carries newline-delimited
// JSON control messages (Envelope). Every further stream is opened by the
// backend and starts with a single JSON StreamHeader line, followed by raw
// bytes.
package protocol

import "time"

// Version of the wire protocol. Bump on incompatible changes; the backend
// refuses agents whose ProtocolVersion is lower than its minimum.
const Version = 1

// ---- Domain enums ---------------------------------------------------------

type HostStatus string

const (
	HostOffline  HostStatus = "offline"
	HostOnline   HostStatus = "online"
	HostBusy     HostStatus = "busy"
	HostDraining HostStatus = "draining"
)

type RentalStatus string

const (
	RentalReserved     RentalStatus = "reserved"
	RentalProvisioning RentalStatus = "provisioning"
	RentalReady        RentalStatus = "ready"
	RentalActive       RentalStatus = "active"
	RentalExtending    RentalStatus = "extending"
	RentalTerminating  RentalStatus = "terminating"
	RentalCompleted    RentalStatus = "completed"
	RentalFailed       RentalStatus = "failed"
	RentalCancelled    RentalStatus = "cancelled"
	RentalExpired      RentalStatus = "expired"
)

// Live reports whether a rental in this status occupies the machine.
func (s RentalStatus) Live() bool {
	switch s {
	case RentalReserved, RentalProvisioning, RentalReady, RentalActive, RentalExtending, RentalTerminating:
		return true
	}
	return false
}

type GPUAccess string

const (
	GPUNone          GPUAccess = "none"
	GPUContainer     GPUAccess = "container"      // compute-only, reduced isolation
	GPUVMPassthrough GPUAccess = "vm_passthrough" // VFIO / DDA
)

type SandboxState string

const (
	SandboxCreating SandboxState = "creating"
	SandboxRunning  SandboxState = "running"
	SandboxStopping SandboxState = "stopping"
	SandboxStopped  SandboxState = "stopped"
	SandboxFailed   SandboxState = "failed"
)

// ---- Hardware ------------------------------------------------------------

type Hardware struct {
	Hostname   string `json:"hostname"`
	OSFamily   string `json:"os_family"` // windows | darwin | linux
	OSVersion  string `json:"os_version"`
	Arch       string `json:"arch"` // amd64 | arm64
	CPUModel   string `json:"cpu_model"`
	CPUCores   int    `json:"cpu_cores"`
	CPUThreads int    `json:"cpu_threads"`
	RAMMB      int64  `json:"ram_mb"`
	DiskFreeGB int64  `json:"disk_free_gb"`
	GPUs       []GPU  `json:"gpus"`
	// Virt describes which hypervisor accelerators the agent believes are
	// usable on this host. The backend uses it to decide which drivers and
	// GPU tiers may ever be offered.
	Virt VirtCapability `json:"virt"`
}

type GPU struct {
	Vendor string `json:"vendor"`
	Model  string `json:"model"`
	VRAMMB int64  `json:"vram_mb"`
}

type VirtCapability struct {
	WHPX   bool `json:"whpx"`   // Windows Hypervisor Platform
	HyperV bool `json:"hyperv"` // Hyper-V management available (Pro/Ent)
	HVF    bool `json:"hvf"`    // macOS Hypervisor.framework
	VZ     bool `json:"vz"`     // macOS Virtualization.framework
	KVM    bool `json:"kvm"`    // /dev/kvm
	QEMU   bool `json:"qemu"`   // qemu-system binary found
}

// ---- Control envelope -----------------------------------------------------

// Envelope wraps every control message on stream 0.
type Envelope struct {
	Type string `json:"type"`
	// ID correlates requests with results (set by the sender of a request,
	// echoed by the responder).
	ID      string `json:"id,omitempty"`
	Payload any    `json:"payload,omitempty"`
}

// Message type names. Agent → backend.
const (
	TypeHello           = "hello"
	TypeHeartbeat       = "heartbeat"
	TypeProvisionResult = "provision_result"
	TypeTerminateResult = "terminate_result"
	TypeSandboxStatus   = "sandbox_status"
	TypeStateReport     = "state_report"
)

// Backend → agent.
const (
	TypeWelcome   = "welcome"
	TypeProvision = "provision"
	TypeTerminate = "terminate"
	TypeExtend    = "extend"
	TypeError     = "error"
)

type Hello struct {
	ProtocolVersion int      `json:"protocol_version"`
	AgentVersion    string   `json:"agent_version"`
	Hardware        Hardware `json:"hardware"`
	Driver          string   `json:"driver"` // mock | qemu | vz | hyperv | ...
}

type Welcome struct {
	HostID         string    `json:"host_id"`
	ServerTime     time.Time `json:"server_time"`
	HeartbeatEvery int       `json:"heartbeat_every_sec"`
}

type Heartbeat struct {
	Load1     float64         `json:"load1"`
	FreeRAMMB int64           `json:"free_ram_mb"`
	OnBattery bool            `json:"on_battery"`
	Sandboxes []SandboxStatus `json:"sandboxes"`
}

// SandboxSpec is what the backend asks the agent to create.
type SandboxSpec struct {
	RentalID  string    `json:"rental_id"`
	CPUs      int       `json:"cpus"`
	RAMMB     int64     `json:"ram_mb"`
	DiskGB    int64     `json:"disk_gb"`
	GPU       GPUAccess `json:"gpu"`
	SSHPubKey string    `json:"ssh_pubkey"`
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ProvisionResult struct {
	RentalID  string `json:"rental_id"`
	OK        bool   `json:"ok"`
	SandboxID string `json:"sandbox_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

type TerminateRequest struct {
	RentalID string `json:"rental_id"`
	Reason   string `json:"reason"`
}

type TerminateResult struct {
	RentalID string `json:"rental_id"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

type ExtendRequest struct {
	RentalID  string    `json:"rental_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type SandboxStatus struct {
	RentalID  string       `json:"rental_id"`
	SandboxID string       `json:"sandbox_id"`
	State     SandboxState `json:"state"`
	CPUPct    float64      `json:"cpu_pct"`
	MemMB     int64        `json:"mem_mb"`
	NetTxB    int64        `json:"net_tx_bytes"`
	NetRxB    int64        `json:"net_rx_bytes"`
	Error     string       `json:"error,omitempty"`
}

// StateReport is sent by the agent right after (re)connecting so the backend
// can reconcile rentals with the sandboxes that actually exist on the host.
type StateReport struct {
	Sandboxes []SandboxStatus `json:"sandboxes"`
}

type ErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ---- Data streams ---------------------------------------------------------

// StreamKind selects what the agent attaches a backend-opened stream to.
type StreamKind string

const (
	// StreamTCP pipes raw bytes to guest:Port (default 22). Used for native
	// ssh/scp through the renter CLI's ProxyCommand.
	StreamTCP StreamKind = "tcp"
	// StreamPTY makes the agent open an SSH session with a PTY inside the
	// guest and pipe it; used by the browser terminal. After the header and
	// ack, the stream carries records framed by WriteFrame/ReadFrame so that
	// resize requests can travel in-band (see frame.go).
	StreamPTY StreamKind = "pty"
)

// StreamHeader is the first line (JSON + '\n') of every backend-opened stream.
type StreamHeader struct {
	RentalID string     `json:"rental_id"`
	Kind     StreamKind `json:"kind"`
	Port     int        `json:"port,omitempty"`
	Cols     int        `json:"cols,omitempty"`
	Rows     int        `json:"rows,omitempty"`
}

// StreamAck is the agent's one-line reply before raw data begins.
type StreamAck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// PTYResize is sent by the browser terminal over its websocket as a text
// frame of the form {"type":"resize","cols":..,"rows":..}; the relay
// forwards it to the agent as a FrameResize record.
type PTYResize struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}
