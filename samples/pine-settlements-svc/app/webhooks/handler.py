"""Settlement webhooks from the acquiring bank.

The bank retries deliveries, so the same event can arrive more than once.
"""

import hashlib
import hmac
import json
import os

from fastapi import APIRouter, Header, HTTPException, Request
from pydantic import BaseModel, Field

from app import db
from app.ledger import store

router = APIRouter(prefix="/webhooks", tags=["webhooks"])


class SettlementEvent(BaseModel):
    event_id: str = Field(min_length=1, max_length=128)
    type: str
    merchant_id: str = Field(min_length=1, max_length=64)
    amount_paise: int = Field(gt=0)


def _secret() -> bytes:
    secret = os.environ.get("SETTLEMENT_WEBHOOK_SECRET")
    if not secret:
        raise RuntimeError("SETTLEMENT_WEBHOOK_SECRET is not set")
    return secret.encode()


def sign(body: bytes) -> str:
    return hmac.new(_secret(), body, hashlib.sha256).hexdigest()


@router.post("/settlement", status_code=200)
async def settlement_webhook(request: Request, x_signature: str = Header()) -> dict:
    body = await request.body()
    if not hmac.compare_digest(sign(body), x_signature):
        raise HTTPException(401, "bad signature")
    event = SettlementEvent.model_validate(json.loads(body))
    if event.type != "settlement.completed":
        return {"status": "ignored"}

    with db.transaction() as conn:
        store.credit(conn, event.merchant_id, event.amount_paise, reference=event.event_id)
    return {"status": "credited"}
