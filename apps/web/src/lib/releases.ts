// Where prebuilt agent / CLI binaries live. Built by .github/workflows/release.yml
// on every `v*` tag; GitHub keeps `releases/latest/download/<asset>` pointing at
// the newest release.
export const RELEASES_URL =
  process.env.NEXT_PUBLIC_RELEASES_URL ?? "https://github.com/kavishshahh/rentPC/releases/latest/download";

export type Platform = "windows" | "darwin" | "linux";

export const PLATFORMS: { id: Platform; label: string; blurb: string }[] = [
  { id: "windows", label: "Windows", blurb: "Windows 10/11, Home or Pro. Uses Windows Hypervisor Platform." },
  { id: "darwin", label: "macOS", blurb: "macOS 13+, Apple Silicon or Intel. Uses Apple's Hypervisor framework." },
  { id: "linux", label: "Linux", blurb: "Ubuntu/Debian/Fedora with KVM. Future GPU tier lands here first." },
];

export function platformLabel(p: string) {
  return PLATFORMS.find((x) => x.id === p)?.label ?? p;
}

export function agentAsset(p: Platform, arch: "amd64" | "arm64") {
  return `rentpc-host-${p}-${arch}${p === "windows" ? ".exe" : ""}`;
}

export function cliAsset(p: Platform, arch: "amd64" | "arm64") {
  return `rentpc-${p}-${arch}${p === "windows" ? ".exe" : ""}`;
}

/** Detects the visitor's OS from the browser; falls back to windows. */
export function detectPlatform(): Platform {
  if (typeof navigator === "undefined") return "windows";
  const ua = navigator.userAgent;
  if (/Mac/i.test(ua)) return "darwin";
  if (/Linux/i.test(ua) && !/Android/i.test(ua)) return "linux";
  return "windows";
}

/**
 * One-paste setup for the host agent: download → register → run.
 * `server` is the API/public URL, `token` the one-time registration token
 * (may be empty, in which case a placeholder is shown).
 */
export function hostSetupScript(p: Platform, server: string, token: string): string {
  const t = token || "<token>";
  switch (p) {
    case "windows":
      return [
        `# PowerShell — run as your normal user`,
        `mkdir "$env:LOCALAPPDATA\\rentpc" -Force | Out-Null; cd "$env:LOCALAPPDATA\\rentpc"`,
        `iwr -useb ${RELEASES_URL}/${agentAsset("windows", "amd64")} -OutFile rentpc-host.exe`,
        `.\\rentpc-host.exe doctor`,
        `.\\rentpc-host.exe register --server ${server} --token ${t}`,
        `.\\rentpc-host.exe run`,
      ].join("\n");
    case "darwin":
      return [
        `# Terminal`,
        `brew install qemu`,
        `ARCH=$([ "$(uname -m)" = "arm64" ] && echo arm64 || echo amd64)`,
        `mkdir -p ~/.rentpc && cd ~/.rentpc`,
        `curl -fsSL -o rentpc-host ${RELEASES_URL}/rentpc-host-darwin-$ARCH && chmod +x rentpc-host`,
        `./rentpc-host doctor`,
        `./rentpc-host register --server ${server} --token ${t}`,
        `./rentpc-host run`,
      ].join("\n");
    case "linux":
      return [
        `# Terminal`,
        `sudo apt-get install -y qemu-system-x86 qemu-utils   # Fedora: sudo dnf install qemu-kvm qemu-img`,
        `sudo usermod -aG kvm $USER   # log out/in once if /dev/kvm was not accessible`,
        `ARCH=$([ "$(uname -m)" = "aarch64" ] && echo arm64 || echo amd64)`,
        `mkdir -p ~/.rentpc && cd ~/.rentpc`,
        `curl -fsSL -o rentpc-host ${RELEASES_URL}/rentpc-host-linux-$ARCH && chmod +x rentpc-host`,
        `./rentpc-host doctor`,
        `./rentpc-host register --server ${server} --token ${t}`,
        `./rentpc-host run`,
      ].join("\n");
  }
}

/** Prerequisite notes shown above the script. */
export function hostPrereqs(p: Platform): string[] {
  switch (p) {
    case "windows":
      return [
        "Install QEMU: https://qemu.weilnetz.de/w64/ → latest qemu-w64-setup-*.exe, then add C:\\Program Files\\qemu to PATH.",
        "Turn Windows features on or off → tick “Windows Hypervisor Platform” and “Virtual Machine Platform” → reboot. (Works on Home; Hyper-V is not required.)",
        "Make sure virtualization is enabled in BIOS (Task Manager → Performance → CPU → Virtualization: Enabled).",
      ];
    case "darwin":
      return [
        "Install Homebrew if you don't have it: https://brew.sh — the script installs QEMU with it.",
        "macOS 13 or newer. Apple Silicon Macs run arm64 Ubuntu guests; Intel Macs run x86_64 guests.",
        "The first `run` downloads an Ubuntu cloud image (~600 MB) once.",
      ];
    case "linux":
      return [
        "/dev/kvm must be accessible; enable VT-x / AMD-V in BIOS if it's missing.",
        "The first `run` downloads an Ubuntu cloud image (~600 MB) once.",
      ];
  }
}

/** Renter CLI setup for the rental page. */
export function cliSetup(p: Platform, server: string, rentalId: string, shortId: string): { install: string; connect: string; keyNote: string } {
  switch (p) {
    case "windows":
      return {
        install: [
          `mkdir "$env:LOCALAPPDATA\\rentpc" -Force | Out-Null`,
          `iwr -useb ${RELEASES_URL}/${cliAsset("windows", "amd64")} -OutFile "$env:LOCALAPPDATA\\rentpc\\rentpc.exe"`,
          `$env:RENTPC_API_KEY = "rpk_..."   # Account → API keys`,
        ].join("\n"),
        keyNote: `icacls "$HOME\\Downloads\\rentpc_${shortId}.key" /inheritance:r /grant:r "$($env:USERNAME):R"`,
        connect: `ssh -i "$HOME\\Downloads\\rentpc_${shortId}.key" -o ProxyCommand="$env:LOCALAPPDATA\\rentpc\\rentpc.exe tunnel --server ${server} ${rentalId}" -o StrictHostKeyChecking=no renter@rentpc`,
      };
    default: {
      const os = p === "darwin" ? "darwin" : "linux";
      const archExpr = p === "darwin" ? `$([ "$(uname -m)" = "arm64" ] && echo arm64 || echo amd64)` : `$([ "$(uname -m)" = "aarch64" ] && echo arm64 || echo amd64)`;
      return {
        install: [
          `mkdir -p ~/.rentpc`,
          `curl -fsSL -o ~/.rentpc/rentpc ${RELEASES_URL}/rentpc-${os}-${archExpr} && chmod +x ~/.rentpc/rentpc`,
          `export RENTPC_API_KEY=rpk_...   # Account → API keys`,
        ].join("\n"),
        keyNote: `chmod 600 ~/Downloads/rentpc_${shortId}.key`,
        connect: `ssh -i ~/Downloads/rentpc_${shortId}.key -o ProxyCommand="~/.rentpc/rentpc tunnel --server ${server} ${rentalId}" -o StrictHostKeyChecking=no renter@rentpc`,
      };
    }
  }
}
