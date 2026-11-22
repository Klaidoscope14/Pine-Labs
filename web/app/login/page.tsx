import { service } from "@/lib/api";
import type { UserItem } from "@/lib/types";
import { LoginForm } from "./form";

export const metadata = { title: "Sign in" };
export const dynamic = "force-dynamic";

export default async function Login() {
  let users: UserItem[] = [];
  let down = false;
  try { users = await service<UserItem[]>("/api/users"); } catch { down = true; }
  return (
    <div className="center">
      <div className="card login">
        <div className="brand big"><span className="logo" />Aegis</div>
        <p className="muted">{down ? "Can't reach the Aegis server right now." : "Choose who you are"}</p>
        {!down && <LoginForm users={users} />}
        <p className="hint">Demo sign-in behind an access code. Passkeys sign approvals.</p>
      </div>
    </div>
  );
}
