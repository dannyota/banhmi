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
import traceback

import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

INPUT_ROOT = "/kaggle/input"
OUTPUT_PATH = "/kaggle/working/vectors.jsonl.gz"
MODEL_FILENAME = "model_fp16.onnx"
TOKENIZER_FILENAME = "tokenizer.json"
MAX_LENGTH = 8192
# Two launch budgets, sized to the EMPIRICAL T4 memory model (measured from an
# exact 1,879,048,192-byte OOM at [count=256, pad=256]):
#   - ORT's unfused MHA allocates ONE contiguous workspace per layer:
#     packed QKV in FP16 (count*pad*12,288 B) + attention scores in FP32
#     (count*16*pad^2*4 B). Scores are FP32, not FP16 — 64 B per unit of area.
#   - The model declares present-KV as graph outputs, so even unfetched they
#     stay allocated for all 24 layers of a run: ~98,304 B per token
#     (24 layers x K+V x 8 KV heads x 128 dim x FP16).
# TOKEN_BUDGET bounds the retained KV (32,768 tokens -> ~3.2 GB) and the QKV
# workspace term; AREA_BUDGET bounds the FP32 score term (8M -> 512 MB).
# Predicted live peak ~5.5 GB incl. 1.2 GB weights — 2x headroom on 16 GB.
PAD_STEP = 128                 # pads quantized to multiples of this → few
                               # distinct [count, pad] shapes over the run
TOKEN_BUDGET = 32_768          # max count*pad per sess.run (retained KV +
                               # linear workspace bound)
AREA_BUDGET = 8_000_000        # max count*pad^2 per sess.run (FP32 attention
                               # score bound, 512 MB at 16 heads)
DIMS = 1024


