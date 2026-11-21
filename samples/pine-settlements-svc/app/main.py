"""FastAPI application."""

from contextlib import asynccontextmanager

from fastapi import FastAPI

from app import db
from app.ledger import store
from app.payments.service import router as payments_router
from app.webhooks.handler import router as webhooks_router


@asynccontextmanager
async def lifespan(_: FastAPI):
    db.init_db()
    yield


app = FastAPI(title="pine-settlements-svc", version="0.1.0", lifespan=lifespan)
app.include_router(payments_router)
app.include_router(webhooks_router)


@app.get("/healthz")
def healthz() -> dict:
    return {"status": "ok"}


@app.get("/merchants/{merchant_id}/balance")
def merchant_balance(merchant_id: str) -> dict:
    return {"merchant_id": merchant_id, "balance_paise": store.balance(merchant_id)}
