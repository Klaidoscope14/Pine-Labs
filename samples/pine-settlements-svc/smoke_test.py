"""Live HTTP smoke test: boots the service on loopback and exercises it.

Works with --network=none because it only uses 127.0.0.1.
"""

import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

from app.webhooks.handler import sign


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def call(url: str, data: bytes | None = None, headers: dict | None = None) -> dict:
    req = urllib.request.Request(url, data=data, headers=headers or {})  # noqa: S310
    with urllib.request.urlopen(req, timeout=5) as resp:  # noqa: S310
        return json.loads(resp.read())


def main() -> int:
    env = {
        **os.environ,
        "SETTLEMENTS_DB_PATH": os.path.join(tempfile.mkdtemp(), "smoke.db"),
        "SETTLEMENT_WEBHOOK_SECRET": os.environ.get("SETTLEMENT_WEBHOOK_SECRET", "smoke-secret"),
    }
    os.environ.update(env)
    port = free_port()
    base = f"http://127.0.0.1:{port}"
    proc = subprocess.Popen(  # noqa: S603
        [sys.executable, "-m", "uvicorn", "app.main:app",
         "--host", "127.0.0.1", "--port", str(port)],
        env=env,
    )
    try:
        for _ in range(50):
            try:
                if call(f"{base}/healthz")["status"] == "ok":
                    break
            except OSError:
                time.sleep(0.1)
        else:
            print("SMOKE FAIL: service did not become healthy")
            return 1
        body = json.dumps(
            {"event_id": "evt_smoke", "type": "settlement.completed", "merchant_id": "ms",
             "amount_paise": 1000}
        ).encode()
        headers = {"X-Signature": sign(body), "Content-Type": "application/json"}
        assert call(f"{base}/webhooks/settlement", body, headers)["status"] == "credited"
        assert call(f"{base}/merchants/ms/balance")["balance_paise"] == 1000
        print("SMOKE OK")
        return 0
    finally:
        proc.terminate()
        proc.wait(timeout=10)


if __name__ == "__main__":
    sys.exit(main())
