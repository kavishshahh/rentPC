-- rentPC initial schema. See docs/ARCHITECTURE.md §5.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         citext UNIQUE NOT NULL,
    password_hash text NOT NULL,
    display_name  text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id         text PRIMARY KEY,               -- sha256(token), hex
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    user_agent text,
    ip         text
);
CREATE INDEX sessions_expires_idx ON sessions(expires_at);

CREATE TABLE api_keys (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_hash   text UNIQUE NOT NULL,
    label      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used  timestamptz
);

CREATE TABLE hosts (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name               text NOT NULL,
    agent_token_hash   text UNIQUE,            -- null until the agent registers
    register_token_hash text UNIQUE,           -- one-time token shown in UI
    register_expires_at timestamptz,
    agent_version      text,
    driver             text,
    status             text NOT NULL DEFAULT 'offline',
    last_heartbeat_at  timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX hosts_user_idx ON hosts(user_id);

CREATE TABLE machines (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    host_id       uuid UNIQUE NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    hostname      text NOT NULL,
    os_family     text NOT NULL,
    os_version    text NOT NULL DEFAULT '',
    arch          text NOT NULL DEFAULT '',
    cpu_model     text NOT NULL DEFAULT '',
    cpu_cores     int  NOT NULL DEFAULT 0,
    cpu_threads   int  NOT NULL DEFAULT 0,
    ram_mb        bigint NOT NULL DEFAULT 0,
    gpu_vendor    text NOT NULL DEFAULT '',
    gpu_model     text NOT NULL DEFAULT '',
    gpu_vram_mb   bigint NOT NULL DEFAULT 0,
    disk_free_gb  bigint NOT NULL DEFAULT 0,
    virt          jsonb NOT NULL DEFAULT '{}'::jsonb,
    country       text NOT NULL DEFAULT '',
    region        text NOT NULL DEFAULT '',
    detected_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE listings (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id           uuid UNIQUE NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    title                text NOT NULL,
    description          text NOT NULL DEFAULT '',
    price_cents_per_hour int  NOT NULL CHECK (price_cents_per_hour >= 0),
    currency             text NOT NULL DEFAULT 'USD',
    alloc_cpus           int  NOT NULL CHECK (alloc_cpus > 0),
    alloc_ram_mb         bigint NOT NULL CHECK (alloc_ram_mb > 0),
    alloc_disk_gb        bigint NOT NULL CHECK (alloc_disk_gb > 0),
    gpu_access           text NOT NULL DEFAULT 'none',
    min_hours            int  NOT NULL DEFAULT 1,
    max_hours            int  NOT NULL DEFAULT 24,
    is_active            boolean NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE rentals (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    listing_id             uuid NOT NULL REFERENCES listings(id),
    machine_id             uuid NOT NULL REFERENCES machines(id),
    host_id                uuid NOT NULL REFERENCES hosts(id),
    renter_id              uuid NOT NULL REFERENCES users(id),
    host_user_id           uuid NOT NULL REFERENCES users(id),
    status                 text NOT NULL,
    hours_booked           int  NOT NULL,
    price_cents_per_hour   int  NOT NULL,
    currency               text NOT NULL,
    cpus                   int  NOT NULL,
    ram_mb                 bigint NOT NULL,
    disk_gb                bigint NOT NULL,
    gpu_access             text NOT NULL DEFAULT 'none',
    reservation_expires_at timestamptz NOT NULL,
    starts_at              timestamptz,
    ends_at                timestamptz,
    ssh_username           text NOT NULL DEFAULT 'renter',
    ssh_pubkey             text NOT NULL,
    ssh_privkey            text,                -- delivered once then nulled
    sandbox_id             text,
    failure_reason         text,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);
-- The database is the lock: at most one live rental per machine.
CREATE UNIQUE INDEX one_live_rental_per_machine ON rentals(machine_id)
    WHERE status IN ('reserved','provisioning','ready','active','extending','terminating');
CREATE INDEX rentals_renter_idx ON rentals(renter_id, created_at DESC);
CREATE INDEX rentals_host_idx   ON rentals(host_id, created_at DESC);
CREATE INDEX rentals_status_idx ON rentals(status);

CREATE TABLE rental_events (
    id        bigserial PRIMARY KEY,
    rental_id uuid NOT NULL REFERENCES rentals(id) ON DELETE CASCADE,
    type      text NOT NULL,
    payload   jsonb NOT NULL DEFAULT '{}'::jsonb,
    at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX rental_events_rental_idx ON rental_events(rental_id, at);

CREATE TABLE usage_samples (
    rental_id uuid NOT NULL REFERENCES rentals(id) ON DELETE CASCADE,
    at        timestamptz NOT NULL DEFAULT now(),
    cpu_pct   real NOT NULL DEFAULT 0,
    mem_mb    bigint NOT NULL DEFAULT 0,
    net_tx    bigint NOT NULL DEFAULT 0,
    net_rx    bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (rental_id, at)
);

CREATE TABLE ledger_entries (
    id           bigserial PRIMARY KEY,
    rental_id    uuid REFERENCES rentals(id),
    user_id      uuid NOT NULL REFERENCES users(id),
    kind         text NOT NULL,   -- charge | earning | fee | refund
    amount_cents bigint NOT NULL,
    currency     text NOT NULL,
    provider     text NOT NULL,
    provider_ref text,
    at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ledger_user_idx ON ledger_entries(user_id, at DESC);

CREATE TABLE reviews (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    rental_id      uuid UNIQUE NOT NULL REFERENCES rentals(id),
    author_id      uuid NOT NULL REFERENCES users(id),
    target_user_id uuid NOT NULL REFERENCES users(id),
    rating         int NOT NULL CHECK (rating BETWEEN 1 AND 5),
    text           text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now()
);
