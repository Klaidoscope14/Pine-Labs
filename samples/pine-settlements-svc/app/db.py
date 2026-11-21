"""SQLite access. The database path comes from the environment, never from code."""

import os
import sqlite3
import threading
from collections.abc import Iterator
from contextlib import contextmanager

_lock = threading.Lock()

SCHEMA = """
CREATE TABLE IF NOT EXISTS ledger_entries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    merchant_id TEXT NOT NULL,
    amount_paise INTEGER NOT NULL,
    kind TEXT NOT NULL,
    reference TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS payments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    idempotency_key TEXT NOT NULL UNIQUE,
    merchant_id TEXT NOT NULL,
    amount_paise INTEGER NOT NULL,
    status TEXT NOT NULL
);
"""


def db_path() -> str:
    path = os.environ.get("SETTLEMENTS_DB_PATH")
    if not path:
        raise RuntimeError("SETTLEMENTS_DB_PATH is not set")
    return path


def init_db() -> None:
    with connect() as conn:
        conn.executescript(SCHEMA)


@contextmanager
def connect() -> Iterator[sqlite3.Connection]:
    conn = sqlite3.connect(db_path(), timeout=10, isolation_level=None)
    conn.row_factory = sqlite3.Row
    try:
        yield conn
    finally:
        conn.close()


@contextmanager
def transaction() -> Iterator[sqlite3.Connection]:
    """Serialised write transaction."""
    with _lock, connect() as conn:
        conn.execute("BEGIN IMMEDIATE")
        try:
            yield conn
            conn.execute("COMMIT")
        except Exception:
            conn.execute("ROLLBACK")
            raise
