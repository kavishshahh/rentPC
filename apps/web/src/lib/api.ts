// Typed client for the rentPC backend. Works in both server components
// (cookie forwarded explicitly) and client components (credentials: include).

export const API_URL = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";

export type User = { id: string; email: string; display_name: string; created_at: string };

export type Virt = { whpx: boolean; hyperv: boolean; hvf: boolean; vz: boolean; kvm: boolean; qemu: boolean };

export type Machine = {
  id: string; host_id: string; hostname: string; os_family: string; os_version: string; arch: string;
  cpu_model: string; cpu_cores: number; cpu_threads: number; ram_mb: number;
  gpu_vendor: string; gpu_model: string; gpu_vram_mb: number; disk_free_gb: number;
  virt: Virt; country: string; region: string; detected_at: string;
};

export type Listing = {
  id: string; machine_id: string; title: string; description: string; price_cents_per_hour: number; currency: string;
  alloc_cpus: number; alloc_ram_mb: number; alloc_disk_gb: number; gpu_access: "none" | "container" | "vm_passthrough";
  min_hours: number; max_hours: number; is_active: boolean; updated_at: string;
};

export type Host = {
  id: string; user_id: string; name: string; platform: "windows" | "darwin" | "linux"; registered: boolean; agent_version: string | null; driver: string | null;
  status: string; online: boolean; last_heartbeat_at: string | null; created_at: string;
  machine?: Machine | null; listing?: Listing | null;
};

export type MarketItem = {
  listing: Listing; machine: Machine; host_id: string; host_name: string; owner_name: string;
  online: boolean; available: boolean; rating: number | null; completed_rentals: number;
};

export type RentalStatus =
  | "reserved" | "provisioning" | "ready" | "active" | "extending" | "terminating"
  | "completed" | "failed" | "cancelled" | "expired";

export type Rental = {
  id: string; listing_id: string; machine_id: string; host_id: string; renter_id: string; host_user_id: string;
  status: RentalStatus; hours_booked: number; price_cents_per_hour: number; currency: string;
  cpus: number; ram_mb: number; disk_gb: number; gpu_access: string;
  reservation_expires_at: string; starts_at: string | null; ends_at: string | null;
  ssh_username: string; ssh_pubkey: string; sandbox_id: string | null; failure_reason: string | null;
  created_at: string; updated_at: string; host_online: boolean; estimated_cost_cents: number; machine_title?: string;
};

export type Earnings = { total_cents: number; last_30_days_cents: number; completed_rentals: number; hours_rented: number };

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

type Opts = { method?: string; body?: unknown; cookie?: string; cache?: RequestCache };

export async function api<T>(path: string, opts: Opts = {}): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (opts.cookie) headers["Cookie"] = opts.cookie;
  const res = await fetch(API_URL + path, {
    method: opts.method ?? "GET",
    headers,
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
    credentials: "include",
    cache: opts.cache ?? "no-store",
  });
  const text = await res.text();
  let data: unknown = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* non-json */ }
  if (!res.ok) {
    const msg = (data as { error?: string } | null)?.error ?? res.statusText;
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export const LIVE: RentalStatus[] = ["reserved", "provisioning", "ready", "active", "extending", "terminating"];
export const isLive = (s: RentalStatus) => LIVE.includes(s);
