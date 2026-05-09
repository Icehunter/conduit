#!/usr/bin/env node
/**
 * Phase 1: Extract wire fingerprint from decoded bun-demincer output.
 *
 * Scans decoded/*.js (excluding vendor/) for known stable string anchors.
 * For each anchor, applies a regex to extract the relevant value. Also
 * extracts the full beta registry and tool list via pattern matching.
 *
 * Writes wire-fingerprint.json and history/<version>/wire-fingerprint.json.
 */

import { readFileSync, writeFileSync, readdirSync, statSync, existsSync, mkdirSync } from "fs";
import path from "path";

function loadAnchors(scriptDir) {
  return JSON.parse(readFileSync(path.join(scriptDir, "anchors.json"), "utf8"));
}

function listDecodedFiles(decodedDir) {
  const files = [];
  function walk(dir) {
    for (const entry of readdirSync(dir)) {
      const full = path.join(dir, entry);
      const st = statSync(full);
      if (st.isDirectory()) {
        if (entry === "vendor") continue;
        walk(full);
      } else if (entry.endsWith(".js")) {
        files.push(full);
      }
    }
  }
  walk(decodedDir);
  return files;
}

function extractSingleValue(files, anchor, regexStr, group) {
  const re = new RegExp(regexStr);
  for (const file of files) {
    const content = readFileSync(file, "utf8");
    if (!content.includes(anchor)) continue;
    const m = content.match(re);
    if (m && m[group]) return m[group];
  }
  return null;
}

function extractBetaRegistry(files) {
  const betas = new Set();
  // Match YYYY-MM-DD or YYYYMMDD date suffix (claude-code-20250219 uses no-dash format).
  const datePattern = "(?:\\d{4}-\\d{2}-\\d{2}|\\d{8})";
  const qfRe = new RegExp(`qf\\(\\s*"[^"]+"\\s*,\\s*"([a-z0-9-]+-${datePattern})"\\s*\\)`, "g");
  const assignRe = new RegExp(`=\\s*"([a-z0-9-]+-${datePattern})"\\s*[,;)]`, "g");

  for (const file of files) {
    const content = readFileSync(file, "utf8");
    if (!content.includes("claude-code-20250219") && !content.includes("oauth-2025-")) continue;
    let m;
    while ((m = qfRe.exec(content)) !== null) betas.add(m[1]);
    while ((m = assignRe.exec(content)) !== null) betas.add(m[1]);
  }
  return [...betas].sort();
}

