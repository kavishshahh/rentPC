import { Suspense } from "react";

import { MarketBrowser } from "@/components/market-browser";

export default function MarketplacePage() {
  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight">Rent a machine by the hour</h1>
        <p className="text-sm text-muted-foreground">
          Every rental is an isolated VM on someone&apos;s idle PC: your own disk, your own kernel, internet access,
          and no access to the owner&apos;s files or network.
        </p>
      </div>
      <Suspense>
        <MarketBrowser />
      </Suspense>
    </div>
  );
}
