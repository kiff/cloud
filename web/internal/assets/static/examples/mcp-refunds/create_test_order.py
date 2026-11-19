"""Create one fictional EUR payment in the Stripe test environment."""
import getpass
import json
import uuid
from urllib.parse import urlencode
from urllib.request import Request, urlopen

key = getpass.getpass("Stripe test secret (sk_test_...): ").strip()
if not key.startswith("sk_test_"):
    raise SystemExit("A Stripe test secret is required.")
number = "KIFF-TUTORIAL-" + uuid.uuid4().hex[:12].upper()
params = {
    "amount": "30000",
    "currency": "eur",
    "payment_method": "pm_card_visa",
    "confirm": "true",
    "payment_method_types[]": "card",
    "description": "KIFF tutorial: fictional test order",
    "metadata[order_number]": number,
    "metadata[customer]": "Fictional tutorial customer",
}
request = Request(
    "https://api.stripe.com/v1/payment_intents",
    data=urlencode(params).encode(),
    headers={"Authorization": "Bearer " + key,
             "Idempotency-Key": "create-" + number},
)
with urlopen(request, timeout=30) as response:
    payment = json.load(response)
if payment.get("livemode") is not False or payment.get("status") != "succeeded":
    raise SystemExit("Expected a successful test payment; inspect Stripe before continuing.")
print(json.dumps({"order_number": number, "payment_intent": payment["id"],
                  "paid_eur": 300, "livemode": False,
                  "status": payment["status"]}, indent=2))
