"use client";

import * as React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Plus } from "lucide-react";
import { api, isLive, type Earnings, type Host, type Rental } from "@/lib/api";
import { gb, money, timeLeft } from "@/lib/utils";
import { useAuth } from "@/components/auth-provider";
import { osLabel } from "@/components/machine-card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export default function HostDashboard() {
  const { user, loading } = useAuth();
  const router = useRouter();
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

  async function addHost() {
    setBusy(true);
    try {
      const r = await api<{ host: Host; register_token: string; register_command: string }>("/v1/hosts", { method: "POST", body: { name: "My PC" } });
      try { sessionStorage.setItem(`rentpc_regcmd_${r.host.id}`, r.register_command); } catch {}
      router.push(`/host/${r.host.id}`);
    } finally {
      setBusy(false);
    }
  }

  if (!user) return null;
  const live = rentals.filter((r) => isLive(r.status));

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">Host dashboard</h1>
        <Button onClick={addHost} disabled={busy}><Plus /> Add a machine</Button>
      </div>

      <div className="grid gap-4 sm:grid-cols-4">
        <Stat label="Net earnings" value={money(earnings?.total_cents ?? 0)} sub="after 10% platform fee" />
        <Stat label="Last 30 days" value={money(earnings?.last_30_days_cents ?? 0)} />
        <Stat label="Completed rentals" value={String(earnings?.completed_rentals ?? 0)} />
        <Stat label="Hours rented" value={(earnings?.hours_rented ?? 0).toFixed(1)} />
      </div>

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
                    <div>Agent has not reported hardware yet.</div>
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
