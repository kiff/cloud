"""Initialize KIFF MCP, list tools, and read the caller's Card. No refunds."""
import getpass
import json
from urllib.request import Request, urlopen

key = getpass.getpass("KIFF agent gateway key: ").strip()
if not key:
    raise SystemExit("An agent gateway key is required.")
headers = {"Authorization": "Bearer " + key,
           "Accept": "application/json, text/event-stream",
           "Content-Type": "application/json"}


def rpc(message):
    request = Request("https://mcp.kiff.dev/mcp",
                      data=json.dumps(message).encode(), headers=headers)
    with urlopen(request, timeout=30) as response:
        session = response.headers.get("Mcp-Session-Id")
        if session:
            headers["Mcp-Session-Id"] = session
        data = response.read().decode()
    if not data:
        return None
    if data.startswith("event:") or data.startswith("data:"):
        data = next(line[5:].strip() for line in data.splitlines()
                    if line.startswith("data:"))
    result = json.loads(data)
    if "error" in result:
        raise RuntimeError(json.dumps(result["error"]))
    return result


initialized = rpc({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                   "params": {"protocolVersion": "2025-03-26", "capabilities": {},
                              "clientInfo": {"name": "kiff-tutorial", "version": "1"}}})
headers["MCP-Protocol-Version"] = initialized["result"]["protocolVersion"]
print("Initialized:", json.dumps(initialized["result"]))
rpc({"jsonrpc": "2.0", "method": "notifications/initialized"})
listing = rpc({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
print("Tools:", ", ".join(t["name"] for t in listing["result"]["tools"]))
card = rpc({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
            "params": {"name": "kiff_card", "arguments": {}}})
print("Card:", json.dumps(card["result"], indent=2, ensure_ascii=False))
if card["result"].get("isError"):
    raise SystemExit("The Card tool returned an error; read the result above.")
