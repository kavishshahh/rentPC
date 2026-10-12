"use client";

import * as React from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { PrivyProvider } from "@privy-io/react-auth";
import { WagmiProvider } from "wagmi";
import { tempoChain, wagmiConfig } from "@/lib/wagmi";

// Privy app id is public (it identifies the app, not a secret).
export const PRIVY_APP_ID = process.env.NEXT_PUBLIC_PRIVY_APP_ID || "cmv5is3ao00f30fig66qjyefq";

export function WalletProvider({ children }: { children: React.ReactNode }) {
  const [qc] = React.useState(() => new QueryClient());
  return (
    <PrivyProvider
      appId={PRIVY_APP_ID}
      config={{
        appearance: { theme: "dark", accentColor: "#f97316" },
        loginMethods: ["email", "google"],
        // Every login gets a Tempo embedded wallet; it signs the MPP split
        // (host share + platform fee) as one batched Tempo transaction.
        embeddedWallets: { ethereum: { createOnLogin: "all-users" }, showWalletUIs: false },
        supportedChains: [tempoChain],
        defaultChain: tempoChain,
      }}
    >
      <WagmiProvider config={wagmiConfig}>
        <QueryClientProvider client={qc}>{children}</QueryClientProvider>
      </WagmiProvider>
    </PrivyProvider>
  );
}
