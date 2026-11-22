"use client";

import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";

export function Nav({ reviewCount }: { reviewCount: number }) {
  const path = usePathname();
  const view = useSearchParams().get("view");
  const on = (k: string) =>
    (k === "review" && path === "/runs" && view === "review") ||
    (k === "runs" && path.startsWith("/runs") && view !== "review") ||
    (k === "settings" && path === "/settings") ? "on" : "";
  return (
    <nav>
      <Link href="/runs" className={on("runs")}><i className="ic ic-runs" />Runs</Link>
      <Link href="/runs?view=review" className={on("review")}><i className="ic ic-review" />Review
        {reviewCount > 0 && <span className="count">{reviewCount}</span>}</Link>
      <Link href="/settings" className={on("settings")}><i className="ic ic-settings" />Settings</Link>
    </nav>
  );
}
