"""Launch configured Codex with the agent key supplied privately."""
import getpass
import os
import subprocess

key = getpass.getpass("KIFF agent gateway key: ").strip()
if not key:
    raise SystemExit("An agent gateway key is required.")
subprocess.run(["codex"], env=dict(os.environ, KIFF_GATEWAY_KEY=key), check=True)
