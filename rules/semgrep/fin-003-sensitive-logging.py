import logging

logger = logging.getLogger(__name__)


def bad(req, card_number, token):
    # ruleid: fin-003-sensitive-logging
    logger.info("charging %s", card_number)
    # ruleid: fin-003-sensitive-logging
    logger.debug(f"payload {req.cvv}")
    # ruleid: fin-003-sensitive-logging
    print("token", token)


def good(req, merchant_id):
    # ok: fin-003-sensitive-logging
    logger.info("charging merchant %s", merchant_id)
    # ok: fin-003-sensitive-logging
    logger.info("request id %s", req.request_id)
