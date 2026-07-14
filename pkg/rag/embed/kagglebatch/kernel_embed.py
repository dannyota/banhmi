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
import threading
import traceback

import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

INPUT_ROOT = "/kaggle/input"
OUTPUT_PATH = "/kaggle/working/vectors.jsonl.gz"
MODEL_FILENAME = "model_fp16.onnx"
TOKENIZER_FILENAME = "tokenizer.json"
MAX_LENGTH = 8192
# Memory budget for CUTLASS memory-efficient attention (sm_75 T4).
# With memory-efficient attention enabled, the O(N^2) FP32 score matrix is
# never materialized in global memory — Q*K^T is tiled in shared memory /
# registers. The dominant VRAM consumers are:
#   1. Model weights: ~1.2 GB (FP16)
#   2. Present-KV cache: 24 layers x K+V x 8 KV heads x 128 dim x FP16
#      = 98,304 B per token across all layers
#   3. Linear workspace (QKV projections): count * pad * 12,288 B (FP16)
# T4 has 16 GB VRAM. With ~1.2 GB weights + ~1 GB overhead, ~13.8 GB
# remains for KV + workspace. 131,072 tokens x 98,304 B = ~12.3 GB.
PAD_STEP = 128                 # pads quantized to multiples of this
TOKEN_BUDGET = 128 * 1024      # 128k tokens max count*pad per sess.run (KV cache bound)
DIMS = 1024


def round_pad(length):
    """Quantize a token length up to the next PAD_STEP multiple, capped at MAX_LENGTH."""
    return min(((max(length, 1) + PAD_STEP - 1) // PAD_STEP) * PAD_STEP, MAX_LENGTH)


def count_for(pad):
    """Deterministic row count for a pad: largest count under the KV budget,
    floored at 1 so an outlier near MAX_LENGTH still forms a batch."""
    return max(1, TOKEN_BUDGET // pad)


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


def gpu_count():
    """Number of visible NVIDIA GPUs. Kaggle's T4 accelerator option provisions
    TWO T4s; using both halves wall time AND quota (quota bills per session
    hour, not per GPU). Falls back to 1 if nvidia-smi is unavailable."""
    try:
        out = subprocess.run(["nvidia-smi", "-L"], capture_output=True, text=True, timeout=30)
        n = len([ln for ln in out.stdout.splitlines() if ln.strip().startswith("GPU ")])
        return max(1, n)
    except Exception:
        return 1


def load_session(model_dir, device_id=0):
    model_path = os.path.join(model_dir, MODEL_FILENAME)

    # Enable CUTLASS memory-efficient attention (has a dedicated sm_75 kernel
    # for T4). This tiles Q*K^T in shared memory instead of materializing the
    # full O(N^2) FP32 score matrix in global memory — dramatically reducing
    # VRAM usage and enabling much larger batches. Flash attention (sm_80+)
    # stays disabled since T4 is sm_75.
    os.environ.pop("ORT_DISABLE_MEMORY_EFFICIENT_ATTENTION", None)
    os.environ["ORT_DISABLE_FUSED_ATTENTION"] = "0"
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

    providers = [("CUDAExecutionProvider", {
        "device_id": device_id,
        "arena_extend_strategy": "kSameAsRequested",
    }), "CPUExecutionProvider"]
    opts = ort.SessionOptions()
    opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
    sess = ort.InferenceSession(model_path, sess_options=opts, providers=providers)
    active = sess.get_providers()
    print(f"ORT providers (gpu {device_id}):", active)

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

    # One ORT session per visible GPU (Kaggle's T4 option provisions two).
    # Sessions are built serially — concurrent session builds on one process
    # are not worth the risk — then each worker thread drives its own session;
    # ORT releases the GIL during Run, so two threads keep both GPUs busy.
    n_gpus = gpu_count()
    print("visible GPUs:", n_gpus)
    sessions = []
    input_names = None
    for dev in range(n_gpus):
        sess, input_names, _ = load_session(model_dir, dev)
        sessions.append(sess)
    global sess_inputs_global
    sess_inputs_global = sessions[0].get_inputs()

    # Pre-tokenize every text once (the tokenizer already truncates to
    # MAX_LENGTH), then pack shape-bucketed batches under the KV token budget:
    # count*pad <= TOKEN_BUDGET. With memory-efficient attention, the O(N^2)
    # score matrix is tiled in SRAM, so only the KV cache limits batch size.
    # Pads quantize to PAD_STEP multiples; full batches at a given pad run the
    # same [count_for(pad), pad] shape. pad=128 → count=1024, pad=8192 → count=16.
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

    # Deal batches round-robin across GPUs: the pack order is ascending by pad,
    # so alternating assignment gives each GPU a near-identical pad mix (and
    # each GPU still sees ONE exact shape per pad step — the arena invariant
    # holds per session/device).
    total = len(texts)
    done = 0
    done_lock = threading.Lock()
    worker_errs = [None] * len(sessions)

    def run_shard(dev, sess):
        nonlocal done
        my_batches = [(o, b) for o, b in enumerate(batches) if o % len(sessions) == dev]
        for ordinal, real in my_batches:  # real = original indices
            n_real = len(real)
            final_pad = round_pad(max(lengths[i] for i in real))
            final_count = count_for(final_pad)

            # Use actual row count — no dummy padding.  Full batches
            # still hit the repeating [count_for(pad), pad] arena shape;
            # only the tail batch of each pad step introduces one extra
            # (smaller) shape, which is fine.
            actual_count = min(final_count, n_real)
            input_ids = np.full((actual_count, final_pad), 151643, dtype=np.int64)  # pad with EOS
            attention_mask = np.zeros((actual_count, final_pad), dtype=np.int64)
            for row in range(actual_count):
                ids = token_ids[real[row]]
                input_ids[row, :len(ids)] = ids
                attention_mask[row, :len(ids)] = 1

            feeds = build_feeds(input_names, input_ids, attention_mask)
            try:
                out = sess.run(["last_hidden_state"], feeds)
            except Exception as e:
                print(f"  sess.run FAILED at batch {ordinal} gpu={dev} "
                      f"(input_ids shape count={actual_count} pad={final_pad}): {repr(e)}",
                      flush=True)
                traceback.print_exc()
                raise
            hidden_states = out[0]  # [actual_count, final_pad, dims]

            vecs = last_token_pool(hidden_states, attention_mask)

            # Disjoint indices per batch → no lock needed for results.
            for row, i in enumerate(real):
                results[i] = vecs[row].tolist()

            with done_lock:
                done += n_real
                print(f"  batch {ordinal} gpu={dev}: {done}/{total} embedded "
                      f"({actual_count}x{final_pad}, max_count {final_count})", flush=True)

    def worker(dev, sess):
        try:
            run_shard(dev, sess)
        except Exception as e:  # noqa: BLE001 — re-raised in main below
            worker_errs[dev] = e

    threads = [threading.Thread(target=worker, args=(d, s), daemon=True)
               for d, s in enumerate(sessions)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    for e in worker_errs:
        if e is not None:
            raise e

    dims = len(results[0]) if results else 0
    print("writing", len(results), "vectors, dims", dims)
    with gzip.open(OUTPUT_PATH, "wt") as f:
        for idx, embedding in zip(indices, results):
            f.write(json.dumps({"index": idx, "embedding": embedding}) + "\n")
    print("done:", OUTPUT_PATH)


main()
