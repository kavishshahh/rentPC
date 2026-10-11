"use client";

import * as React from "react";
import { useParams } from "next/navigation";
import { Download, KeyRound, TerminalSquare } from "lucide-react";
import { api, ApiError, isLive, type Rental } from "@/lib/api";
import { money, timeLeft } from "@/lib/utils";
import { Terminal } from "@/components/terminal";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

const statusVariant: Record<string, "default" | "secondary" | "destructive" | "success" | "warning" | "outline"> = {
  reserved: "warning", provisioning: "warning", ready: "success", active: "success", extending: "success",
  terminating: "secondary", completed: "outline", failed: "destructive", cancelled: "secondary", expired: "secondary",
};

export default function RentalPage() {
  const { id } = useParams<{ id: string }>();
  const [rental, setRental] = React.useState<Rental | null>(null);
  const [sshCmd, setSshCmd] = React.useState("");
  const [key, setKey] = React.useState<string | null>(null);
  const [error, setError] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [, tick] = React.useReducer((x: number) => x + 1, 0);

  const load = React.useCallback(async () => {
    try {
      const r = await api<{ rental: Rental; ssh_command: string }>(`/v1/rentals/${id}`);
      setRental(r.rental);
      setSshCmd(r.ssh_command);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "failed to load");
    }
  }, [id]);

  React.useEffect(() => {
    void load();
    try { const k = sessionStorage.getItem(`rentpc_key_${id}`); if (k) setKey(k); } catch {}
  }, [id, load]);

  React.useEffect(() => {
    if (!rental || !isLive(rental.status)) return;
    const fast = rental.status === "reserved" || rental.status === "provisioning" || rental.status === "terminating";
    const t = setInterval(() => { void load(); tick(); }, fast ? 2000 : 10000);
    return () => clearInterval(t);
  }, [rental, load]);

  async function act(path: string, body?: unknown) {
    setBusy(true);
    setError(null);
    try {
      await api(`/v1/rentals/${id}/${path}`, { method: "POST", body });
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "action failed");
    } finally {
      setBusy(false);
    }
  }

  async function fetchKey() {
    try {
      const r = await api<{ ssh_private_key: string }>(`/v1/rentals/${id}/private-key`, { method: "POST" });
      setKey(r.ssh_private_key);
      try { sessionStorage.setItem(`rentpc_key_${id}`, r.ssh_private_key); } catch {}
    } catch (e) {
      setError(e instanceof ApiError && e.status === 404 ? "The private key was already retrieved; it is only delivered once." : "could not fetch key");
    }
  }

  function downloadKey() {
    if (!key) return;
    const blob = new Blob([key], { type: "text/plain" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `rentpc_${id.slice(0, 8)}.key`;
    a.click();
  }

  if (error && !rental) return <p className="text-sm text-destructive">{error}</p>;
  if (!rental) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const live = isLive(rental.status);
  const connectable = rental.status === "ready" || rental.status === "active" || rental.status === "extending";

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-2xl font-semibold tracking-tight">{rental.machine_title || "Rental"}</h1>
        <Badge variant={statusVariant[rental.status] ?? "secondary"}>{rental.status}</Badge>
        {!rental.host_online && live && <Badge variant="destructive">host offline</Badge>}
      </div>

      <div className="grid gap-4 sm:grid-cols-4">
        <Stat label="Time left" value={rental.ends_at ? timeLeft(rental.ends_at) : rental.status === "provisioning" ? "booting…" : "—"} />
        <Stat label="Booked" value={`${rental.hours_booked} h`} />
        <Stat label="Estimated cost" value={money(rental.estimated_cost_cents, rental.currency)} sub={`${money(rental.price_cents_per_hour, rental.currency)}/h`} />
        <Stat label="Resources" value={`${rental.cpus} vCPU · ${Math.round(rental.ram_mb / 1024)} GB · ${rental.disk_gb} GB`} />
      </div>

      {rental.failure_reason && <p className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm">{rental.failure_reason}</p>}
      {error && <p className="text-sm text-destructive">{error}</p>}

      {live && (
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" disabled={busy || !connectable} onClick={() => act("extend", { hours: 1 })}>Extend +1 h</Button>
          <Button variant="destructive" size="sm" disabled={busy} onClick={() => confirm("End this rental now? The VM and its disk are destroyed.") && act("terminate")}>Terminate</Button>
        </div>
      )}

      {rental.status === "provisioning" && (
        <Card><CardContent className="p-6 text-sm text-muted-foreground">Creating your VM on the host: overlay disk, cloud-init with your key, first boot. Usually 30–90 s.</CardContent></Card>
      )}

      {connectable && (
        <section className="space-y-2">
          <h2 className="flex items-center gap-2 text-base font-medium"><TerminalSquare className="size-4" /> Terminal</h2>
          <Terminal rentalId={rental.id} onConnected={load} />
        </section>
      )}

      {live && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base"><KeyRound className="size-4" /> SSH, scp, VS Code Remote</CardTitle>
            <CardDescription>Connect with your own tools through the relay. Your private key is generated per rental and shown once.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            <ol className="list-decimal space-y-2 pl-5">
              <li>Install the CLI: <code className="rounded bg-muted px-1">go install github.com/cadencz/rentpc/tools/rentpc-cli@latest</code> (binary name <code>rentpc</code>), and set <code className="rounded bg-muted px-1">RENTPC_API_KEY</code> from Account → API keys.</li>
              <li>
                Save your key:{" "}
                {key ? (
                  <Button size="sm" variant="outline" onClick={downloadKey}><Download /> Download rentpc_{id.slice(0, 8)}.key</Button>
                ) : (
                  <Button size="sm" variant="outline" onClick={fetchKey}>Reveal private key (once)</Button>
                )}
                {" "}then <code className="rounded bg-muted px-1">chmod 600</code> it.
              </li>
              <li>Connect:<pre className="mt-1 overflow-x-auto rounded bg-muted p-2 text-xs">{sshCmd}</pre></li>
            </ol>
            <p className="text-xs text-muted-foreground">Inside the VM you are <code>renter</code> with passwordless sudo. Outbound internet uses the preset <code>http_proxy</code>; the host&apos;s LAN is unreachable by design.</p>
          </CardContent>
        </Card>
      )}
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
