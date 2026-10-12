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

## Not yet verified (needs real hardware)

* QEMU driver boot on **Windows + WHPX** and **macOS + HVF** — code paths are
  written but no VM has been booted yet. Expect small fixes (QEMU flags,
  firmware path on Apple Silicon, Windows path quoting). Follow
  `docs/HOST-SETUP.md`.
* cloud-init proxy env inside the guest, `growpart` of the overlay.
* Egress policy from inside a real guest (unit-tested on the host side).

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
