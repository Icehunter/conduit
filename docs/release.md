# Release Process

Conduit releases are cut by pushing a `v*` git tag. GoReleaser does the rest:
builds cross-platform binaries, signs the macOS universal binary ad-hoc,
publishes the GitHub Release, updates the Homebrew tap, updates the Scoop
bucket, and opens a winget PR.

## Cutting a release

```bash
make tag-patch   # bumps VERSION + tags v{x.y.z+1}
git push --follow-tags
```

(`tag-minor` and `tag-major` exist for non-patch bumps.)

The `release.yml` workflow runs on the tag, builds everything, and publishes.

## One-time setup — GitHub repos

Two repos must exist before the first release that targets them:

1. **`Icehunter/homebrew-tap`** — public, empty. Contains the `Formula/`
   directory after the first release. Local scaffold lives at
   `../homebrew-tap`.
2. **`Icehunter/scoop-bucket`** — public, empty. Contains the `bucket/`
   directory after the first release. Local scaffold lives at
   `../scoop-bucket`.

For winget, fork `microsoft/winget-pkgs` to **`Icehunter/winget-pkgs`**.
GoReleaser commits the manifest to a branch on the fork and opens a PR
upstream.

## One-time setup — secrets

Add these GitHub Actions secrets to `Icehunter/conduit`:

| Secret | Purpose | Scope |
|---|---|---|
| `HOMEBREW_TAP_TOKEN` | Push formula updates to the tap | PAT (classic) with `repo` scope on `Icehunter/homebrew-tap` |
| `SCOOP_TOKEN` | Push manifest updates to the bucket | PAT (classic) with `repo` scope on `Icehunter/scoop-bucket` |
| `WINGET_TOKEN` | Push branches to the winget fork + open PRs | PAT (classic) with `repo` and `workflow` scopes on `Icehunter/winget-pkgs` |

The default `GITHUB_TOKEN` already has write access to `Icehunter/conduit`
itself; no extra secret is needed for the GitHub Release.

> Use fine-grained PATs if your org policy requires them — they need
> `Contents: read & write` on the target repo.

## Versioning

Conduit uses semver via git tags. **Do not reset the version line** — the
existing `v1.x` history is correct. Next release after `v1.4.0` is `v1.4.1`
(patch) or `v1.5.0` (feature).

`AppVersion` defaults to `"dev"` in the source. The Makefile and GoReleaser
inject the real version from VERSION/git tag at build time.

## Local dry-run

Before pushing a tag, validate locally:

```bash
brew install goreleaser/tap/goreleaser
goreleaser check                                  # validate config
goreleaser release --snapshot --clean --skip=publish   # full build, no upload
```

`dist/` will contain every artifact a real release would upload.

## Distribution channels

| Channel | Updated by | First release |
|---|---|---|
| GitHub Releases | `release.yml` (always) | ✅ shipped |
| Homebrew tap (`Icehunter/homebrew-tap`) | `release.yml` via `HOMEBREW_TAP_TOKEN` | pending |
| Scoop bucket (`Icehunter/scoop-bucket`) | `release.yml` via `SCOOP_TOKEN` | pending |
| winget (`microsoft/winget-pkgs`) | `release.yml` via `WINGET_TOKEN` | pending |

### Homebrew core (future)

Once conduit has steady users, submit to homebrew-core via
`brew bump-formula-pr`. Until then the third-party tap is the supported path.

## Code signing

- **macOS**: GoReleaser signs the universal binary ad-hoc (`codesign -s -`).
  This is free and satisfies the Apple Silicon "must be signed" check.
  Homebrew installs do not trigger Gatekeeper because `brew` uses curl,
  which doesn't set the `com.apple.quarantine` xattr. Direct `.tar.gz`
  downloads via browser will trigger Gatekeeper — buy an Apple Developer
  ID ($99/yr) only if direct downloads become a real install path.
- **Windows**: unsigned. Scoop and winget installs do not trigger
  SmartScreen because they don't set the Mark of the Web. Direct `.exe`
  downloads via Edge/Chrome will. Buy an OV ($200–500/yr) or EV
  (~$300/yr) certificate when this becomes a complaint.
- **Linux**: no signing. Checksums + GoReleaser SBOMs are sufficient.

## In-app update notifier

`internal/updater/` queries
`https://api.github.com/repos/Icehunter/conduit/releases/latest` on REPL
startup, with a 24h cache at `~/.conduit/update-cache.json` and a 5s
timeout. Result is shown as a startup warning. `conduit update` is the
explicit subcommand. Dev builds (`AppVersion == "dev"`) skip the check.
