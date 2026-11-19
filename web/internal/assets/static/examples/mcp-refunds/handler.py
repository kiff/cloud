"""A remote MCP tool server for the KIFF refunds demo.

It exposes two tools over MCP (Streamable HTTP, revision 2025-11-25, plain
JSON responses, no sessions) and runs as an AWS Lambda function URL:

  get_order      one paid order by its order number (read-only)
  refund_order   refund one order, in whole euros

It is meant to be connected to KIFF's MCP gateway, never to an agent
directly. Every request must present TOOL_TOKEN as a bearer token; KIFF holds
it as the connection's stored credential (encrypted at rest), so an agent can
only reach these tools through its Card. The source address proves nothing:
KIFF's gateway IP is shared by every KIFF account. STRIPE_KEY must be a test-mode
key (sk_test_...); the server refuses to start a refund with anything else.
"""
import base64
import hmac
import json
import os
import urllib.error
import urllib.parse
import urllib.request

STRIPE = "https://api.stripe.com/v1"

TOOLS = [
    {
        "name": "get_order",
        "description": "Look up one paid order by its order number (for example 1042): customer, item, amount in euros, already refunded.",
        "inputSchema": {
            "type": "object",
            "properties": {"order_number": {"type": "string", "description": "The shop's order number, without #."}},
            "required": ["order_number"],
        },
        "annotations": {"readOnlyHint": True},
    },
    {
        "name": "refund_order",
        "description": "Refund a paid order, in whole euros. Fails if the order is not paid or the amount exceeds what is left to refund.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "order_number": {"type": "string", "description": "The shop's order number, without #."},
                "amount_eur": {"type": "integer", "description": "Amount to refund, in whole euros."},
                "reason": {"type": "string", "description": "Why the customer is refunded."},
                "idempotency_key": {"type": "string", "description": "A key that makes a retry of the same refund safe."},
            },
            "required": ["order_number", "amount_eur", "reason"],
        },
    },
]


def stripe(method, path, params=None, idem=None):
    key = os.environ["STRIPE_KEY"]
    data = urllib.parse.urlencode(params or {}, doseq=True).encode() if method == "POST" else None
    url = STRIPE + path + ("?" + urllib.parse.urlencode(params) if method == "GET" and params else "")
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", "Basic " + base64.b64encode((key + ":").encode()).decode())
    if idem:
        req.add_header("Idempotency-Key", idem[:255])
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            return json.loads(r.read())
    except urllib.error.HTTPError as e:
        body = json.loads(e.read() or b"{}")
        raise ValueError(body.get("error", {}).get("message", "Stripe refused the request"))


def text(t, error=False):
    return {"content": [{"type": "text", "text": t}], "isError": error}


def find_order(number):
    """The paid PaymentIntent whose metadata order_number matches, newest first."""
    number = str(number).lstrip("#").strip()
    pis = stripe("GET", "/payment_intents", {"limit": 100, "expand[]": "data.latest_charge"})["data"]
    for pi in pis:
        if pi["status"] == "succeeded" and pi.get("metadata", {}).get("order_number") == number:
            return pi
    return None


def get_order(args):
    pi = find_order(args.get("order_number", ""))
    if pi is None:
        return text("No paid order with that number.", True)
    ch = pi.get("latest_charge") or {}
    md = pi.get("metadata", {})
    return text(f"Order #{md.get('order_number')}: {md.get('customer', '')}, {pi.get('description') or ''}, "
                f"paid €{pi['amount'] // 100}, refunded €{(ch.get('amount_refunded') or 0) // 100}.")


def refund_order(args):
    if not os.environ["STRIPE_KEY"].startswith("sk_test_"):
        return text("This demo tool only refunds in Stripe test mode.", True)
    amount = args.get("amount_eur")
    if not isinstance(amount, int) or amount <= 0:
        return text("amount_eur must be a positive whole number.", True)
    pi = find_order(args.get("order_number", ""))
    if pi is None:
        return text("No paid order with that number.", True)
    oid = pi["id"]
    try:
        r = stripe("POST", "/refunds", {"payment_intent": oid, "amount": amount * 100,
                                       "metadata[reason]": str(args.get("reason", ""))[:400]},
                   idem=args.get("idempotency_key"))
    except ValueError as e:
        return text(f"Refund refused by Stripe: {e}", True)
    return text(f"Refunded €{r['amount'] // 100} on order #{pi['metadata'].get('order_number')}. "
                f"Stripe refund {r['id']}, status {r['status']}.")


def rpc(msg):
    method, mid = msg.get("method"), msg.get("id")
    if mid is None:
        return None
    if method == "initialize":
        result = {"protocolVersion": "2025-11-25", "capabilities": {"tools": {}},
                  "serverInfo": {"name": "kiff-demo-refunds", "version": "1"}}
    elif method == "ping":
        result = {}
    elif method == "tools/list":
        result = {"tools": TOOLS}
    elif method == "tools/call":
        p = msg.get("params") or {}
        fn = {"get_order": get_order, "refund_order": refund_order}.get(p.get("name"))
        if fn is None:
            return {"jsonrpc": "2.0", "id": mid, "error": {"code": -32602, "message": "unknown tool"}}
        result = fn(p.get("arguments") or {})
    else:
        return {"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": "method not found"}}
    return {"jsonrpc": "2.0", "id": mid, "result": result}


TOKEN = os.environ.get("TOOL_TOKEN", "")
if not TOKEN.strip():
    # Refuse to start: an empty token would make "Bearer " a valid credential.
    raise RuntimeError("TOOL_TOKEN must be set")


def authorized(header):
    return hmac.compare_digest(header.encode(), ("Bearer " + TOKEN).encode())


def lambda_handler(event, context):
    http = event.get("requestContext", {}).get("http", {})
    headers = {k.lower(): v for k, v in (event.get("headers") or {}).items()}
    if http.get("method") != "POST":
        return {"statusCode": 405, "body": ""}
    if not authorized(headers.get("authorization", "")):
        return {"statusCode": 401, "body": "unauthorized"}
    raw = event.get("body") or ""
    if event.get("isBase64Encoded"):
        raw = base64.b64decode(raw).decode()
    try:
        msg = json.loads(raw)
    except ValueError:
        return {"statusCode": 400, "body": "invalid JSON"}
    if msg.get("method") == "server/discover":
        # Not a 2026-07-28 server: the gateway falls back to initialize.
        return {"statusCode": 404, "body": "not found"}
    out = rpc(msg)
    if out is None:
        return {"statusCode": 202, "body": ""}
    return {"statusCode": 200, "headers": {"Content-Type": "application/json"}, "body": json.dumps(out)}
