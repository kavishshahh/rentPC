# rentPC

Peer-to-peer compute marketplace: owners list idle PCs, renters get an
**isolated VM** on them by the hour. Built for the Colosseum Web3 hackathon;
Web2 core first, Web3 behind clean interfaces (see `docs/ARCHITECTURE.md`).

```
apps/api          Go backend: REST, agent relay (WSS+yamux), lifecycle sweeper, mock payments
apps/web          Next.js 16 + TypeScript + Tailwind v4 + shadcn-style UI
agents/host       Go host agent: hwdetect, registration, heartbeat, sandbox drivers (qemu, mock)
tools/rentpc-cli  Renter CLI: ssh ProxyCommand tunnel + console terminal
services/paygate  MPP (Tempo) payment gateway: HTTP 402 → stablecoin → rental (docs/WEB3.md)
packages/protocol Shared agent⇄backend wire types
docs/             ARCHITECTURE.md · SECURITY.md · STATUS.md · HOST-SETUP.md · DEMO.md
deploy/           render.yaml, Dockerfile, docker-compose for local Postgres/Redis
```

## Quick start (local, mock driver — no hypervisor needed)

```bash
# 1. Postgres
docker compose -f deploy/docker-compose.dev.yml up -d        # or any Postgres 15+

# 2. API (migrations run automatically)
cd apps/api
DATABASE_URL=postgres://rentpc:rentpc@localhost:5432/rentpc?sslmode=disable DEV_MODE=true go run ./cmd/api

# 3. Web
cd apps/web && pnpm install && pnpm dev                       # http://localhost:3000

# 4. Host agent (another terminal). Create an account → Host → Add a machine → copy the register command:
cd agents/host
go run ./cmd/rentpc-host register --server http://localhost:8080 --token <token>
go run ./cmd/rentpc-host run --driver mock                    # or --driver qemu for real VMs (see docs/HOST-SETUP.md)
```

Then set a price on the host page, sign in as a *different* user, and rent
it. The rental page opens a browser terminal into the sandbox.

> Before the first build run `go mod tidy` in each Go module (`apps/api`,
> `agents/host`, `tools/rentpc-cli`) to refresh `go.sum` for your machine.

## Tests

```bash
cd agents/host && go test ./...                                           # egress policy
cd apps/api && TEST_DATABASE_URL=postgres://... go test ./...             # 50-way reservation race
```

## Deploy

* **API → Render**: `deploy/render.yaml` (web service from `deploy/Dockerfile.api`).
  Env: `DATABASE_URL` (Neon, with `sslmode=require`), `PUBLIC_URL`
  (`https://<service>.onrender.com`), `WEB_ORIGINS` (`https://<app>.vercel.app`),
  `COOKIE_SECURE=true`. Keep **one instance** — the relay is in-process.
* **Web → Vercel**: root directory `apps/web`, env `NEXT_PUBLIC_API_URL=https://<service>.onrender.com`.
* **DB → Neon**: create a database, paste its URL. Migrations run at API start.

## Status

See `docs/STATUS.md` for what is implemented, tested, and next.
