import hmac

from fastapi import APIRouter, Depends

router = APIRouter()


def require_merchant():
    return "m"


# ruleid: fin-012-route-auth
@router.post("/refunds")
def create_refund(body: dict) -> dict:
    return body


# ok: fin-012-route-auth
@router.post("/payouts")
def create_payout(body: dict, merchant: str = Depends(require_merchant)) -> dict:
    return body


# ok: fin-012-route-auth
@router.post("/webhook")
async def webhook(body: bytes, sig: str, expected: str) -> dict:
    if not hmac.compare_digest(sig, expected):
        return {}
    return {"ok": True}


# ok: fin-012-route-auth
@router.get("/healthz")
def healthz() -> dict:
    return {"ok": True}
