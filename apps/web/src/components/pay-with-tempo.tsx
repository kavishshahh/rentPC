"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { Wallet, Droplets, Coins } from "lucide-react";
import { useAccount, useConnect, useDisconnect } from "wagmi";
import { Hooks } from "wagmi/tempo";
import { API_URL, type Rental } from "@/lib/api";
import { mppx, PATHUSD } from "@/lib/wagmi";
import { money } from "@/lib/utils";
import { Button } from "@/components/ui/button";

type Props = { listingId: string; hours: number; totalCents: number; currency: string; disabled?: boolean };

/**
 * Rent by paying the MPP (HTTP 402) challenge with a Tempo wallet.
 * Flow: connect (passkey or MetaMask) → auto-fund on testnet → POST /v1/rentals
 * with X-RentPC-Pay: mpp → gateway answers 402 → mppx signs & broadcasts the
 * stablecoin transfer → retry → 201 with the rental.
 */
export function PayWithTempo({ listingId, hours, totalCents, currency, disabled }: Props) {
  const router = useRouter();
  const { address, status } = useAccount();
  const { connectors, connect, isPending: connecting, error: connectError } = useConnect();
  const [secure, setSecure] = React.useState(true);
  React.useEffect(() => { setSecure(typeof window !== "undefined" && window.isSecureContext); }, []);
  const { disconnect } = useDisconnect();
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [step, setStep] = React.useState<string>("");

  const balance = Hooks.token.useGetBalance({
    account: address,
    token: PATHUSD,
    query: { enabled: !!address, refetchInterval: 4000 },
  });
  const fund = Hooks.faucet.useFundSync();
  const bal = balance.data ? Number(balance.data.formatted) : null;
  const needs = totalCents / 100;

  async function rent() {
    setBusy(true);
    setError(null);
    try {
      setStep("Requesting price (HTTP 402)…");
      const res = await mppx.fetch(`${API_URL}/v1/rentals`, {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json", "X-RentPC-Pay": "mpp" },
        body: JSON.stringify({ listing_id: listingId, hours }),
      });
      const text = await res.text();
      let data: { rental?: Rental; ssh_private_key?: string; error?: string } = {};
      try { data = JSON.parse(text); } catch {}
      if (!res.ok || !data.rental) throw new Error(data.error || `${res.status} ${text.slice(0, 200)}`);
      const receipt = res.headers.get("Payment-Receipt");
      try {
        if (data.ssh_private_key) sessionStorage.setItem(`rentpc_key_${data.rental.id}`, data.ssh_private_key);
        if (receipt) sessionStorage.setItem(`rentpc_receipt_${data.rental.id}`, receipt);
      } catch {}
      router.push(`/rentals/${data.rental.id}`);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
      setStep("");
    }
  }

  if (status !== "connected" || !address) {
    return (
      <div className="space-y-2 rounded-lg border p-3">
        <div className="flex items-center gap-2 text-sm font-medium"><Wallet className="size-4" /> Pay with a Tempo wallet</div>
        <p className="text-xs text-muted-foreground">Stablecoin payment over MPP (HTTP 402) on Tempo testnet. A passkey wallet needs nothing installed.</p>
        <div className="flex flex-wrap gap-2">
          {connectors.map((c) => {
            const passkey = c.type === "webAuthn";
            return (
              <Button key={c.uid} size="sm" variant="outline" disabled={connecting || disabled || (passkey && !secure)}
                title={passkey && !secure ? "Passkeys need an https:// page" : undefined}
                onClick={() => connect({ connector: c })}>
                {passkey ? "Connect (Passkey)" : `Connect (${c.name})`}
              </Button>
            );
          })}
        </div>
        {!secure && <p className="text-xs text-muted-foreground">Passkey wallets need HTTPS; open this page over https:// or use MetaMask.</p>}
        {connectError && <p className="text-xs text-destructive break-words">{connectError.message.split("\n")[0]}</p>}
        {error && <p className="text-xs text-destructive">{error}</p>}
      </div>
    );
  }

  const short = `${address.slice(0, 6)}…${address.slice(-4)}`;
  const enough = bal !== null && bal >= needs;

  return (
    <div className="space-y-3 rounded-lg border p-3">
      <div className="flex items-center justify-between text-sm">
        <span className="flex items-center gap-2 font-medium"><Wallet className="size-4" /> {short}</span>
        <button className="text-xs text-muted-foreground underline" onClick={() => disconnect()}>disconnect</button>
      </div>
      <div className="flex items-center justify-between text-sm">
        <span className="flex items-center gap-1 text-muted-foreground"><Coins className="size-3.5" /> pathUSD balance</span>
        <span className="font-medium">{bal === null ? "…" : bal.toFixed(2)}</span>
      </div>
      {!enough && (
        <Button size="sm" variant="outline" className="w-full" disabled={fund.isPending} onClick={() => fund.mutate({ account: address })}>
          <Droplets /> {fund.isPending ? "Funding from testnet faucet…" : "Get testnet pathUSD (faucet)"}
        </Button>
      )}
      <Button className="w-full" disabled={disabled || busy || !enough} onClick={rent}>
        {busy ? step || "Paying…" : `Pay ${money(totalCents, currency)} on Tempo & rent`}
      </Button>
      {error && <p className="text-xs text-destructive break-words">{error}</p>}
      <p className="text-[11px] text-muted-foreground">The wallet signs one transfer for the exact quote; the gateway verifies it on-chain before the machine is reserved.</p>
    </div>
  );
}
