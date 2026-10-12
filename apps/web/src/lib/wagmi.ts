"use client";

// Tempo wallet + MPP client for in-browser payments.
// Mirrors mppx's charge-wagmi example: the connected wagmi account signs the
// Tempo transfer that satisfies the gateway's HTTP 402 challenge.
import { Mppx, tempo } from "mppx/client";
import { Chain } from "viem/tempo";
import { createConfig, http } from "wagmi";
import type { Client } from "viem";
import { getConnectorClient } from "wagmi/actions";
import { webAuthn } from "wagmi/tempo";

// Moderato, chain id 42431. viem waits max(blockTime*3, 5s) for a wallet-submitted
// call bundle to confirm; Tempo's real 1s block time would give a human 5 seconds
// to approve the MetaMask popup. Stretch it so the wait covers the approval.
export const tempoChain = { ...Chain.testnet, blockTime: 60_000 } as unknown as typeof Chain.testnet;

export const wagmiConfig = createConfig({
  chains: [tempoChain],
  // Passkey wallet only: a Tempo EIP-7702 account that signs native Tempo
  // transactions and batches calls atomically, which the MPP split payment
  // (host share + platform fee in one tx) requires. Plain EOA extension
  // wallets (MetaMask) cannot batch on Tempo, so they are not offered.
  connectors: [webAuthn()],
  transports: { [tempoChain.id]: http() },
  ssr: true,
});

declare module "wagmi" {
  interface Register {
    config: typeof wagmiConfig;
  }
}

// A wallet source other than wagmi (the Privy embedded wallet) registers its
// viem wallet client here; while set, MPP payments are signed by it.
let externalClient: Client | null = null;
export function setExternalWalletClient(c: Client | null) {
  externalClient = c;
}

// Does NOT polyfill global fetch: the rest of the app must not be intercepted.
export const mppx = Mppx.create({
  methods: [
    tempo({
      mode: "push",
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      getClient: () => (externalClient ? Promise.resolve(externalClient as any) : getConnectorClient(wagmiConfig)),
    }),
  ],
  polyfill: false,
});

/** pathUSD — the testnet stablecoin the faucet hands out. */
export const PATHUSD = "0x20c0000000000000000000000000000000000000" as const;
