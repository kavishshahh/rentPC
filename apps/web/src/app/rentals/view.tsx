"use client";

import * as React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { api, isLive, type Rental } from "@/lib/api";
import { money, timeLeft } from "@/lib/utils";
import { useAuth } from "@/components/auth-provider";
import { Badge } from "@/components/ui/badge";

export default function RentalsPage() {
  const { user, loading } = useAuth();
  const router = useRouter();
  const [rentals, setRentals] = React.useState<Rental[] | null>(null);

  React.useEffect(() => {
    if (!loading && !user) router.push("/login?next=/rentals");
  }, [loading, user, router]);

  React.useEffect(() => {
    if (!user) return;
    const load = () => api<{ rentals: Rental[] }>("/v1/rentals").then((r) => setRentals(r.rentals)).catch(() => {});
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [user]);

  if (!user) return null;
  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold tracking-tight">My rentals</h1>
      {rentals === null && <p className="text-sm text-muted-foreground">Loading…</p>}
      {rentals?.length === 0 && (
        <p className="rounded-xl border border-dashed p-10 text-center text-sm text-muted-foreground">
          Nothing yet. <Link className="underline" href="/">Browse the marketplace</Link>.
        </p>
      )}
      {rentals && rentals.length > 0 && (
        <div className="overflow-hidden rounded-xl border">
          <table className="w-full text-sm">
            <thead className="bg-muted/50 text-left text-xs text-muted-foreground">
              <tr><th className="p-3">Machine</th><th className="p-3">Status</th><th className="p-3">Time left</th><th className="p-3">Resources</th><th className="p-3 text-right">Cost</th></tr>
            </thead>
            <tbody>
              {rentals.map((r) => (
                <tr key={r.id} className="border-t hover:bg-accent/40">
                  <td className="p-3"><Link href={`/rentals/${r.id}`} className="font-medium underline-offset-2 hover:underline">{r.machine_title || r.id.slice(0, 8)}</Link><div className="text-xs text-muted-foreground">{new Date(r.created_at).toLocaleString()}</div></td>
                  <td className="p-3"><Badge variant={isLive(r.status) ? "success" : r.status === "failed" ? "destructive" : "outline"}>{r.status}</Badge></td>
                  <td className="p-3">{isLive(r.status) ? timeLeft(r.ends_at) || "—" : "—"}</td>
                  <td className="p-3">{r.cpus} vCPU · {Math.round(r.ram_mb / 1024)} GB · {r.disk_gb} GB</td>
                  <td className="p-3 text-right">{money(r.estimated_cost_cents, r.currency)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
