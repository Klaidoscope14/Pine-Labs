from decimal import Decimal

from pydantic import BaseModel


def bad(event, amount_paise):
    # ruleid: fin-001-float-money
    amount = float(event.amount_paise)
    # ruleid: fin-001-float-money
    x = float(amount_paise)
    # ruleid: fin-001-float-money
    fee = amount_paise * 0.015
    return amount, x, fee


class Bad(BaseModel):
    # ruleid: fin-001-float-money
    amount: float


def good(event, ratio_text):
    # ok: fin-001-float-money
    amount = int(event.amount_paise)
    # ok: fin-001-float-money
    fee = amount * Decimal("1.5") / 100
    # ok: fin-001-float-money
    ratio = float(ratio_text)
    return amount, fee, ratio
