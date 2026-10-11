import Link from "next/link";
import { Cpu, HardDrive, MemoryStick, Monitor, MapPin, Star } from "lucide-react";
import type { MarketItem } from "@/lib/api";
import { gb, money } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export function osLabel(os: string) {
  return { windows: "Windows", darwin: "macOS", linux: "Linux" }[os] ?? os;
}

export function StatusBadge({ item }: { item: MarketItem }) {
  if (!item.online) return <Badge variant="secondary">Offline</Badge>;
  if (!item.available) return <Badge variant="warning">Rented</Badge>;
  return <Badge variant="success">Available</Badge>;
}

export function MachineCard({ item }: { item: MarketItem }) {
  const { listing: l, machine: m } = item;
  return (
    <Link href={`/machines/${l.id}`} className="block transition hover:-translate-y-0.5">
      <Card className="h-full">
        <CardHeader className="pb-3">
          <div className="flex items-start justify-between gap-2">
            <CardTitle className="text-base leading-snug">{l.title}</CardTitle>
            <StatusBadge item={item} />
          </div>
          <div className="text-xs text-muted-foreground">
            {osLabel(m.os_family)} host · {m.arch} · by {item.owner_name}
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <dl className="grid grid-cols-2 gap-x-3 gap-y-1.5 text-sm">
            <Spec icon={<Cpu />} label={`${l.alloc_cpus} vCPU`} sub={m.cpu_model} />
            <Spec icon={<MemoryStick />} label={`${gb(l.alloc_ram_mb)} RAM`} sub={`of ${gb(m.ram_mb)}`} />
            <Spec icon={<HardDrive />} label={`${l.alloc_disk_gb} GB disk`} sub="dedicated virtual disk" />
            <Spec
              icon={<Monitor />}
              label={m.gpu_model ? m.gpu_model : "No GPU"}
              sub={l.gpu_access === "none" ? "GPU not exposed (CPU-only VM)" : l.gpu_access}
            />
          </dl>
          <div className="flex items-center justify-between border-t pt-3 text-sm">
            <span className="flex items-center gap-1 text-muted-foreground">
              {m.country ? (<><MapPin className="size-3.5" /> {m.country}{m.region ? `, ${m.region}` : ""}</>) : "Location unset"}
              {item.rating != null && (
                <span className="ml-2 flex items-center gap-0.5"><Star className="size-3.5" /> {item.rating.toFixed(1)}</span>
              )}
            </span>
            <span className="font-semibold">{money(l.price_cents_per_hour, l.currency)}<span className="font-normal text-muted-foreground">/hr</span></span>
          </div>
        </CardContent>
      </Card>
    </Link>
  );
}

function Spec({ icon, label, sub }: { icon: React.ReactNode; label: string; sub?: string }) {
  return (
    <div className="flex items-start gap-2 min-w-0">
      <span className="mt-0.5 text-muted-foreground [&_svg]:size-4">{icon}</span>
      <div className="min-w-0">
        <div className="font-medium">{label}</div>
        {sub && <div className="truncate text-xs text-muted-foreground">{sub}</div>}
      </div>
    </div>
  );
}
