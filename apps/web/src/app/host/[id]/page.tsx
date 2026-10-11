import { Suspense } from "react";
import HostDetail from "./view";


export default function Page() {
  return (
    <Suspense fallback={<p className="text-sm text-muted-foreground">Loading…</p>}>
      <HostDetail />
    </Suspense>
  );
}
