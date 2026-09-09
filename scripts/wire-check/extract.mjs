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

function extractTools(files, decodedDir) {
  const manifestPath = path.join(decodedDir, "manifest.json");
  if (existsSync(manifestPath)) return extractToolsESM(files, manifestPath);
  return extractToolsMonolithic(files);
}

// Monolithic bundle (<= 2.1.226): one module per file, so a file that has both
// userFacingName and input_schema is a tool definition and every `name: "X"`
// in it is a tool name.
function extractToolsMonolithic(files) {
  const tools = new Set();
  const nameRe = /\bname:\s*"([A-Za-z][A-Za-z0-9_]+)"/g;

  for (const file of files) {
    const content = readFileSync(file, "utf8");
    if (!content.includes("userFacingName") || (!content.includes("input_schema") && !content.includes("inputSchema")))
      continue;
    let m;
    nameRe.lastIndex = 0;
    while ((m = nameRe.exec(content)) !== null) {
      tools.add(m[1]);
    }
  }
  return [...tools].sort();
}

// Code-split ESM bundle (>= 2.1.259): most tool definitions share one huge
// module, so the file-level gate above matches everything (protobuf enums,
// X.509 field names, …). Instead gate per object — `name:` must be followed
// within the same object by userFacingName and inputSchema — and resolve the
// name, which is now usually an identifier (`name: Ft`) declared either in the
// same file (`var Ft = "Edit"` or a `, Ft = "Edit"` continuation) or imported
// from another chunk, which manifest.json maps back to its decoded file.
function extractToolsESM(files, manifestPath) {
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const chunkToFile = new Map();
  for (const [chunk, mod] of Object.entries(manifest.modules ?? {})) {
    if (mod?.file) chunkToFile.set(chunk, mod.file);
  }

  const contents = new Map(files.map((f) => [path.basename(f), readFileSync(f, "utf8")]));

  // Per-file string-constant declarations. Walk each `var/let/const` statement
  // and take every `IDENT = "lit"` declarator in it, so multi-declarator lists
  // (`var a = 1,\n  Pd = "memory_read",`) resolve without also matching
  // comma-expression reassignments elsewhere.
  const stmtRe = /\b(?:var|let|const)\s+([^;]*?);/gs;
  const declRe = /(?:^|,)\s*([A-Za-z_$][\w$]*)\s*=\s*"([A-Za-z][A-Za-z0-9_]*)"\s*(?=,|$)/g;
  const localConsts = new Map();
  for (const [name, content] of contents) {
    const m = new Map();
    let s;
    stmtRe.lastIndex = 0;
    while ((s = stmtRe.exec(content)) !== null) {
      let d;
      declRe.lastIndex = 0;
      while ((d = declRe.exec(s[1])) !== null) {
        if (!m.has(d[1])) m.set(d[1], d[2]);
      }
    }
    localConsts.set(name, m);
  }

  // Per-file import bindings: ident → decoded file that exports it.
  const importRe = /^import\s*\{([^}]*)\}\s*from\s*"[^"]*\/(chunk-[A-Za-z0-9]+)\.js";/gm;
  const importsOf = (content) => {
    const out = new Map();
    let m;
    importRe.lastIndex = 0;
    while ((m = importRe.exec(content)) !== null) {
      const file = chunkToFile.get(m[2]);
      if (!file) continue;
      for (const raw of m[1].split(",")) {
        const ident = raw.trim().split(/\s+as\s+/).pop();
        if (ident && !out.has(ident)) out.set(ident, file);
      }
    }
    return out;
  };

  const tools = new Set();
  const unresolved = new Set();
  const dynamic = new Set();

  // The decoded output is prettier-formatted, so object literals can be read
  // structurally: a line ending in `({` or `= {` opens an object whose own
  // keys share the indent of the first line after it (2 deeper normally, 4
  // inside a `var a = …,` continuation), until the closing line at the
  // opening indent. A tool definition is any such object whose own keys
  // include `name`, `inputSchema`, and `call` or `userFacingName` (Skill
  // omits the latter; MCP tool arrays and destructured params have neither).
  const openRe = /^(\s*)\S.*(?:\(|=\s*)\{\s*$/;
  const keyRe = /^\s*(?:async\s+|get\s+)?([A-Za-z_$][\w$]*)\s*[:(]/;
  const nameRe = /^\s*name:\s*(?:"([A-Za-z][A-Za-z0-9_]+)"|([A-Za-z_$][\w$]*))\s*,\s*$/;

  for (const [name, content] of contents) {
    const imports = importsOf(content);
    const lines = content.split("\n");
    for (let i = 0; i < lines.length; i++) {
      const open = openRe.exec(lines[i]);
      if (!open || i + 1 >= lines.length) continue;
      const indent = /^\s*/.exec(lines[i + 1])[0];
      if (indent.length <= open[1].length) continue;
      const keys = new Map();
      for (let j = i + 1; j < lines.length; j++) {
        const l = lines[j];
        if (l.startsWith(open[1] + "}")) break;
        if (!l.startsWith(indent) || l.startsWith(indent + " ")) continue;
        const k = keyRe.exec(l);
        if (k && !keys.has(k[1])) keys.set(k[1], l);
      }
      if (!keys.has("name")) continue;
      if (!keys.has("inputSchema") && !keys.has("input_schema")) continue;
      if (!keys.has("call") && !keys.has("userFacingName")) continue;

      const nm = nameRe.exec(keys.get("name"));
      if (!nm) {
        // Template literal or member expression — the name is computed at
        // runtime (e.g. per-registered eval tool), not a static tool name.
        dynamic.add(keys.get("name").trim());
        continue;
      }
      if (nm[1]) {
        tools.add(nm[1]);
        continue;
      }
      // Imported bindings can't be redeclared at module top level, so they
      // take precedence over any same-named local (minifiers reuse short
      // names freely inside function bodies).
      const ident = nm[2];
      const lit = localConsts.get(imports.get(ident))?.get(ident) ?? localConsts.get(name)?.get(ident);
      if (lit) tools.add(lit);
      else unresolved.add(`${ident} (${name}:${i + 1})`);
    }
  }
  if (unresolved.size) {
    process.stderr.write(`[extract] WARN: unresolved tool-name identifiers: ${[...unresolved].join(", ")}\n`);
  }
  if (dynamic.size) {
    process.stderr.write(`[extract] note: ${dynamic.size} tool object(s) with computed names skipped: ${[...dynamic].join(", ")}\n`);
  }
  return [...tools].sort();
}

// Known headers used as baseline for discovery — anything NOT in this set is flagged.
// Entries are lowercase. Update this set when a new header is confirmed intentional.
const KNOWN_HEADERS = new Set([
  // Core request headers conduit sends
  "anthropic-version",
  "anthropic-beta",
  "anthropic-dangerous-direct-browser-access",
  "x-api-key",
  "x-app",
  "x-claude-code-session-id",
  "x-stainless-lang",
  "x-stainless-package-version",
  "x-stainless-os",
  "x-stainless-arch",
  "x-stainless-runtime",
  "x-stainless-runtime-version",
  "x-stainless-retry-count",
  "x-stainless-timeout",
  "x-stainless-helper",
  "x-anthropic-billing-header",
  "authorization",
  "content-type",
  "accept",
  "user-agent",
  // Standard infra / rate-limit response headers (noisy — suppress whole prefix below)
  "x-request-id",
  "x-organization-uuid",
  "x-idempotency-key",
  "x-cloud-trace-context",
  "x-forwarded-for",
  "x-client-request-id",
  "x-client-app",
  // Bridge / session headers (internal CC infra)
  "x-claude-code-session-id",
  "x-should-retry",
  "x-service-name",
  // Per-request UUID header (sent by v133+, maps to CLIENT_REQUEST_ID_HEADER)
  "x-client-request-id",
  // Auth / identity headers (v137+)
  "anthropic-admin-api-key", // admin API key variant
  "anthropic-api-key", // alternate API key header name
  "anthropic-client-platform", // client platform identifier
  "anthropic-marketplace", // marketplace feature routing
  "anthropic-plugins", // plugin manifest header
  "anthropic-workspace-id", // workspace/org scoping
  // Security / protection headers (v137+)
  "x-anthropic-additional-protection", // extra rate-limit / abuse protection
  // CCR (Claude Code Remote) bridge headers — not sent by conduit (bridge-only)
  "x-claude-remote-container-id",
  "x-claude-remote-session-id",
  // Sub-agent tracking headers (v2.1.153+) — sent only when CC spawns child agents;
  // conduit does not implement multi-agent orchestration yet, safe to suppress.
  "x-claude-code-agent-id",
  "x-claude-code-parent-agent-id",
  // Agent-skills beta header (v2.1.153+) — beta for the agent skills plugin feature.
  "anthropic-agent-skills",
  // Plugin/skill marketplace scope header (v2.1.177+) — names the active skill set
  // ("anthropic-skills", "core", "engineering", …) for marketplace routing. Not sent
  // by conduit; managed by CC's plugin marketplace layer.
  "anthropic-skills",
  // MCP client capabilities header (v2.1.177+) — base64-encoded init-projection sent
  // by CC's claudeai-mcp proxy bridge. Conduit's MCP client doesn't use this proxy path.
  "anthropic-mcp-client-capabilities",
  // Usage-limit header (v2.1.177+) — set to "extended" only behind the tengu_lantern_spool
  // LaunchDarkly flag for first-party deep-query tracking. Feature-flagged + conditional,
  // not part of conduit's baseline request.
  "anthropic-usage-limit",
  // Dispatch stream header (v2.1.226+) — CC sets it only on the dispatch streaming
  // path, and its own error handling retries without it when the connection fails
  // before the first event. Optional even upstream; conduit has no dispatch path.
  "anthropic-dispatch-id",
  // NOT a wire header (v2.1.226+) — false positive from the broad namespace scan.
  // The literal is an MCP server-kind discriminator in the cli_mcp_login switch
  // (the "anthropic_hosted_blocked" case), never a request or response header.
  "anthropic-hosted",
  // MCP discovery/registry headers (v2.1.259+) — sent by CC's hosted MCP registry
  // and discovery-protocol integration. Conduit's MCP client talks to configured
  // servers directly and doesn't go through CC's registry/discovery layer.
  "anthropic-mcp-discover-protocol-version",
  "anthropic-mcp-registry",
  // OAuth token forwarding header (v2.1.259+) — used on CC's internal bridge/proxy
  // paths that relay a session's OAuth token to another service. Conduit talks to
  // the Anthropic API directly; no such bridge exists to forward through.
  "anthropic-oauth-token",
  // Org/user account-identity headers (v2.1.259+) — same family as the already-
  // suppressed anthropic-workspace-id; scoping metadata for enterprise/org
  // accounts. Not part of conduit's request shape.
  "anthropic-organization-id",
  "anthropic-user-profile-id",
  // Telemetry opt header (v2.1.259+) — CC's own first-party telemetry pipeline.
  // Conduit has no equivalent telemetry channel to gate with this header.
  "anthropic-telemetry",
  // Request integrity signature (v2.1.259+) — computed over the official CC
  // binary's own signing material; not reproducible (or required — it's
  // supplementary to OAuth, not an acceptance gate) outside that binary.
  "x-claude-code-signature",
  // Claude Gateway (enterprise self-hosted proxy) user-identity headers
  // (v2.1.259+) — only relevant when routed through that product; conduit talks
  // to the Anthropic API directly.
  "x-claude-gateway-user-email",
  "x-claude-gateway-user-id",
  // Strict prefix-lock RESPONSE header (v2.1.266+) — not a request header. The
  // API sets it on a 400 when a replayed thinking block's signature is bound to
  // a different conversation: `block=messages.N.content.M;kind=<word>`. Conduit
  // parses it in internal/api (ThinkingMismatch) and heals in the agent loop by
  // stripping thinking blocks from that point, mirroring CC's own recovery.
  "anthropic-thinking-prefix-mismatch",
]);

// Header prefix patterns to suppress entirely — too noisy or well-understood.
const HEADER_PREFIX_SUPPRESS = [
  /^anthropic-ratelimit-/, // rate-limit response headers
  /^x-ratelimit-/, // standard rate-limit headers
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
  fingerprint.billing_note =
    'cc_version suffix is SHA256(billing_salt + firstMsg[4,7,20] + version).slice(0,3); cch is always "00000" for non-bedrock/vertex';
  fingerprint.sdk_package_version = extractSDKVersion(files) ?? fingerprint.sdk_package_version;
  fingerprint.stainless_runtime_version = extractStainlessRuntime(files);
  fingerprint.beta_registry = extractBetaRegistry(files);
  fingerprint.oauth_scopes = extractScopes(files);
  fingerprint.tools = extractTools(files, decodedDir);
  fingerprint.discovered_headers = discoverHeaders(files);

  // cch: decoded JS shows "00000" as the source literal (it's the value sent
  // for Vertex accounts). The real CLI substitutes it natively at runtime with
  // a per-request value whose inputs are not visible in the JS bundle; live
  // captures on 2.1.266 showed a different 5-hex string on every request.
  // It is not reproducible. The API accepts "00000" from conduit (has done
  // since 2.1.200), so we mark it as <<bun-macro>> to suppress verify drift.
  if (fingerprint.cch === "00000") {
    fingerprint.cch_note = "Per-request native substitution — not reproducible from the decoded bundle; source literal is 00000 (Vertex value). API accepts 00000 from conduit (see COMPATIBILITY.md).";
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
