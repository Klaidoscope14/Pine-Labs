from fastapi import APIRouter

router = APIRouter()


# ruleid: fin-004-missing-idempotency
@router.post("/webhooks/settlement")
async def bad_handler(event, conn, store):
    with conn:
        store.credit(conn, event.merchant_id, event.amount_paise, reference=event.event_id)
    return {"status": "credited"}


# ok: fin-004-missing-idempotency
@router.post("/webhooks/settlement2")
async def good_handler(event, conn, store):
    with conn:
        cur = conn.execute("INSERT OR IGNORE INTO processed_events (event_id) VALUES (?)", (event.event_id,))
        if cur.rowcount == 0:
            return {"status": "duplicate"}
        store.credit(conn, event.merchant_id, event.amount_paise, reference=event.event_id)
    return {"status": "credited"}


# ok: fin-004-missing-idempotency
@router.post("/payments")
def no_money_movement(body):
    return {"ok": True}
