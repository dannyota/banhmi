# banhmi batch embed kernel — runs on a Kaggle GPU session.
#
# Reads input.jsonl (one {"index": i, "text": "..."} per line) mounted from the
# banhmi-embed-input Kaggle dataset, embeds every text with BGE-M3, and writes
# /kaggle/working/vectors.jsonl.gz (gzip; one {"index": i, "embedding": [..1024..]}
# JSON line per row) — gzip keeps the download small (~4x off the raw JSON).
# The embedding recipe MUST match banhmi's local OVMS embedder exactly:
# BGE-M3 dense = CLS pooling (last_hidden_state[:, 0]) + L2 normalize, 1024-d.
#
# The model loads offline from a mounted Kaggle dataset mirror when one is
# present (a directory holding config.json + a weights file); otherwise it pulls
# BAAI/bge-m3 from HuggingFace (requires the session to have internet enabled).
# Across multiple GPUs (Kaggle's "T4 x2" accelerator), one model replica per GPU
# runs in parallel threads — ~1.9x faster than a single T4.

import glob
import gzip
import json
import os
import sys
import threading

import torch
import torch.nn.functional as F
from transformers import AutoModel, AutoTokenizer

INPUT_ROOT = "/kaggle/input"
OUTPUT_PATH = "/kaggle/working/vectors.jsonl.gz"
HF_MODEL_ID = "BAAI/bge-m3"
MAX_LENGTH = 8192
BATCH_SIZE = 64


def find_input():
    """Locate the input JSONL under /kaggle/input.

    Prefer a file under a banhmi-embed-input dataset mount; fall back to any
    input.jsonl anywhere under the input root.
    """
    preferred = glob.glob(f"{INPUT_ROOT}/**/banhmi-embed-input/**/input.jsonl", recursive=True)
    if preferred:
        return preferred[0]
    any_input = glob.glob(f"{INPUT_ROOT}/**/input.jsonl", recursive=True)
    if any_input:
        return any_input[0]
    return None


def find_model_dir():
    """Find a mounted model directory holding config.json + a weights file.

    Returns the directory path, or None if no offline mirror is mounted.
    """
    for config_path in glob.glob(f"{INPUT_ROOT}/**/config.json", recursive=True):
        d = os.path.dirname(config_path)
        has_weights = os.path.exists(os.path.join(d, "pytorch_model.bin")) or os.path.exists(
            os.path.join(d, "model.safetensors")
        )
        if has_weights:
            return d
    return None


def load_model(source, device):
    model = AutoModel.from_pretrained(source)
    if device.startswith("cuda"):
        model = model.half()
    return model.to(device).eval()


def embed_shard(texts, indices, tokenizer, model, device):
    """Embed a shard of texts on one device, writing into a shared result list.

    Texts are sorted by length so each batch pads to a similar size. Returns a
    list of (global_index, embedding) pairs.
    """
    order = sorted(range(len(texts)), key=lambda i: len(texts[i]))
    out = [None] * len(texts)
    for start in range(0, len(order), BATCH_SIZE):
        local_idx = order[start : start + BATCH_SIZE]
        batch = tokenizer(
            [texts[i] for i in local_idx],
            padding=True,
            truncation=True,
            max_length=MAX_LENGTH,
            return_tensors="pt",
        ).to(device)
        with torch.no_grad():
            outputs = model(**batch)
        # CLS pooling (the [:, 0] token) + L2 normalize — banhmi's exact recipe.
        dense = F.normalize(outputs.last_hidden_state[:, 0].float(), p=2, dim=1)
        vectors = dense.cpu().tolist()
        for j, i in enumerate(local_idx):
            out[i] = vectors[j]
    return [(indices[i], out[i]) for i in range(len(texts))]


def main():
    input_path = find_input()
    if not input_path:
        print("ERROR: no input.jsonl found under", INPUT_ROOT, file=sys.stderr)
        sys.exit(1)
    print("input file:", input_path)

    rows = [json.loads(line) for line in open(input_path) if line.strip()]
    indices = [int(r["index"]) for r in rows]
    texts = [r["text"] for r in rows]
    print("loaded", len(texts), "texts")

    model_dir = find_model_dir()
    if model_dir:
        os.environ["HF_HUB_OFFLINE"] = "1"
        os.environ["TRANSFORMERS_OFFLINE"] = "1"
        model_source = model_dir
        print("model: offline mirror at", model_dir)
    else:
        model_source = HF_MODEL_ID
        print("model: pulling", HF_MODEL_ID, "from HuggingFace")

    tokenizer = AutoTokenizer.from_pretrained(model_source)

    num_gpus = torch.cuda.device_count()
    print("device_count:", num_gpus)

    results = []
    if num_gpus >= 2:
        # One model replica per GPU; shard the texts round-robin and run the
        # shards in parallel threads. ~1.9x faster on Kaggle's 2x T4.
        models = [load_model(model_source, f"cuda:{i}") for i in range(num_gpus)]
        shard_texts = [texts[i::num_gpus] for i in range(num_gpus)]
        shard_indices = [indices[i::num_gpus] for i in range(num_gpus)]
        shard_results = [None] * num_gpus

        def work(gpu):
            shard_results[gpu] = embed_shard(
                shard_texts[gpu], shard_indices[gpu], tokenizer, models[gpu], f"cuda:{gpu}"
            )

        threads = [threading.Thread(target=work, args=(i,)) for i in range(num_gpus)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        for sr in shard_results:
            results.extend(sr)
    else:
        device = "cuda:0" if num_gpus == 1 else "cpu"
        print("device:", device, "| torch:", torch.__version__)
        model = load_model(model_source, device)
        results = embed_shard(texts, indices, tokenizer, model, device)

    dims = len(results[0][1]) if results else 0
    print("writing", len(results), "vectors, dims", dims)
    with gzip.open(OUTPUT_PATH, "wt") as f:
        for global_index, embedding in results:
            f.write(json.dumps({"index": global_index, "embedding": embedding}) + "\n")
    print("done:", OUTPUT_PATH)


main()
