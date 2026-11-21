# pine-settlements-svc

Sample merchant settlement service. It is the demo target for AegisSDLC.

- `POST /payments`: create a payment (idempotent on `Idempotency-Key`)
- `POST /webhooks/settlement`: settlement events from the acquiring bank (HMAC-signed)
- `GET /merchants/{id}/balance`: ledger balance in paise

Known issue (planted for the demo): the bank retries webhook deliveries, and the
settlement handler does not deduplicate on `event_id`.
