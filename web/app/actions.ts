"use server";

import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import { api, ApiError, service } from "@/lib/api";
import { accessCodeOk, clearSession, setUserId } from "@/lib/session";
import type { UserItem } from "@/lib/types";

// Server actions: every write goes through the Go API as the signed-in user.
// Next.js checks the Origin header on actions, so they can't be forged cross-site.

export async function signIn(_: unknown, form: FormData): Promise<{ error?: string }> {
  if (!accessCodeOk(String(form.get("code") ?? ""))) return { error: "Wrong access code." };
  const id = String(form.get("user") ?? "");
  const users = await service<UserItem[]>("/api/users");
  if (!users.some((u) => u.id === id)) return { error: "Pick who you are." };
  await setUserId(id);
  redirect("/runs");
}

export async function signOut() {
  await clearSession();
  redirect("/login");
}

const msg = (e: unknown) => (e instanceof ApiError ? e.message : "Something went wrong.");

export async function createRun(_: unknown, form: FormData): Promise<{ error?: string }> {
  let id = "";
  try {
    const res = await api<{ run_id: string }>("/api/runs", {
      method: "POST",
      body: JSON.stringify({
        requirement: String(form.get("requirement") ?? ""),
        repo: String(form.get("repo") ?? ""),
        scenario: String(form.get("scenario") ?? ""),
      }),
    });
    id = res.run_id;
  } catch (e) {
    return { error: msg(e) };
  }
  redirect(`/runs/${id}`);
}

export async function decide(id: string, approve: boolean): Promise<{ error?: string }> {
  try {
    await api(`/api/runs/${encodeURIComponent(id)}/decision`, { method: "POST", body: JSON.stringify({ approve }) });
  } catch (e) {
    return { error: msg(e) };
  }
  await new Promise((r) => setTimeout(r, 700)); // let the workflow record it
  revalidatePath(`/runs/${id}`);
  return {};
}

export async function kill(_: unknown, form: FormData): Promise<{ error?: string }> {
  try {
    await api("/api/kill", { method: "POST", body: JSON.stringify({ reason: String(form.get("reason") ?? "") }) });
  } catch (e) {
    return { error: msg(e) };
  }
  revalidatePath("/settings");
  return {};
}

export async function unkill(): Promise<void> {
  await api("/api/unkill", { method: "POST", body: "{}" });
  revalidatePath("/settings");
}
