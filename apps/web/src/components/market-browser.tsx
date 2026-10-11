"use client";

import * as React from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { api, type MarketItem } from "@/lib/api";
import { MachineCard } from "@/components/machine-card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";

export function MarketBrowser() {
  const router = useRouter();
  const params = useSearchParams();
  const [items, setItems] = React.useState<MarketItem[] | null>(null);
  const [error, setError] = React.useState<string | null>(null);

  const q = params.get("q") ?? "";
  const os = params.get("os") ?? "";
  const gpu = params.get("gpu") ?? "";
  const minCpus = params.get("min_cpus") ?? "";
  const minRam = params.get("min_ram_gb") ?? "";
  const maxPrice = params.get("max_price") ?? "";
  const online = params.get("online") ?? "1";
  const sort = params.get("sort") ?? "";

  React.useEffect(() => {
    const qs = new URLSearchParams();
    if (q) qs.set("q", q);
    if (os) qs.set("os", os);
    if (gpu) qs.set("gpu", gpu);
    if (minCpus) qs.set("min_cpus", minCpus);
    if (minRam) qs.set("min_ram_mb", String(Number(minRam) * 1024));
    if (maxPrice) qs.set("max_price_cents", String(Math.round(Number(maxPrice) * 100)));
    if (online === "1") qs.set("online", "1");
    if (sort) qs.set("sort", sort);
    let cancelled = false;
    setItems(null);
    api<{ items: MarketItem[] }>(`/v1/market?${qs}`)
      .then((r) => !cancelled && setItems(r.items))
      .catch((e) => !cancelled && setError(e.message));
    const t = setInterval(() => {
      api<{ items: MarketItem[] }>(`/v1/market?${qs}`).then((r) => !cancelled && setItems(r.items)).catch(() => {});
    }, 15000);
    return () => { cancelled = true; clearInterval(t); };
  }, [q, os, gpu, minCpus, minRam, maxPrice, online, sort]);

  function set(key: string, value: string) {
    const next = new URLSearchParams(params.toString());
    if (value) next.set(key, value); else next.delete(key);
    router.replace(`/?${next}`);
  }

  return (
    <div className="grid gap-6 md:grid-cols-[240px_1fr]">
      <aside className="space-y-4 rounded-xl border p-4 text-sm h-fit">
        <div className="space-y-1.5">
          <Label>Search</Label>
          <Input placeholder="CPU, GPU, title…" defaultValue={q} onKeyDown={(e) => e.key === "Enter" && set("q", (e.target as HTMLInputElement).value)} />
        </div>
        <div className="space-y-1.5">
          <Label>Host OS</Label>
          <Select value={os} onChange={(e) => set("os", e.target.value)}>
            <option value="">Any</option>
            <option value="windows">Windows</option>
            <option value="darwin">macOS</option>
            <option value="linux">Linux</option>
          </Select>
        </div>
        <div className="space-y-1.5">
          <Label>GPU vendor (on host)</Label>
          <Select value={gpu} onChange={(e) => set("gpu", e.target.value)}>
            <option value="">Any</option>
            <option value="NVIDIA">NVIDIA</option>
            <option value="AMD">AMD</option>
            <option value="Apple">Apple</option>
            <option value="Intel">Intel</option>
          </Select>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <Label>Min vCPU</Label>
            <Input type="number" min={1} defaultValue={minCpus} onBlur={(e) => set("min_cpus", e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label>Min RAM (GB)</Label>
            <Input type="number" min={1} defaultValue={minRam} onBlur={(e) => set("min_ram_gb", e.target.value)} />
          </div>
        </div>
        <div className="space-y-1.5">
          <Label>Max $/hour</Label>
          <Input type="number" min={0} step="0.1" defaultValue={maxPrice} onBlur={(e) => set("max_price", e.target.value)} />
        </div>
        <div className="space-y-1.5">
          <Label>Sort</Label>
          <Select value={sort} onChange={(e) => set("sort", e.target.value)}>
            <option value="">Online first, cheapest</option>
            <option value="price_desc">Price: high → low</option>
            <option value="ram_desc">Most RAM</option>
            <option value="newest">Newest</option>
          </Select>
        </div>
        <label className="flex items-center gap-2">
          <input type="checkbox" checked={online === "1"} onChange={(e) => set("online", e.target.checked ? "1" : "0")} /> Online hosts only
        </label>
        <Button variant="outline" size="sm" className="w-full" onClick={() => router.replace("/")}>Reset</Button>
      </aside>
      <section>
        {error && <p className="text-sm text-destructive">Could not load the marketplace: {error}</p>}
        {items === null && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
        {items && items.length === 0 && (
          <div className="rounded-xl border border-dashed p-10 text-center text-sm text-muted-foreground">
            No machines match. Hosts come online when their agent is running — or list your own under <strong>Host</strong>.
          </div>
        )}
        {items && items.length > 0 && (
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {items.map((it) => <MachineCard key={it.listing.id} item={it} />)}
          </div>
        )}
      </section>
    </div>
  );
}
