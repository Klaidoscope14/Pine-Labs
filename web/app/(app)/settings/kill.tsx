"use client";

import { useActionState } from "react";
import { kill } from "@/app/actions";

export function KillForm({ tenant }: { tenant: string }) {
  const [state, act, pending] = useActionState(kill, {});
  return (
    <form action={act} className="crow" onSubmit={(e) => { if (!confirm(`Stop every run in ${tenant}?`)) e.preventDefault(); }}>
      <input name="reason" required placeholder="Reason" className="grow" />
      <button className="btn danger" disabled={pending}>Stop everything</button>
      {state.error && <span className="muted small">{state.error}</span>}
    </form>
  );
}
