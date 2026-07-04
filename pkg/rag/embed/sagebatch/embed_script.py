# banhmi batch embed script — runs inside a SageMaker Processing Job container.
#
# Reads input.jsonl.gz (gzip; one {"index": i, "text": "..."} per line) from
# /opt/ml/processing/input/, embeds every text with BGE-M3, and writes
# /opt/ml/processing/output/vectors.jsonl.gz (gzip; one {"index": i,
# "embedding": [..1024..]} JSON line per row).
#
# The embedding recipe MUST match banhmi's local OpenVINO embedder exactly:
# BGE-M3 dense = CLS pooling (last_hidden_state[:, 0]) + L2 normalize, 1024-d.
#
# Dependencies (sentence-transformers, torch) are pre-installed in the PyTorch
# DLC container. The script pip-installs sentence-transformers at startup if
# missing (the training DLC ships torch but not sentence-transformers).

import gzip
import json
import os
import subprocess
import sys

# Ensure sentence-transformers is available (the PyTorch training DLC does not
# ship it, but the inference DLC does; install it unconditionally for safety).
try:
    from sentence_transformers import SentenceTransformer
except ImportError:
    subprocess.check_call([sys.executable, "-m", "pip", "install", "-q", "sentence-transformers"])
    from sentence_transformers import SentenceTransformer

INPUT_DIR = "/opt/ml/processing/input"
OUTPUT_DIR = "/opt/ml/processing/output"
HF_MODEL_ID = "BAAI/bge-m3"
BATCH_SIZE = 64


def main():
    # Find the input JSONL (gzipped or plain).
    input_path = None
    for fname in sorted(os.listdir(INPUT_DIR)):
        if fname.endswith(".jsonl.gz") or fname.endswith(".jsonl"):
            input_path = os.path.join(INPUT_DIR, fname)
            break
    if not input_path:
        print("ERROR: no input JSONL found in", INPUT_DIR, file=sys.stderr)
        sys.exit(1)
    print("input file:", input_path)

    # Load input rows.
    opener = gzip.open if input_path.endswith(".gz") else open
    with opener(input_path, "rt") as f:
        rows = [json.loads(line) for line in f if line.strip()]
    indices = [int(r["index"]) for r in rows]
    texts = [r["text"] for r in rows]
    print("loaded", len(texts), "texts")

    # Load model. SentenceTransformer handles download + GPU placement.
    print("loading model:", HF_MODEL_ID)
    model = SentenceTransformer(HF_MODEL_ID)
    print("model loaded, device:", model.device)

    # Encode all texts.
    embeddings = model.encode(
        texts,
        normalize_embeddings=True,
        batch_size=BATCH_SIZE,
        show_progress_bar=True,
    )
    dims = len(embeddings[0]) if len(embeddings) > 0 else 0
    print("encoded", len(embeddings), "vectors, dims", dims)

    # Write output.
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    output_path = os.path.join(OUTPUT_DIR, "vectors.jsonl.gz")
    with gzip.open(output_path, "wt") as f:
        for idx, emb in zip(indices, embeddings):
            f.write(json.dumps({"index": idx, "embedding": emb.tolist()}) + "\n")
    print("done:", output_path)


main()
