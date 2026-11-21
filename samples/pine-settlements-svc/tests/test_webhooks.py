EVENT = {
    "event_id": "evt_001",
    "type": "settlement.completed",
    "merchant_id": "m42",
    "amount_paise": 125_000,
}


def test_settlement_webhook_credits_merchant(client, send_webhook):
    assert send_webhook(EVENT).json() == {"status": "credited"}
    assert client.get("/merchants/m42/balance").json()["balance_paise"] == 125_000


def test_webhook_rejects_bad_signature(client, send_webhook):
    r = send_webhook(EVENT, signature="0" * 64)
    assert r.status_code == 401
    assert client.get("/merchants/m42/balance").json()["balance_paise"] == 0


def test_webhook_ignores_other_event_types(client, send_webhook):
    r = send_webhook({**EVENT, "type": "settlement.initiated"})
    assert r.json() == {"status": "ignored"}


def test_different_events_both_credit(client, send_webhook):
    send_webhook(EVENT)
    send_webhook({**EVENT, "event_id": "evt_002"})
    assert client.get("/merchants/m42/balance").json()["balance_paise"] == 250_000
