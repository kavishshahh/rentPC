"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { Cpu, LogOut } from "lucide-react";
import { useAuth } from "@/components/auth-provider";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const links = [
  { href: "/", label: "Marketplace" },
  { href: "/rentals", label: "My rentals" },
  { href: "/host", label: "Host" },
];

export function Nav() {
  const { user, loading, logout } = useAuth();
  const path = usePathname();
  const router = useRouter();
  return (
    <header className="border-b bg-background/80 backdrop-blur sticky top-0 z-20">
      <div className="mx-auto flex h-14 max-w-6xl items-center gap-6 px-4">
        <Link href="/" className="flex items-center gap-2 font-semibold">
          <Cpu className="size-5" /> rentPC
        </Link>
        <nav className="flex items-center gap-1 text-sm">
          {links.map((l) => (
            <Link
              key={l.href}
              href={l.href}
              className={cn(
                "rounded-md px-3 py-1.5 text-muted-foreground hover:text-foreground",
                (l.href === "/" ? path === "/" : path.startsWith(l.href)) && "bg-accent text-foreground"
              )}
            >
              {l.label}
            </Link>
          ))}
        </nav>
        <div className="ml-auto flex items-center gap-2 text-sm">
          {loading ? null : user ? (
            <>
              <Link href="/account" className="text-muted-foreground hover:text-foreground">
                {user.display_name}
              </Link>
              <Button
                variant="ghost"
                size="sm"
                onClick={async () => {
                  await logout();
                  router.push("/");
                  router.refresh();
                }}
              >
                <LogOut /> Sign out
              </Button>
            </>
          ) : (
            <>
              <Button variant="ghost" size="sm" onClick={() => router.push("/login")}>Sign in</Button>
              <Button size="sm" onClick={() => router.push("/register")}>Create account</Button>
            </>
          )}
        </div>
      </div>
    </header>
  );
}
