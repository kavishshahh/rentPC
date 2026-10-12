"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { Wallet, Droplets, Coins, Mail } from "lucide-react";
import { useAccount, useConnect, useDisconnect, useSwitchChain } from "wagmi";
import { Hooks } from "wagmi/tempo";
import { useCreateWallet, usePrivy, useWallets } from "@privy-io/react-auth";
import { toViemAccount } from "@privy-io/react-auth/tempo";
import { createWalletClient, http } from "viem";
import { API_URL, type Rental } from "@/lib/api";
import { mppx, PATHUSD, setExternalWalletClient, tempoChain } from "@/lib/wagmi";
import { money } from "@/lib/utils";
import { Button } from "@/components/ui/button";

type Props = { listingId: string; hours: number; totalCents: number; currency: string; disabled?: boolean };

/**
 * Rent by paying the MPP (HTTP 402) challenge with a Tempo wallet.
 * Wallets: a Tempo passkey wallet (wagmi webAuthn connector) or a Privy
 * embedded wallet (email / Google login). Both are Tempo accounts that batch
 * calls, so one transaction pays the host's share and the platform fee.
 * Flow: connect → auto-fund on testnet → POST /v1/rentals with
 * X-RentPC-Pay: mpp → gateway answers 402 → mppx signs & broadcasts → retry
 * → 201 with the rental.
 */