def round_pad(length):
    """Quantize a token length up to the next PAD_STEP multiple, capped at MAX_LENGTH."""
    return min(((max(length, 1) + PAD_STEP - 1) // PAD_STEP) * PAD_STEP, MAX_LENGTH)


def count_for(pad):
    """Deterministic row count for a pad: largest count under both budgets,
    floored at 1 so an outlier near MAX_LENGTH still forms a batch."""
    return max(1, min(TOKEN_BUDGET // pad, AREA_BUDGET // (pad * pad)))


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

    # Pin ORT to the deterministic unfused attention math path (most-exercised
    # on the T4 sm_75). These pins only SELECT that math path — they do NOT fix
    # the OOM. The OOM is solved by keeping the set of distinct [count, pad]
    # input shapes SMALL and exactly repeating (see main): pads are quantized to
    # PAD_STEP multiples and every batch at a given pad runs the IDENTICAL
    # [count_for(pad), pad] shape, so the CUDA arena allocates one buffer set
    # per pad step and reuses it, instead of fragmenting/accumulating across
    # hundreds of unique [batch, seq] shapes. The unfused path allocates one
    # contiguous per-layer workspace of packed FP16 QKV + FP32 scores
    # (count*pad*12,288 + count*16*pad^2*4 bytes) and retains present-KV for
    # all 24 layers; the two budgets (count*pad <= TOKEN_BUDGET, count*pad^2
    # <= AREA_BUDGET) cap those — see the budget comment at the constants.
    # Worst case pad=8192 → count 1 → ~4.4 GB workspace, fits the T4's 16 GB.
    # ORT reads these env vars lazily at session build, so setting them before
    # InferenceSession works.
    os.environ["ORT_DISABLE_MEMORY_EFFICIENT_ATTENTION"] = "1"
    os.environ["ORT_DISABLE_FUSED_ATTENTION"] = "1"
    os.environ["ORT_DISABLE_TRT_FLASH_ATTENTION"] = "1"
    os.environ["ORT_DISABLE_FUSED_CROSS_ATTENTION"] = "1"

    # Enforce GPU: this kernel exists to exercise the CUDA attention path on a
    # T4. A silent CPU fallback is slow and defeats the purpose, so fail loudly.
    available = ort.get_available_providers()
    if "CUDAExecutionProvider" not in available:
        raise RuntimeError(
            "CUDAExecutionProvider not available — onnxruntime-gpu not installed "
            "or no GPU visible. The Kaggle kernel must request a GPU accelerator "
            f"(e.g. NvidiaTeslaT4). Available providers: {available}")

    # CPU stays only as ORT's per-op fallback for ops lacking a CUDA kernel.
    # Plain provider defaults are correct here: with a small fixed set of input
    # shapes (one [count_for(pad), pad] shape per pad step), the default arena
    # and mem-pattern planning allocate once per shape and reuse.
    providers = ["CUDAExecutionProvider", "CPUExecutionProvider"]
    opts = ort.SessionOptions()
    opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
    sess = ort.InferenceSession(model_path, sess_options=opts, providers=providers)
    active = sess.get_providers()
    print("ORT providers:", active)

    # CUDA can be "available" yet fail to initialize at session build (driver /
    # CUDA-version mismatch), silently dropping to CPU. Catch that here.
    if "CUDAExecutionProvider" not in active:
        raise RuntimeError(
            "Session fell back to CPU — CUDAExecutionProvider failed to "
            f"initialize. Active providers: {active}")

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

    # Pre-tokenize every text once (the tokenizer already truncates to
    # MAX_LENGTH), then pack shape-bucketed batches under two budgets:
    # count*pad <= TOKEN_BUDGET (retained present-KV + linear workspace) and
    # count*pad^2 <= AREA_BUDGET (FP32 attention scores — see the budget
    # comment at the constants). Pads quantize to PAD_STEP multiples and every
    # batch at a given pad runs the IDENTICAL shape [count_for(pad), pad] —
    # ONE shape per pad step over the whole run, so the CUDA arena never
    # fragments. Worst case pad=8192 → count 1 → ~4.4 GB workspace, fits the T4.
    token_ids = [e.ids for e in tokenizer.encode_batch(texts)]
    lengths = [len(ids) for ids in token_ids]
    results = [None] * len(texts)

    # Greedy pack over indices sorted by token length (ascending): within a
    # batch pads only grow, so checking the candidate's pad suffices. Close the
    # batch when adding the candidate would exceed count_for(its pad).
    order = sorted(range(len(texts)), key=lambda i: lengths[i])
    batches = []
    current = []
    for i in order:
        new_pad = round_pad(lengths[i])
        if current and len(current) + 1 > count_for(new_pad):
            batches.append(current)
            current = [i]
        else:
            current.append(i)
    if current:
        batches.append(current)

    done = 0
    total = len(texts)
    for ordinal, real in enumerate(batches):  # real = original indices
        n_real = len(real)
        final_pad = round_pad(max(lengths[i] for i in real))
        final_count = count_for(final_pad)

        input_ids = np.full((final_count, final_pad), 151643, dtype=np.int64)  # pad with EOS
        attention_mask = np.zeros((final_count, final_pad), dtype=np.int64)
        for row in range(final_count):
            # Real rows carry their text; dummy rows (row >= n_real) reuse the
            # batch's first sequence purely to hold the exact
            # [count_for(pad), pad] shape.
            i = real[row] if row < n_real else real[0]
            ids = token_ids[i]
            input_ids[row, :len(ids)] = ids
            attention_mask[row, :len(ids)] = 1

        feeds = build_feeds(input_names, input_ids, attention_mask)
        try:
            out = sess.run(["last_hidden_state"], feeds)
        except Exception as e:
            print(f"  sess.run FAILED at batch {ordinal} "
                  f"(input_ids shape count={final_count} pad={final_pad}): {repr(e)}",
                  flush=True)
            traceback.print_exc()
            raise
        hidden_states = out[0]  # [final_count, final_pad, dims]

        vecs = last_token_pool(hidden_states, attention_mask)

        # Write only the real rows, keyed by ORIGINAL index; dummy rows discarded.
        for row, i in enumerate(real):
            results[i] = vecs[row].tolist()

        done += n_real
        print(f"  batch {ordinal}: {done}/{total} embedded "
              f"(real {n_real}, shape {final_count}x{final_pad})", flush=True)

    dims = len(results[0]) if results else 0
    print("writing", len(results), "vectors, dims", dims)
    with gzip.open(OUTPUT_PATH, "wt") as f:
        for idx, embedding in zip(indices, results):
            f.write(json.dumps({"index": idx, "embedding": embedding}) + "\n")
    print("done:", OUTPUT_PATH)


main()
