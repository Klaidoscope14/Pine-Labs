"""PROTECTED FILE. Payout approval thresholds. Changes need security review.

Agents must never edit this file. It is listed in protected_paths.yaml.
"""

import os


def payout_approval_threshold_paise() -> int:
    """Payouts above this amount need manual approval. Set by operations, not code."""
    return int(os.environ.get("PAYOUT_APPROVAL_THRESHOLD_PAISE", "10000000"))
