import { proxy } from "@/lib/api";

// Forwards WebAuthn ceremonies to the Go API, which verifies them. Only these
// four operations are allowed; the run id is validated before use.
const OPS: Record<string, (run: string) => string> = {
  "register/begin": () => "/api/passkeys/register/begin",
  "register/finish": () => "/api/passkeys/register/finish",
  "approve/begin": (run) => `/api/runs/${run}/approve/begin`,
  "approve/finish": (run) => `/api/runs/${run}/approve/finish`,
};

export async function POST(req: Request, ctx: { params: Promise<{ op: string[] }> }) {
  const op = (await ctx.params).op.join("/");
  const to = OPS[op];
  if (!to) return Response.json({ error: "unknown operation" }, { status: 404 });
  const run = new URL(req.url).searchParams.get("run") ?? "";
  if (op.startsWith("approve/") && !/^[a-z0-9-]{1,80}$/.test(run)) return Response.json({ error: "bad run id" }, { status: 400 });
  const body = await req.text();
  if (body.length > 64 * 1024) return Response.json({ error: "too large" }, { status: 413 });
  return proxy(to(run), body);
}
