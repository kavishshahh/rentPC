"use client";

import * as React from "react";
import "@xterm/xterm/css/xterm.css";
import { API_URL } from "@/lib/api";

// Browser terminal: xterm.js ⇄ WebSocket /v1/rentals/{id}/terminal ⇄ relay ⇄ agent ⇄ sandbox PTY.
export function Terminal({ rentalId, onConnected }: { rentalId: string; onConnected?: () => void }) {
  const ref = React.useRef<HTMLDivElement>(null);
  const [state, setState] = React.useState<"connecting" | "open" | "closed" | "error">("connecting");
  const [msg, setMsg] = React.useState("");
  const [gen, setGen] = React.useState(0);

  React.useEffect(() => {
    let disposed = false;
    let ws: WebSocket | null = null;
    let cleanup = () => {};
    (async () => {
      const [{ Terminal: XTerm }, { FitAddon }] = await Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit")]);
      if (disposed || !ref.current) return;
      const term = new XTerm({ cursorBlink: true, fontSize: 13, fontFamily: "Menlo, Consolas, 'DejaVu Sans Mono', 'Liberation Mono', monospace", theme: { background: "#0b0b0d" }, scrollback: 5000 });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(ref.current);
      fit.fit();
      const url = `${API_URL.replace(/^http/, "ws")}/v1/rentals/${rentalId}/terminal?cols=${term.cols}&rows=${term.rows}`;
      ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      ws.onopen = () => { setState("open"); onConnected?.(); term.focus(); };
      ws.onmessage = (ev) => { if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data)); };
      ws.onerror = () => { setState("error"); setMsg("Connection failed — the sandbox may still be booting."); };
      ws.onclose = (ev) => { setState((s) => (s === "error" ? s : "closed")); if (ev.reason) setMsg(ev.reason); };
      const enc = new TextEncoder();
      const d1 = term.onData((data) => ws?.readyState === 1 && ws.send(enc.encode(data)));
      const d2 = term.onResize(({ cols, rows }) => ws?.readyState === 1 && ws.send(JSON.stringify({ type: "resize", cols, rows })));
      const ro = new ResizeObserver(() => fit.fit());
      ro.observe(ref.current);
      cleanup = () => { d1.dispose(); d2.dispose(); ro.disconnect(); term.dispose(); };
    })();
    return () => { disposed = true; cleanup(); ws?.close(); };
  }, [rentalId, gen, onConnected]);

  return (
    <div className="overflow-hidden rounded-lg border bg-[#0b0b0d]">
      <div className="flex items-center justify-between border-b border-white/10 px-3 py-1.5 text-xs text-white/70">
        <span>renter@sandbox — {state === "open" ? "connected" : state === "connecting" ? "connecting…" : state}</span>
        {state !== "open" && state !== "connecting" && (
          <button className="underline" onClick={() => { setState("connecting"); setMsg(""); setGen((g) => g + 1); }}>reconnect</button>
        )}
      </div>
      <div ref={ref} className="h-[420px] p-2" />
      {msg && <div className="border-t border-white/10 px-3 py-1.5 text-xs text-amber-300">{msg}</div>}
    </div>
  );
}
