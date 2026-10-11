import { Suspense } from "react";
import RentalsPage from "./view";


export default function Page() {
  return (
    <Suspense fallback={<p className="text-sm text-muted-foreground">Loading…</p>}>
      <RentalsPage />
    </Suspense>
  );
}
