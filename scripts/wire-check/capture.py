"""
mitmproxy addon: capture a CC /v1/messages request and emit live-capture.json.

Usage:
    mitmdump -p 8888 -s scripts/wire-check/capture.py

Then in a second terminal run real CC through the proxy:
    HTTPS_PROXY=http://127.0.0.1:8888 \
    SSL_CERT_FILE=~/.mitmproxy/mitmproxy-ca-cert.pem \
    /Users/icehunter/.local/bin/claude -p "say the word hello and nothing else"

The addon writes the capture to:
    scripts/wire-check/history/<version>/live-capture.json

It exits mitmdump after the first matching request is captured.
"""

import json
import os
import re
import sys
import gzip
import zlib
from datetime import datetime, timezone
from pathlib import Path

# Headers to redact
REDACT = {"authorization", "cookie", "set-cookie"}

# Headers to record verbatim (case-insensitive matching)
RECORD = {
    "user-agent",
    "x-stainless-runtime-version",
    "x-stainless-package-version",
    "x-stainless-lang",
    "x-stainless-os",
    "x-stainless-arch",
    "x-stainless-retry-count",
    "x-stainless-timeout",
    "x-claude-code-session-id",
    "x-client-request-id",
    "anthropic-version",
    "anthropic-beta",
    "anthropic-mcp-client-capabilities",
    "anthropic-dangerous-direct-browser-access",
    "x-app",
    "x-api-key",
    "authorization",
}

REPO_ROOT = Path(__file__).parent.parent.parent
CONDUIT_SYSTEMPROMPT = REPO_ROOT / "internal" / "agent" / "systemprompt.go"
CONDUIT_AUTH = REPO_ROOT / "internal" / "app" / "auth.go"
CONDUIT_CLIENT = REPO_ROOT / "internal" / "api" / "client.go"


def _read_conduit_constant(path: Path, pattern: str) -> str:
    """Extract a string constant from a Go file via regex."""
    try:
        text = path.read_text()
        m = re.search(pattern, text)
        return m.group(1) if m else "(not found)"
    except Exception:
        return "(error reading)"


def _conduit_billing_cch() -> str:
    return _read_conduit_constant(
        CONDUIT_SYSTEMPROMPT, r'BillingCch\s*=\s*"([^"]+)"'
    )


def _conduit_billing_version() -> str:
    return _read_conduit_constant(
        CONDUIT_SYSTEMPROMPT, r'BillingVersion\s*=\s*"([^"]+)"'
    )


def _conduit_stainless_runtime() -> str:
    return _read_conduit_constant(
        CONDUIT_CLIENT, r'StainlessRuntimeVersion\s*=\s*"([^"]+)"'
    )


def _conduit_sdk_version() -> str:
    return _read_conduit_constant(
        CONDUIT_CLIENT, r'SDKPackageVersion\s*=\s*"([^"]+)"'
    )


def _conduit_betas() -> list[str]:
    try:
        text = CONDUIT_AUTH.read_text()
        # Find the betaHeaders slice
        m = re.search(r'betaHeaders\s*=\s*\[\]string\{([^}]+)\}', text, re.DOTALL)
        if not m:
            return []
        items = re.findall(r'"([^"]+)"', m.group(1))
        return items
    except Exception:
        return []


def _decode_body(flow) -> bytes | None:
    """Decode the request body, handling gzip/deflate."""
    body = flow.request.raw_content
    if not body:
        return None
    enc = flow.request.headers.get("content-encoding", "")
    try:
        if "gzip" in enc:
            return gzip.decompress(body)
        if "deflate" in enc:
            return zlib.decompress(body)
        return body
    except Exception:
        return body


captured = False


