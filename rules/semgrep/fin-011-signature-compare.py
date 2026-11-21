import hmac


def bad(body, x_signature, sign):
    # ruleid: fin-011-signature-compare
    if sign(body) != x_signature:
        raise ValueError("bad signature")
    expected_hmac = sign(body)
    # ruleid: fin-011-signature-compare
    return expected_hmac == x_signature


def good(body, x_signature, sign):
    # ok: fin-011-signature-compare
    if x_signature == "":
        return False
    # ok: fin-011-signature-compare
    return hmac.compare_digest(sign(body), x_signature)
