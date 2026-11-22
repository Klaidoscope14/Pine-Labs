import { api } from "@/lib/api";
import type { Me } from "@/lib/types";
import { signOut, unkill } from "@/app/actions";
import { PasskeyRegister } from "@/components/client";
import { KillForm } from "./kill";

export const metadata = { title: "Settings" };

export default async function Settings() {
  const me = await api<Me>("/api/me");
  const operator = me.roles.includes("operator");
  return (<>
    <header className="top"><h1>Settings</h1></header>
    <section className="pane narrow">
      <h3>Kill switch</h3>
      {operator ? (me.killed ? (<>
        <p className="muted">On for {me.tenant}. New runs and all model calls are refused.</p>
        <form action={unkill}><button className="btn">Turn off</button></form>
      </>) : (<>
        <p className="muted">Stops every run in {me.tenant} and refuses new ones.</p>
        <KillForm tenant={me.tenant} />
      </>)) : <p className="muted">Operators only.</p>}

      <h3>Passkey</h3>
      <p className="muted">{me.passkeys ? `${me.passkeys} registered. Approvals are signed with it.` : "Sign approvals with a passkey (Touch ID, Windows Hello or a security key)."}</p>
      <PasskeyRegister count={me.passkeys} />

      <h3>Switch user</h3>
      <form action={signOut}><button className="btn">Sign out</button></form>
    </section>
  </>);
}