def request(flow):
    global captured
    if captured:
        return

    # Only intercept /v1/messages POST to Anthropic
    host = flow.request.pretty_host
    path = flow.request.path
    method = flow.request.method

    if not (
        method == "POST"
        and path.startswith("/v1/messages")
        and "anthropic.com" in host
    ):
        return

    captured = True
    print(f"\n[capture] Intercepted: {method} https://{host}{path}", file=sys.stderr)

    # ── Headers ────────────────────────────────────────────────────────────────
    headers_sent = {}
    for k, v in flow.request.headers.items():
        kl = k.lower()
        if kl in REDACT:
            headers_sent[k] = f"(redacted)"
        elif kl in RECORD or kl.startswith("x-"):
            headers_sent[k] = v

    # ── Body → billing header + system blocks ─────────────────────────────────
    body_bytes = _decode_body(flow)
    billing_header_example = ""
    system_blocks = []
    billing_version = ""
    billing_cch = ""
    billing_suffix = ""
    billing_entrypoint = ""

    if body_bytes:
        try:
            body = json.loads(body_bytes)
            system = body.get("system", [])
            if isinstance(system, str):
                # Old-style plain string
                system = [{"type": "text", "text": system}]
            for i, block in enumerate(system):
                text = block.get("text", "")
                cc = block.get("cache_control")
                if i == 0 and text.startswith("x-anthropic-billing-header:"):
                    billing_header_example = text.strip()
                    # Parse cc_version=X.Y.Z.ABC; cc_entrypoint=E; cch=CCH;
                    m = re.search(r"cc_version=([^\s;]+)", text)
                    if m:
                        full = m.group(1)  # e.g. 2.1.179.49e
                        parts = full.rsplit(".", 1)
                        if len(parts) == 2:
                            billing_version = parts[0]
                            billing_suffix = parts[1]
                    m2 = re.search(r"cch=([^\s;]+)", text)
                    if m2:
                        billing_cch = m2.group(1).rstrip(";")
                    m3 = re.search(r"cc_entrypoint=([^\s;]+)", text)
                    if m3:
                        billing_entrypoint = m3.group(1).rstrip(";")
                    desc = "billing header text"
                elif i == 1:
                    desc = f"identity: {text[:80]}" if text else "(empty)"
                elif i == 2:
                    desc = f"main system prompt (first 80 chars): {text[:80]!r}"
                else:
                    desc = f"block {i}: {text[:60]!r}"
                system_blocks.append({
                    "index": i,
                    "description": desc,
                    "cache_control": cc,
                })
        except Exception as e:
            print(f"[capture] Body parse error: {e}", file=sys.stderr)

    # ── Extract version from User-Agent ────────────────────────────────────────
    ua = headers_sent.get("User-Agent", headers_sent.get("user-agent", ""))
    version_match = re.search(r"claude-cli/(\S+)", ua)
    binary_version = version_match.group(1).split()[0] if version_match else "unknown"

    # ── CC beta list ────────────────────────────────────────────────────────────
    upstream_betas = [
        b.strip()
        for b in headers_sent.get(
            "anthropic-beta", headers_sent.get("Anthropic-Beta", "")
        ).split(",")
        if b.strip()
    ]

    # ── conduit_diff ────────────────────────────────────────────────────────────
    conduit_cch = _conduit_billing_cch()
    conduit_betas = _conduit_betas()
    conduit_stainless = _conduit_stainless_runtime()
    conduit_sdk = _conduit_sdk_version()

    upstream_stainless = headers_sent.get(
        "X-Stainless-Runtime-Version",
        headers_sent.get("x-stainless-runtime-version", ""),
    )
    upstream_sdk = headers_sent.get(
        "X-Stainless-Package-Version",
        headers_sent.get("x-stainless-package-version", ""),
    )

    upstream_beta_set = set(upstream_betas)
    conduit_beta_set = set(conduit_betas)
    betas_added = sorted(upstream_beta_set - conduit_beta_set)
    betas_removed = sorted(conduit_beta_set - upstream_beta_set)

    conduit_diff = {}
    if conduit_stainless != upstream_stainless:
        conduit_diff["X-Stainless-Runtime-Version"] = {
            "conduit": conduit_stainless,
            "upstream": upstream_stainless,
            "action": "update StainlessRuntimeVersion in internal/api/client.go",
        }
    if conduit_sdk != upstream_sdk:
        conduit_diff["X-Stainless-Package-Version"] = {
            "conduit": conduit_sdk,
            "upstream": upstream_sdk,
            "action": "update SDKPackageVersion in internal/api/client.go",
        }
    if conduit_cch != billing_cch and billing_cch:
        conduit_diff["cch"] = {
            "conduit": conduit_cch,
            "upstream": billing_cch,
            "action": "update BillingCch in internal/agent/systemprompt.go",
        }
    if betas_added:
        conduit_diff["betas_added"] = betas_added
    if betas_removed:
        conduit_diff["betas_removed"] = betas_removed
    if not conduit_diff:
        conduit_diff["status"] = "no drift detected"

    # ── Build output ────────────────────────────────────────────────────────────
    out = {
        "captured_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "method": "mitmproxy 8888 via HTTPS_PROXY",
        "binary_version": binary_version,
        "headers_sent": headers_sent,
        "billing_header_example": billing_header_example,
        "billing_notes": {
            "cc_version": billing_version,
            "cc_version_suffix": billing_suffix,
            "cc_version_suffix_note": (
                f"SHA256('59cf53e54c78' + firstMsg[4,7,20] + '{billing_version}').slice(0,3) "
                "— verify conduit's computeBillingSuffix('say the word hello and nothing else') "
                f"matches '{billing_suffix}'"
            ),
            "cch": f"{billing_cch} — Bun compile-time macro, static per-build; decoded JS shows '00000' placeholder",
            "cch_formula": "unknown — not in decoded JS source; baked into Bun binary at build time",
            "cc_entrypoint": billing_entrypoint,
        },
        "system_blocks": system_blocks,
        "conduit_diff": conduit_diff,
    }

    # ── Write output ────────────────────────────────────────────────────────────
    out_dir = REPO_ROOT / "scripts" / "wire-check" / "history" / binary_version
    out_dir.mkdir(parents=True, exist_ok=True)
    out_path = out_dir / "live-capture.json"
    out_path.write_text(json.dumps(out, indent=2, ensure_ascii=False) + "\n")
    print(f"[capture] Written: {out_path}", file=sys.stderr)
    print(f"[capture] cch = {billing_cch!r}", file=sys.stderr)
    print(f"[capture] cc_version suffix = {billing_suffix!r}", file=sys.stderr)
    print(f"[capture] conduit_diff = {json.dumps(conduit_diff, indent=2)}", file=sys.stderr)
    print("[capture] Done — you can Ctrl+C mitmdump now.", file=sys.stderr)
