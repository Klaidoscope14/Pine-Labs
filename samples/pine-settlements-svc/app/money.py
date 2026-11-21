"""Money helpers. All amounts are integer paise; percentages use Decimal."""

from decimal import ROUND_HALF_UP, Decimal


def percent_of(amount_paise: int, rate_percent: Decimal) -> int:
    """Return rate_percent of amount_paise, rounded half-up to whole paise."""
    if amount_paise < 0:
        raise ValueError("amount must be non-negative")
    value = Decimal(amount_paise) * rate_percent / Decimal(100)
    return int(value.quantize(Decimal(1), rounding=ROUND_HALF_UP))
