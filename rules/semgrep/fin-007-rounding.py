from decimal import ROUND_HALF_UP, Decimal


def bad(amount: Decimal) -> Decimal:
    # ruleid: fin-007-rounding
    return amount.quantize(Decimal("0.01"))


def good(amount: Decimal) -> Decimal:
    # ok: fin-007-rounding
    return amount.quantize(Decimal("0.01"), rounding=ROUND_HALF_UP)
