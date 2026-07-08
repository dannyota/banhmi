# banhmi batch embed kernel — runs on a Kaggle GPU session.
#
# Reads input.jsonl (one {"index": i, "text": "..."} per line) mounted from the
# banhmi-embed-input Kaggle dataset, embeds every text with Qwen3-Embedding-0.6B
# (ONNX FP16, onnxruntime), and writes /kaggle/working/vectors.jsonl.gz (gzip;
# one {"index": i, "embedding": [..1024..]} JSON line per row).
#
# The embedding recipe MUST match banhmi's Go ONNX embedder exactly:
# Qwen3-Embedding dense = last-token pooling + L2 normalize, 1024-d.
# Documents are embedded WITHOUT an instruction prefix (asymmetric model —
# only queries get "Instruct: ...\nQuery:...").
#
# The model loads offline from a mounted Kaggle dataset mirror
# (danhsoftware/qwen3-embedding-06b-onnx-fp16 containing model_fp16.onnx +
# model_fp16.onnx_data + tokenizer.json). Internet is enabled for
# pip-installing onnxruntime-gpu and tokenizers (not pre-installed on Kaggle
# GPU images). Pin to a version compatible with Kaggle's CUDA 12.x — ORT
# 1.27.0 GPU requires CUDA 13.

import subprocess, sys
subprocess.check_call([sys.executable, "-m", "pip", "install", "-q",
                       "onnxruntime-gpu==1.26.0", "tokenizers"])

import glob
import gzip
import json
import os

import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

INPUT_ROOT = "/kaggle/input"
OUTPUT_PATH = "/kaggle/working/vectors.jsonl.gz"
MODEL_FILENAME = "model_fp16.onnx"
TOKENIZER_FILENAME = "tokenizer.json"
MAX_LENGTH = 8192
BATCH_SIZE = 128
DIMS = 1024


def find_input():
    """Locate the input JSONL under /kaggle/input."""
    preferred = glob.glob(f"{INPUT_ROOT}/**/banhmi-embed-input/**/input.jsonl", recursive=True)
    if preferred:
        return preferred[0]
    any_input = glob.glob(f"{INPUT_ROOT}/**/input.jsonl", recursive=True)
    if any_input:
        return any_input[0]
    return None


def find_model_dir():
    """Find the mounted ONNX model directory containing model_fp16.onnx + tokenizer.json."""
    for model_path in glob.glob(f"{INPUT_ROOT}/**/{MODEL_FILENAME}", recursive=True):
        d = os.path.dirname(model_path)
        if os.path.exists(os.path.join(d, TOKENIZER_FILENAME)):
            return d
    return None


def load_tokenizer(model_dir):
    tok = Tokenizer.from_file(os.path.join(model_dir, TOKENIZER_FILENAME))
    tok.enable_truncation(max_length=MAX_LENGTH)
    return tok


def load_session(model_dir):
    model_path = os.path.join(model_dir, MODEL_FILENAME)
    providers = []
    if "CUDAExecutionProvider" in ort.get_available_providers():
        providers.append("CUDAExecutionProvider")
    providers.append("CPUExecutionProvider")
    opts = ort.SessionOptions()
    opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
    sess = ort.InferenceSession(model_path, sess_options=opts, providers=providers)
    print("ORT providers:", sess.get_providers())

    input_names = [inp.name for inp in sess.get_inputs()]
    output_names = [out.name for out in sess.get_outputs()]
    print("inputs:", input_names)
    print("outputs:", output_names[:3], f"... ({len(output_names)} total)" if len(output_names) > 3 else "")
    return sess, input_names, output_names


def build_feeds(input_names, input_ids, attention_mask):
    """Build the ORT feed dict, including position_ids and empty KV cache if needed."""
    batch_size, seq_len = input_ids.shape
    feeds = {}
    for name in input_names:
        if name == "input_ids":
            feeds[name] = input_ids
        elif name == "attention_mask":
            feeds[name] = attention_mask
        elif name == "position_ids":
            feeds[name] = np.tile(np.arange(seq_len, dtype=np.int64), (batch_size, 1))
        elif name.startswith("past_key_values"):
            # Empty KV cache for decoder-style model. Shape from the model metadata:
            # [batch, num_heads, 0, head_dim] — the seq dim is 0 (no past).
            sess_input = next(i for i in sess_inputs_global if i.name == name)
            shape = []
            for dim in sess_input.shape:
                if isinstance(dim, int):
                    shape.append(dim if dim > 0 else 0)
                else:
                    shape.append(batch_size if "batch" in str(dim) else 0)
            feeds[name] = np.zeros(shape, dtype=np.float16)
    return feeds


def last_token_pool(hidden_states, attention_mask):
    """Extract the hidden state at the last non-padding position, then L2 normalize."""
    # attention_mask: 1 = real token, 0 = pad. Last real token = sum of mask - 1.
    last_indices = attention_mask.sum(axis=1).astype(np.int64) - 1
    batch_size = hidden_states.shape[0]
    vecs = hidden_states[np.arange(batch_size), last_indices]  # [batch, dims]
    norms = np.linalg.norm(vecs, axis=1, keepdims=True)
    norms = np.where(norms == 0, 1.0, norms)
    return vecs / norms


# Global ref for build_feeds to access input metadata.
sess_inputs_global = None


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
    if not model_dir:
        print("ERROR: no ONNX model found. Mount danhsoftware/qwen3-embedding-06b-onnx-fp16", file=sys.stderr)
        sys.exit(1)
    print("model dir:", model_dir)

    tokenizer = load_tokenizer(model_dir)
    sess, input_names, output_names = load_session(model_dir)
    global sess_inputs_global
    sess_inputs_global = sess.get_inputs()

    # Sort by length for efficient batching (less padding waste).
    order = sorted(range(len(texts)), key=lambda i: len(texts[i]))
    results = [None] * len(texts)

    for start in range(0, len(order), BATCH_SIZE):
        batch_idx = order[start : start + BATCH_SIZE]
        batch_texts = [texts[i] for i in batch_idx]

        encoded = tokenizer.encode_batch(batch_texts)
        max_len = max(len(e.ids) for e in encoded)
        batch_size = len(encoded)

        input_ids = np.full((batch_size, max_len), 151643, dtype=np.int64)  # pad with EOS
        attention_mask = np.zeros((batch_size, max_len), dtype=np.int64)
        for j, e in enumerate(encoded):
            seq_len = len(e.ids)
            input_ids[j, :seq_len] = e.ids
            attention_mask[j, :seq_len] = 1

        feeds = build_feeds(input_names, input_ids, attention_mask)
        out = sess.run(["last_hidden_state"], feeds)
        hidden_states = out[0]  # [batch, seq, dims]

        vecs = last_token_pool(hidden_states, attention_mask)

        for j, i in enumerate(batch_idx):
            results[i] = vecs[j].tolist()

        if (start // BATCH_SIZE) % 10 == 0:
            done = min(start + BATCH_SIZE, len(order))
            print(f"  {done}/{len(texts)} embedded")

    dims = len(results[0]) if results else 0
    print("writing", len(results), "vectors, dims", dims)
    with gzip.open(OUTPUT_PATH, "wt") as f:
        for idx, embedding in zip(indices, results):
            f.write(json.dumps({"index": idx, "embedding": embedding}) + "\n")
    print("done:", OUTPUT_PATH)


main()
