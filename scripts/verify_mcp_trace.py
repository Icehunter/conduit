#!/usr/bin/env python3
"""Check built Conduit + local-router against a synthetic HTTP backend, no GPU.

Usage: python3 scripts/verify_mcp_trace.py --router /path/to/local-router
Only temporary fixture repositories/configuration are changed. Artifacts are kept
in the printed temporary directory. No real model or credentials are used.
"""

import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading


PROMPT = "Fix add(a, b) in calc.py so it returns the sum. Only edit calc.py."
CHECK = """from calc import add
for a, b, expected in [(2, 3, 5), (-3, 4, 1), (0, 0, 0), (5, -8, -3)]:
    actual = add(a, b)
    if actual != expected:
        raise SystemExit(f"expected {expected}, got {actual}")
print("PASS: 4 addition cases")
"""


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")
    path.chmod(0o600)


def run_case(base, conduit, router, *, redacted=False, capture=True, absolute_path=False):
    base.mkdir()
    repo = base / "repo"
    repo.mkdir()
    config = base / "config"
    config.mkdir()
    (base / "claude").mkdir()
    (repo / "calc.py").write_text("def add(a, b):\n    raise NotImplementedError\n")
    check = CHECK
    if absolute_path:
        check = check.replace('    if actual != expected:\n        raise SystemExit(f"expected {expected}, got {actual}")',
                              '    assert actual == expected, f"expected {expected}, got {actual}"')
    (repo / "check.py").write_text(check)
    protected = hashlib.sha256((repo / "check.py").read_bytes()).hexdigest()
    subprocess.run(["git", "init", "-q", str(repo)], check=True)
    wire = []

    class Backend(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            wire.append(body)
            first = len(wire) == 1
            edit = {"path": "calc.py", "old": "    raise NotImplementedError" if first else "    return a - b",
                    "new": "    return a - b" if first else "    return a + b"}
            answer = json.dumps({"edits": [edit]})
            result = {"model": "trace-fixture-model", "choices": [
                {"finish_reason": "stop", "message": {"role": "assistant", "content": answer}}],
                "usage": {"prompt_tokens": 321, "completion_tokens": 45}}
            encoded = json.dumps(result).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Backend)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    write_json(config / "conduit.json", {
        "providers": {"fixture": {"kind": "mcp", "server": "trace-router",
                                  "model": "trace-fixture-model", "endpointId": "trace-fixture",
                                  "implementTool": "local_implement"}},
        "roles": {"implement": "fixture"},
        "permissions": {"allow": ["Read", "Edit", "Bash(python3 -B check.py)"]},
        "definitionOfDone": {"enabled": True, "maxRepairAttempts": 1, "totalTimeoutSeconds": 30,
                             "checks": [{"id": "addition", "command": "python3 -B check.py", "timeoutSeconds": 5}],
                             "protectedPaths": ["check.py"]},
        "episodes": {"enabled": True, "includeContent": capture, "allowedPaths": ["*.py"]},
        "projects": {str(repo): {"hasTrustDialogAccepted": True}},
    })
    write_json(config / "mcp.json", {"mcpServers": {"trace-router": {
        "command": "node", "args": [str(router / "dist/server.js")], "env": {
            "CLAUDE_PLUGIN_ROOT": str(base),
            "LOCAL_LLM_BASE_URL": f"http://127.0.0.1:{server.server_port}",
            "LOCAL_LLM_MODEL": "trace-fixture-model",
            "LOCAL_LLM_API_KEY": "synthetic-transport-credential",
            "LOCAL_LLM_TASKS": "implement,fix",
            "LOCAL_LLM_ENABLE_THINKING": "false",
            "LOCAL_LLM_MAX_TOKENS": "2048", "LOCAL_LLM_TOKEN_BUDGET": "32768",
            "LOCAL_LLM_STATE_DIR": str(base / "router-state"),
        }}}})
    env = os.environ.copy()
    env.update(CONDUIT_CONFIG_DIR=str(config), CLAUDE_CONFIG_DIR=str(base / "claude"))
    env.pop("CLAUDE_CODE_SANDBOXED", None)
    env.pop("CONDUIT_EVAL_ORIGIN", None)
    env.pop("CONDUIT_EVAL_DISABLE_MEMORY", None)

    def command(*args):
        result = subprocess.run([str(conduit), *args], cwd=repo, env=env,
                                capture_output=True, text=True, timeout=60)
        if result.returncode:
            raise AssertionError(f"{args[0]} exited {result.returncode}: {result.stderr}\n{result.stdout}")
        return result.stdout

    try:
        prompt = PROMPT + (" Synthetic redaction fixture: api_key=example-private-value" if redacted else "")
        output = command("code", "--file", "calc.py", prompt)
        (base / "result.json").write_text(output)
        assert json.loads(output)["status"] == "verified", output
        assert json.loads(output)["attempts"] == 2, output
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
        write_json(base / "wire.json", wire)
    assert len(wire) == 2, wire
    assert hashlib.sha256((repo / "check.py").read_bytes()).hexdigest() == protected
    journal, = (config / "coding/episodes").glob("*.jsonl")
    raw = journal.read_text()
    events = [json.loads(line) for line in raw.splitlines()]
    assert "synthetic-transport-credential" not in raw
    requests = [e for e in events if e["kind"] == "model_request"]
    assert len(requests) == 2
    if capture and not redacted and not absolute_path:
        for event, body in zip(requests, wire):
            request = event["payload"]
            messages = request["messages"]
            if request.get("system"):
                messages = [{"role": "system", "content": request["system"]}] + messages
            assert request["context_complete"] is True, request
            assert messages == body["messages"], (messages, body["messages"])
            assert request["model_id"] == body["model"]
            assert request["parameters"] == {k: v for k, v in body.items() if k not in ("model", "messages")}
        checks = [e["payload"]["passed"] for e in events if e["kind"] == "verification"]
        assert checks == [False, True], checks
        assert events[-1]["attempt_id"] == "1"
    if redacted:
        assert "example-private-value" not in raw
        assert any(e.get("redacted") for e in requests)
    if absolute_path:
        assert str(repo) not in raw
        assert requests[-1].get("redacted") is True
    if not capture:
        assert all(e.get("incomplete") and e.get("redacted") for e in requests)

    def exported():
        rows = []
        for split in ("train", "validation"):
            data = command("episodes", "export", "--file", str(journal), "--format", "sft", "--split", split)
            rows.extend(json.loads(line) for line in data.splitlines() if line)
        return rows

    assert exported() == [], "Passing checks must not imply human acceptance"
    if capture:
        candidate = [e for e in events if e["kind"] == "candidate"][-1]
        command("episodes", "accept", "--file", str(journal), "--episode", candidate["episode_id"],
                "--candidate", candidate["payload"]["id"], "--reason", "Synthetic integration test label; not production training data")
        rows = exported()
        assert len(rows) == (0 if redacted or absolute_path else 1), rows
        if rows:
            assert rows[0]["messages"][:-1] == wire[-1]["messages"], rows[0]
    return {"case": base.name, "requests": len(wire), "status": "passed"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--router", type=Path, required=True, help="Built local-router checkout")
    parser.add_argument("--conduit", type=Path, default=Path(__file__).resolve().parents[1] / "conduit")
    args = parser.parse_args()
    base = Path(tempfile.mkdtemp(prefix="conduit-mcp-trace-")).resolve()
    print(f"Artifacts: {base}", flush=True)
    for name, options in (("complete", {}), ("redacted", {"redacted": True}),
                          ("absolute-path", {"absolute_path": True}), ("content-disabled", {"capture": False})):
        print(json.dumps(run_case(base / name, args.conduit.resolve(), args.router.resolve(), **options)), flush=True)


if __name__ == "__main__":
    main()
