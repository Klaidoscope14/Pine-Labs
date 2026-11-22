import Link from "next/link";
import { api } from "@/lib/api";
import type { RunRow } from "@/lib/types";
import { LiveRefresh } from "@/components/client";

export const metadata = { title: "Runs" };

export default async function Runs({ searchParams }: { searchParams: Promise<{ view?: string }> }) {
  const { view: v } = await searchParams;
  const { view, rows } = await api<{ view: string; rows: RunRow[] }>(`/api/views/runs?view=${encodeURIComponent(v ?? "active")}`);
  const tab = (k: string, label: string) => (
    <Link href={k === "active" ? "/runs" : `/runs?view=${k}`} className={view === k ? "on" : ""}>{label}</Link>
  );
  return (<>
    <LiveRefresh />
    <header className="top">
      <h1>{view === "review" ? "Review" : "Runs"}</h1>
      {view !== "review" && <div className="tabs">{tab("active", "Active")}{tab("review", "Review")}{tab("done", "Done")}</div>}
    </header>
    {rows.length ? (
      <ul className="list">
        {rows.map((r) => (
          <li key={r.ID}><Link href={`/runs/${r.ID}`} className="row">
            <span className={`dot ${r.Tone}`} />
            <span className="rid">{r.ID.slice(0, 8)}</span>
            <span className="rtitle">{r.Title}</span>
            {r.Tier === "high" && <span className="tag">High risk</span>}
            <span className="rstatus">{r.Status}</span>
            <span className="rcost">{r.Cost}</span>
            <span className="rage">{r.Age}</span>
          </Link></li>
        ))}
      </ul>
    ) : (
      <div className="empty">
        <p>{view === "review" ? "Nothing to review." : view === "done" ? "No finished runs yet." : "No active runs."}</p>
        {view === "active" && <Link className="btn primary" href="/new">New run</Link>}
      </div>
    )}
  </>);
}
