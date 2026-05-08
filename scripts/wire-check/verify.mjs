#!/usr/bin/env node
/**
 * Phase 2: Diff wire fingerprint against conduit's pinned constants.
 *
 * Reads wire-fingerprint.json (produced by extract.mjs), extracts conduit's
 * pinned constants from Go source via line-level regex, and emits a colored
 * terminal diff grouped by severity.
 *
 * Severity:
 *   CHANGED  — values differ; conduit MUST be updated
 *   NEW      — present upstream, absent in conduit (review and add)
 *   DIVERGED — known intentional divergence (suppressed if noted in PARITY.md)
 *   OK       — values match
 *
 * Exit 0 if only OK / DIVERGED rows; exit 1 if CHANGED or NEW rows exist.
 */

import { readFileSync, existsSync, readdirSync, statSync } from "fs";
import path from "path";

// ── ANSI helpers ─────────────────────────────────────────────────────────────

const isTTY = process.stdout.isTTY;
const c = {
  red: (s) => (isTTY ? `\x1b[31m${s}\x1b[0m` : s),
  yellow: (s) => (isTTY ? `\x1b[33m${s}\x1b[0m` : s),
  green: (s) => (isTTY ? `\x1b[32m${s}\x1b[0m` : s),
  dim: (s) => (isTTY ? `\x1b[2m${s}\x1b[0m` : s),
  bold: (s) => (isTTY ? `\x1b[1m${s}\x1b[0m` : s),
};

// ── Conduit source extraction ─────────────────────────────────────────────────

function readSource(p) {
  if (!existsSync(p)) throw new Error(`expected conduit source file not found: ${p}`);
  return readFileSync(p, "utf8");
}

function extractConduitConstants(conduitDir) {
  const mainGo = readSource(path.join(conduitDir, "cmd/conduit/main.go"));
  const systemPromptGo = readSource(path.join(conduitDir, "internal/agent/systemprompt.go"));
  const authGo = readSource(path.join(conduitDir, "internal/app/auth.go"));
  const clientGo = readSource(path.join(conduitDir, "internal/api/client.go"));
  const authConfigGo = readSource(path.join(conduitDir, "internal/auth/config.go"));
  const parityMd = existsSync(path.join(conduitDir, "PARITY.md"))
    ? readFileSync(path.join(conduitDir, "PARITY.md"), "utf8")
    : "";

  const version = mainGo.match(/var\s+Version\s*=\s*"([^"]+)"/)?.[1] ?? null;

  const billingHeader = systemPromptGo.match(/const\s+BillingHeader\s*=\s*"([^"]+)"/)?.[1] ?? null;
  const cch = billingHeader?.match(/cch=([0-9a-f]+)/)?.[1] ?? null;

  const betaBlockM = authGo.match(/betaHeaders\s*:=\s*\[\]string\{([^}]+)\}/s);
  const betaHeaders = betaBlockM
    ? [...betaBlockM[1].matchAll(/"([^"]+)"/g)].map((m) => m[1])
    : [];

  const sdkVersion = clientGo.match(/SDKPackageVersion\s*=\s*"([^"]+)"/)?.[1] ?? null;
  const anthropicVersion = clientGo.match(/AnthropicVersion\s*=\s*"([^"]+)"/)?.[1] ?? null;
  const stainlessRuntimeVersion =
    clientGo.match(/X-Stainless-Runtime-Version[^"]*"(v[\d.]+)"/)?.[1] ?? null;

  const clientId = authConfigGo.match(/ClientID:\s*"([^"]+)"/)?.[1] ?? null;
  const tokenUrl = authConfigGo.match(/TokenURL:\s*"([^"]+)"/)?.[1] ?? null;
  const claudeAIAuthorizeUrl =
    authConfigGo.match(/ClaudeAIAuthorizeURL:\s*"([^"]+)"/)?.[1] ?? null;

  const scopesAllM = authConfigGo.match(/ScopesAll\s*=\s*\[\]string\{([^}]+)\}/s);
  const scopesAll = scopesAllM
    ? [...scopesAllM[1].matchAll(/"([^"]+)"/g)].map((m) => m[1])
    : [];

  const extraHeadersM = authGo.match(/ExtraHeaders:\s*map\[string\]string\{([^}]+)\}/s);
  const extraHeaders = extraHeadersM
    ? [...extraHeadersM[1].matchAll(/"([^"]+)"\s*:/g)].map((m) => m[1])
    : [];

  // Tool dirs under internal/tools/ (each subdir is a tool package).
  const registeredTools = [];
  const toolsDir = path.join(conduitDir, "internal/tools");
  if (existsSync(toolsDir)) {
    for (const entry of readdirSync(toolsDir)) {
      if (statSync(path.join(toolsDir, entry)).isDirectory()) {
        // Normalize: "bashtool" → "BashTool", "filereadtool" → "FileReadTool"
        const name = entry.replace(/tool$/, "Tool");
        registeredTools.push(name.charAt(0).toUpperCase() + name.slice(1));
      }
    }
  }

  return {
    version,
    cch,
    billingHeader,
    betaHeaders,
    sdkVersion,
    anthropicVersion,
    stainlessRuntimeVersion,
    clientId,
    tokenUrl,
    claudeAIAuthorizeUrl,
    scopesAll,
    extraHeaders,
    registeredTools,
    parityMd,
  };
}

