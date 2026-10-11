# rentPC — Architecture

*Status: v0.2 — design + Phase 1/2 implementation. Last updated 2026-10-12. See `docs/STATUS.md` for what is built and tested.*

rentPC is a peer-to-peer compute marketplace: PC owners ("hosts") run a small
agent that lists their machine; renters (humans or AI agents) book it by the
hour and get an **isolated compute environment on that machine**, never the
host's own desktop, files, credentials, or LAN.

This document records the research behind the design, the decisions taken,
and the implementation roadmap. `SECURITY.md` holds the threat model.

---

## 1. Research summary: how do you safely rent a consumer PC?

### 1.1 Isolation technologies by host OS

| Host OS | Technology | Isolation | Works on consumer hardware? | Notes |
|---|---|---|---|---|
| Windows 10/11 (Home + Pro) | **QEMU + WHPX** (Windows Hypervisor Platform) | Hardware VM | Yes — WHPX is an optional feature on *all* client editions, incl. Home ([QEMU WHPX docs](https://qemu.readthedocs.io/en/master/system/whpx.html)) | Requires the "Windows Hypervisor Platform" feature. Known caveats: MMX/SSE MMIO access unsupported, legacy VGA slow (irrelevant for headless Linux guests). arm64 needs Win11 24H2 + May-2025 updates. |
| Windows 10/11 Pro/Enterprise | **Hyper-V** (PowerShell/WMI) | Hardware VM | Pro/Enterprise only | Native, fast, supports GPU-P (see 1.2). Not available on Home. Good *second* driver. |
| Windows | **Windows Sandbox** | Hardware VM (lightweight) | Pro/Enterprise only | Disposable Windows guest, shares host kernel image; no programmatic multi-instance API, no Linux. Not suitable. |
| Windows | **WSL2 / Docker Desktop containers** | Container *inside a shared utility VM* | Yes | Renter would share one VM with other tenants and with the host's own WSL distros; the utility VM can reach the host LAN and mounts `/mnt/c` by default. **Compute-only, weaker tier.** |
| macOS (Apple Silicon + Intel) | **Apple Virtualization.framework** via Go `vz` bindings | Hardware VM | Yes (macOS ≥ 11, practically ≥ 13) | Binary needs the `com.apple.security.virtualization` entitlement and codesigning ([lima-vm/vz](https://github.com/lima-vm/vz)). Fastest path on Mac; no GPU compute passthrough exists on macOS. |
| macOS | **QEMU + HVF** | Hardware VM | Yes | Slower than VZ but zero entitlement friction and shares our Windows/Linux driver code. |
| Linux | **QEMU/KVM**, Firecracker, Cloud Hypervisor | Hardware VM | Yes | Best-documented; VFIO GPU passthrough realistic on desktops with IOMMU. Firecracker is Linux/KVM only. |
| Linux | gVisor / Kata containers | Container w/ user-space kernel / microVM | Yes | Compute-only tier on Linux; Kata effectively *is* a VM. |
| Any | Plain Docker / OCI container | Kernel namespaces | Yes | **Not** VM-equivalent: shares host kernel, kernel exploits escape. Only acceptable for a clearly-labelled "compute-only, trusted-renter" tier. |

### 1.2 GPU access reality check

* **Hyper-V GPU-P (partitioning)** on Windows 11 Pro gives a *Windows* guest
  DX11/OpenGL and basic DirectCompute; DX12 and Vulkan are not supported in
  a supported way and Linux guests are effectively unsupported for compute
  ([MS Q&A](https://learn.microsoft.com/en-us/answers/questions/5605686/is-it-possible-to-add-support-for-directx-12-in-hy),
  [level1techs thread](https://forum.level1techs.com/t/gpu-paravirtualization-hyper-v-with-linux-guest/198336/4)).
  CUDA inside a GPU-P guest is not an officially supported configuration.
* **DDA (discrete device assignment)** needs Windows Server / specific
  hardware; consumer GeForce + laptop MUX designs mostly fail.
* **WSL2 CUDA** works well on consumer NVIDIA but is *not* a VM boundary we
  control per-tenant (shared utility VM, host LAN reachable).
* **Linux VFIO passthrough** is the only robust "VM gets the GPU" path, and
  needs IOMMU, a second GPU or headless host, and a desktop (laptops with
  hybrid graphics usually can't).
* **macOS**: no GPU passthrough into VMs at all.

**Conclusion:** GPU access must be modular and *opt-in per driver*. The
MVP is CPU-only in a real VM; GPU tiers land later as (a) Linux/KVM VFIO
(VM-grade) and (b) container-based CUDA (compute-only, explicitly weaker).
We will never relax VM isolation to make a GPU appear.

### 1.3 Networking / connectivity

* Hosts are behind NAT/CGNAT; no inbound port forwarding and the host's
  public IP must not be revealed to renters → **outbound-only tunnel from the
  agent to a relay**, renter traffic goes relay → agent → VM.
* Render supports long-lived WebSockets on the single public port, no max
  duration, needs ping/pong keepalives and reconnect-with-backoff; instances
  are replaced on deploy ([Render docs](https://render.com/docs/websocket)).
  This fits a WebSocket + yamux relay. (Multi-instance relay routing needs
  Redis pub/sub — noted in roadmap, single instance is fine for the hackathon.)
* Guest egress: QEMU's default slirp lets the guest reach the host (`10.0.2.2`)
  and the host's LAN, and slirp has no ACLs. gvproxy (gvisor-tap-vsock) was
  evaluated as a replacement — it is pure Go and supports QEMU/vfkit/Hyper-V —
  but its configuration has **no egress filter either** (checked
  `pkg/types/configuration.go`), so using it would mean patching the gVisor
  forwarder. The MVP therefore runs the guest with **`-netdev user,restrict=on`**:
  the guest has *no* network path at all except (a) the loopback `hostfwd` to
  guest:22 that the agent uses, and (b) a **policy-enforcing HTTP(S) egress
  proxy** that the agent runs and publishes inside the guest as
  `127.0.0.1:3128` through an SSH remote forward (the agent has its own key in
  the VM). cloud-init presets `http_proxy`/`https_proxy` system-wide and for
  apt, so pip, npm, Go modules, curl and HTTPS API calls work; raw sockets to
  the internet do not (documented). The proxy resolves names itself and
  refuses private, loopback, link-local/metadata ranges and every address
  bound on the host — including names that resolve to such addresses.
  gvproxy with a forked forwarder remains the path to *transparent* networking
  later.
* WireGuard/Tailscale were considered: great for ops, but they put the renter
  on a network with the host and need key distribution; the relay keeps the
  renter at arm's length and needs nothing but outbound HTTPS from the host.

### 1.4 Prior art checked

Multipass (Canonical) is now fully open source and drives Hyper-V/QEMU/VZ
VMs across all three OSes ([Multipass 1.16 notes](https://canonical.com/multipass/docs/latest/reference/release-notes/1.16.0/)).
It validates the "cloud-image + cloud-init + per-OS hypervisor driver" pattern
we adopt, but it is a developer tool (shared daemon, host mounts, no tenant
egress policy), so we build a purpose-made driver layer instead of shelling
out to it.

---

## 2. Decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | **Sandbox unit = per-rental Linux microVM** (Ubuntu cloud image, cloud-init, separate qcow2/raw overlay disk). Never the host OS session. | Only hardware virtualization gives the isolation the brief demands on all three OSes. |
| D2 | **MVP driver = QEMU** with the native accelerator (WHPX on Windows incl. Home, HVF on macOS, KVM on Linux). | One driver codebase, works on *both* of Kavish's machines, no Pro edition or entitlement friction. |
| D3 | Driver interface is pluggable: `qemu` (MVP) → `vz` (macOS native) → `hyperv` (Win Pro, GPU-P experiments) → `kvm-vfio` (Linux GPU) → `container` (compute-only tier, labelled weaker). | Modular GPU, no rewrites. |
| D4 | **Guest networking = `restrict=on` + policy-enforcing egress proxy** (agent-side, published into the guest over SSH remote forward); host LAN, host loopback and metadata ranges are unreachable; internet via proxy works for pip/npm/go/apt/HTTPS APIs. Transparent networking (patched gvproxy) is a later upgrade. | Satisfies "external API calls yes, host network no" with code we can unit-test today. |
| D5 | **Connectivity = relay**. Agent keeps an outbound WSS (yamux-multiplexed) to the backend; renters reach the VM through (a) a browser terminal (xterm.js) and (b) native `ssh` via a `ProxyCommand` CLI. | No inbound ports, host IP hidden, works from Render. |
| D6 | **Credentials**: backend generates an ed25519 keypair per rental; public key injected via cloud-init, private key shown once to the renter; the agent itself gets a per-session scoped token. | Temporary, revocable, per session. |
| D7 | **Backend = Go** (chi, pgx, coder/websocket), **Postgres** (Neon), **Redis** (presence, pub/sub). **Frontend = Next.js 15 / TS / Tailwind / shadcn**. **Agent = Go** (shares `pkg/protocol` with backend). | Brief + user preference; single language for agent & relay. |
| D8 | Reservation atomicity: `UPDATE machines SET status='reserved' WHERE id=$1 AND status='available'` + `rentals` row in one transaction; expiry via `reservation_expires_at` + a sweeper. | Prevents double booking without distributed locks. |
| D9 | Payments behind a `PaymentProvider` interface, `mock` implementation first; every money event is a ledger row. | Tempo/MPP/escrow later without touching rental logic. |
| D10 | GPU is CPU-only in MVP. `machines.gpu_*` are *detected and listed* so the marketplace is honest, but `listing.gpu_access` is `none` until a GPU-capable driver is active. | Don't compromise host security for GPU. |

### Recommended MVP host configuration

* **Windows 10 2004+ / Windows 11** (Home or Pro), x86_64, VT-x/AMD-V enabled
  in BIOS, "Windows Hypervisor Platform" feature on, ≥16 GB RAM, ≥40 GB free
  disk. (Kavish's primary dev box.)
* **macOS 13+** Apple Silicon or Intel (QEMU+HVF; VZ driver later). (Second
  machine — ideal as the *renter* for the end-to-end demo, or as a second host.)
* **Linux** (Ubuntu 22.04+/KVM) supported by the same driver; becomes the
  GPU-capable path later.

---

## 3. System overview

```
                ┌──────────────────────────────────────────────────────────┐
                │                    Backend (Go, Render)                   │
  Next.js web ──┤  REST API  │  Relay (WSS)  │  Scheduler/Sweeper  │ Ledger │
  (Vercel)      └─────┬───────────────┬────────────────┬───────────────────┘
  AI agents ──────────┘   Postgres (Neon)   Redis (presence, pub/sub)
  (REST / MCP)                        │
                                      │ outbound WSS + yamux (agent → relay)
                                      ▼
                   ┌──────────────────────────────────────────┐
                   │  Host agent (Go) on the owner's PC        │
                   │  hwdetect · heartbeat · sandbox driver    │
                   │  egress-policy proxy · session monitor    │
                   │   ┌──────────────────────────────────┐   │
                   │   │  Per-rental microVM (Ubuntu)      │   │
                   │   │  sshd · renter user · own disk    │   │
                   │   └──────────────────────────────────┘   │
                   └──────────────────────────────────────────┘
```

Renter connection paths (both through the relay, never direct to the host):

1. **Web terminal**: browser ⇄ WSS `/v1/rentals/{id}/terminal` ⇄ relay ⇄ yamux
   stream ⇄ agent, which opens an SSH PTY to the VM using the session key.
2. **Native SSH**: `ssh -o ProxyCommand="rentpc tunnel <rental-id>" renter@rentpc`
   — the CLI opens a WSS to `/v1/rentals/{id}/tunnel`, relay pipes raw TCP to
   agent → VM:22. Also works for `scp`, `rsync`, VS Code Remote-SSH.

---

## 4. Monorepo layout

```
rentpc/
├── apps/
│   ├── web/                 Next.js 15 + TS + Tailwind + shadcn/ui
│   └── api/                 Go backend (REST + relay + scheduler)
│       ├── cmd/api/         main
│       ├── internal/
│       │   ├── auth/        sessions, argon2id passwords, middleware
│       │   ├── db/          pgx pool, migrations runner, queries
│       │   ├── http/        chi router, handlers, DTOs
│       │   ├── machines/    hardware listings, search
│       │   ├── rentals/     reservation, lifecycle state machine, sweeper
│       │   ├── relay/       agent hub (WSS+yamux), renter tunnel & terminal
│       │   ├── payments/    PaymentProvider interface + mock, ledger
│       │   └── config/
│       └── migrations/      SQL, numbered
├── agents/
│   └── host/                Go host agent
│       ├── cmd/rentpc-host/ main (register, run, status)
│       └── internal/
│           ├── hwdetect/    CPU/GPU/RAM/disk/OS detection (gopsutil + per-OS)
│           ├── client/      outbound WSS + yamux, heartbeat, RPC
│           ├── sandbox/     Driver interface; drivers: mock, qemu (vz/hyperv later)
│           ├── netpolicy/   egress policy + HTTP(S) proxy served into the guest
│           └── session/     rental session supervisor, cleanup
├── packages/
│   ├── protocol/            Go: shared agent⇄backend message types (single source of truth)
│   └── ui-types/            TS: API DTO types (generated later via OpenAPI)
├── tools/
│   └── rentpc-cli/          Go: `rentpc tunnel`, `rentpc ssh` for renters
├── deploy/                  render.yaml, vercel notes, docker-compose.dev.yml
├── docs/                    ARCHITECTURE.md, SECURITY.md, ROADMAP.md, ADRs
├── go.work
└── pnpm-workspace.yaml
```

---

## 5. Data model (PostgreSQL)

```
users            id, email (unique), password_hash, display_name, role flags,
                 created_at
sessions         id (random 256-bit), user_id, expires_at, created_at, ua, ip
hosts            id, user_id, name, agent_token_hash, agent_version,
                 status (offline|online|busy|draining), last_heartbeat_at,
                 created_at
machines         id, host_id (1:1 for now), hostname, os_family, os_version,
                 arch, cpu_model, cpu_cores, cpu_threads, ram_mb,
                 gpu_vendor, gpu_model, gpu_vram_mb, disk_free_gb,
                 virt_capability jsonb  {whpx,hvf,kvm,vz,hyperv: bool},
                 country, region, detected_at
listings         id, machine_id, title, description, price_cents_per_hour,
                 currency, alloc_cpus, alloc_ram_mb, alloc_disk_gb,
                 gpu_access (none|container|vm_passthrough),
                 min_hours, max_hours, is_active, availability jsonb
rentals          id, listing_id, machine_id, renter_id, host_user_id,
                 status (reserved|provisioning|ready|active|extending|
                         terminating|completed|failed|cancelled|expired),
                 starts_at, ends_at, reservation_expires_at,
                 price_cents_per_hour (snapshot), hours_booked,
                 ssh_pubkey, ssh_privkey_enc (encrypted at rest, delivered once),
                 sandbox_id, failure_reason, created_at, updated_at
rental_events    id, rental_id, type, payload jsonb, at   (audit trail)
usage_samples    rental_id, at, cpu_pct, mem_mb, net_tx, net_rx
ledger_entries   id, rental_id, user_id, kind (charge|earning|fee|refund),
                 amount_cents, currency, provider, provider_ref, at
reviews          id, rental_id, author_id, target_user_id, rating, text
```

Indexes: `machines(status, gpu_model, ram_mb, price)` via listing join;
`rentals(machine_id) WHERE status IN (active-ish)`; `sessions(expires_at)`.

**Double-booking guard** (D8): a partial unique index
`CREATE UNIQUE INDEX one_live_rental_per_machine ON rentals(machine_id)
WHERE status IN ('reserved','provisioning','ready','active','extending','terminating')`
— the database is the lock.

---

## 6. Rental lifecycle (state machine)

```
reserved ──(agent ack provision)──► provisioning ──(sandbox ready)──► ready
   │ reservation_expires_at (5 min)       │ provision timeout / error
   ▼                                      ▼
expired                                 failed ──► machine released, refund
ready ──(renter first connects or 10 min)──► active
active ──(renter extend)──► extending ──► active (ends_at += n h)
active ──(ends_at reached | renter terminate | host offline > grace)──► terminating
terminating ──(agent reports destroyed)──► completed ──► ledger: earning + charge
```

Failure handling: every transition is a DB update guarded by the *from* state
(`WHERE status = $from`), events are appended to `rental_events`, and a sweeper
goroutine (every 15 s) handles timeouts: expired reservations, provisioning
that never acked, hosts whose heartbeat lapsed (grace 90 s, then the rental is
marked `failed` and prorated), and sessions past `ends_at`. On agent
reconnect the agent sends a `state_report` listing live sandboxes and the
backend reconciles: unknown sandboxes are destroyed, missing ones mark the
rental `failed`.

---

## 7. Agent ⇄ backend protocol (`packages/protocol`)

Transport: one outbound WSS per agent, authenticated with the host token;
inside it a **yamux** session. Stream 0 = control JSON-RPC; additional
streams are opened by the backend for renter tunnels/terminals.

Control messages (backend→agent): `provision{rental_id, spec, ssh_pubkey,
expires_at}`, `terminate{rental_id}`, `extend{rental_id, ends_at}`,
`open_tunnel{rental_id, kind: tcp|pty}`.
Agent→backend: `hello{agent_version, hardware, virt_capability}`,
`heartbeat{load, free_ram, sandboxes[]}`, `provision_result{ok, sandbox_id,
err}`, `sandbox_status{rental_id, state, usage}`, `terminate_result`.

---

## 8. Host agent architecture

```
cmd/rentpc-host
  register  --token <one-time token from web UI>   → stores host credentials
  run                                              → connects, serves rentals
  doctor                                           → checks virtualization, qemu, disk

internal/sandbox.Driver interface
  Capabilities() (gpu bool, maxCPUs, maxRAM)
  Provision(ctx, Spec) (Sandbox, error)   // base image → overlay disk → cloud-init seed → start VM
  Status(ctx, id) (Status, error)
  Dial(ctx, id, port) (net.Conn, error)   // reaches the guest through gvproxy, never host network
  Destroy(ctx, id) error                  // kill VM, shred overlay + seed, free ports
Drivers: mock (dev/CI), qemu (MVP), vz, hyperv, kvm-vfio, container (later)
```

The QEMU driver downloads a pinned Ubuntu cloud image once (checksum
verified), creates a per-rental qcow2 overlay, builds a cloud-init NoCloud seed
(renter user, authorized key, no password, `sudo` allowed *inside* the VM —
that is fine, the VM is disposable), and launches `qemu-system-x86_64 -accel
whpx|hvf|kvm -nodefaults -nographic -smp N -m M -drive overlay -drive seed
-netdev user,restrict=on,hostfwd=tcp:127.0.0.1:P-:22`. Resource limits are the VM's own (`-smp/-m`) plus
host-side process priority. The guest sees only its virtual disk; nothing is
mounted from the host (no 9p/virtiofs in rental VMs).

---

## 9. Frontend (Next.js)

Routes: `/` marketplace browse + filters · `/machines/[id]` detail + rent ·
`/rentals/[id]` session page (status, timer, cost, web terminal, SSH
instructions, extend/terminate) · `/host` host dashboard (machines, earnings,
agent setup token) · `/host/listings/[id]` edit listing · `/login`, `/register`.
Auth via httpOnly session cookie; API calls through a thin typed client in
`lib/api.ts`. Server components for listing pages, client components for live
session state (poll + SSE later).

---

## 10. AI-agent access

Every renter action is a plain REST call with a bearer API key
(`api_keys` table, scoped to a user) so an agent can `GET /v1/machines?gpu=…`,
`POST /v1/rentals`, `POST /v1/rentals/{id}/exec` (runs a command over the same
PTY path, returns output) and `DELETE`. An MCP server (`tools/rentpc-mcp`,
Phase 4) wraps those endpoints; the payment-authorization hook for agents is
the `PaymentProvider.Authorize(agentKey, maxSpend)` interface in Phase 5.

---

## 11. Web3 seams (not implemented yet)

* `payments.Provider` — `Quote`, `Authorize`, `Capture`, `Refund`, `Settle`;
  `mock` now, Tempo/MPP later.
* `identity.Verifier` — session cookie now; wallet-signature login later.
* `receipts.Signer` — usage samples are hashed per rental; a signed receipt
  (host key + backend key) can be anchored on-chain later.
* `reputation` — reviews + uptime today; portable DID-based later.

Rental infrastructure never imports these packages' concrete implementations.

---

## 12. Roadmap with testable milestones

| Phase | Milestone | Test |
|---|---|---|
| 1a | Backend boots with Postgres/Redis, migrations, auth | `go test ./...`, curl register/login/me |
| 1b | Host registration, machine upsert, listing CRUD, search | integration test with the **mock agent** |
| 1c | Atomic reservation + lifecycle + sweeper | concurrent-booking test (100 goroutines, 1 success) |
| 1d | Next.js browse/detail/dashboards against the API | manual + Playwright smoke |
| 2a | Agent: hwdetect, register, heartbeat, reconnect | run on Windows + Mac, hosts show online |
| 2b | Relay: yamux tunnels, web terminal with **mock driver** (echo PTY) | terminal round-trip from the browser |
| 2c | QEMU driver on Windows (WHPX) and macOS (HVF): boot, cloud-init key, ssh | `rentpc-host doctor` + provision test |
| 2d | Egress policy | from guest: `curl https://pypi.org` ok (via proxy), `curl 192.168.x.1`, `10.0.2.2`, `169.254.169.254` all refused; unit tests in `agents/host/internal/netpolicy` |
| 3 | **End-to-end real rental**: Mac rents Windows PC, runs Python/Go, pip install, API calls; host files unreachable | demo script + checklist in `docs/DEMO.md` |
| 4 | Mock billing, ledger, API keys, MCP server | agent books & runs a job via MCP |
| 5 | Tempo/MPP, wallet auth, signed receipts | — |

---

## 13. Known limitations and tradeoffs

* QEMU must be installed on the host (we bundle a pinned build in the
  installer later; for now `doctor` checks `PATH`).
* WHPX is slower than Hyper-V proper and has MMIO quirks; acceptable for
  headless Linux guests. Hyper-V driver on Pro editions is the upgrade.
* No GPU in MVP (by design). Listing GPU specs sets expectations for later tiers.
* Relay bandwidth passes through the backend; fine for terminals/SSH, not for
  bulk data — a direct NAT-traversal (WebRTC/QUIC) path is a later optimisation.
* Single relay instance on Render keeps routing trivial; horizontal scaling
  needs Redis-routed streams.
* A malicious *host* can always inspect the VM's memory; see `SECURITY.md` for
  why we treat hosts as untrusted for confidentiality and how reputation,
  no-secrets guidance and (later) attestation mitigate it.
