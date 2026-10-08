# Local coding episodes, evaluation and training

Start by collecting verified coding tasks with repository-local opt-in settings. Recording and enforcement default to off; recording content is a separate opt-in. **Code in a sanitized dataset remains private code.** Secret detection does not anonymize business logic or guarantee that every secret can be recognized.

## Episode collection and labels

The runtime's bounded coding workflow writes a separate append-only JSONL ledger under its coding state directory. It does not replace resumable conversation transcripts. Configure `episodes.enabled`, `episodes.includeContent`, `episodes.allowedPaths`, `episodes.excludedPaths`, and optional `episodes.retentionDays` in repository settings. For example:

```json
{
  "episodes": {
    "enabled": true,
    "includeContent": true,
    "allowedPaths": ["internal/example/**"],
    "excludedPaths": ["**/fixtures/private/**"],
    "retentionDays": 30
  }
}
```

Use the definition-of-done configuration documented with verified coding to enable that workflow. Setting recording alone does not turn ordinary chat history into training episodes. Missing allowlists deny source-bearing content. `.env`, key material, credential directories and sources escaping the repository are excluded. The recorder sanitizes all payload channels and omits private reasoning/credential fields before writing. It does not alter live file bytes. Existing transcripts, CCR output stores and external hooks retain their own behavior.

An event retains episode/run/attempt/request lineage, candidate/context/base digests, verification and separate decision provenance. Recorder failures, incomplete router context, redacted content, cancellation and truncated journals cannot silently become eligible training records. Updated local-router servers provide [structured request traces](mcp-request-traces.md); validated, complete and unredacted traces establish the actual MCP model input. Calls without this evidence remain ineligible; a successful answer alone is insufficient. A full terminal trace may include a failed task; trace completeness is separate from success.

Passing tests, applying a patch and model review are separate signals. Human labels require an explicit command against a known candidate in a completed trace:

```sh
conduit episodes accept --file /path/session.jsonl --episode EPISODE_ID --candidate CANDIDATE_ID --reason "Reviewed the patch"
conduit episodes reject --file /path/session.jsonl --episode EPISODE_ID --candidate CANDIDATE_ID --reason "Fails the required behavior"
```

These commands append decisions; they do not rewrite evidence or imply that checks passed. A running writer holds the ledger lock, so close that coding session before labeling its journal.

## Deterministic dataset export

```sh
conduit episodes export --file /path/session.jsonl --format sft --split train --manifest /path/new-manifest.json > /path/train.jsonl
conduit episodes export --file /path/session.jsonl --format sft --split validation > /path/validation.jsonl
conduit episodes export --file /path/session.jsonl --file /path/other-session.jsonl --format dpo --split train --held-out-groups group-a,group-b > /path/preferences.jsonl
```

SFT requires a complete text-only input, a coding candidate, matching passing verification and explicit acceptance. `--allow-model-review` separately permits reviewer-model SFT labels. DPO always requires explicit human preferences, the same repository identity, conditioning-context digest and base-state digest. Independently sampled episodes may pair, including across repeated `--file` inputs; pairs never cross train/validation splits. Both episode IDs are retained. A repair conditioned on new errors is **not** automatically paired with the original response. This initial exporter rejects unsupported tool/media trajectories rather than guessing their context. It may correctly export zero rows.

All attempts in a repository/task group receive the same deterministic train/validation split. Explicit held-out groups and evaluation-origin episodes are excluded. Keep the held-out group registry with dataset manifests, and pass it on every export; the exporter does not discover benchmark leakage across unrelated files or semantically equivalent tasks. Manifests contain deterministic dataset/policy digests and excluded episode IDs. New manifest paths are created exclusively, with restrictive permissions; existing manifests are not overwritten. Shell redirection permissions are controlled by your shell, so use a private destination and `umask 077` for exported JSONL.

## Held-out evaluation

Use a version-1 manifest with a full fixture commit, immutable model revision, explicit budgets and fixed checks:

```json
{
  "version": 1,
  "id": "fix-parser-case-01",
  "repo_path": "/local/fixtures/parser",
  "commit": "REPLACE_WITH_FULL_COMMIT_SHA",
  "prompt": "Fix the supplied parser task.",
  "model_revision": "REPLACE_WITH_IMMUTABLE_MODEL_ARTIFACT_ID",
  "budget": {"timeout_seconds": 600, "max_turns": 20, "max_tokens": 32768},
  "policy": {
    "enabled": true,
    "checks": [{"id": "test", "command": "make test", "timeoutSeconds": 120}]
  },
  "protected_paths": ["Makefile", "tests/parser_test.go"]
}
```

