#!/usr/bin/env python3
"""
Controlled single-request probe for the vbpl doc/all search API.

Why this exists: we overloaded the vbpl gateway with bursty/parallel/retry
requests. This tool does the opposite — it sends EXACTLY ONE request, only when
you pass --send, and on any non-200 it STOPS and reports (it never retries and
never loops). Dry-run by default: it prints the request it WOULD send.

Usage:
  python3 probe.py                      # dry run: print the request, send nothing
  python3 probe.py --send               # send ONE request, print data.total
  python3 probe.py --send -k "an toàn thông tin"   # one request for a keyword
  python3 probe.py --send -k "" -p 1    # one request: SBV agency-only total

Edit BODY below to test a single shape at a time (e.g. toggle matchMode /
optionDoc) — always one request.
"""
import argparse
import json
import urllib.error
import urllib.request

URL = "https://vbpl-bientap-gateway.moj.gov.vn/api/qtdc/public/doc/all"
HEADERS = {
    "Origin": "https://vbpl.vn",
    "Referer": "https://vbpl.vn/",
    "User-Agent": "banhmi/0.1 (+https://github.com/dannyota/banhmi)",
    "Content-Type": "application/json",
    "Accept": "application/json",
}


def build_body(keyword: str, page_size: int) -> dict:
    return {
        "pageNumber": 1,
        "pageSize": page_size,          # 1 = just read total (cheapest)
        "keyword": keyword,             # "" = agency-only (all SBV docs)
        "agencyIds": ["62", "908"],     # SBV: Ngân hàng Nhà nước (+ legacy)
        # --- fields whose exact wire values we still want to confirm vs the
        # --- browser's DevTools request (matchMode is the suspect):
        "optionDoc": "title",           # title | content | number
        "matchMode": "all_words",       # <-- VERIFY against the browser; suspect
        "groupVbpl": False,
        "agencyLevel": "TRUNG_UONG",
        "sortBy": "issueDate",
        "sortDirection": "desc",
    }


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("-k", "--keyword", default="", help='search keyword ("" = SBV agency-only total)')
    ap.add_argument("-p", "--page-size", type=int, default=1, help="pageSize (1 = total only)")
    ap.add_argument("--send", action="store_true", help="actually send the ONE request")
    args = ap.parse_args()

    body = build_body(args.keyword, args.page_size)
    print("ONE request only:")
    print("  POST", URL)
    print("  body:", json.dumps(body, ensure_ascii=False))

    if not args.send:
        print("\nDRY RUN — nothing sent. Add --send to send exactly one request.")
        return

    try:
        req = urllib.request.Request(URL, data=json.dumps(body).encode(), headers=HEADERS)
        with urllib.request.urlopen(req, timeout=45) as resp:
            payload = json.load(resp)
    except urllib.error.HTTPError as e:
        print(f"\nHTTP {e.code} {e.reason} — STOPPING (no retry, no loop).")
        return
    except Exception as e:  # noqa: BLE001 - report and stop, never retry
        print(f"\nERROR: {e} — STOPPING (no retry, no loop).")
        return

    data = payload.get("data", {}) or {}
    print(f"\nHTTP 200  success={payload.get('success')}  data.total={data.get('total')}")
    items = data.get("items") or []
    if items:
        it = items[0]
        print(f"  first item: {it.get('docNum')} | {(it.get('title') or '')[:72]}")


if __name__ == "__main__":
    main()
