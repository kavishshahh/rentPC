"use client";

// Tempo wallet + MPP client for in-browser payments.
// Mirrors mppx's charge-wagmi example: the connected wagmi account signs the
// Tempo transfer that satisfies the gateway's HTTP 402 challenge.
import { Mppx, tempo } from "mppx/client";
import { Chain } from "viem/tempo";
import { createConfig, http } from "wagmi";
import { getConnectorClient } from "wagmi/actions";
import { injected } from "wagmi/connectors";
import { webAuthn } from "wagmi/tempo";

export const tempoChain = Chain.testnet; // Moderato, chain id 42431

export const wagmiConfig = createConfig({
  chains: [tempoChain],
  // webAuthn = passkey wallet (nothing to install); injected = MetaMask / Brave / Phantom etc.
  connectors: [webAuthn(), injected()],
  transports: { [tempoChain.id]: http() },
  ssr: true,
});

declare module "wagmi" {
  interface Register {
    config: typeof wagmiConfig;
  }
}

// Does NOT polyfill global fetch: the rest of the app must not be intercepted.
export const mppx = Mppx.create({
  methods: [
    tempo({
      mode: "push",
      getClient: () => getConnectorClient(wagmiConfig),
    }),
  ],
  polyfill: false,
});

/** pathUSD — the testnet stablecoin the faucet hands out. */
export const PATHUSD = "0x20c0000000000000000000000000000000000000" as const;
