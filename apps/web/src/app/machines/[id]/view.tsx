"use client";

import * as React from "react";
import { useParams, useRouter } from "next/navigation";
import { ShieldCheck, Globe, Lock } from "lucide-react";
import { api, ApiError, type MarketItem, type Rental } from "@/lib/api";
import { gb, money } from "@/lib/utils";
import { useAuth } from "@/components/auth-provider";
import { osLabel, StatusBadge } from "@/components/machine-card";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export default function MachinePage() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const { user } = useAuth();
  const [item, setItem] = React.useState<MarketItem | null>(null);
  const [error, setError] = React.useState<string | null>(null);
  const [hours, setHours] = React.useState(1);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    api<{ item: MarketItem }>(`/v1/market/${id}`).then((r) => setItem(r.item)).catch((e) => setError(e.message));
  }, [id]);

  async function rent() {
    if (!user) return router.push(`/login?next=/machines/${id}`);
    setBusy(true);
    setError(null);
    try {
      const r = await api<{ rental: Rental; ssh_private_key: string; ssh_command: string }>("/v1/rentals", {
        method: "POST",
        body: { listing_id: id, hours },
      });
      // Hand the one-time private key to the session page via sessionStorage (never the URL).
      try { sessionStorage.setItem(`rentpc_key_${r.rental.id}`, r.ssh_private_key); } catch {}
      router.push(`/rentals/${r.rental.id}`);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Could not start the rental");
      setBusy(false);
    }
  }

  if (error && !item) return <p className="text-sm text-destructive">{error}</p>;
  if (!item) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const { listing: l, machine: m } = item;
  const isOwner = user?.id && m.host_id && item.owner_name === user.display_name; // best-effort; server enforces

  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_340px]">
      <div className="space-y-6">
        <div>
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-semibold tracking-tight">{l.title}</h1>
            <StatusBadge item={item} />
          </div>
          <p className="mt-1 text-sm text-muted-foreground">
            {osLabel(m.os_family)} {m.os_version} host · {m.arch} · hosted by {item.owner_name} · {item.completed_rentals} completed rentals
          </p>
          {l.description && <p className="mt-3 text-sm">{l.description}</p>}
        </div>

        <Card>
          <CardHeader><CardTitle className="text-base">What you get</CardTitle></CardHeader>
          <CardContent>
            <dl className="grid gap-3 sm:grid-cols-2 text-sm">
              <Row k="vCPUs" v={`${l.alloc_cpus} of ${m.cpu_threads} threads`} sub={m.cpu_model} />
              <Row k="Memory" v={gb(l.alloc_ram_mb)} sub={`host has ${gb(m.ram_mb)}`} />
              <Row k="Disk" v={`${l.alloc_disk_gb} GB`} sub="dedicated virtual disk, wiped on termination" />
              <Row k="GPU" v={l.gpu_access === "none" ? "Not exposed" : l.gpu_access} sub={m.gpu_model ? `host GPU: ${m.gpu_model}${m.gpu_vram_mb ? ` (${gb(m.gpu_vram_mb)})` : ""}` : "host has no discrete GPU"} />
              <Row k="Guest OS" v="Ubuntu 24.04 LTS (VM)" sub="root via sudo inside your VM" />
              <Row k="Booking" v={`${l.min_hours}–${l.max_hours} h`} sub="extend while active" />
            </dl>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">Isolation</CardTitle>
            <CardDescription>How the owner and you are protected from each other.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-3 text-sm sm:grid-cols-3">
            <Pill icon={<ShieldCheck />} title="Own VM" text="Hardware-virtualised Linux VM: own kernel and disk, no host folders mounted." />
            <Pill icon={<Globe />} title="Internet, not LAN" text="Outbound via a policy proxy; the owner's LAN, localhost and metadata ranges are unreachable." />
            <Pill icon={<Lock />} title="Temporary keys" text="A fresh SSH key per rental, revoked and the disk wiped when it ends." />
          </CardContent>
        </Card>
      </div>

      <Card className="h-fit lg:sticky lg:top-20">
        <CardHeader>
          <CardTitle className="text-2xl">{money(l.price_cents_per_hour, l.currency)}<span className="text-sm font-normal text-muted-foreground"> / hour</span></CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="hours">Hours</Label>
            <Input id="hours" type="number" min={l.min_hours} max={l.max_hours} value={hours} onChange={(e) => setHours(Math.max(l.min_hours, Math.min(l.max_hours, Number(e.target.value) || l.min_hours)))} />
          </div>
          <div className="flex justify-between text-sm">
            <span className="text-muted-foreground">Estimated total</span>
            <span className="font-medium">{money(l.price_cents_per_hour * hours, l.currency)}</span>
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <Button className="w-full" disabled={!item.available || busy || !!isOwner} onClick={rent}>
            {busy ? "Reserving…" : !item.online ? "Host offline" : !item.available ? "Currently rented" : isOwner ? "This is your machine" : user ? "Rent now" : "Sign in to rent"}
          </Button>
          <p className="text-xs text-muted-foreground">Billing is mocked in this build: no real charge is made. Payment providers plug in behind a single interface.</p>
        </CardContent>
      </Card>
    </div>
  );
}

function Row({ k, v, sub }: { k: string; v: string; sub?: string }) {
  return (
    <div>
      <dt className="text-muted-foreground">{k}</dt>
      <dd className="font-medium">{v}</dd>
      {sub && <dd className="text-xs text-muted-foreground">{sub}</dd>}
    </div>
  );
}

function Pill({ icon, title, text }: { icon: React.ReactNode; title: string; text: string }) {
  return (
    <div className="rounded-lg border p-3">
      <div className="flex items-center gap-2 font-medium [&_svg]:size-4">{icon}{title}</div>
      <p className="mt-1 text-xs text-muted-foreground">{text}</p>
    </div>
  );
}
