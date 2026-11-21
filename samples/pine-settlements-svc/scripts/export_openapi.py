"""Write the service's OpenAPI document to stdout (used by the contract gate)."""

import json
import os
import tempfile

os.environ.setdefault("SETTLEMENTS_DB_PATH", os.path.join(tempfile.mkdtemp(), "openapi.db"))
os.environ.setdefault("SETTLEMENT_WEBHOOK_SECRET", "openapi-export-only")

from app.main import app  # noqa: E402

print(json.dumps(app.openapi(), indent=2, sort_keys=True))
