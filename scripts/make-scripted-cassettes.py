"""Write SCRIPTED replay cassettes for the live-happy scenario.

These are hand-written stand-ins for model responses, so the full real-agent path
(router, data filter, gateway, tool loop, contracts, gates, ledger) can run offline
and deterministically. They are not recordings of a real model. Live Bedrock runs
record real cassettes in the same format, which replace these.

Usage: python scripts/make-scripted-cassettes.py <repo-root>
"""

import json
import sys
from pathlib import Path

root = Path(sys.argv[1])
sample = root / "samples" / "pine-settlements-svc"
out = root / "evals" / "cassettes" / "live-happy"
out.mkdir(parents=True, exist_ok=True)
common = root / "evals" / "scenarios" / "_common"


def message(content, stop, n_in=1200, n_out=300):
    return {
        "id": "msg_scripted",
        "type": "message",
        "role": "assistant",
        "model": "scripted",
        "content": content,
        "stop_reason": stop,
        "stop_sequence": None,
        "usage": {"input_tokens": n_in, "output_tokens": n_out, "cache_read_input_tokens": 0,
                  "cache_creation_input_tokens": 0, "inference_geo": "scripted"},
    }


def write(name, response):
    (out / f"{name}.json").write_text(json.dumps({"request": None, "response": response}, indent=2))


def text_reply(obj):
    return message([{"type": "text", "text": json.dumps(obj)}], "end_turn")


tool_n = 0


def tool(name, args):
    global tool_n
    tool_n += 1
    return message([{"type": "tool_use", "id": f"toolu_scripted_{tool_n:03d}", "name": name, "input": args}], "tool_use")


spec = json.loads((common / "requirement_spec.json").read_text())
spec.setdefault("split_into", [])
plan = json.loads((common / "architecture_plan.json").read_text())
write("analyst-a0-000", text_reply(spec))
write("architect-a0-000", text_reply(plan))

handler_old = (
    '    with db.transaction() as conn:\n'
    '        store.credit(conn, event.merchant_id, event.amount_paise, reference=event.event_id)\n'
    '    return {"status": "credited"}'
)
handler_new = (
    '    with db.transaction() as conn:\n'
    '        # The bank retries deliveries: record each event_id once, credit only on first sight.\n'
    '        inserted = conn.execute(\n'
    '            "INSERT OR IGNORE INTO processed_events (event_id) VALUES (?)", (event.event_id,)\n'
    '        ).rowcount\n'
    '        if inserted == 0:\n'
    '            return {"status": "duplicate"}\n'
    '        store.credit(conn, event.merchant_id, event.amount_paise, reference=event.event_id)\n'
    '    return {"status": "credited"}'
)
db_old = "CREATE TABLE IF NOT EXISTS payments ("
db_new = (
    "CREATE TABLE IF NOT EXISTS processed_events (\n"
    "    event_id TEXT PRIMARY KEY,\n"
    "    processed_at TEXT NOT NULL DEFAULT (datetime('now'))\n"
    ");\n"
    "CREATE TABLE IF NOT EXISTS payments ("
)
test_old = '    assert client.get("/merchants/m42/balance").json()["balance_paise"] == 250_000\n'
test_new = test_old + (
    "\n\ndef test_duplicate_event_credits_once(client, send_webhook):\n"
    "    send_webhook(EVENT)\n"
    '    assert send_webhook(EVENT).json() == {"status": "duplicate"}\n'
    '    assert client.get("/merchants/m42/balance").json()["balance_paise"] == 125_000\n'
)
for old, f in [(handler_old, "app/webhooks/handler.py"), (db_old, "app/db.py"), (test_old, "tests/test_webhooks.py")]:
    assert (sample / f).read_text().count(old) == 1, f"edit anchor not unique in {f}"

steps = [
    tool("read_file", {"path": "app/webhooks/handler.py", "start_line": 0, "end_line": 0}),
    tool("read_file", {"path": "app/db.py", "start_line": 0, "end_line": 0}),
    tool("read_file", {"path": "tests/test_webhooks.py", "start_line": 0, "end_line": 0}),
    tool("edit_file", {"path": "app/db.py", "old_text": db_old, "new_text": db_new}),
    tool("edit_file", {"path": "app/webhooks/handler.py", "old_text": handler_old, "new_text": handler_new}),
    tool("edit_file", {"path": "tests/test_webhooks.py", "old_text": test_old, "new_text": test_new}),
    tool("run_tests", {"selector": "tests/test_webhooks.py"}),
    tool("finish", {"summary": "Settlement webhooks are now idempotent: each event_id is recorded in "
                               "processed_events inside the crediting transaction, and duplicates are acknowledged "
                               "without crediting again.",
                    "decisions": [{"decision": "INSERT OR IGNORE on a primary key, in the same transaction as the credit",
                                   "reason": "the database guarantees one credit per event even under concurrent deliveries"}]}),
]
for i, step in enumerate(steps):
    write(f"developer-a1-{i:03d}", step)

write("release-a1-000", text_reply({
    "summary": "Duplicate settlement webhooks no longer double-credit merchants. Each bank event is recorded once in a "
               "new processed_events table inside the crediting transaction; repeats return 'duplicate'. Verified by "
               "unit tests, hidden concurrency tests and a replay of recorded traffic with duplicates.",
    "risks": ["New table: confirm the migration runs before the new code is deployed",
              "Duplicate deliveries now return status 'duplicate'; check the bank does not treat that as an error"],
}))
(out / "README.md").write_text(
    "SCRIPTED cassettes (hand-written stand-ins, not real model output), generated by\n"
    "scripts/make-scripted-cassettes.py. Live Bedrock runs record real cassettes in the same format.\n"
)
print(f"wrote {len(list(out.glob('*.json')))} cassettes to {out}")
