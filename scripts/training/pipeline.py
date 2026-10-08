#!/usr/bin/env python3
"""Opt-in offline QLoRA training and PEFT merge. Plan/check use only the stdlib.

No installation, network download, upload or model promotion is performed.
Training requires local Hugging Face safetensors weights, not serving GGUFs.
"""
import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import re
import sys

DEPENDENCIES = ("torch", "transformers", "datasets", "peft", "trl", "accelerate", "bitsandbytes", "safetensors")
HEX = re.compile(r"^[a-fA-F0-9]{64}$")
REVISION = re.compile(r"^[a-fA-F0-9]{40,64}$")
EXACT_VERSION = re.compile(r"^\d+\.\d+(?:\.\d+)*(?:[a-zA-Z0-9.+-]*)$")


def file_digest(path):
    h = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def tree_digest(root, exclude=()):
    root = Path(root)
    if not root.is_dir() or root.is_symlink():
        raise ValueError("model/adapter path must be a materialized local directory")
    h = hashlib.sha256()
    count = 0
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise ValueError("artifact symlinks are not supported; materialize pinned files first")
        if path.is_file() and path.relative_to(root).as_posix() not in exclude:
            relative = path.relative_to(root).as_posix()
            h.update(relative.encode() + b"\0" + file_digest(path).encode() + b"\n")
            count += 1
    if not count:
        raise ValueError("empty artifact directory")
    return h.hexdigest()


def keys(value, allowed, label):
    if not isinstance(value, dict) or set(value) != set(allowed):
        raise ValueError(f"{label} requires exactly: {', '.join(allowed)}")


def positive(value, label, maximum):
    if type(value) is not int or not 0 < value <= maximum:
        raise ValueError(f"{label} must be an integer in 1..{maximum}")


def verify_file(pin):
    keys(pin, ("path", "sha256"), "file pin")
    if not HEX.fullmatch(pin["sha256"]) or file_digest(pin["path"]) != pin["sha256"]:
        raise ValueError("file digest mismatch")


def messages(value):
    if not isinstance(value, list) or not value:
        raise ValueError("nonempty conversational messages required")
    for msg in value:
        keys(msg, ("role", "content"), "message")
        if msg["role"] not in ("system", "user", "assistant") or not isinstance(msg["content"], str):
            raise ValueError("unsupported message type")
        if any(marker in msg["content"] for marker in ("[REDACTED:", "[OMITTED:", "<think>", "<thinking>")):
            raise ValueError("masked or private-reasoning examples are ineligible")


def dataset_rows(config):
    rows = []
    with Path(config["dataset"]["path"]).open(encoding="utf-8") as source:
        for line in source:
            if not line.endswith("\n") or not line.strip():
                raise ValueError("dataset must contain complete nonempty JSONL records")
            row = json.loads(line)
            meta = row.get("meta", {})
            if meta.get("split") != "train" or meta.get("origin") == "evaluation":
                raise ValueError("training accepts train split only; evaluation data is forbidden")
            required = ("episode_id", "candidate_id", "context_digest", "base_digest", "split_group")
            if not all(meta.get(field) for field in required):
                raise ValueError("export provenance is missing")
            allowed_labels = ("explicit_human", "reviewer_model") if config["method"] == "sft" else ("explicit_human",)
            if meta.get("label_source") not in allowed_labels:
                raise ValueError("unsupported acceptance label")
            if config["method"] == "sft":
                messages(row["messages"])
                if len(row["messages"]) < 2 or row["messages"][-1]["role"] != "assistant" or row["messages"][-2]["role"] != "user":
                    raise ValueError("SFT needs a user prompt followed by its assistant completion")
                rows.append({"prompt": row["messages"][:-1], "completion": row["messages"][-1:]})
            else:
                for field in ("prompt", "chosen", "rejected"):
                    messages(row[field])
                if row["prompt"][-1]["role"] != "user" or len(row["chosen"]) != 1 or len(row["rejected"]) != 1:
                    raise ValueError("DPO requires one shared user-ending prompt and two completions")
                if row["chosen"][0]["role"] != "assistant" or row["rejected"][0]["role"] != "assistant" or row["chosen"] == row["rejected"]:
                    raise ValueError("DPO completions must be distinct assistant messages")
                if not row.get("rejected_candidate_id"):
                    raise ValueError("DPO rejected candidate provenance is missing")
                rows.append({field: row[field] for field in ("prompt", "chosen", "rejected")})
    if not rows:
        raise ValueError("empty training dataset")
    return rows


