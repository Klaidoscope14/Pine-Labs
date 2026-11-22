"use client";

import { useActionState } from "react";
import { signIn } from "@/app/actions";
import type { UserItem } from "@/lib/types";

export function LoginForm({ users }: { users: UserItem[] }) {
  const [state, act, pending] = useActionState(signIn, {});
  return (
    <form action={act} className="people">
      <input name="code" type="password" required placeholder="Access code" autoComplete="current-password" />
      {state.error && <p className="muted small" role="alert">{state.error}</p>}
      {users.map((u) => (
        <button key={u.id} name="user" value={u.id} className="person" disabled={pending}>
          <span className="avatar">{u.initials}</span>
          <span className="who"><b>{u.name}</b><small>{u.roles} · {u.tenant}</small></span>
        </button>
      ))}
    </form>
  );
}
