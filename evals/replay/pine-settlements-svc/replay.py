"""Replay harness (outside the repo, mounted read-only into the replay gate only).

Feeds recorded settlement webhooks, including duplicates, through the service under
test and checks ledger invariants. Writes a JSON report to argv[1]; exit 0 only if
every invariant held.
"""

import hashlib
import hmac
import json
import os
import sqlite3
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
SECRET = "replay-harness-only"


def main(report_path: str) -> int:
    tmp = tempfile.mkdtemp()
    db_path = os.path.join(tmp, "replay.db")
    os.environ["SETTLEMENTS_DB_PATH"] = db_path
    os.environ["SETTLEMENT_WEBHOOK_SECRET"] = SECRET
    invariants = []

    def record(name: str, held: bool, detail: str) -> None:
        invariants.append({"name": name, "held": held, "detail": detail[:300]})

    try:
        from fastapi.testclient import TestClient

        from app.main import app

        server_errors = 0
        with TestClient(app) as client:
            for line in (HERE / "events.jsonl").read_text().splitlines():
                body = line.encode()
                sig = hmac.new(SECRET.encode(), body, hashlib.sha256).hexdigest()
                r = client.post("/webhooks/settlement", content=body,
                                headers={"X-Signature": sig, "Content-Type": "application/json"})
                if r.status_code >= 500:
                    server_errors += 1
            record("NO_SERVER_ERRORS", server_errors == 0, f"{server_errors} deliveries returned 5xx")

            expected = json.loads((HERE / "expected.json").read_text())
            wrong = []
            for merchant, balance in expected.items():
                got = client.get(f"/merchants/{merchant}/balance").json()["balance_paise"]
                if got != balance:
                    wrong.append(f"{merchant}: ledger {got} != expected {balance}")
            record("BALANCES_MATCH", not wrong, "; ".join(wrong) or "all balances match")

        conn = sqlite3.connect(db_path)
        dupes = conn.execute(
            "SELECT reference, COUNT(*) FROM ledger_entries WHERE kind = 'credit' "
            "GROUP BY reference HAVING COUNT(*) > 1"
        ).fetchall()
        conn.close()
        record("ONE_CREDIT_PER_EVENT", not dupes,
               ", ".join(f"{ref} credited {n}x" for ref, n in dupes) or "each event credited once")
    except Exception as exc:  # the change under test may be broken in any way
        record("HARNESS", False, f"replay could not complete: {type(exc).__name__}: {exc}")

    Path(report_path).write_text(json.dumps({"invariants": invariants}))
    return 0 if invariants and all(i["held"] for i in invariants) else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1]))
