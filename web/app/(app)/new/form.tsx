"use client";

import Link from "next/link";
import { useActionState, useRef } from "react";
import { createRun } from "@/app/actions";

export function NewRunForm({ repos, scenarios }: { repos: string[]; scenarios: boolean }) {
  const [state, act, pending] = useActionState(createRun, {});
  const form = useRef<HTMLFormElement>(null);
  return (
    <form ref={form} action={act} className="card compose">
      <h1>New run</h1>
      <textarea name="requirement" rows={7} required maxLength={8000} autoFocus placeholder="Describe the change, like a ticket…"
        onKeyDown={(e) => { if ((e.metaKey || e.ctrlKey) && e.key === "Enter") form.current?.requestSubmit(); }} />
      {state.error && <p className="muted small" role="alert">{state.error}</p>}
      <div className="crow">
        <select name="repo">{repos.map((r) => <option key={r}>{r}</option>)}</select>
        {scenarios && <input name="scenario" className="ghost" placeholder="scenario (testing)" />}
        <span className="grow" />
        <Link href="/runs" className="btn">Cancel</Link>
        <button className="btn primary" disabled={pending}>{pending ? "Starting…" : <>Start run <kbd>⌘↵</kbd></>}</button>
      </div>
    </form>
  );
}
