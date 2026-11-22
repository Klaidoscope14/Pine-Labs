import Link from "next/link";
import { api } from "@/lib/api";
import type { Me, RunRow } from "@/lib/types";
import { Palette } from "@/components/client";
import { Nav } from "./nav";

export const dynamic = "force-dynamic";

export default async function AppLayout({ children }: { children: React.ReactNode }) {
  const me = await api<Me>("/api/me");
  const recent = await api<{ rows: RunRow[] }>("/api/views/runs?view=active").catch(() => ({ rows: [] as RunRow[] }));
  return (
    <div className="shell">
      <aside className="side">
        <Link className="brand" href="/runs"><span className="logo" />Aegis</Link>
        <Link className="newbtn" href="/new"><span>New run</span><kbd>C</kbd></Link>
        <Nav reviewCount={me.reviewCount} />
        <div className="me">
          <span className="avatar">{me.initials}</span>
          <span className="who"><b>{me.name}</b><small>{me.tenant}</small></span>
        </div>
      </aside>
      <main>
        {me.killed && <div className="banner">Kill switch on — {me.killed}</div>}
        {children}
      </main>
      <Palette runs={recent.rows.map((r) => [r.Title, `/runs/${r.ID}`, r.ID.slice(0, 8)])} />
    </div>
  );
}
