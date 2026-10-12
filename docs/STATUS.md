# Status — 2026-10-12

## Implemented and tested (in a Linux sandbox, mock driver)

| Area | What works | How it was verified |
|---|---|---|
| Backend | Register/login (argon2id, server-side sessions, API keys), hosts + one-time registration tokens, hardware upsert from agent hello, listings with allocation clamps, marketplace search/filter/sort, atomic reservation, lifecycle state machine with guarded transitions and event log, sweeper (expired reservations, provisioning timeout, end-of-rental, hung terminations, heartbeat lapse), mock payments + ledger, host earnings, agent relay (WSS + yamux, control stream, backend-opened data streams), browser-terminal WS, raw SSH tunnel WS | `go vet`, HTTP smoke run, 50-goroutine double-booking test (`internal/rentals/reserve_test.go`) |
| Host agent | hwdetect (CPU/RAM/disk/GPU/accelerators on Windows, macOS, Linux), register/run/doctor, outbound client with reconnect + heartbeat + state report, session supervisor (provision/terminate/extend, PTY and TCP streams, local expiry enforcer), **mock driver**, **QEMU driver** (base-image download with SHA256 check, qcow2 overlay, pure-Go cloud-init ISO, per-sandbox agent key, `restrict=on` networking, egress proxy over SSH remote forward, crash recovery via meta.json, shred + delete on destroy), egress policy proxy | `go test ./...` (policy + proxy denial tests), full rental loop with mock driver |
| Renter CLI | `rentpc tunnel` (ProxyCommand), `rentpc term`, `rentpc ssh` | Used to drive the PTY path in the smoke run |
| Web | Marketplace with filters, machine detail + rent, rental page with live xterm.js terminal, extend/terminate, one-time private key download, SSH instructions, my rentals, host dashboard (earnings, machines, rentals), host detail (agent setup command, detected hardware, listing editor, location), account/API keys | `next build`, Playwright run through the whole flow with screenshots |

## Added 2026-10-12 (evening)

* Hosts choose a platform (Windows / macOS / Linux) when adding a machine; the host page shows platform-specific prerequisites and a copy-paste download → register → run script using prebuilt binaries from GitHub Releases.
* `.github/workflows/release.yml` builds `rentpc-host` and `rentpc` for windows/amd64, darwin/arm64+amd64, linux/amd64+arm64 on every `v*` tag.
* Rental page SSH section: per-OS CLI download and connect commands (icacls on Windows).
* AWS single-box deployment (`deploy/aws`) live at http://44.222.123.73.

## Verified on real hardware (2026-10-12)

* **macOS (Apple M1, HVF, arm64 Ubuntu 24.04)**: rented from a Windows PC
  through the AWS relay. SSH via `rentpc tunnel` landed in the VM; disk
  resized to the listed 20 GB; `curl https://pypi.org` → 200 via the egress
  proxy; `10.0.2.2` (host) and `192.168.1.1` (LAN) refused by policy.
* Bugs found and fixed in the process: 60 s request timeout was cutting
  WebSocket sessions; `processAlive` on unix sent a nil signal so a
  restarted agent deleted live VMs; added sleep prevention (caffeinate /
  SetThreadExecutionState / systemd-inhibit) while rentals run.

## Not yet verified

* QEMU driver boot on **Windows + WHPX** (same code path; expected to work).
* `apt-get update` through the proxy inside the guest (interrupted by the
  timeout bug before it could finish).

## Known gaps / next steps (in order)

1. Milestone 2c/2d/3: boot a real VM on the Windows PC, rent it from the Mac, run the isolation checklist, write `docs/DEMO.md`.
2. Rate limiting on `/v1/auth/login` and `/v1/auth/register`; request logging with secret redaction.
3. Host "prevent sleep while rented" + refuse rentals on battery (agent).
4. Usage samples from the QEMU driver (CPU/mem via QMP) — currently only proxy byte counters.
5. Reviews UI (table exists), host reliability score on cards.
6. MCP server for agents (`tools/rentpc-mcp`) over the existing REST + API keys.
7. Redis: presence + pub/sub so the relay can run on >1 instance.
8. VZ driver (macOS native), Hyper-V driver (Windows Pro), Linux VFIO GPU tier.
9. Web3 seams: `payments.Provider` for Tempo/MPP, wallet login, signed receipts.

## Dev notes

* Go modules were fetched from GitHub mirrors in the build sandbox; the
  shipped `go.sum` files may need `go mod tidy` once on your machine.
* `apps/web` uses Next 16 / Tailwind v4; shadcn components are hand-written
  under `src/components/ui` (CLI registry was unreachable), compatible with
  `components.json` for adding more via `pnpm dlx shadcn add <name>`.
