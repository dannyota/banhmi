#!/usr/bin/env python3
"""EasyOCR helper for banhmi extraction (the local OCR engine).

Reads one scanned PDF and prints a single JSON object to stdout:

    {"engine": "easyocr/<ver>",
     "text": "<full document text, reading order>",
     "confidence": 0.0-1.0,
     "pages": [{"page": n, "text": "...", "confidence": c, "boxes": k}, ...]}

Classic detect + recognize (Apache-2.0): transcribes every region, never
hallucinates. Pages are rendered with PyMuPDF at --dpi and recognized with
EasyOCR (Vietnamese by default). This is the same recipe the Kaggle batch kernel
runs on a GPU; here it runs on the local CPU. Invoked like tools/markitdown_convert.py:
`python3 easyocr_ocr.py <pdf> [--languages vi] [--dpi 300] [--batch-size 32] [--gpu]`.

EasyOCR writes progress/notices to stdout, so we keep stdout for the JSON only and
send everything else to stderr.
"""
import argparse
import json
import sys

_REAL_STDOUT = sys.stdout
sys.stdout = sys.stderr  # EasyOCR/download chatter must not corrupt the JSON


def easyocr_version():
    try:
        from importlib.metadata import version

        return version("easyocr")
    except Exception:
        return "?"


def reading_order(results):
    """Join EasyOCR boxes [(bbox, text, conf), ...] into reading-order text:
    group boxes into lines by vertical overlap, lines top->bottom, boxes left->right."""
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
    if total == 0:
        return 0.0
    return sum(c * w for c, w in confs) / total


def ocr_pdf(path, languages, dpi, batch_size, gpu):
    import easyocr
    import fitz  # PyMuPDF

    langs = [l for l in languages.replace(",", " ").split() if l]
    reader = easyocr.Reader(langs, gpu=gpu, verbose=False)

    doc = fitz.open(path)
    pages, all_results, full = [], [], []
    for i, page in enumerate(doc):
        png = page.get_pixmap(dpi=dpi).tobytes("png")
        results = reader.readtext(png, detail=1, paragraph=False, batch_size=batch_size)
        text = reading_order(results)
        pages.append({"page": i, "text": text,
                      "confidence": round(weighted_confidence(results), 4),
                      "boxes": len(results)})
        full.append(text)
        all_results.extend(results)
    return {
        "engine": "easyocr/" + easyocr_version(),
        "text": "\n\n".join(full),
        "confidence": round(weighted_confidence(all_results), 4),
        "pages": pages,
    }


def main():
    ap = argparse.ArgumentParser(description="EasyOCR a scanned PDF to JSON.")
    ap.add_argument("pdf")
    ap.add_argument("--languages", default="vi")
    ap.add_argument("--dpi", type=int, default=300)
    ap.add_argument("--batch-size", type=int, default=32)
    ap.add_argument("--gpu", action="store_true")
    args = ap.parse_args()

    out = ocr_pdf(args.pdf, args.languages, args.dpi, args.batch_size, args.gpu)
    json.dump(out, _REAL_STDOUT, ensure_ascii=False)


if __name__ == "__main__":
    main()
