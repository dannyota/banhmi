# OCR batch kernel — EasyOCR (Apache-2.0) on Kaggle GPU(s).
#
# Reads scan PDFs mounted from the banhmi-ocr-data dataset (each named
# <sha256>.pdf) plus params.json, OCRs each (PyMuPDF render + EasyOCR), and writes
# one JSON object per doc to /kaggle/working/ocr.jsonl keyed by sha256:
#   {"sha256": ..., "text": ..., "confidence": 0-1, "pages": n}  (or {"sha256", "error"})
# Shards across every available GPU. Same recipe as tools/easyocr_ocr.py.
import glob
import json
import os
import subprocess
import sys
import multiprocessing as mp


def reading_order(results):
    """Join EasyOCR boxes [(bbox, text, conf), ...] into reading-order text."""
    items = []
    for bbox, text, _conf in results:
        if not text or not text.strip():
            continue
        ys = [p[1] for p in bbox]
        xs = [p[0] for p in bbox]
        items.append({"y0": min(ys), "y1": max(ys), "x0": min(xs),
                      "cy": (min(ys) + max(ys)) / 2.0, "text": text})
    items.sort(key=lambda it: (it["cy"], it["x0"]))
    lines, cur = [], []
    for it in items:
        if cur:
            h = max(1.0, cur[-1]["y1"] - cur[-1]["y0"])
            if abs(it["cy"] - cur[-1]["cy"]) > 0.6 * h:
                lines.append(cur)
                cur = []
        cur.append(it)
    if cur:
        lines.append(cur)
    out = []
    for ln in lines:
        ln.sort(key=lambda it: it["x0"])
        out.append(" ".join(it["text"] for it in ln))
    return "\n".join(out)


def weighted_confidence(results):
    confs = [(c, len(t)) for (_b, t, c) in results if t and t.strip()]
    total = sum(w for _c, w in confs)
    return (sum(c * w for c, w in confs) / total) if total else 0.0


def worker(gpu_id, pdfs, params, ret):
    os.environ["CUDA_VISIBLE_DEVICES"] = str(gpu_id)  # set before importing torch/easyocr
    import easyocr
    import fitz  # PyMuPDF

    langs = [l for l in str(params.get("languages", "vi")).replace(",", " ").split() if l]
    dpi = int(params.get("dpi", 300))
    batch_size = int(params.get("batch_size", 32))
    reader = easyocr.Reader(langs, gpu=True, verbose=False)

    out = []
    for pdf in pdfs:
        sha = os.path.basename(pdf)
        if sha.endswith(".pdf"):
            sha = sha[:-4]
        try:
            doc = fitz.open(pdf)
            full, all_results = [], []
            for page in doc:
                png = page.get_pixmap(dpi=dpi).tobytes("png")
                r = reader.readtext(png, detail=1, paragraph=False, batch_size=batch_size)
                full.append(reading_order(r))
                all_results.extend(r)
            out.append({"sha256": sha, "text": "\n\n".join(full),
                        "confidence": round(weighted_confidence(all_results), 4), "pages": len(full)})
            print(f"[gpu{gpu_id}] {sha[:12]} {len(full)}pp ok", flush=True)
        except Exception as e:  # noqa: BLE001 — record per-doc failure, keep the batch going
            out.append({"sha256": sha, "error": str(e)})
            print(f"[gpu{gpu_id}] {sha[:12]} ERR {e}", flush=True)
    ret[gpu_id] = out


def gpu_count():
    try:
        out = subprocess.check_output(["nvidia-smi", "-L"]).decode()
        return max(1, len([l for l in out.splitlines() if l.strip()]))
    except Exception:
        return 1


if __name__ == "__main__":
    subprocess.run([sys.executable, "-m", "pip", "install", "-q", "easyocr", "pymupdf"], check=False)

    params = {}
    for p in glob.glob("/kaggle/input/**/params.json", recursive=True):
        try:
            with open(p) as f:
                params = json.load(f)
            break
        except Exception:
            pass
    pdfs = sorted(glob.glob("/kaggle/input/**/*.pdf", recursive=True), key=os.path.getsize)
    print("ocr inputs:", len(pdfs), "| params:", params, flush=True)

    n = min(gpu_count(), max(1, len(pdfs)))
    mp.set_start_method("spawn", force=True)
    ret = mp.Manager().dict()
    procs = []
    for g in range(n):
        shard = pdfs[g::n]
        if not shard:
            continue
        pr = mp.Process(target=worker, args=(g, shard, params, ret))
        pr.start()
        procs.append(pr)
    for pr in procs:
        pr.join()

    os.makedirs("/kaggle/working", exist_ok=True)
    with open("/kaggle/working/ocr.jsonl", "w") as f:
        for g in ret:
            for rec in ret[g]:
                f.write(json.dumps(rec, ensure_ascii=False) + "\n")
    print("done", flush=True)
