#!/usr/bin/env node
/**
 * Wire-check orchestrator: decode → extract → verify.
 *
 * Usage:
 *   node run.mjs [options]
 *
 * Options:
 *   --bun-demincer <dir>   Path to bun-demincer repo (default: ../../../bun-demincer)
 *   --conduit <dir>        Path to conduit repo root (default: ../../..)
 *   --skip-decode          Skip Phase 0; require decoded-<version>/ to exist
 *   --force                Force Phase 0 even if decoded-<version>/ already exists
 *   --against <version>    Diff history/<from>/wire-fingerprint.json instead of current binary
 *   --no-verify            Run Phase 0+1 only (extract but do not diff vs conduit)
 */

import path from "path";
import { fileURLToPath } from "url";
import { existsSync } from "fs";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));

function parseArgs(argv) {
  const opts = {
    bunDemincerDir: null,
    conduitDir: null,
    skipDecode: false,
    force: false,
    againstVersion: null,
    noVerify: false,
  };

  for (let i = 0; i < argv.length; i++) {
    switch (argv[i]) {
      case "--bun-demincer": opts.bunDemincerDir = argv[++i]; break;
      case "--conduit": opts.conduitDir = argv[++i]; break;
      case "--skip-decode": opts.skipDecode = true; break;
      case "--force": opts.force = true; break;
      case "--against": opts.againstVersion = argv[++i]; break;
      case "--to": /* unused in run.mjs, handled by caller */ ++i; break;
      case "--no-verify": opts.noVerify = true; break;
      default:
        if (argv[i].startsWith("--")) {
          process.stderr.write(`Unknown flag: ${argv[i]}\n`);
          process.exit(1);
        }
    }
  }

  const repoRoot = path.resolve(scriptDir, "../..");
  opts.bunDemincerDir ??= process.env.BUN_DEMINCER_DIR ?? path.resolve(repoRoot, "../bun-demincer");
  opts.conduitDir ??= repoRoot;

  return opts;
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  const historyDir = path.join(scriptDir, "history");
  const fingerprintPath = path.join(scriptDir, "wire-fingerprint.json");

  if (!existsSync(opts.bunDemincerDir)) {
    process.stderr.write(
      `bun-demincer not found at ${opts.bunDemincerDir}\n` +
      `Clone it or set --bun-demincer or BUN_DEMINCER_DIR env var.\n`,
    );
    process.exit(1);
  }

  let version;
  let decodedDir;

  if (opts.againstVersion) {
    // Historical diff: skip decode + extract, use archived fingerprint.
    const historyFp = path.join(historyDir, opts.againstVersion, "wire-fingerprint.json");
    if (!existsSync(historyFp)) {
      process.stderr.write(`No archived fingerprint for version ${opts.againstVersion}\nExpected: ${historyFp}\n`);
      process.exit(1);
    }
    version = opts.againstVersion;
    decodedDir = path.join(opts.bunDemincerDir, `decoded-${version}`);

    if (!opts.noVerify) {
      const { runVerify } = await import("./verify.mjs");
      const code = runVerify({ fingerprintPath: historyFp, conduitDir: opts.conduitDir });
      process.exit(code);
    }
    return;
  }

  // Phase 0: decode.
  const { runDecode } = await import("./decode.mjs");
  ({ version, decodedDir } = await runDecode({
    bunDemincerDir: opts.bunDemincerDir,
    historyDir,
    force: opts.force,
    skipDecode: opts.skipDecode,
  }));

  // Phase 1: extract.
  const { runExtract } = await import("./extract.mjs");
  runExtract({ decodedDir, version, historyDir, scriptDir, outPath: fingerprintPath });

  // Phase 2: verify.
  if (!opts.noVerify) {
    const { runVerify } = await import("./verify.mjs");
    const code = runVerify({ fingerprintPath, conduitDir: opts.conduitDir });
    process.exit(code);
  }
}

main().catch((err) => {
  process.stderr.write(`[wire-check] fatal: ${err.message}\n`);
  process.exit(1);
});
