import "server-only";
import { createHmac, timingSafeEqual } from "node:crypto";
import { cookies } from "next/headers";

// The signed-in user lives in an HMAC-signed, HttpOnly cookie. The browser never
// sees the service token; it only ever talks to this Next.js app.

const COOKIE = "aegis_s";
const TTL_SECONDS = 12 * 60 * 60;

function secret(): string {
  const s = process.env.AEGIS_SESSION_SECRET;
  if (!s || s.length < 32) throw new Error("AEGIS_SESSION_SECRET must be at least 32 characters");
  return s;
}

const sign = (payload: string) => createHmac("sha256", secret()).update(payload).digest("base64url");

export async function getUserId(): Promise<string | null> {
  const raw = (await cookies()).get(COOKIE)?.value;
  if (!raw) return null;
  const [payload, sig] = raw.split(".");
  if (!payload || !sig) return null;
  const want = Buffer.from(sign(payload));
  const got = Buffer.from(sig);
  if (want.length !== got.length || !timingSafeEqual(want, got)) return null;
  const [id, exp] = Buffer.from(payload, "base64url").toString().split("|");
  if (!id || Number(exp) < Date.now() / 1000) return null;
  return id;
}

export async function setUserId(id: string) {
  const payload = Buffer.from(`${id}|${Math.floor(Date.now() / 1000) + TTL_SECONDS}`).toString("base64url");
  (await cookies()).set(COOKIE, `${payload}.${sign(payload)}`, {
    httpOnly: true, secure: process.env.NODE_ENV === "production", sameSite: "strict", path: "/", maxAge: TTL_SECONDS,
  });
}

export async function clearSession() {
  (await cookies()).delete(COOKIE);
}

export function accessCodeOk(given: string): boolean {
  const want = process.env.AEGIS_ACCESS_CODE ?? "";
  if (want.length < 8) return false; // refuse to run open
  const a = Buffer.from(given), b = Buffer.from(want);
  return a.length === b.length && timingSafeEqual(a, b);
}
