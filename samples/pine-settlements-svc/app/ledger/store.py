"""Merchant ledger. Every balance change is an append-only entry."""

import sqlite3

from app import db


def credit(conn: sqlite3.Connection, merchant_id: str, amount_paise: int, reference: str) -> None:
    if amount_paise <= 0:
        raise ValueError("credit amount must be positive")
    conn.execute(
        "INSERT INTO ledger_entries (merchant_id, amount_paise, kind, reference) "
        "VALUES (?, ?, 'credit', ?)",
        (merchant_id, amount_paise, reference),
    )


def balance(merchant_id: str) -> int:
    with db.connect() as conn:
        row = conn.execute(
            "SELECT COALESCE(SUM(amount_paise), 0) AS total "
            "FROM ledger_entries WHERE merchant_id = ?",
            (merchant_id,),
        ).fetchone()
    return int(row["total"])
