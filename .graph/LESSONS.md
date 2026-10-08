# Validated lessons

Every entry was written by `graph-lesson record`, which ran the named anchor
and refused to write unless it exited 0. Nothing reaches this file on the
strength of an agent saying so.

Delete an entry the moment it stops being true. A stale lesson is worse than
no lesson, because it is trusted.

- **2026-10-08** — Derive candidate postimages from validated edits and bind verification workspace fingerprints to them; a fresh disk read does not establish model authorship.
  - anchor: `go test ./internal/coding` (passed in 0.4s)
  - run: `.graph/runs/2026-10-08T06-46-55-077Z-derive-candidate-postimages-from-validated-edits.json`
- **2026-10-08** — Treat repository and home path substitutions as redaction: any post-call change to model-visible text invalidates exact-context training eligibility.
  - anchor: `go test ./internal/episodes -run ^TestPrivacyPathTransformationMarksEpisodeRedacted$ -count=1` (passed in 0.4s)
  - run: `.graph/runs/2026-10-08T08-31-00-224Z-treat-repository-and-home-path-substitutions-as-.json`
