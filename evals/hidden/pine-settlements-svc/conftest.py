"""Fixtures for the hidden oracle suite. Independent of the repo's own conftest,
so changes to the repo's test helpers can't make these tests pass."""

import hashlib
import hmac
import json

import pytest

SECRET = "hidden-oracle-only"


@pytest.fixture()
def client(tmp_path, monkeypatch):
    monkeypatch.setenv("SETTLEMENTS_DB_PATH", str(tmp_path / "hidden.db"))
    monkeypatch.setenv("SETTLEMENT_WEBHOOK_SECRET", SECRET)
    from fastapi.testclient import TestClient

    from app.main import app

    with TestClient(app) as c:
        yield c


@pytest.fixture()
def send_webhook(client):
    def _send(event: dict):
        body = json.dumps(event).encode()
        sig = hmac.new(SECRET.encode(), body, hashlib.sha256).hexdigest()
        return client.post("/webhooks/settlement", content=body,
                           headers={"X-Signature": sig, "Content-Type": "application/json"})

    return _send