// ── Diff utilities ────────────────────────────────────────────────────────────

function formatVal(v) {
  if (v === null || v === undefined) return "(none)";
  if (Array.isArray(v)) return `[${v.join(", ")}]`;
  return String(v);
}

function makeRow(severity, field, upstream, conduit, note) {
  const tag = { CHANGED: c.red("CHANGED "), NEW: c.yellow("NEW     "), DIVERGED: c.dim("DIVERGED"), OK: c.green("OK      ") }[severity];
  const lines = [`${tag}  ${c.bold(field)}`, `          upstream: ${formatVal(upstream)}`, `          conduit:  ${formatVal(conduit)}`];
  if (note) lines.push(`          ${c.dim(note)}`);
  return { severity, line: lines.join("\n") };
}

function setDiff(a, b) {
  const setA = new Set(a);
  const setB = new Set(b);
  return {
    onlyInA: [...setA].filter((x) => !setB.has(x)),
    onlyInB: [...setB].filter((x) => !setA.has(x)),
  };
}

// ── Report ────────────────────────────────────────────────────────────────────

export function runVerify(opts) {
  const { fingerprintPath, conduitDir } = opts;

  if (!existsSync(fingerprintPath)) {
    throw new Error(`wire-fingerprint.json not found: ${fingerprintPath}\nRun 'make verify-wire' first.`);
  }

  const fp = JSON.parse(readFileSync(fingerprintPath, "utf8"));
  const co = extractConduitConstants(conduitDir);
  const rows = [];

  // Version.
  rows.push(
    fp.version === co.version
      ? makeRow("OK", "version", fp.version, co.version)
      : makeRow("CHANGED", "version", fp.version, co.version, "update var Version in cmd/conduit/main.go"),
  );

  // cch (billing block) — Bun compile-time macro; can't extract from decoded JS.
  if (fp.cch === "<<bun-macro>>") {
    rows.push(makeRow("DIVERGED", "cch (billing header)", "<<bun-macro: requires live capture>>", co.cch, "run via mitmproxy to get real value; see billing_note in wire-fingerprint.json"));
  } else {
    rows.push(
      fp.cch === co.cch
        ? makeRow("OK", "cch (billing header)", fp.cch, co.cch)
        : makeRow("CHANGED", "cch (billing header)", fp.cch, co.cch, "update const BillingHeader in internal/agent/systemprompt.go"),
    );
  }

  // SDK package version.
  rows.push(
    fp.sdk_package_version === co.sdkVersion
      ? makeRow("OK", "sdk_package_version", fp.sdk_package_version, co.sdkVersion)
      : makeRow("CHANGED", "sdk_package_version", fp.sdk_package_version, co.sdkVersion, "update SDKPackageVersion in internal/api/client.go"),
  );

  // anthropic-version header.
  rows.push(
    fp.anthropic_version === co.anthropicVersion || !fp.anthropic_version
      ? makeRow("OK", "anthropic-version", fp.anthropic_version, co.anthropicVersion)
      : makeRow("CHANGED", "anthropic-version", fp.anthropic_version, co.anthropicVersion, "update AnthropicVersion in internal/api/client.go"),
  );

  // Stainless runtime version (upstream uses process.version at runtime; conduit pins a literal).
  if (fp.stainless_runtime_version === "<<runtime:process.version>>") {
    rows.push(makeRow("DIVERGED", "stainless_runtime_version", "<<runtime>>", co.stainlessRuntimeVersion, "upstream reads process.version at runtime; conduit pins v22.0.0 (Bun node-compat version)"));
  } else {
    rows.push(
      !fp.stainless_runtime_version || fp.stainless_runtime_version === co.stainlessRuntimeVersion
        ? makeRow("OK", "stainless_runtime_version", fp.stainless_runtime_version, co.stainlessRuntimeVersion)
        : makeRow("CHANGED", "stainless_runtime_version", fp.stainless_runtime_version, co.stainlessRuntimeVersion, "update X-Stainless-Runtime-Version in internal/api/client.go"),
    );
  }

  // OAuth client ID.
  rows.push(
    !fp.client_id || fp.client_id === co.clientId
      ? makeRow("OK", "oauth_client_id", fp.client_id, co.clientId)
      : makeRow("CHANGED", "oauth_client_id", fp.client_id, co.clientId, "update ClientID in internal/auth/config.go"),
  );

  // Token URL.
  rows.push(
    !fp.token_url || fp.token_url === co.tokenUrl
      ? makeRow("OK", "token_url", fp.token_url, co.tokenUrl)
      : makeRow("CHANGED", "token_url", fp.token_url, co.tokenUrl, "update TokenURL in internal/auth/config.go"),
  );

  // Claude.ai authorize URL — conduit intentionally skips the claude.com/cai attribution
  // bounce (goes direct to claude.ai/oauth/authorize). Documented in internal/auth/config.go.
  // Mark DIVERGED unconditionally when upstream uses the cai bounce and conduit doesn't.
  if (fp.claude_ai_authorize_url?.includes("claude.com/cai") && co.claudeAIAuthorizeUrl?.includes("claude.ai/oauth/authorize")) {
    rows.push(makeRow("DIVERGED", "claude_ai_authorize_url", fp.claude_ai_authorize_url, co.claudeAIAuthorizeUrl, "intentional: conduit skips claude.com/cai attribution bounce (documented in internal/auth/config.go)"));
  } else if (fp.claude_ai_authorize_url && fp.claude_ai_authorize_url !== co.claudeAIAuthorizeUrl) {
    rows.push(makeRow("CHANGED", "claude_ai_authorize_url", fp.claude_ai_authorize_url, co.claudeAIAuthorizeUrl, "update ClaudeAIAuthorizeURL in internal/auth/config.go"));
  } else {
    rows.push(makeRow("OK", "claude_ai_authorize_url", fp.claude_ai_authorize_url, co.claudeAIAuthorizeUrl));
  }

  // OAuth beta header.
  const upstreamOAuthBeta = fp.oauth_beta_header;
  const conduitOAuthBeta = co.betaHeaders.find((b) => b.startsWith("oauth-")) ?? null;
  rows.push(
    upstreamOAuthBeta === conduitOAuthBeta
      ? makeRow("OK", "oauth_beta_header", upstreamOAuthBeta, conduitOAuthBeta)
      : makeRow("CHANGED", "oauth_beta_header", upstreamOAuthBeta, conduitOAuthBeta, "update betaHeaders in internal/app/auth.go"),
  );

  // Beta registry: betas in upstream registry vs conduit's sent list.
  if (fp.beta_registry?.length) {
    const { onlyInA: newUpstream, onlyInB: removedFromRegistry } = setDiff(fp.beta_registry, co.betaHeaders);
    if (newUpstream.length) {
      rows.push(makeRow("NEW", "betas in upstream registry (not sent by conduit)", newUpstream, [], "review — some are feature-gated; add always-on ones to betaHeaders in internal/app/auth.go"));
    }
    if (removedFromRegistry.length) {
      rows.push(makeRow("CHANGED", "betas sent by conduit (missing from upstream registry)", [], removedFromRegistry, "these betas may be removed/renamed upstream"));
    }
    if (!newUpstream.length && !removedFromRegistry.length) {
      rows.push(makeRow("OK", "beta_registry overlap", `${fp.beta_registry.length} upstream`, `${co.betaHeaders.length} sent by conduit`));
    }
  }

  // OAuth scopes.
  if (fp.oauth_scopes?.length && co.scopesAll.length) {
    const { onlyInA: newScopes, onlyInB: removed } = setDiff(fp.oauth_scopes, co.scopesAll);
    if (newScopes.length) rows.push(makeRow("NEW", "oauth_scopes (upstream only)", newScopes, [], "update ScopesAll in internal/auth/config.go"));
    if (removed.length) rows.push(makeRow("CHANGED", "oauth_scopes (conduit only)", [], removed));
    if (!newScopes.length && !removed.length) rows.push(makeRow("OK", "oauth_scopes", fp.oauth_scopes, co.scopesAll));
  }

  // Tool registry.
  if (fp.tools?.length && co.registeredTools.length) {
    const { onlyInA: newTools } = setDiff(fp.tools, co.registeredTools);
    if (newTools.length) {
      rows.push(makeRow("NEW", "tools (upstream only)", newTools, [], "new tools detected upstream — check bun-demincer/decoded for impl details"));
    } else {
      rows.push(makeRow("OK", "tool_registry", `${fp.tools.length} upstream`, `${co.registeredTools.length} in conduit`));
    }
  }

  // Discovered headers — broad scan for new header strings not in baseline.
  if (fp.discovered_headers?.length) {
    rows.push(makeRow("NEW", "discovered headers (not in baseline)", fp.discovered_headers, [], "potential new headers detected via broad scan — review each; add to KNOWN_HEADERS in extract.mjs if benign"));
  }

  // Render.
  const changed = rows.filter((r) => r.severity === "CHANGED");
  const newItems = rows.filter((r) => r.severity === "NEW");
  const diverged = rows.filter((r) => r.severity === "DIVERGED");
  const ok = rows.filter((r) => r.severity === "OK");

  process.stdout.write(
    `\n${c.bold("Wire fingerprint diff")}  upstream=${fp.binary_version ?? "unknown"}  conduit=${co.version ?? "unknown"}\n` +
    `extracted: ${fp.extracted_at ?? "unknown"}\n\n`,
  );

  for (const r of [...changed, ...newItems, ...diverged, ...ok]) {
    process.stdout.write(r.line + "\n");
  }

  process.stdout.write(
    `\n${c.bold("Summary")}  ${c.red(`${changed.length} CHANGED`)}  ${c.yellow(`${newItems.length} NEW`)}  ${c.dim(`${diverged.length} DIVERGED`)}  ${c.green(`${ok.length} OK`)}\n\n`,
  );

  return changed.length + newItems.length > 0 ? 1 : 0;
}
