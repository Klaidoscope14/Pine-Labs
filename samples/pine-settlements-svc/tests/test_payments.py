def test_create_payment(client):
    r = client.post(
        "/payments",
        json={"merchant_id": "m1", "amount_paise": 5000},
        headers={"Idempotency-Key": "key-00000001"},
    )
    assert r.status_code == 201
    assert r.json()["replayed"] is False


def test_payment_replay_returns_same_payment(client):
    headers = {"Idempotency-Key": "key-00000002"}
    body = {"merchant_id": "m1", "amount_paise": 5000}
    first = client.post("/payments", json=body, headers=headers).json()
    second = client.post("/payments", json=body, headers=headers).json()
    assert first["id"] == second["id"]
    assert second["replayed"] is True


def test_payment_key_reuse_with_different_body_conflicts(client):
    headers = {"Idempotency-Key": "key-00000003"}
    client.post("/payments", json={"merchant_id": "m1", "amount_paise": 5000}, headers=headers)
    r = client.post("/payments", json={"merchant_id": "m1", "amount_paise": 9999}, headers=headers)
    assert r.status_code == 409


def test_payment_requires_idempotency_key(client):
    r = client.post("/payments", json={"merchant_id": "m1", "amount_paise": 5000})
    assert r.status_code == 422


def test_payment_rejects_non_positive_amount(client):
    r = client.post(
        "/payments",
        json={"merchant_id": "m1", "amount_paise": 0},
        headers={"Idempotency-Key": "key-00000004"},
    )
    assert r.status_code == 422
