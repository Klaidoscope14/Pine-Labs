"""Payment creation. Idempotent on the client-supplied Idempotency-Key header."""

import sqlite3

from fastapi import APIRouter, Header, HTTPException
from pydantic import BaseModel, Field

from app import db

router = APIRouter(prefix="/payments", tags=["payments"])


class PaymentIn(BaseModel):
    merchant_id: str = Field(min_length=1, max_length=64)
    amount_paise: int = Field(gt=0, le=10_000_000_00)


class PaymentOut(BaseModel):
    id: int
    merchant_id: str
    amount_paise: int
    status: str
    replayed: bool


@router.post("", response_model=PaymentOut, status_code=201)
def create_payment(
    body: PaymentIn, idempotency_key: str = Header(min_length=8, max_length=128)
) -> PaymentOut:
    with db.transaction() as conn:
        existing = conn.execute(
            "SELECT * FROM payments WHERE idempotency_key = ?", (idempotency_key,)
        ).fetchone()
        if existing:
            if (existing["merchant_id"], existing["amount_paise"]) != (
                body.merchant_id,
                body.amount_paise,
            ):
                raise HTTPException(409, "idempotency key reused with a different request")
            return _out(existing, replayed=True)
        cur = conn.execute(
            "INSERT INTO payments (idempotency_key, merchant_id, amount_paise, status) "
            "VALUES (?, ?, ?, 'created')",
            (idempotency_key, body.merchant_id, body.amount_paise),
        )
        row = conn.execute("SELECT * FROM payments WHERE id = ?", (cur.lastrowid,)).fetchone()
        return _out(row, replayed=False)


def _out(row: sqlite3.Row, replayed: bool) -> PaymentOut:
    return PaymentOut(
        id=row["id"],
        merchant_id=row["merchant_id"],
        amount_paise=row["amount_paise"],
        status=row["status"],
        replayed=replayed,
    )
