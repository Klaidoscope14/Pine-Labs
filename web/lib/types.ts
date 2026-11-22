// Shapes returned by the Aegis Go API (internal/web/views.go).

export type Tone = "active" | "review" | "ok" | "bad" | "muted" | "wait";

export interface Me {
  id: string; name: string; initials: string; tenant: string; roles: string[];
  reviewCount: number; killed: string; repos: string[]; scenarios: boolean;
  passkeys: number; needPasskey: boolean;
}

export interface UserItem { id: string; name: string; initials: string; tenant: string; roles: string }

export interface RunRow { ID: string; Title: string; Status: string; Tone: Tone; Tier: string; Cost: string; Age: string }

export interface Step { Name: string; State: "done" | "now" | "fail" | "todo" }
export interface DiffLine { Kind: "add" | "del" | "hunk" | "ctx"; Text: string }
export interface DiffFile { Path: string; Add: number; Del: number; Lines: DiffLine[] | null; Truncated: boolean }
export interface Finding { Rule: string; Where: string; Message: string; Tone: Tone }
export interface Gate { Name: string; Verdict: string; Tone: Tone; Summary: string; Findings: Finding[] | null; Reused: boolean }
export interface Activity { Text: string; When: string; Tone: Tone }
export interface Approval { reviewer: string; decision: string; head_sha: string; note: string; created_at: string }

export interface RunView {
  ID: string; Title: string; Status: string; Tone: Tone; Tier: string; Submitter: string; Cost: string; Age: string;
  Steps: Step[]; Packet: { pr_title: string; summary: string; risks: string[] | null }; HasPacket: boolean;
  Diff: DiffFile[] | null; Checks: Gate[] | null; Attempt: number; Activity: Activity[] | null;
  Approvals: Approval[] | null; Need: number; Have: number;
  InReview: boolean; CanDecide: boolean; HasPasskey: boolean; NeedPasskey: boolean; Why: string;
  Calls: number; Tokens: string; Files: number;
}
