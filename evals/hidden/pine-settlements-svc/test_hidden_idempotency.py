"""HIDDEN ORACLE TESTS. Never mounted where agents can read them.

These define "done" for the duplicate-webhook ticket. They fail on the base code.
"""

import json
import threading
from pathlib import Path

REPO = Path(__file__).resolve().parent


def _event(event_id="evt_dup", merchant="m_dup", amount=50_000):
    return {"event_id": event_id, "type": "settlement.completed", "merchant_id": merchant,
            "amount_paise": amount}


def test_duplicate_delivery_credits_once(client, send_webhook):
    send_webhook(_event())
    second = send_webhook(_event())
    assert second.status_code == 200
    assert client.get("/merchants/m_dup/balance").json()["balance_paise"] == 50_000


def test_concurrent_duplicates_credit_once(client, send_webhook):
    threads = [threading.Thread(target=send_webhook, args=(_event("evt_race", "m_race"),))
               for _ in range(8)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert client.get("/merchants/m_race/balance").json()["balance_paise"] == 50_000


def test_replay_fixture_matches_expected_balances(client, send_webhook):
    lines = Path("fixtures/webhook_replay.jsonl").read_text().splitlines()
    for line in lines:
        send_webhook(json.loads(line))
    expected = json.loads(Path("fixtures/replay_expected.json").read_text())
    for merchant, balance in expected.items():
        got = client.get(f"/merchants/{merchant}/balance").json()["balance_paise"]
        assert got == balance, f"{merchant}: ledger {got} != expected {balance}"
