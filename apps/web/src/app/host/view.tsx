"use client";

import * as React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Laptop, Monitor, Plus, Server, Wallet } from "lucide-react";
import { api, isLive, type Earnings, type Host, type Rental } from "@/lib/api";
import { gb, money, timeLeft } from "@/lib/utils";
import { useAuth } from "@/components/auth-provider";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { PLATFORMS, detectPlatform, platformLabel, type Platform } from "@/lib/releases";
import { osLabel } from "@/components/machine-card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export default function HostDashboard() {
  const { user, loading, refresh } = useAuth();
  const router = useRouter();
  const [payout, setPayout] = React.useState("");
  const [payoutMsg, setPayoutMsg] = React.useState<string | null>(null);
  React.useEffect(() => { setPayout(user?.payout_address ?? ""); }, [user?.payout_address]);
  async function savePayout() {
    setPayoutMsg(null);
    try {
      await api("/v1/account/payout", { method: "PUT", body: { address: payout.trim() } });
      await refresh();
      setPayoutMsg(payout.trim() ? "Saved. Renters now pay your wallet directly." : "Cleared. Earnings accrue to your balance.");
    } catch (e) {
      setPayoutMsg(e instanceof Error ? e.message : String(e));
    }
  }
  const [hosts, setHosts] = React.useState<Host[] | null>(null);
  const [earnings, setEarnings] = React.useState<Earnings | null>(null);
  const [rentals, setRentals] = React.useState<Rental[]>([]);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!loading && !user) router.push("/login?next=/host");
  }, [loading, user, router]);

  const load = React.useCallback(() => {
    api<{ hosts: Host[] }>("/v1/hosts").then((r) => setHosts(r.hosts)).catch(() => {});
    api<{ earnings: Earnings }>("/v1/host/earnings").then((r) => setEarnings(r.earnings)).catch(() => {});
    api<{ rentals: Rental[] }>("/v1/host/rentals").then((r) => setRentals(r.rentals)).catch(() => {});
  }, []);

  React.useEffect(() => {
    if (!user) return;
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [user, load]);

  const [picking, setPicking] = React.useState(false);
  const [platform, setPlatform] = React.useState<Platform>("windows");
  const [newName, setNewName] = React.useState("");
  React.useEffect(() => { setPlatform(detectPlatform()); }, []);

  async function addHost() {
    setBusy(true);
    try {
      const r = await api<{ host: Host; register_token: string; server: string }>("/v1/hosts", { method: "POST", body: { name: newName, platform } });
      try { sessionStorage.setItem(`rentpc_reg_${r.host.id}`, JSON.stringify({ token: r.register_token, server: r.server })); } catch {}
      router.push(`/host/${r.host.id}`);
    } finally {
      setBusy(false);
      setPicking(false);
    }
  }

  if (!user) return null;
  const live = rentals.filter((r) => isLive(r.status));

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">Host dashboard</h1>
        <Button onClick={() => setPicking(true)} disabled={busy}><Plus /> Add a machine</Button>
      </div>

      {picking && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">What does this machine run?</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-3">
              {PLATFORMS.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => setPlatform(p.id)}
                  className={cn("rounded-lg border p-4 text-left transition hover:bg-accent", platform === p.id && "border-primary ring-1 ring-primary")}
                >
                  <div className="flex items-center gap-2 font-medium [&_svg]:size-4">
                    {p.id === "windows" ? <Monitor /> : p.id === "darwin" ? <Laptop /> : <Server />} {p.label}
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">{p.blurb}</p>
                </button>
              ))}
            </div>
            <div className="flex flex-wrap items-end gap-3">
              <div className="space-y-1.5">
                <label className="text-sm font-medium">Name (optional)</label>
                <Input placeholder={platform === "darwin" ? "MacBook Pro" : platform === "windows" ? "Gaming PC" : "Home server"} value={newName} onChange={(e) => setNewName(e.target.value)} className="w-64" />
              </div>
              <Button onClick={addHost} disabled={busy}>{busy ? "Creating…" : `Continue with ${platformLabel(platform)}`}</Button>
              <Button variant="ghost" onClick={() => setPicking(false)}>Cancel</Button>
            </div>
            <p className="text-xs text-muted-foreground">GPU access is CPU-only on every platform in this version; the host&apos;s GPU is listed for information. Linux hosts get the first GPU tier later.</p>
          </CardContent>
        </Card>
      )}

      <div className="grid gap-4 sm:grid-cols-4">
        <Stat label="Net earnings" value={money(earnings?.total_cents ?? 0)} sub="after 10% platform fee" />
        <Stat label="Last 30 days" value={money(earnings?.last_30_days_cents ?? 0)} />
        <Stat label="Completed rentals" value={String(earnings?.completed_rentals ?? 0)} />
        <Stat label="Hours rented" value={(earnings?.hours_rented ?? 0).toFixed(1)} />
      </div>

      <Card>
        <CardHeader className="pb-2"><CardTitle className="text-base flex items-center gap-2"><Wallet className="size-4" /> Get paid on-chain</CardTitle></CardHeader>
        <CardContent className="space-y-2">
          <p className="text-sm text-muted-foreground">
            Enter a Tempo wallet address and every rental an agent pays over MPP sends your share ({100 - 10}% of the price) straight to it in the renter&apos;s own transaction — the platform fee goes to the treasury, nothing is held for you. Browser rentals (MetaMask) are a single transfer, so their earnings accrue to your balance here.
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Input placeholder="0x… your Tempo wallet" value={payout} onChange={(e) => setPayout(e.target.value)} className="w-full max-w-md font-mono text-xs" spellCheck={false} />
            <Button size="sm" onClick={savePayout} disabled={payout.trim() === (user?.payout_address ?? "")}>Save</Button>
          </div>
          {payoutMsg && <p className="text-xs text-muted-foreground">{payoutMsg}</p>}
        </CardContent>
      </Card>

      <section className="space-y-3">
        <h2 className="text-base font-medium">Machines</h2>
        {hosts === null && <p className="text-sm text-muted-foreground">Loading…</p>}
        {hosts?.length === 0 && (
          <Card><CardContent className="p-8 text-center text-sm text-muted-foreground">
            No machines yet. Add one, run the agent on it, and set a price.
          </CardContent></Card>
        )}
        <div className="grid gap-4 md:grid-cols-2">
          {hosts?.map((h) => (
            <Link key={h.id} href={`/host/${h.id}`}>
              <Card className="h-full transition hover:-translate-y-0.5">
                <CardHeader className="pb-2">
                  <div className="flex items-center justify-between">
                    <CardTitle className="text-base">{h.name}</CardTitle>
                    <Badge variant={h.online ? (h.status === "busy" ? "warning" : "success") : "secondary"}>{h.online ? h.status : h.registered ? "offline" : "not set up"}</Badge>
                  </div>
                </CardHeader>
                <CardContent className="text-sm text-muted-foreground">
                  {h.machine ? (
                    <>
                      <div>{osLabel(h.machine.os_family)} · {h.machine.cpu_model}</div>
                      <div>{h.machine.cpu_threads} threads · {gb(h.machine.ram_mb)} RAM · {h.machine.gpu_model || "no discrete GPU"}</div>
                      <div className="mt-1">{h.listing ? (h.listing.is_active ? `Listed at ${money(h.listing.price_cents_per_hour, h.listing.currency)}/h` : "Listing paused") : "No listing yet"} · driver {h.driver ?? "—"}</div>
                    </>
                  ) : (
                    <div>{platformLabel(h.platform)} · agent not set up yet — open to see the install steps.</div>
                  )}
                </CardContent>
              </Card>
            </Link>
          ))}
        </div>
      </section>

      <section className="space-y-3">
        <h2 className="text-base font-medium">Rentals of your machines</h2>
        {rentals.length === 0 ? (
          <p className="text-sm text-muted-foreground">None yet.</p>
        ) : (
          <div className="overflow-hidden rounded-xl border">
            <table className="w-full text-sm">
              <thead className="bg-muted/50 text-left text-xs text-muted-foreground">
                <tr><th className="p-3">Started</th><th className="p-3">Status</th><th className="p-3">Time left</th><th className="p-3">Resources</th><th className="p-3 text-right">Value</th></tr>
              </thead>
              <tbody>
                {rentals.map((r) => (
                  <tr key={r.id} className="border-t">
                    <td className="p-3">{new Date(r.created_at).toLocaleString()}</td>
                    <td className="p-3"><Badge variant={isLive(r.status) ? "success" : "outline"}>{r.status}</Badge></td>
                    <td className="p-3">{isLive(r.status) ? timeLeft(r.ends_at) || "—" : "—"}</td>
                    <td className="p-3">{r.cpus} vCPU · {Math.round(r.ram_mb / 1024)} GB</td>
                    <td className="p-3 text-right">{money(r.estimated_cost_cents, r.currency)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {live.length > 0 && <p className="text-xs text-muted-foreground">{live.length} live rental(s). Keep this machine on and awake; the agent prevents sleep while a rental runs.</p>}
      </section>
    </div>
  );
}

function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="rounded-xl border p-4">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="text-lg font-semibold">{value}</div>
      {sub && <div className="text-xs text-muted-foreground">{sub}</div>}
    </div>
  );
}