def dependency_status(pins):
    result = {}
    for name in DEPENDENCIES:
        try:
            installed = importlib.metadata.version(name)
        except importlib.metadata.PackageNotFoundError:
            installed = None
        result[name] = {"required": pins[name], "installed": installed, "matches": installed == pins[name]}
    return result


def prepare(config):
    keys(config, ("version", "method", "model", "tokenizer", "chat_template", "dataset", "output_dir", "dependencies", "lora", "training"), "config")
    if config["version"] != 1 or config["method"] not in ("sft", "dpo"):
        raise ValueError("version 1 and method sft/dpo required")
    keys(config["dependencies"], DEPENDENCIES, "dependency pins")
    if any(not isinstance(v, str) or not EXACT_VERSION.fullmatch(v) for v in config["dependencies"].values()):
        raise ValueError("every dependency must have one exact installed version")
    hashed = {}
    for label in ("model", "tokenizer"):
        pin = config[label]
        keys(pin, ("path", "revision", "sha256"), label)
        if not REVISION.fullmatch(pin["revision"]) or not HEX.fullmatch(pin["sha256"]):
            raise ValueError("model/tokenizer need immutable revision and artifact sha256")
        path = str(Path(pin["path"]).resolve())
        if path not in hashed:
            hashed[path] = tree_digest(pin["path"])
        if hashed[path] != pin["sha256"]:
            raise ValueError(f"{label} artifact digest mismatch")
    if not list(Path(config["model"]["path"]).glob("*.safetensors")):
        raise ValueError("training requires safetensors base weights, not GGUF serving weights")
    verify_file(config["chat_template"])
    verify_file(config["dataset"])
    keys(config["lora"], ("r", "alpha", "dropout", "target_modules"), "lora")
    positive(config["lora"]["r"], "lora r", 512)
    positive(config["lora"]["alpha"], "lora alpha", 2048)
    if not isinstance(config["lora"]["dropout"], (int, float)) or not 0 <= config["lora"]["dropout"] < 1 or config["lora"]["target_modules"] != "all-linear":
        raise ValueError("LoRA requires dropout in [0,1) and target_modules all-linear")
    training = config["training"]
    keys(training, ("max_steps", "max_length", "learning_rate", "gradient_accumulation_steps", "seed"), "training")
    for name, maximum in (("max_steps", 1000000), ("max_length", 131072), ("gradient_accumulation_steps", 1024)):
        positive(training[name], name, maximum)
    if not isinstance(training["learning_rate"], (int, float)) or not 0 < training["learning_rate"] <= 0.01 or type(training["seed"]) is not int:
        raise ValueError("invalid learning rate or seed")
    output = Path(config["output_dir"])
    if output.exists() or output.is_symlink():
        raise ValueError("output directory must not already exist")
    for pin in (config["model"], config["tokenizer"]):
        if output.resolve().is_relative_to(Path(pin["path"]).resolve()):
            raise ValueError("output must not be inside source model artifacts")
    rows = dataset_rows(config)
    canonical = json.dumps(config, sort_keys=True, separators=(",", ":")).encode()
    return {"version": 1, "config_sha256": hashlib.sha256(canonical).hexdigest(), "dataset_rows": len(rows),
            "dependencies": dependency_status(config["dependencies"]), "method": config["method"], "execution": "not_started"}


def require_dependencies(plan):
    if any(not status["matches"] for status in plan["dependencies"].values()):
        raise ValueError("dependency versions are missing or differ from pins; no installation was attempted")


