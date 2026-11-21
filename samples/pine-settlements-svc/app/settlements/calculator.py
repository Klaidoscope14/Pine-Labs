"""Settlement maths. Integer paise in, integer paise out."""

from decimal import Decimal

from app.money import percent_of


def net_settlement(gross_paise: int, mdr_percent: Decimal) -> int:
    """Gross minus the merchant discount rate fee."""
    fee = percent_of(gross_paise, mdr_percent)
    return gross_paise - fee
