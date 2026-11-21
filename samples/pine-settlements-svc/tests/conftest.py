import json

import pytest
from fastapi.testclient import TestClient


@pytest.fixture()
def client(tmp_path, monkeypatch):
    monkeypatch.setenv("SETTLEMENTS_DB_PATH", str(tmp_path / "test.db"))
    monkeypatch.setenv("SETTLEMENT_WEBHOOK_SECRET", "test-only-secret")
    from app.main import app

    with TestClient(app) as c:
        yield c


@pytest.fixture()
def send_webhook(client):
    from app.webhooks.handler import sign

    def _send(event: dict, signature: str | None = None):
        body = json.dumps(event).encode()
        return client.post(
            "/webhooks/settlement",
            content=body,
            headers={"X-Signature": signature or sign(body), "Content-Type": "application/json"},
        )

    return _send
