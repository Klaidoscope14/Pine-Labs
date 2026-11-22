import "server-only";
import { redirect } from "next/navigation";
import { getUserId } from "./session";

// Server-to-server calls to the Aegis Go API (on AWS, through CloudFront). The
// service token and the API address stay on the server.

const base = () => {
  const u = process.env.AEGIS_API_URL;
  if (!u) throw new Error("AEGIS_API_URL is not set");
  return u.replace(/\/$/, "");
};

export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

async function call<T>(path: string, init: RequestInit & { user?: string | null } = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("X-Aegis-Service-Token", process.env.AEGIS_BFF_TOKEN ?? "");
  if (init.user) headers.set("X-Aegis-User", init.user);
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const res = await fetch(base() + path, { ...init, headers, cache: "no-store" });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new ApiError(res.status, (data && data.error) || res.statusText);
  return data as T;
}

/** Calls the API as the signed-in user; sends them to sign-in if there is none. */
export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const user = await getUserId();
  if (!user) redirect("/login");
  try {
    return await call<T>(path, { ...init, user });
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) redirect("/login");
    throw e;
  }
}

/** Service-only call (no user), e.g. the sign-in picker. */
export const service = <T>(path: string) => call<T>(path);

/** Raw proxy for WebAuthn ceremonies: forwards the JSON body unchanged. */
export async function proxy(path: string, body: string): Promise<Response> {
  const user = await getUserId();
  if (!user) return Response.json({ error: "sign in required" }, { status: 401 });
  const res = await fetch(base() + path, {
    method: "POST", cache: "no-store", body,
    headers: { "X-Aegis-Service-Token": process.env.AEGIS_BFF_TOKEN ?? "", "X-Aegis-User": user, "Content-Type": "application/json" },
  });
  return new Response(await res.text(), { status: res.status, headers: { "Content-Type": "application/json" } });
}