def offline_environment():
    for key in ("HF_HUB_OFFLINE", "TRANSFORMERS_OFFLINE", "HF_DATASETS_OFFLINE", "HF_HUB_DISABLE_TELEMETRY", "DO_NOT_TRACK"):
        os.environ[key] = "1"
    os.environ["WANDB_DISABLED"] = "true"
    os.umask(0o077)


def write_manifest(directory, manifest):
    path = Path(directory) / "artifact-manifest.json"
    with path.open("x", encoding="utf-8") as output:
        json.dump(manifest, output, indent=2, sort_keys=True)
        output.write("\n")


def train(config):
    plan = prepare(config)
    require_dependencies(plan)
    offline_environment()
    import torch
    from datasets import Dataset
    from peft import LoraConfig, prepare_model_for_kbit_training
    from transformers import AutoModelForCausalLM, AutoTokenizer, BitsAndBytesConfig
    from trl import DPOConfig, DPOTrainer, SFTConfig, SFTTrainer

    if not torch.cuda.is_available() or not torch.cuda.is_bf16_supported():
        raise ValueError("this QLoRA pipeline requires a CUDA GPU with bfloat16 support")
    tokenizer = AutoTokenizer.from_pretrained(config["tokenizer"]["path"], local_files_only=True, trust_remote_code=False)
    tokenizer.chat_template = Path(config["chat_template"]["path"]).read_text(encoding="utf-8")
    if tokenizer.pad_token is None:
        tokenizer.pad_token = tokenizer.eos_token
    model = AutoModelForCausalLM.from_pretrained(config["model"]["path"], local_files_only=True, trust_remote_code=False,
        use_safetensors=True, dtype=torch.bfloat16, device_map={"": torch.cuda.current_device()},
        quantization_config=BitsAndBytesConfig(load_in_4bit=True, bnb_4bit_quant_type="nf4", bnb_4bit_use_double_quant=True, bnb_4bit_compute_dtype=torch.bfloat16))
    model = prepare_model_for_kbit_training(model)
    model.config.use_cache = False
    lora = config["lora"]
    adapter = LoraConfig(r=lora["r"], lora_alpha=lora["alpha"], lora_dropout=lora["dropout"], target_modules=lora["target_modules"], bias="none", task_type="CAUSAL_LM")
    params = config["training"]
    args = dict(output_dir=config["output_dir"], max_steps=params["max_steps"], max_length=params["max_length"], learning_rate=params["learning_rate"],
        per_device_train_batch_size=1, gradient_accumulation_steps=params["gradient_accumulation_steps"], seed=params["seed"], bf16=True,
        gradient_checkpointing=True, report_to=[], push_to_hub=False, save_strategy="no", logging_steps=1)
    dataset = Dataset.from_list(dataset_rows(config))
    # Reject overlong examples instead of silently cutting their evidence/answer.
    for row in dataset:
        completions = [row["completion"]] if config["method"] == "sft" else [row["chosen"], row["rejected"]]
        if any(len(tokenizer.apply_chat_template(row["prompt"] + completion, tokenize=True)) > params["max_length"] for completion in completions):
            raise ValueError("example exceeds max_length; explicitly filter/re-export rather than truncate")
    if config["method"] == "sft":
        trainer = SFTTrainer(model=model, args=SFTConfig(**args, completion_only_loss=True), train_dataset=dataset, processing_class=tokenizer, peft_config=adapter)
    else:
        trainer = DPOTrainer(model=model, args=DPOConfig(**args), train_dataset=dataset, processing_class=tokenizer, peft_config=adapter)
    trainer.train()
    trainer.save_model(config["output_dir"])
    tokenizer.save_pretrained(config["output_dir"])
    plan.update(status="complete", config=config, artifact_sha256=tree_digest(config["output_dir"]))
    write_manifest(config["output_dir"], plan)
    return {"status": "complete", "output_dir": config["output_dir"], "artifact_sha256": plan["artifact_sha256"]}


