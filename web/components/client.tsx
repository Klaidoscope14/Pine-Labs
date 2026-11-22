"use client";

import { useRouter } from "next/navigation";
import { useEffect, useMemo, useRef, useState, useTransition } from "react";
import { decide } from "@/app/actions";

/** Re-fetches the page every few seconds while it's visible (live status). */
export function LiveRefresh({ every = 3000 }: { every?: number }) {
  const router = useRouter();
  useEffect(() => {
    const t = setInterval(() => {
      const a = document.activeElement;
      if (document.visibilityState !== "visible") return;
      if (a && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName)) return;
      if (document.getElementById("palette")?.dataset.open === "1") return;
      router.refresh();
    }, every);
    return () => clearInterval(t);
  }, [router, every]);
  return null;
}

type Item = [label: string, href: string, hint: string];

/** ⌘K command palette and the C shortcut for a new run. */
export function Palette({ runs }: { runs: Item[] }) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const items = useMemo<Item[]>(() => {
    const all: Item[] = [["New run", "/new", "C"], ["Runs", "/runs", ""], ["Review", "/runs?view=review", ""],
      ["Done", "/runs?view=done", ""], ["Settings", "/settings", ""], ...runs];
    const s = q.toLowerCase();
    return all.filter((a) => a[0].toLowerCase().includes(s) || a[2].toLowerCase().includes(s)).slice(0, 12);
  }, [q, runs]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const typing = e.target instanceof HTMLElement && /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName);
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") { e.preventDefault(); setOpen((o) => !o); setQ(""); setSel(0); return; }
      if (e.key === "Escape") { setOpen(false); return; }
      if (!typing && !e.metaKey && !e.ctrlKey && !e.altKey && e.key === "c") router.push("/new");
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [router]);
  useEffect(() => { if (open) input.current?.focus(); }, [open]);

  const go = (href: string) => { setOpen(false); router.push(href); };
  return (
    <div className="palette" id="palette" data-open={open ? "1" : "0"} hidden={!open} onClick={(e) => e.target === e.currentTarget && setOpen(false)}>
      <div className="pbox">
        <input ref={input} value={q} placeholder="Type a command or search…" autoComplete="off"
          onChange={(e) => { setQ(e.target.value); setSel(0); }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") { setSel((s) => Math.min(s + 1, items.length - 1)); e.preventDefault(); }
            if (e.key === "ArrowUp") { setSel((s) => Math.max(s - 1, 0)); e.preventDefault(); }
            if (e.key === "Enter" && items[sel]) go(items[sel][1]);
          }} />
        <ul>{items.map((a, i) => (
          <li key={a[1]} className={i === sel ? "sel" : ""} onClick={() => go(a[1])}><span>{a[0]}</span><span>{a[2]}</span></li>
        ))}</ul>
      </div>
    </div>
  );
}

/** Approve / Reject (plain), with errors shown inline. */
export function DecideButtons({ id, canApprove }: { id: string; canApprove: boolean }) {
  const [pending, start] = useTransition();
  const [err, setErr] = useState("");
  const act = (approve: boolean) => start(async () => { const r = await decide(id, approve); setErr(r.error ?? ""); });
  return (
    <div className="actions">
      {err && <span className="muted small">{err}</span>}
      <button className="btn" disabled={pending} onClick={() => act(false)}>Reject</button>
      {canApprove && <button className="btn primary" disabled={pending} onClick={() => act(true)}>Approve</button>}
    </div>
  );
}

// ---- passkeys (WebAuthn) ----

const b64u = {
  dec: (s: string) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4)), (c) => c.charCodeAt(0)),
  enc: (b: ArrayBuffer) => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""),
};

async function post(url: string, body?: unknown) {
  const r = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body ?? {}) });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}

export function PasskeyRegister({ count }: { count: number }) {
  const router = useRouter();
  const [msg, setMsg] = useState("");
  const add = async () => {
    try {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const o: any = (await post("/api/passkey/register/begin")).publicKey;
      o.challenge = b64u.dec(o.challenge);
      o.user.id = b64u.dec(o.user.id);
      (o.excludeCredentials || []).forEach((c: { id: string | Uint8Array }) => (c.id = b64u.dec(c.id as string)));
      const c = (await navigator.credentials.create({ publicKey: o })) as PublicKeyCredential;
      const res = c.response as AuthenticatorAttestationResponse;
      await post("/api/passkey/register/finish", {
        id: c.id, rawId: b64u.enc(c.rawId), type: c.type,
        response: { attestationObject: b64u.enc(res.attestationObject), clientDataJSON: b64u.enc(res.clientDataJSON),
          transports: res.getTransports ? res.getTransports() : [] },
      });
      router.refresh();
    } catch (e) { setMsg("Passkey not added: " + (e as Error).message); }
  };
  return (<>
    <button type="button" className="btn" onClick={add}>{count ? "Add another" : "Add passkey"}</button>
    {msg && <p className="muted small">{msg}</p>}
  </>);
}

export function PasskeyApprove({ id }: { id: string }) {
  const router = useRouter();
  const [busy, setBusy] = useState(false);
  const approve = async () => {
    setBusy(true);
    try {
      const q = `?run=${encodeURIComponent(id)}`;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const o: any = (await post("/api/passkey/approve/begin" + q)).publicKey;
      o.challenge = b64u.dec(o.challenge);
      (o.allowCredentials || []).forEach((c: { id: string | Uint8Array }) => (c.id = b64u.dec(c.id as string)));
      const a = (await navigator.credentials.get({ publicKey: o })) as PublicKeyCredential;
      const res = a.response as AuthenticatorAssertionResponse;
      await post("/api/passkey/approve/finish" + q, {
        id: a.id, rawId: b64u.enc(a.rawId), type: a.type,
        response: { authenticatorData: b64u.enc(res.authenticatorData), clientDataJSON: b64u.enc(res.clientDataJSON),
          signature: b64u.enc(res.signature), userHandle: res.userHandle ? b64u.enc(res.userHandle) : null },
      });
      setTimeout(() => router.refresh(), 800);
    } catch (e) { alert("Not approved: " + (e as Error).message); setBusy(false); }
  };
  return <button className="btn primary" disabled={busy} onClick={approve}>Approve with passkey</button>;
}
