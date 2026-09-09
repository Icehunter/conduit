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

// ── Reviewed betas ───────────────────────────────────────────────────────────

// Betas that appear in the upstream registry and that conduit deliberately does
// NOT send. Reported as DIVERGED with the reason rather than NEW, so a decision
// already made doesn't come back as an open question on every run. Only add an
// entry after tracing how upstream gates the beta — if it turns out to be
// always-on, it belongs in betaHeaders (internal/app/auth.go) instead.
const BETAS_DELIBERATELY_NOT_SENT = {
  "oidc-federation-2026-04-01":
    "intentional: scoped to the OIDC federation token exchange (grant_type=jwt-bearer with federation_rule_id/organization_id, upstream oidcFederationProvider). Conduit authenticates with the Max/Pro PKCE flow and never calls that path, so sending it on the messages API would be wrong.",
  "ccr-byoc-2025-07-29":
    "intentional: bring-your-own-cloud gate for Bedrock/Vertex customer-hosted deployments. Conduit only authenticates via Max/Pro OAuth and never exercises that path (COMPATIBILITY.md, 2.1.259 sync).",
};

// Upstream tools conduit deliberately does not port. Reported as DIVERGED with
// the reason rather than NEW. Conduit's scope is the agent loop, tools,
// permissions, hooks, MCP, skills and plugins — Claude-product integrations
// (claude.ai Design/Projects/Artifacts, Cron, self-hosted runners, cloud
// memory, onboarding/feedback UI) are out of scope. Only add an entry after
// reading the upstream definition; a genuinely new coding tool belongs in
// internal/tools/ instead.
const TOOLS_DELIBERATELY_NOT_PORTED = {
  Artifact: "claude.ai Artifacts add-on (ARTIFACT_ADDON_TOOLS) — product integration",
  ClaudeDesign: "claude.ai/design project integration — product integration",
  DesignSync: "claude.ai/design sync — product integration",
  Projects: "claude.ai Projects — product integration",
  CronDelete: "remote scheduled-task (cron) management — Claude cloud feature",
  CronList: "remote scheduled-task (cron) management — Claude cloud feature",
  ScheduleWakeup: "remote scheduled-task wakeup — Claude cloud feature",
  Workflow: "remote workflow orchestration — Claude cloud feature",
  Monitor: "WebSocket monitor task for remote devices — Claude cloud feature",
  EndConversation: "remote/Claude-app conversation termination — not applicable to a local TUI",
  ListAgents: "remote agent registry listing — conduit uses in-process Task/team dispatch",
  PushNotification: "Claude mobile push — product integration",
  SendFile: "Claude app file transfer — product integration",
  SendUserFile: "Claude app file transfer — product integration",
  SendUserMessage: "Claude app cross-device messaging — product integration",
  SendFeedback: "in-product feedback submission — product integration",
  ShowOnboardingRolePicker: "CC onboarding UI — product integration",
  SuggestPluginInstall: "CC marketplace plugin suggestion UI — conduit's plugin flow is /plugins",
  SearchPlugins: "CC marketplace plugin search — conduit's plugin flow is /plugins",
  ReportFindings: "ultrareview/auto-mode findings reporter — Claude cloud feature",
  TestingPermission: "CC internal permission-system test fixture — not a real tool",
  RefreshMcpTools: "MCP tool-list refresh — conduit refreshes on reconnect via /mcp",
  WaitForMcpServers: "MCP startup wait — conduit bounds MCP connect at startup instead",
  ReadMcpResourceDirTool: "MCP resource directory listing — covered by ListMcpResources",
  memory_list: "Anthropic cloud memory store — conduit uses local memdir",
  memory_read: "Anthropic cloud memory store — conduit uses local memdir",
  memory_write: "Anthropic cloud memory store — conduit uses local memdir",
  self_hosted_runner_read_health: "SELF_HOSTED_RUNNER_TOOLS — enterprise runner ops",
  self_hosted_runner_read_metrics: "SELF_HOSTED_RUNNER_TOOLS — enterprise runner ops",
  self_hosted_runner_tail_log: "SELF_HOSTED_RUNNER_TOOLS — enterprise runner ops",
};

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
  const billingHeaderVersion = billingHeader?.match(/cc_version=([\d.]+)/)?.[1] ?? null;
  const billingVersion = systemPromptGo.match(/BillingVersion\s*=\s*"([^"]+)"/)?.[1] ?? null;

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

  // Tool names as sent on the wire: every `Name() string` method under
  // internal/tools/ that returns a string literal or a package-level const.
  const registeredTools = [];
  const toolsDir = path.join(conduitDir, "internal/tools");
  if (existsSync(toolsDir)) {
    const goFiles = [];
    const walk = (dir) => {
      for (const entry of readdirSync(dir)) {
        const full = path.join(dir, entry);
        if (statSync(full).isDirectory()) walk(full);
        else if (entry.endsWith(".go") && !entry.endsWith("_test.go")) goFiles.push(full);
      }
    };
    walk(toolsDir);
    const byPkg = new Map();
    for (const f of goFiles) {
      const pkg = path.dirname(f);
      if (!byPkg.has(pkg)) byPkg.set(pkg, []);
      byPkg.get(pkg).push(readFileSync(f, "utf8"));
    }
    const nameM = /func\s+\([^)]*\)\s+Name\(\)\s+string\s*\{\s*return\s+(?:"([^"]+)"|([A-Za-z_]\w*))\s*\}/g;
    for (const sources of byPkg.values()) {
      const consts = new Map();
      for (const src of sources) {
        for (const m of src.matchAll(/^\s*(?:const\s+)?([A-Za-z_]\w*)\s*=\s*"([^"]+)"\s*(?:\/\/.*)?$/gm)) {
          if (!consts.has(m[1])) consts.set(m[1], m[2]);
        }
      }
      for (const src of sources) {
        for (const m of src.matchAll(nameM)) {
          const n = m[1] ?? consts.get(m[2]);
          if (n && !registeredTools.includes(n)) registeredTools.push(n);
        }
      }
    }
    registeredTools.sort();
  }

  return {
    version,
    cch,
    billingHeader,
    billingHeaderVersion,
    billingVersion,
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

  // Billing-header cc_version. The API validates it against the User-Agent
  // version and 400s every request on drift — the 2.1.259 sync bumped Version
  // but missed this constant and broke all requests until live-tested.
  rows.push(
    fp.version === co.billingVersion && co.billingHeaderVersion === co.billingVersion
      ? makeRow("OK", "billing cc_version", fp.version, co.billingVersion)
      : makeRow("CHANGED", "billing cc_version", fp.version, `BillingVersion=${co.billingVersion} BillingHeader=${co.billingHeaderVersion}`, "update BillingVersion and the BillingHeader literal in internal/agent/systemprompt.go"),
  );

  // cch (billing block): not reproducible — the real CLI substitutes the
  // source literal "00000" natively with a per-request value whose inputs
  // aren't visible in the decoded JS. API accepts "00000" from conduit.
  if (fp.cch === "<<bun-macro>>") {
    rows.push(makeRow("DIVERGED", "cch (billing header)", "<<not reproducible — per-request native substitution>>", co.cch, "run via mitmproxy to observe real values; see COMPATIBILITY.md 2.1.266 entry"));
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
    const { onlyInA: allNewUpstream, onlyInB: removedFromRegistry } = setDiff(fp.beta_registry, co.betaHeaders);
    const reviewed = allNewUpstream.filter((b) => b in BETAS_DELIBERATELY_NOT_SENT);
    const newUpstream = allNewUpstream.filter((b) => !(b in BETAS_DELIBERATELY_NOT_SENT));
    if (newUpstream.length) {
      rows.push(makeRow("NEW", "betas in upstream registry (not sent by conduit)", newUpstream, [], "review — some are feature-gated; add always-on ones to betaHeaders in internal/app/auth.go"));
    }
    for (const b of reviewed) {
      rows.push(makeRow("DIVERGED", `beta not adopted: ${b}`, [b], [], BETAS_DELIBERATELY_NOT_SENT[b]));
    }
    if (removedFromRegistry.length) {
      // Conduit intentionally sends more betas than the extractor finds in the
      // upstream registry. The extractor only matches specific code patterns;
      // many betas are present in upstream CC but aren't caught by the regex.
      // Downgrade to DIVERGED (not a blocking wire incompatibility).
      rows.push(makeRow("DIVERGED", "betas sent by conduit (not in upstream extracted registry)", removedFromRegistry, [], "conduit sends feature-enabling betas not found by extractor — verify periodically via mitmproxy capture"));
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

  // Tool registry. Upstream API names vs conduit's Name() literals.
  // KNOWN_TOOL_ALIASES maps upstream names that conduit implements under a
  // different wire name.
  const KNOWN_TOOL_ALIASES = {
    Agent: "Task",                       // internal/tools/agenttool/ — conduit kept the pre-2.1.x name
    Skill: "SkillTool",                  // internal/tools/skilltool/
    PowerShell: "Shell",                 // Windows shell tool; registered instead of Bash on windows
    ListMcpResourcesTool: "ListMcpResources", // internal/tools/mcpresourcetool/
    ReadMcpResourceTool: "ReadMcpResource",   // internal/tools/mcpresourcetool/
    mcp: "(mcp__<server>__<tool> dynamic registration)", // upstream MCP pass-through alias → internal/mcp
  };
  if (fp.tools?.length && co.registeredTools.length) {
    const conduitSet = new Set(co.registeredTools);
    const { onlyInA: newTools } = setDiff(fp.tools, co.registeredTools);
    const aliased = newTools.filter((t) => t in KNOWN_TOOL_ALIASES && (KNOWN_TOOL_ALIASES[t].startsWith("(") || conduitSet.has(KNOWN_TOOL_ALIASES[t])));
    const notPorted = newTools.filter((t) => !aliased.includes(t) && t in TOOLS_DELIBERATELY_NOT_PORTED);
    const genuinelyNew = newTools.filter((t) => !aliased.includes(t) && !notPorted.includes(t));
    if (aliased.length) {
      rows.push(makeRow("DIVERGED", "tools handled under different conduit names", aliased, aliased.map((t) => KNOWN_TOOL_ALIASES[t]), "implemented in conduit under a different wire name — see KNOWN_TOOL_ALIASES"));
    }
    for (const t of notPorted) {
      rows.push(makeRow("DIVERGED", `tool not ported: ${t}`, [t], [], TOOLS_DELIBERATELY_NOT_PORTED[t]));
    }
    if (genuinelyNew.length) {
      rows.push(makeRow("NEW", "tools (upstream only)", genuinelyNew, [], "new tools detected upstream — read the definition in bun-demincer/decoded; port it or add to TOOLS_DELIBERATELY_NOT_PORTED with a reason"));
    }
    if (!genuinelyNew.length) {
      rows.push(makeRow("OK", "tool_registry", `${fp.tools.length} upstream`, `${co.registeredTools.length} in conduit; ${aliased.length} aliased, ${notPorted.length} not ported`));
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
