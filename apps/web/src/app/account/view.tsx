"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { api } from "@/lib/api";
import { useAuth } from "@/components/auth-provider";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";

export default function AccountPage() {
  const { user, loading } = useAuth();
  const router = useRouter();
  const [label, setLabel] = React.useState("my-agent");
  const [key, setKey] = React.useState<string | null>(null);

  React.useEffect(() => {
    if (!loading && !user) router.push("/login?next=/account");
  }, [loading, user, router]);
  if (!user) return null;

  async function create() {
    const r = await api<{ api_key: string }>("/v1/auth/api-keys", { method: "POST", body: { label } });
    setKey(r.api_key);
  }

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">Account</h1>
      <Card>
        <CardHeader><CardTitle className="text-base">{user.display_name}</CardTitle><CardDescription>{user.email}</CardDescription></CardHeader>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">API keys</CardTitle>
          <CardDescription>For the <code>rentpc</code> CLI and for AI agents that rent compute programmatically (<code>Authorization: Bearer rpk_…</code>). Shown once.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex gap-2">
            <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="label" className="max-w-xs" />
            <Button onClick={create}>Create key</Button>
          </div>
          {key && (
            <div className="rounded-md bg-muted p-3 text-sm">
              <div className="text-xs text-muted-foreground">Copy it now — it will not be shown again.</div>
              <code className="break-all">{key}</code>
              <pre className="mt-2 overflow-x-auto text-xs">export RENTPC_API_KEY={key}</pre>
            </div>
          )}
          <details className="text-sm">
            <summary className="cursor-pointer text-muted-foreground">API quick reference for agents</summary>
            <pre className="mt-2 overflow-x-auto rounded bg-muted p-3 text-xs">{`GET  /v1/market?min_cpus=4&min_ram_mb=8192&online=1     # discover
POST /v1/rentals  {"listing_id": "...", "hours": 1}       # book → returns ssh_private_key once
GET  /v1/rentals/{id}                                     # poll status: provisioning → ready → active
WS   /v1/rentals/{id}/tunnel                              # raw TCP to guest:22 (rentpc tunnel)
POST /v1/rentals/{id}/extend  {"hours": 1}
POST /v1/rentals/{id}/terminate`}</pre>
          </details>
        </CardContent>
      </Card>
    </div>
  );
}