function extractScopes(files) {
  const scopeRe = /["']((?:user|org):[a-z0-9:_]+)["']/g;
  const scopes = new Set();
  for (const file of files) {
    const content = readFileSync(file, "utf8");
    if (!content.includes("user:") && !content.includes("org:")) continue;
    let m;
    while ((m = scopeRe.exec(content)) !== null) scopes.add(m[1]);
  }
  return [...scopes].sort();
}

function extractTools(files) {
  // Extract tool names from files that look like tool definitions: must have both
  // userFacingName AND input_schema (the API shape field), ensuring we only pick up
  // real tool objects, not any object with a `name` key.
  //
  // The `name` property value is the API tool name sent in the request.
  const tools = new Set();
  const nameRe = /\bname:\s*"([A-Za-z][A-Za-z0-9_]+)"/g;

  for (const file of files) {
    const content = readFileSync(file, "utf8");
    // Require both markers to be present in the file — filters out non-tool files.
    if (!content.includes("userFacingName") || (!content.includes("input_schema") && !content.includes("inputSchema"))) continue;
    let m;
    nameRe.lastIndex = 0;
    while ((m = nameRe.exec(content)) !== null) {
      tools.add(m[1]);
    }
  }
  return [...tools].sort();
}

// Known headers used as baseline for discovery — anything NOT in this set is flagged.
// Entries are lowercase. Update this set when a new header is confirmed intentional.
const KNOWN_HEADERS = new Set([
  // Core request headers conduit sends
  "anthropic-version", "anthropic-beta", "anthropic-dangerous-direct-browser-access",
  "x-api-key", "x-app", "x-claude-code-session-id",
  "x-stainless-lang", "x-stainless-package-version", "x-stainless-os", "x-stainless-arch",
  "x-stainless-runtime", "x-stainless-runtime-version", "x-stainless-retry-count",
  "x-stainless-timeout", "x-stainless-helper", "x-anthropic-billing-header",
  "authorization", "content-type", "accept", "user-agent",
  // Standard infra / rate-limit response headers (noisy — suppress whole prefix below)
  "x-request-id", "x-organization-uuid", "x-idempotency-key",
  "x-cloud-trace-context", "x-forwarded-for",
  "x-client-request-id", "x-client-app",
  // Bridge / session headers (internal CC infra)
  "x-claude-code-session-id",
  "x-should-retry", "x-service-name",
  // Per-request UUID header (sent by v133+, maps to CLIENT_REQUEST_ID_HEADER)
  "x-client-request-id",
  // Auth / identity headers (v137+)
  "anthropic-admin-api-key",    // admin API key variant
  "anthropic-api-key",          // alternate API key header name
  "anthropic-client-platform",  // client platform identifier
  "anthropic-marketplace",      // marketplace feature routing
  "anthropic-plugins",          // plugin manifest header
  "anthropic-workspace-id",     // workspace/org scoping
  // Security / protection headers (v137+)
  "x-anthropic-additional-protection",  // extra rate-limit / abuse protection
  // CCR (Claude Code Remote) bridge headers — not sent by conduit (bridge-only)
  "x-claude-remote-container-id",
  "x-claude-remote-session-id",
]);

// Header prefix patterns to suppress entirely — too noisy or well-understood.
const HEADER_PREFIX_SUPPRESS = [
  /^anthropic-ratelimit-/,  // rate-limit response headers
  /^x-ratelimit-/,          // standard rate-limit headers
];

function discoverHeaders(files) {
  // Broad scan for string literals that look like HTTP headers with interesting prefixes.
  // Focuses on `anthropic-*`, `x-anthropic-*`, and `x-claude-*` — the namespaces where
  // Anthropic would introduce new wire-relevant headers. Excludes model names and betas
  // (betas are tracked separately via beta_registry).
  const found = new Set();

  // Match `anthropic-foo-bar` and `x-anthropic-foo` and `x-claude-foo`.
  // Deliberately excludes the generic `x-` prefix to avoid false positives from
  // infrastructure headers (x-forwarded-for, etc.) and `claude-foo` to exclude
  // model names (claude-sonnet-4-6, claude-opus-4-7) and feature slug strings.
  const headerRe = /"((?:anthropic-|x-anthropic-|x-claude-)[a-z][a-z0-9-]{2,50})"/g;

  for (const file of files) {
    const content = readFileSync(file, "utf8");
    if (!content.includes("anthropic-") && !content.includes("x-claude-")) continue;
    let m;
    while ((m = headerRe.exec(content)) !== null) {
      const h = m[1].toLowerCase();
      if (KNOWN_HEADERS.has(h)) continue;
      if (HEADER_PREFIX_SUPPRESS.some((re) => re.test(h))) continue;
      // Exclude date-suffix betas (tracked in beta_registry already).
      if (/\d{4}-\d{2}-\d{2}$/.test(h)) continue;
      // Exclude strings ending in `-` (just a prefix, not a full header name).
      if (h.endsWith("-")) continue;
      found.add(h);
    }
  }
  return [...found].sort();
}

function extractBillingSalt(files) {
  // The salt used in SHA256(salt + firstMsg[4,7,20] + VERSION) to compute the
  // cc_version build suffix. Stored as a string constant in the billing module.
  for (const file of files) {
    const content = readFileSync(file, "utf8");
    if (!content.includes("59cf53e54c78") && !content.includes("createHash") && !content.includes("cch=")) continue;
    if (!content.includes("[4, 7, 20]") && !content.includes("[4,7,20]")) continue;
    const m = content.match(/=\s*"([0-9a-f]{8,20})"\s*;/);
    if (m) return m[1];
  }
  return null;
}

function extractSDKVersion(files) {
  for (const file of files) {
    const content = readFileSync(file, "utf8");
    if (!content.includes("X-Stainless-Lang")) continue;
    const m = content.match(/var\s+\w+\s*=\s*"(\d+\.\d+\.\d+)"\s*;/);
    if (m) return m[1];
    const pkgIdx = content.indexOf("X-Stainless-Package-Version");
    if (pkgIdx !== -1) {
      const chunk = content.slice(Math.max(0, pkgIdx - 200), pkgIdx + 200);
      const v = chunk.match(/"(\d+\.\d+\.\d+)"/);
      if (v) return v[1];
    }
  }
  return null;
}

function extractStainlessRuntime(files) {
  // In v133+, X-Stainless-Runtime-Version uses globalThis.process.version at runtime —
  // there is no hardcoded literal. Return a sentinel so callers know it's dynamic.
  for (const file of files) {
    const content = readFileSync(file, "utf8");
    if (!content.includes("X-Stainless-Runtime-Version")) continue;
    // Check for a hardcoded literal first.
    const m = content.match(/X-Stainless-Runtime-Version[^"]*"(v[\d.]+)"/);
    if (m) return m[1];
    // If dynamic, return the marker so verify.mjs knows to skip the diff.
    if (content.includes("process.version")) return "<<runtime:process.version>>";
  }
  return null;
}

export function runExtract(opts) {
  const { decodedDir, version, historyDir, scriptDir, outPath } = opts;

  if (!existsSync(decodedDir)) {
    throw new Error(`decoded dir not found: ${decodedDir}`);
  }

  process.stderr.write(`[extract] scanning ${decodedDir}\n`);
  const files = listDecodedFiles(decodedDir);
  process.stderr.write(`[extract] ${files.length} JS files\n`);

  const anchors = loadAnchors(scriptDir);
  const fingerprint = {
    extracted_at: new Date().toISOString(),
    decoded_dir: decodedDir,
    binary_version: version,
  };

  for (const [key, def] of Object.entries(anchors)) {
    const val = extractSingleValue(files, def.anchor, def.regex, def.group);
    fingerprint[key] = val;
    if (!val) process.stderr.write(`[extract] WARN: could not extract ${key}\n`);
  }

  fingerprint.billing_salt = extractBillingSalt(files);
  fingerprint.billing_note = "cc_version suffix is SHA256(billing_salt + firstMsg[4,7,20] + version).slice(0,3); cch is always \"00000\" for non-bedrock/vertex";
  fingerprint.sdk_package_version = extractSDKVersion(files) ?? fingerprint.sdk_package_version;
  fingerprint.stainless_runtime_version = extractStainlessRuntime(files);
  fingerprint.beta_registry = extractBetaRegistry(files);
  fingerprint.oauth_scopes = extractScopes(files);
  fingerprint.tools = extractTools(files);
  fingerprint.discovered_headers = discoverHeaders(files);

  // cch is a Bun compile-time macro: decoded JS shows "00000" as placeholder.
  // The real per-build value can only be obtained from a live mitmproxy capture.
  // Extractor marks it so verify.mjs knows to skip the diff for this field.
  if (fingerprint.cch === "00000") {
    fingerprint.cch_note = "Bun compile-time macro placeholder — actual value requires live mitmproxy capture";
    fingerprint.cch = "<<bun-macro>>";
  }

  // billing_suffix_formula: cc_version suffix is SHA256(billing_salt + firstMsg[4,7,20] + version).slice(0,3)
  if (fingerprint.billing_salt && fingerprint.version) {
    fingerprint.billing_template = `x-anthropic-billing-header: cc_version=${fingerprint.version}.SHA256("${fingerprint.billing_salt}"+msg[4,7,20]+"${fingerprint.version}")[0:3]; cc_entrypoint=<entrypoint>; cch=<<bun-macro>>;`;
  }

  mkdirSync(path.join(historyDir, version), { recursive: true });
  const json = JSON.stringify(fingerprint, null, 2);
  writeFileSync(path.join(historyDir, version, "wire-fingerprint.json"), json);
  writeFileSync(outPath, json);
  process.stderr.write(`[extract] wrote ${outPath}\n`);

  return fingerprint;
}
