"use client";

import * as React from "react";
import { useParams, useRouter } from "next/navigation";
import { Copy, RefreshCw } from "lucide-react";
import { API_URL, api, ApiError, type Host, type Listing } from "@/lib/api";
import { gb } from "@/lib/utils";
import { osLabel } from "@/components/machine-card";
import { hostPrereqs, hostSetupScript, platformLabel } from "@/lib/releases";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export default function HostDetail() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const [host, setHost] = React.useState<Host | null>(null);
  const [reg, setReg] = React.useState<{ token: string; server: string } | null>(null);
  const [copied, setCopied] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [saved, setSaved] = React.useState(false);
  const [form, setForm] = React.useState({ title: "", description: "", price: "0.50", cpus: 2, ram_gb: 4, disk_gb: 20, min_hours: 1, max_hours: 24, is_active: true, country: "", region: "" });

  const load = React.useCallback(() => {
    api<{ host: Host }>(`/v1/hosts/${id}`).then((r) => {
      setHost(r.host);
      const l = r.host.listing;
      if (l) setForm((f) => ({ ...f, title: l.title, description: l.description, price: (l.price_cents_per_hour / 100).toFixed(2), cpus: l.alloc_cpus, ram_gb: Math.round(l.alloc_ram_mb / 1024), disk_gb: l.alloc_disk_gb, min_hours: l.min_hours, max_hours: l.max_hours, is_active: l.is_active }));
      if (r.host.machine) setForm((f) => ({ ...f, country: r.host.machine!.country, region: r.host.machine!.region }));
    }).catch((e) => setError(e.message));
  }, [id]);

  React.useEffect(() => {
    load();
    try { const r = sessionStorage.getItem(`rentpc_reg_${id}`); if (r) setReg(JSON.parse(r)); } catch {}
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [id, load]);

  async function newToken() {
    const r = await api<{ register_token: string; server: string }>(`/v1/hosts/${id}/register-token`, { method: "POST" });
    const v = { token: r.register_token, server: r.server };
    setReg(v);
    try { sessionStorage.setItem(`rentpc_reg_${id}`, JSON.stringify(v)); } catch {}
  }

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSaved(false);
    try {
      await api<{ listing: Listing }>(`/v1/hosts/${id}/listing`, {
        method: "PUT",
        body: {
          title: form.title, description: form.description, price_cents_per_hour: Math.round(Number(form.price) * 100), currency: "USD",
          alloc_cpus: Number(form.cpus), alloc_ram_mb: Number(form.ram_gb) * 1024, alloc_disk_gb: Number(form.disk_gb),
          gpu_access: "none", min_hours: Number(form.min_hours), max_hours: Number(form.max_hours), is_active: form.is_active,
        },
      });
      if (form.country) await api(`/v1/hosts/${id}/location`, { method: "PUT", body: { country: form.country, region: form.region } });
      setSaved(true);
      load();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "save failed");
    }
  }

  async function remove() {
    if (!confirm("Remove this machine from rentPC?")) return;
    try {
      await api(`/v1/hosts/${id}`, { method: "DELETE" });
      router.push("/host");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "delete failed");
    }
  }

  if (error && !host) return <p className="text-sm text-destructive">{error}</p>;
  if (!host) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const m = host.machine;

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-2xl font-semibold tracking-tight">{host.name}</h1>
        <Badge variant={host.online ? "success" : "secondary"}>{host.online ? host.status : host.registered ? "offline" : "not set up"}</Badge>
        <Button variant="ghost" size="sm" className="ml-auto text-destructive" onClick={remove}>Remove</Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">1 · Run the host agent on this {platformLabel(host.platform)} machine</CardTitle>
          <CardDescription>The agent connects outbound only (no port forwarding) and runs each rental in its own VM. Nothing else is installed on the renter&apos;s behalf.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4 text-sm">
          <div>
            <div className="mb-1 font-medium">Before you start</div>
            <ul className="list-disc space-y-1 pl-5 text-muted-foreground">
              {hostPrereqs(host.platform).map((t, i) => <li key={i}>{t}</li>)}
            </ul>
          </div>
          <div>
            <div className="mb-1 flex flex-wrap items-center gap-2 font-medium">
              Then paste this into {host.platform === "windows" ? "PowerShell" : "Terminal"}
              {reg ? (
                <Button size="sm" variant="outline" className="ml-auto" onClick={() => { navigator.clipboard.writeText(hostSetupScript(host.platform, reg.server, reg.token)); setCopied(true); setTimeout(() => setCopied(false), 1500); }}>
                  <Copy /> {copied ? "Copied" : "Copy"}
                </Button>
              ) : (
                <Button size="sm" variant="outline" className="ml-auto" onClick={newToken}><RefreshCw /> {host.registered ? "Generate a new token (re-register)" : "Generate registration token"}</Button>
              )}
            </div>
            <pre className="overflow-x-auto rounded bg-muted p-3 text-xs leading-relaxed">{hostSetupScript(host.platform, reg?.server ?? apiBase(), reg?.token ?? "")}</pre>
            <p className="mt-1 text-xs text-muted-foreground">
              {reg ? "The token is single-use and valid for 1 hour." : "Generate a token to fill in the register line."} Keep the window open: the agent runs in the foreground and your machine goes offline when it exits.
              {host.platform === "windows" && " Windows Defender may ask about qemu-system-x86_64.exe — allow it."}
              {host.platform === "darwin" && " macOS may ask to allow network access for rentpc-host — allow it."}
            </p>
          </div>
          {m && (
            <div className="rounded-lg border p-3">
              <div className="font-medium">Detected hardware</div>
              <div className="mt-1 grid gap-x-6 gap-y-1 text-xs text-muted-foreground sm:grid-cols-2">
                <div>{osLabel(m.os_family)} {m.os_version} · {m.arch} · {m.hostname}</div>
                <div>{m.cpu_model} · {m.cpu_cores}c / {m.cpu_threads}t</div>
                <div>{gb(m.ram_mb)} RAM · {m.disk_free_gb} GB free</div>
                <div>GPU: {m.gpu_model || "none detected"}{m.gpu_vram_mb ? ` (${gb(m.gpu_vram_mb)})` : ""}</div>
                <div>Accelerators: {Object.entries(m.virt).filter(([, v]) => v).map(([k]) => k).join(", ") || "none detected"}</div>
                <div>Driver: {host.driver ?? "—"} · agent {host.agent_version ?? "—"}</div>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">2 · Listing</CardTitle>
          <CardDescription>What renters get and what it costs. At least one CPU thread and 4 GB RAM are kept for you.</CardDescription>
        </CardHeader>
        <CardContent>
          {!m ? (
            <p className="text-sm text-muted-foreground">Waiting for the agent to report hardware before a listing can be created.</p>
          ) : (
            <form onSubmit={save} className="grid gap-4 sm:grid-cols-2">
              <Field label="Title"><Input value={form.title} placeholder={`${m.cpu_model} · ${form.cpus} vCPU`} onChange={(e) => setForm({ ...form, title: e.target.value })} /></Field>
              <Field label="Price per hour (USD)"><Input type="number" step="0.01" min={0} value={form.price} onChange={(e) => setForm({ ...form, price: e.target.value })} /></Field>
              <Field label={`vCPUs (max ${Math.max(1, m.cpu_threads - 1)})`}><Input type="number" min={1} max={Math.max(1, m.cpu_threads - 1)} value={form.cpus} onChange={(e) => setForm({ ...form, cpus: Number(e.target.value) })} /></Field>
              <Field label={`RAM GB (max ${Math.floor(Math.max(m.ram_mb - 4096, m.ram_mb / 2) / 1024)})`}><Input type="number" min={1} value={form.ram_gb} onChange={(e) => setForm({ ...form, ram_gb: Number(e.target.value) })} /></Field>
              <Field label={`Disk GB (max ${m.disk_free_gb - 10})`}><Input type="number" min={5} value={form.disk_gb} onChange={(e) => setForm({ ...form, disk_gb: Number(e.target.value) })} /></Field>
              <Field label="GPU access"><Input disabled value="none (CPU-only VM tier)" /></Field>
              <Field label="Min hours"><Input type="number" min={1} value={form.min_hours} onChange={(e) => setForm({ ...form, min_hours: Number(e.target.value) })} /></Field>
              <Field label="Max hours"><Input type="number" min={1} value={form.max_hours} onChange={(e) => setForm({ ...form, max_hours: Number(e.target.value) })} /></Field>
              <Field label="Country (ISO code)"><Input maxLength={2} placeholder="IN" value={form.country} onChange={(e) => setForm({ ...form, country: e.target.value.toUpperCase() })} /></Field>
              <Field label="Region / city"><Input value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} /></Field>
              <div className="sm:col-span-2 space-y-1.5">
                <Label>Description</Label>
                <textarea className="w-full rounded-md border border-input bg-transparent p-2 text-sm" rows={3} value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} />
              </div>
              <label className="flex items-center gap-2 text-sm sm:col-span-2">
                <input type="checkbox" checked={form.is_active} onChange={(e) => setForm({ ...form, is_active: e.target.checked })} /> Accept rentals (listing visible in the marketplace)
              </label>
              {error && <p className="text-sm text-destructive sm:col-span-2">{error}</p>}
              {saved && <p className="text-sm text-emerald-600 sm:col-span-2">Saved.</p>}
              <div className="sm:col-span-2"><Button type="submit">Save listing</Button></div>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function apiBase() {
  return API_URL || (typeof window !== "undefined" ? window.location.origin : "");
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="space-y-1.5"><Label>{label}</Label>{children}</div>;
}