def validate_adapter(directory):
    try:
        manifest = json.loads((Path(directory) / "artifact-manifest.json").read_text())
    except (FileNotFoundError, json.JSONDecodeError) as error:
        raise ValueError("adapter completion manifest required") from error
    if manifest.get("status") != "complete" or manifest.get("artifact_sha256") != tree_digest(directory, ("artifact-manifest.json",)):
        raise ValueError("incomplete or modified adapter")
    return manifest


def merge(directory, output, device):
    manifest = validate_adapter(directory)
    config = manifest["config"]
    # prepare must validate a fresh destination rather than the existing adapter.
    config = dict(config, output_dir=str(output))
    plan = prepare(config)
    require_dependencies(plan)
    offline_environment()
    import torch
    from peft import PeftModel
    from transformers import AutoModelForCausalLM, AutoTokenizer

    base = AutoModelForCausalLM.from_pretrained(config["model"]["path"], local_files_only=True, trust_remote_code=False, use_safetensors=True, dtype=torch.bfloat16, device_map={"": device})
    model = PeftModel.from_pretrained(base, str(directory), local_files_only=True, use_safetensors=True)
    merged = model.merge_and_unload(safe_merge=True)
    merged.save_pretrained(str(output), safe_serialization=True)
    tokenizer = AutoTokenizer.from_pretrained(str(directory), local_files_only=True, trust_remote_code=False)
    tokenizer.save_pretrained(str(output))
    result = {"version": 1, "status": "complete", "format": "huggingface-safetensors", "source_adapter_sha256": manifest["artifact_sha256"], "artifact_sha256": tree_digest(output), "config": config}
    write_manifest(output, result)
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    fingerprint = sub.add_parser("fingerprint")
    fingerprint.add_argument("path", type=Path)
    init = sub.add_parser("init", help="print a pinned config for already-installed training dependencies")
    for name in ("model", "tokenizer", "dataset", "chat-template", "output"):
        init.add_argument("--" + name, type=Path, required=True)
    init.add_argument("--model-revision", required=True)
    init.add_argument("--tokenizer-revision", required=True)
    init.add_argument("--method", choices=("sft", "dpo"), default="sft")
    for name in ("plan", "check", "train"):
        command = sub.add_parser(name)
        command.add_argument("--config", type=Path, required=True)
        if name == "train": command.add_argument("--execute", action="store_true", required=True)
    command = sub.add_parser("merge")
    command.add_argument("--adapter", type=Path, required=True)
    command.add_argument("--output", type=Path, required=True)
    command.add_argument("--device", choices=("cpu", "cuda"), default="cpu")
    command.add_argument("--execute", action="store_true", required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "init":
            file_pin = lambda path: {"path": str(path.resolve()), "sha256": file_digest(path)}
            result = {"version": 1, "method": args.method,
                "model": {"path": str(args.model.resolve()), "revision": args.model_revision, "sha256": tree_digest(args.model)},
                "tokenizer": {"path": str(args.tokenizer.resolve()), "revision": args.tokenizer_revision, "sha256": tree_digest(args.tokenizer)},
                "dataset": file_pin(args.dataset), "chat_template": file_pin(args.chat_template), "output_dir": str(args.output.resolve()),
                "dependencies": {name: importlib.metadata.version(name) for name in DEPENDENCIES},
                "lora": {"r": 16, "alpha": 32, "dropout": 0.05, "target_modules": "all-linear"},
                "training": {"max_steps": 100, "max_length": 4096, "learning_rate": 0.0001, "gradient_accumulation_steps": 8, "seed": 42}}
            prepare(result)
        elif args.command == "fingerprint": result = {"sha256": tree_digest(args.path) if args.path.is_dir() else file_digest(args.path)}
        elif args.command == "merge": result = merge(args.adapter, args.output, args.device)
        else:
            config = json.loads(args.config.read_text())
            if args.command == "train": result = train(config)
            else:
                result = prepare(config)
                if args.command == "check": require_dependencies(result)
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0
    except (ValueError, OSError, KeyError, TypeError, ImportError) as error:
        print(f"training: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
