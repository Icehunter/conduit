#!/usr/bin/env node
/**
 * Phase 0: Decode the claude binary via bun-demincer pipeline.
 *
 * Runs `claude -v` to detect the installed version, then executes the
 * 6-step bun-demincer pipeline if that version hasn't been archived yet.
 * Archives decoded outputs under decoded-<version>/ and updates a `decoded`
 * symlink pointing at the latest version (matching the existing decoded-126/
 * convention already present in bun-demincer).
 *
 * History metadata is written to:
 *   <conduit>/scripts/wire-check/history/<version>/source.json
 *   <conduit>/scripts/wire-check/history/index.json
 */

import { spawnSync } from "child_process";
import {
  existsSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
  cpSync,
  rmSync,
  symlinkSync,
  unlinkSync,
  createReadStream,
} from "fs";
import { createHash } from "crypto";
import path from "path";

export function detectVersion() {
  const result = spawnSync("claude", ["-v"], { encoding: "utf8" });
  if (result.error || result.status !== 0) {
    throw new Error(
      `[claude -v] failed: ${result.error?.message ?? result.stderr?.trim() ?? "exit " + result.status}`,
    );
  }
  const raw = (result.stdout ?? "").trim();
  const match = raw.match(/^(\d+\.\d+\.\d+)/);
  if (!match) {
    throw new Error(
      `[claude -v] could not parse semver from output: ${JSON.stringify(raw)}`,
    );
  }
  return { version: match[1], fullString: raw };
}

async function sha256File(filePath) {
  return new Promise((resolve, reject) => {
    const hash = createHash("sha256");
    const stream = createReadStream(filePath);
    stream.on("data", (d) => hash.update(d));
    stream.on("end", () => resolve(hash.digest("hex")));
    stream.on("error", reject);
  });
}

function spawnStep(label, cmd, args, cwd) {
  process.stderr.write(`[${label}] ${cmd} ${args.join(" ")}\n`);
  const result = spawnSync(cmd, args, { cwd, stdio: "inherit" });
  if (result.error) throw new Error(`[${label}] spawn error: ${result.error.message}`);
  if (result.status !== 0) throw new Error(`[${label}] exited with status ${result.status}`);
}

export async function runDecode(opts) {
  const { bunDemincerDir, historyDir, force, skipDecode } = opts;

  const { version, fullString } = detectVersion();
  process.stderr.write(`[claude -v] detected version ${version} (${fullString})\n`);

  const archivedDecoded = path.join(bunDemincerDir, `decoded-${version}`);

  if (skipDecode) {
    if (!existsSync(archivedDecoded)) {
      throw new Error(
        `--skip-decode specified but ${archivedDecoded} does not exist. Run without --skip-decode first.`,
      );
    }
    process.stderr.write(`[decode] --skip-decode: using existing ${archivedDecoded}\n`);
    return { version, decodedDir: archivedDecoded };
  }

  if (existsSync(archivedDecoded) && !force) {
    process.stderr.write(
      `[decode] skip — decoded-${version}/ already exists (use --force to re-decode)\n`,
    );
    return { version, decodedDir: archivedDecoded };
  }

  // Locate the claude binary.
  const whichResult = spawnSync("which", ["claude"], { encoding: "utf8" });
  if (whichResult.error || whichResult.status !== 0) {
    throw new Error("could not locate claude binary via `which claude`");
  }
  const claudeBin = whichResult.stdout.trim();

  // Purge scratch directories.
  for (const dir of ["decoded", "extracted", "resplit"]) {
    const p = path.join(bunDemincerDir, dir);
    if (existsSync(p)) rmSync(p, { recursive: true, force: true });
  }

  // Run the 6-step pipeline.
  spawnStep("extract", "node", ["src/extract.mjs", claudeBin, "extracted/"], bunDemincerDir);
  spawnStep(
    "resplit",
    "node",
    ["src/resplit.mjs", "extracted/src/entrypoints/cli.js", "resplit/"],
    bunDemincerDir,
  );
  spawnStep(
    "match-vendors",
    "node",
    [
      "src/match-vendors.mjs",
      "resplit/",
      "--db",
      "data/vendor-fingerprints-1000.json",
      "--classify",
    ],
    bunDemincerDir,
  );
  // cp -r resplit/ decoded/  (spawnSync cp -r is fine; no shell)
  cpSync(path.join(bunDemincerDir, "resplit"), path.join(bunDemincerDir, "decoded"), {
    recursive: true,
  });
  spawnStep("deobfuscate", "node", ["src/deobfuscate.mjs", "--dir", "decoded/"], bunDemincerDir);

  // Archive the three scratch dirs under versioned names.
  for (const dir of ["extracted", "resplit", "decoded"]) {
    const src = path.join(bunDemincerDir, dir);
    const dst = path.join(bunDemincerDir, `${dir}-${version}`);
    if (existsSync(dst)) rmSync(dst, { recursive: true, force: true });
    cpSync(src, dst, { recursive: true });
    rmSync(src, { recursive: true, force: true });
  }
  process.stderr.write(`[archive] archived to extracted-${version}/ resplit-${version}/ decoded-${version}/\n`);

  // Update the `decoded` symlink to point at the latest version.
  const decodedLink = path.join(bunDemincerDir, "decoded");
  if (existsSync(decodedLink)) {
    try {
      unlinkSync(decodedLink);
    } catch {
      rmSync(decodedLink, { recursive: true, force: true });
    }
  }
  symlinkSync(`decoded-${version}`, decodedLink);
  process.stderr.write(`[archive] decoded → decoded-${version}/\n`);

  // Record provenance in history.
  const sha = await sha256File(claudeBin);
  const stat = (await import("fs")).statSync(claudeBin);
  const source = {
    version,
    full_version_string: fullString,
    binary_path: claudeBin,
    binary_sha256: sha,
    binary_mtime: stat.mtime.toISOString(),
    binary_size: stat.size,
    decoded_at: new Date().toISOString(),
  };

  const versionHistoryDir = path.join(historyDir, version);
  mkdirSync(versionHistoryDir, { recursive: true });
  writeFileSync(path.join(versionHistoryDir, "source.json"), JSON.stringify(source, null, 2));

  // Update the index.
  const indexPath = path.join(historyDir, "index.json");
  const index = existsSync(indexPath)
    ? JSON.parse(readFileSync(indexPath, "utf8"))
    : [];
  const existing = index.findIndex((e) => e.version === version);
  if (existing >= 0) index[existing] = source;
  else index.push(source);
  index.sort((a, b) => a.version.localeCompare(b.version, undefined, { numeric: true }));
  writeFileSync(indexPath, JSON.stringify(index, null, 2));
  process.stderr.write(`[history] updated index.json with ${version}\n`);

  return { version, decodedDir: archivedDecoded };
}