```sh
conduit evaluate --manifest /path/task.json
conduit evaluate --manifest /path/task.json --execute --authorize-checks --implement-argv '["/absolute/path/evaluation-adapter"]'
```

The first command validates only. The second creates a fresh temporary local Git clone, checks out the pinned commit, invokes the explicitly supplied executable and runs authorized checks. Changes to protected files invalidate the run. The original checkout is left intact and the temporary clone is removed. This is repository isolation, not an OS sandbox; commands need to be trusted for the host where they run.

The adapter reads task JSON on stdin, edits the working directory, and writes one usage object on stdout:

```json
{"turns": 4, "tokens": 1500, "model_revision": "PINNED_MODEL_ARTIFACT_ID"}
```

It receives `CONDUIT_EVAL_ORIGIN=evaluation` and `CONDUIT_EVAL_DISABLE_MEMORY=1`, and must propagate them into its runtime, disable persistent memory and training capture, honor cancellation, and enforce turn/token budgets itself. The evaluator forcibly bounds command wall time and validates reported usage afterward. **Token/turn accounting and model-revision reporting are a trusted adapter contract, not independently measured inference.** No built-in live-provider adapter is assumed. The Go API is `evaluation.Run(ctx, task, Options{Implement, Authorize})`; callbacks receive the same budget and isolation contract.

## Offline QLoRA and export

The optional Python entry point is `scripts/training/pipeline.py`. It never installs dependencies, downloads models, uploads artifacts or promotes a model. It needs a materialized local Hugging Face model in safetensors format, a local tokenizer and an explicitly pinned chat-template file. A serving GGUF/Q8 model is not the base training artifact. Symlinked cache snapshots must be materialized before fingerprinting.

1. On the intended training environment, generate a config from already-installed exact package versions and local artifacts:

   ```sh
   python3 scripts/training/pipeline.py init --model /models/base --model-revision FULL_REVISION_SHA --tokenizer /models/tokenizer --tokenizer-revision FULL_REVISION_SHA --dataset /private/train.jsonl --chat-template /models/chat_template.jinja --output /private/new-adapter > /private/train-config.json
   ```

2. Inspect/edit the LoRA and finite training budgets, then run the read-only checks:

   ```sh
   python3 scripts/training/pipeline.py plan --config /private/train-config.json
   python3 scripts/training/pipeline.py check --config /private/train-config.json
   ```

3. Explicitly start training only on suitable hardware:

   ```sh
   python3 scripts/training/pipeline.py train --config /private/train-config.json --execute
   ```

4. Explicitly merge a completed adapter into a new safetensors artifact:

   ```sh
   python3 scripts/training/pipeline.py merge --adapter /private/new-adapter --output /private/new-merged --device cpu --execute
   ```

5. Run the held-out suite against the candidate artifact before separately configuring its serving endpoint. Nothing in this pipeline changes the active model.

`init --method dpo` uses preference JSONL. The generated config pins `model` and `tokenizer` (`path`, `revision`, directory `sha256`); `dataset` and `chat_template` (`path`, file `sha256`); exact versions of `torch`, `transformers`, `datasets`, `peft`, `trl`, `accelerate`, `bitsandbytes`, and `safetensors`; and explicit `lora`/`training` fields. `fingerprint PATH` prints a file/directory digest independently. Fingerprinting reads all model bytes, so its time depends on artifact size and storage throughput.

Training uses NF4 4-bit base weights, double quantization, LoRA over linear layers and completion-only SFT loss. It rejects overlength examples instead of silently cutting context or answers. This initial execution path requires CUDA with bfloat16 support. Merging requires enough memory for the unquantized model. It exports Hugging Face safetensors; conversion to a particular serving format is a separate explicit step. Completion manifests bind configuration, dependencies, dataset and output artifact. A failed training run has no valid completion manifest and cannot be merged through this tool.

The standard-library tests validate pinning, tamper detection, dataset eligibility and side-effect-free planning. **GPU training, Qwen3.8 architecture compatibility, adapter quality and serving conversion have not been validated by those tests.** Use dependency versions that support the actual base model; the script verifies exact installed versions rather than selecting or installing them.

Implementation references: [TRL SFT trainer](https://huggingface.co/docs/trl/sft_trainer), [TRL DPO trainer](https://huggingface.co/docs/trl/dpo_trainer), [Transformers bitsandbytes](https://huggingface.co/docs/transformers/quantization/bitsandbytes), [PEFT quantization](https://huggingface.co/docs/peft/developer_guides/quantization).
