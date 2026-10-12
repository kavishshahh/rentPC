import type { Metadata } from "next";
import { GeistSans } from "geist/font/sans";
import { GeistMono } from "geist/font/mono";
import "./globals.css";
import { AuthProvider } from "@/components/auth-provider";
import { Nav } from "@/components/nav";
import { WalletProvider } from "@/components/wallet-provider";


export const metadata: Metadata = {
  title: "rentPC — rent idle PCs by the hour",
  description: "Peer-to-peer compute marketplace. Hosts list idle machines; renters get an isolated VM by the hour.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`${GeistSans.variable} ${GeistMono.variable} h-full antialiased`}>
      <body className="min-h-full flex flex-col font-sans">
        <AuthProvider>
          <WalletProvider>
          <Nav />
          <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-8">{children}</main>
          <footer className="border-t py-6 text-center text-xs text-muted-foreground">
            rentPC · every rental runs in its own isolated VM · built for the Colosseum hackathon
          </footer>
          </WalletProvider>
        </AuthProvider>
      </body>
    </html>
  );
}
