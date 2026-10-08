"""Standard-library-only validation tests; no models or training dependencies."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("pipeline", Path(__file__).with_name("pipeline.py"))
pipeline = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(pipeline)


class PipelineTest(unittest.TestCase):
    def fixture(self, root):
        model = root / "model"
        model.mkdir()
        (model / "config.json").write_text("{}")
        (model / "model.safetensors").write_bytes(b"fixture")
        template = root / "template.jinja"
        template.write_text("{{ messages }}")
        dataset = root / "train.jsonl"
        row = {"messages": [{"role": "user", "content": "fix"}, {"role": "assistant", "content": "fixed"}],
               "meta": {"split": "train", "split_group": "task", "label_source": "explicit_human", "episode_id": "e", "candidate_id": "c", "context_digest": "context", "base_digest": "base"}}
        dataset.write_text(json.dumps(row) + "\n")
        pin = lambda p: {"path": str(p), "sha256": pipeline.file_digest(p)}
        return {"version": 1, "method": "sft", "model": {"path": str(model), "revision": "a" * 40, "sha256": pipeline.tree_digest(model)},
                "tokenizer": {"path": str(model), "revision": "a" * 40, "sha256": pipeline.tree_digest(model)},
                "chat_template": pin(template), "dataset": pin(dataset), "output_dir": str(root / "adapter"),
                "dependencies": {name: "1.2.3" for name in pipeline.DEPENDENCIES},
                "lora": {"r": 8, "alpha": 16, "dropout": 0.0, "target_modules": "all-linear"},
                "training": {"max_steps": 2, "max_length": 256, "learning_rate": 0.0001, "gradient_accumulation_steps": 1, "seed": 7}}

    def test_plan_without_dependencies_or_side_effects(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            config = self.fixture(root)
            result = pipeline.prepare(config)
            self.assertEqual(result["dataset_rows"], 1)
            self.assertFalse((root / "adapter").exists())

    def test_tampering_and_unpinned_inputs(self):
        for case in ("dataset", "model", "revision", "budget", "unknown", "dependency"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                config = self.fixture(root)
                if case == "dataset": Path(config["dataset"]["path"]).write_text("different\n")
                if case == "model": (Path(config["model"]["path"]) / "config.json").write_text("changed")
                if case == "revision": config["model"]["revision"] = "latest"
                if case == "budget": config["training"]["max_steps"] = 0
                if case == "unknown": config["upload"] = True
                if case == "dependency": config["dependencies"]["trl"] = ">=1.0"
                with self.assertRaises(ValueError): pipeline.prepare(config)

    def test_forbidden_training_rows(self):
        for case in ("held_out", "evaluation", "timeout", "masked"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temp:
                config = self.fixture(Path(temp))
                path = Path(config["dataset"]["path"])
                row = json.loads(path.read_text())
                if case == "held_out": row["meta"]["split"] = "held_out"
                if case == "evaluation": row["meta"]["origin"] = "evaluation"
                if case == "timeout": row["meta"]["label_source"] = "timeout"
                if case == "masked": row["messages"][-1]["content"] = "[REDACTED:token]"
                path.write_text(json.dumps(row) + "\n")
                config["dataset"]["sha256"] = pipeline.file_digest(path)
                with self.assertRaises(ValueError): pipeline.prepare(config)

    def test_merge_requires_artifact_manifest(self):
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaises(ValueError): pipeline.validate_adapter(Path(temp))


if __name__ == "__main__":
    unittest.main()