export function PayWithTempo({ listingId, hours, totalCents, currency, disabled }: Props) {
  const router = useRouter();
  // chainId is the wallet's chain (undefined when it is on a network wagmi does not know).
  const { address: wagmiAddress, status: wagmiStatus, chainId } = useAccount();
  const { connectors, connect, isPending: connecting, error: connectError } = useConnect();
  const [secure, setSecure] = React.useState(true);
  React.useEffect(() => { setSecure(typeof window !== "undefined" && window.isSecureContext); }, []);
  const { disconnect } = useDisconnect();
  const { switchChainAsync } = useSwitchChain();
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [step, setStep] = React.useState<string>("");

  // Privy: email / Google login → embedded Tempo wallet. Its viem account is
  // handed to mppx so the 402 payment is signed by it.
  const privy = usePrivy();
  const { wallets: privyWallets, ready: privyWalletsReady } = useWallets();
  const { createWallet } = useCreateWallet();
  const privyWallet = privy.authenticated ? privyWallets.find((w) => w.walletClientType === "privy") : undefined;
  const [creating, setCreating] = React.useState(false);
  // Normally createOnLogin makes the wallet during login; if the user signed in
  // before that setting existed (or it was off in the dashboard) make it now.
  React.useEffect(() => {
    if (!privy.ready || !privy.authenticated || !privyWalletsReady || privyWallet || creating) return;
    if (privy.user?.linkedAccounts?.some((a) => a.type === "wallet" && "walletClientType" in a && a.walletClientType === "privy")) return;
    setCreating(true);
    createWallet().catch((e) => setError(`Privy wallet: ${e instanceof Error ? e.message : String(e)}`)).finally(() => setCreating(false));
  }, [privy.ready, privy.authenticated, privy.user, privyWalletsReady, privyWallet, creating, createWallet]);
  const [privyAddress, setPrivyAddress] = React.useState<`0x${string}` | null>(null);
  React.useEffect(() => {
    let cancelled = false;
    if (!privyWallet) {
      setExternalWalletClient(null);
      setPrivyAddress(null);
      return;
    }
    (async () => {
      try {
        const account = await toViemAccount({ wallet: privyWallet });
        if (cancelled) return;
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const client = createWalletClient({ account: account as any, chain: tempoChain, transport: http() });
        setExternalWalletClient(client);
        setPrivyAddress(account.address as `0x${string}`);
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      }
    })();
    return () => { cancelled = true; };
  }, [privyWallet]);

  const usingPrivy = !!privyAddress;
  const address = usingPrivy ? privyAddress : wagmiAddress;
  const status = usingPrivy ? "connected" : wagmiStatus;

  const balance = Hooks.token.useGetBalance({
    account: address ?? undefined,
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
      // Extension wallets often sit on another chain; move them to Tempo first.
      // Passkey and Privy wallets are already Tempo accounts.
      if (!usingPrivy && chainId !== tempoChain.id) {
        setStep("Switching wallet to Tempo…");
        await switchChainAsync({ chainId: tempoChain.id });
      }
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
    const privyBusy = privy.authenticated && (!privyWalletsReady || !privyAddress);
    const privyEmail = privy.user?.email?.address ?? privy.user?.google?.email;
    return (
      <div className="space-y-2 rounded-lg border p-3">
        <div className="flex items-center gap-2 text-sm font-medium"><Wallet className="size-4" /> Pay with a Tempo wallet</div>
        <p className="text-xs text-muted-foreground">
          Stablecoin payment over MPP (HTTP 402) on Tempo testnet. Your wallet is a Tempo account (EIP-7702) created for you — no extension, no seed phrase — and one transaction pays the host and the platform fee.
        </p>
        <div className="flex flex-wrap gap-2">
          {connectors.filter((c) => c.type === "webAuthn").map((c) => (
            <Button key={c.uid} size="sm" variant="outline" disabled={connecting || disabled || !secure}
              title={!secure ? "Passkeys need an https:// page" : undefined}
              onClick={() => connect({ connector: c })}>
              Connect (Passkey)
            </Button>
          ))}
          <Button size="sm" variant="outline" disabled={!privy.ready || disabled || privyBusy} onClick={() => privy.login()}>
            <Mail /> {privyBusy ? "Preparing wallet…" : "Log in with email (Privy)"}
          </Button>
        </div>
        {privy.authenticated && !privyAddress && (
          <p className="text-xs text-muted-foreground">
            Signed in to Privy{privyEmail ? ` as ${privyEmail}` : ""} — {privyWalletsReady ? (privyWallets.length ? `preparing wallet (${privyWallets.map((w) => w.walletClientType).join(", ")})` : creating ? "creating your Tempo wallet…" : "no wallet yet") : "loading wallets…"}.
            {" "}<button className="underline" onClick={() => privy.logout()}>Sign out of Privy</button>
          </p>
        )}
        {!secure && <p className="text-xs text-muted-foreground">Passkey wallets need HTTPS; open this page over https://.</p>}
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
        <span className="flex items-center gap-2 font-medium">
          <Wallet className="size-4" /> {short}
          {usingPrivy && <span className="text-xs font-normal text-muted-foreground">via Privy</span>}
        </span>
        <button className="text-xs text-muted-foreground underline" onClick={() => (usingPrivy ? privy.logout() : disconnect())}>disconnect</button>
      </div>
      <div className="flex items-center justify-between text-sm">
        <span className="flex items-center gap-1 text-muted-foreground"><Coins className="size-3.5" /> pathUSD balance</span>
        <span className="font-medium">{bal === null ? "…" : bal.toFixed(2)}</span>
      </div>
      {!usingPrivy && chainId !== tempoChain.id && (
        <p className="text-xs text-muted-foreground">Wallet is on {chainId ? `chain ${chainId}` : "another network"}; it will be switched to Tempo testnet ({tempoChain.id}) when you pay.</p>
      )}
      {!enough && (
        <Button size="sm" variant="outline" className="w-full" disabled={fund.isPending} onClick={() => fund.mutate({ account: address })}>
          <Droplets /> {fund.isPending ? "Funding from testnet faucet…" : "Get testnet pathUSD (faucet)"}
        </Button>
      )}
      <Button className="w-full" disabled={disabled || busy || !enough} onClick={rent}>
        {busy ? step || "Paying…" : `Pay ${money(totalCents, currency)} on Tempo & rent`}
      </Button>
      {error && <p className="text-xs text-destructive break-words">{error}</p>}
      <p className="text-[11px] text-muted-foreground">One transaction for the exact quote: your host&apos;s share goes to their wallet, the fee to the platform; the gateway verifies it on-chain before the machine is reserved.</p>
    </div>
  );
}
