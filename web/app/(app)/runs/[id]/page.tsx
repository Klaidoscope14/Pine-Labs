import Link from "next/link";
import { notFound } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import type { RunView } from "@/lib/types";
import { DecideButtons, LiveRefresh, PasskeyApprove } from "@/components/client";

const TABS = ["overview", "changes", "checks", "activity"] as const;

export async function generateMetadata({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  try {
    return { title: (await api<RunView>(`/api/views/runs/${encodeURIComponent(id)}`)).Title };
  } catch {
    return { title: "Run" };
  }
}

export default async function Run({ params, searchParams }: { params: Promise<{ id: string }>; searchParams: Promise<{ tab?: string }> }) {
  const { id } = await params;
  const t = (await searchParams).tab;
  const tab = (TABS as readonly string[]).includes(t ?? "") ? t! : "overview";
  let r: RunView;
  try {
    r = await api<RunView>(`/api/views/runs/${encodeURIComponent(id)}?tab=${tab}`);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) notFound();
    throw e;
  }
  const approvals = (r.Approvals ?? []).filter((a) => a.decision === "APPROVE");
  const live = !["ok", "bad", "muted"].includes(r.Tone);
  return (<>
    {live && <LiveRefresh />}
    <header className="top run">
      <div className="crumbs"><Link href="/runs">Runs</Link><span>/</span><span className="rid">{id.slice(0, 8)}</span></div>
      <h1>{r.Title}</h1>
      <div className="meta">
        <span className={`pill ${r.Tone}`}><span className={`dot ${r.Tone}`} />{r.Status}</span>
        {r.Tier === "high" && <span className="tag">High risk</span>}
        <span className="muted">{r.Cost} · {r.Age} ago</span>
      </div>
    </header>

    <ol className="strip">{r.Steps.map((s) => <li key={s.Name} className={s.State}><span />{s.Name}</li>)}</ol>

    {r.InReview && (
      <section className="decide">
        <div className="need">
          <b>{r.Have} of {r.Need}</b> approval{r.Need > 1 ? "s" : ""}
          {approvals.map((a) => <span key={a.reviewer} className="chip">✓ {a.reviewer}</span>)}
        </div>
        {r.CanDecide ? (
          r.HasPasskey ? (<div className="actions"><DecideButtons id={id} canApprove={false} /><PasskeyApprove id={id} /></div>)
            : r.NeedPasskey ? (<div className="actions"><DecideButtons id={id} canApprove={false} /><Link className="btn" href="/settings">Add a passkey to approve</Link></div>)
              : <DecideButtons id={id} canApprove />
        ) : <span className="muted">{r.Why}</span>}
      </section>
    )}

    <nav className="tabs under">
      {TABS.map((k) => <Link key={k} href={`?tab=${k}`} className={tab === k ? "on" : ""}>{k[0].toUpperCase() + k.slice(1)}</Link>)}
    </nav>

    {tab === "overview" && (
      <section className="pane overview">
        {r.HasPacket ? (<>
          <p className="summary">{r.Packet.summary}</p>
          {!!r.Packet.risks?.length && (<><h3>Risks</h3><ul className="risks">{r.Packet.risks.map((x) => <li key={x}>{x}</li>)}</ul></>)}
        </>) : <p className="muted">The summary appears when the change is ready for review.</p>}
        <dl className="facts">
          <div><dt>Requested by</dt><dd>{r.Submitter || "—"}</dd></div>
          <div><dt>Files changed</dt><dd>{r.Files}</dd></div>
          <div><dt>Model calls</dt><dd>{r.Calls}</dd></div>
          <div><dt>Tokens</dt><dd>{r.Tokens}</dd></div>
        </dl>
      </section>
    )}
    {tab === "changes" && (
      <section className="pane">
        {(r.Diff ?? []).length ? r.Diff!.map((f) => (
          <details key={f.Path} className="file" open>
            <summary><span className="fpath">{f.Path}</span><span className="add">+{f.Add}</span><span className="del">−{f.Del}</span></summary>
            <pre className="diff">{(f.Lines ?? []).map((l, i) => <span key={i} className={l.Kind}>{l.Text}</span>)}</pre>
            {f.Truncated && <p className="muted small">Long file shortened.</p>}
          </details>
        )) : <p className="muted">No changes yet.</p>}
      </section>
    )}
    {tab === "checks" && (
      <section className="pane">
        {(r.Checks ?? []).length ? (<>
          <ul className="checks">{r.Checks!.map((g) => (
            <li key={g.Name}>
              <div className="crow"><span className={`dot ${g.Tone}`} /><b>{g.Name}</b>
                <span className="muted">{g.Summary}{g.Reused ? " · reused" : ""}</span><span className={`verdict ${g.Tone}`}>{g.Verdict}</span></div>
              {(g.Findings ?? []).map((f, i) => <div key={i} className="finding"><code>{f.Rule}</code><span className="where">{f.Where}</span><span>{f.Message}</span></div>)}
            </li>
          ))}</ul>
          <p className="muted small">Attempt {r.Attempt}</p>
        </>) : <p className="muted">Checks run after the change is built.</p>}
      </section>
    )}
    {tab === "activity" && (
      <section className="pane">
        <ol className="timeline">{(r.Activity ?? []).map((a, i) => <li key={i}><span className={`dot ${a.Tone}`} /><span>{a.Text}</span><time>{a.When}</time></li>)}</ol>
      </section>
    )}
  </>);
}
