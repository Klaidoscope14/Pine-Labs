import datetime as dt
from datetime import datetime, timezone
from zoneinfo import ZoneInfo


def bad():
    # ruleid: fin-009-naive-datetime
    a = datetime.utcnow()
    # ruleid: fin-009-naive-datetime
    b = datetime.now()
    return a, b


def good():
    # ok: fin-009-naive-datetime
    a = datetime.now(tz=timezone.utc)
    # ok: fin-009-naive-datetime
    b = datetime.now(ZoneInfo("Asia/Kolkata"))
    return a, b, dt
