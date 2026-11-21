"""HIDDEN ORACLE TESTS. Never mounted where agents can read them.

They define "done" for the duplicate-webhook ticket and fail on the base code.
"""

import threading


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


def test_distinct_events_still_credit(client, send_webhook):
    send_webhook(_event("evt_a", "m_both"))
    send_webhook(_event("evt_b", "m_both"))
    assert client.get("/merchants/m_both/balance").json()["balance_paise"] == 100_000
