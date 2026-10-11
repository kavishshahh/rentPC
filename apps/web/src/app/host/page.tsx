import { Suspense } from "react";
import HostDashboard from "./view";


export default function Page() {
  return (
    <Suspense fallback={<p className="text-sm text-muted-foreground">Loading…</p>}>
      <HostDashboard />
    </Suspense>
  );
}
