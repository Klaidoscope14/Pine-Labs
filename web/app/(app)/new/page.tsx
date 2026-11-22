import { api } from "@/lib/api";
import type { Me } from "@/lib/types";
import { NewRunForm } from "./form";

export const metadata = { title: "New run" };

export default async function NewRun() {
  const me = await api<Me>("/api/me");
  return (<div className="center top-pad"><NewRunForm repos={me.repos} scenarios={me.scenarios} /></div>);
}
