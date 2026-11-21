from decimal import Decimal

import pytest

from app.money import percent_of
from app.settlements.calculator import net_settlement


def test_percent_rounds_half_up():
    assert percent_of(1000, Decimal("1.5")) == 15
    assert percent_of(1001, Decimal("1.5")) == 15  # 15.015 -> 15
    assert percent_of(1003, Decimal("1.5")) == 15  # 15.045 -> 15
    assert percent_of(1010, Decimal("1.5")) == 15  # 15.15 -> 15
    assert percent_of(1100, Decimal("1.5")) == 17  # 16.5 -> 17


def test_percent_rejects_negative():
    with pytest.raises(ValueError):
        percent_of(-1, Decimal("1"))


def test_net_settlement_subtracts_mdr():
    assert net_settlement(100_000, Decimal("2")) == 98_000
