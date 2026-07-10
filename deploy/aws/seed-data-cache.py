"""Lambda: copy banhmi fetch cache from GCS (S3-compat interop) to S3.

Resumable — compares dest vs source by key+size, copies only what's missing
or changed. Stops before Lambda timeout to allow re-invocation.

Env vars:
  GCS_BUCKET    source bucket on GCS          (danny-banhmi-data)
  GCS_PREFIX    prefix to list under           (files/)
  DEST_BUCKET   destination S3 bucket          (danny-banhmi-data-vn)
  SSM_REGION    region for SSM parameter fetch (ap-southeast-1)
  WORKERS       concurrent copy threads        (8)
"""

import os
import json
from concurrent.futures import ThreadPoolExecutor, as_completed

import boto3
from botocore.config import Config

# 64 MiB threshold — above this, stream via upload_fileobj.
LARGE_OBJECT = 64 * 1024 * 1024
# Stop submitting new copies when less than this many ms remain.
TIME_GUARD_MS = 90_000
MAX_ERRORS = 10


def _get_config():
    return {
        "gcs_bucket": os.environ.get("GCS_BUCKET", "danny-banhmi-data"),
        "gcs_prefix": os.environ.get("GCS_PREFIX", "files/"),
        "dest_bucket": os.environ.get("DEST_BUCKET", "danny-banhmi-data-vn"),
        "ssm_region": os.environ.get("SSM_REGION", "ap-southeast-1"),
        "workers": int(os.environ.get("WORKERS", "8")),
    }


def _fetch_ssm_params(region):
    """Read GCS HMAC credentials from SSM SecureString."""
    ssm = boto3.client("ssm", region_name=region)
    try:
        access = ssm.get_parameter(Name="/banhmi/gcs-hmac-access", WithDecryption=True)
        secret = ssm.get_parameter(Name="/banhmi/gcs-hmac-secret", WithDecryption=True)
    except Exception as exc:
        raise RuntimeError(f"SSM parameter fetch failed: {exc}") from exc
    return access["Parameter"]["Value"], secret["Parameter"]["Value"]


def _build_gcs_client(access_key, secret_key):
    """boto3 S3 client aimed at GCS XML interop."""
    return boto3.client(
        "s3",
        endpoint_url="https://storage.googleapis.com",
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        config=Config(signature_version="s3v4", region_name="auto"),
    )


def _list_dest_keys(s3, bucket, prefix):
    """Return {key: size} for all objects in the destination bucket under prefix."""
    dest = {}
    paginator = s3.get_paginator("list_objects_v2")
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
        for obj in page.get("Contents", []):
            dest[obj["Key"]] = obj["Size"]
    return dest


def _list_gcs_keys(gcs, bucket, prefix):
    """Yield (key, size) from GCS using V1 ListObjects (interop-safe)."""
    paginator = gcs.get_paginator("list_objects")
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
        for obj in page.get("Contents", []):
            yield obj["Key"], obj["Size"]


def _copy_object(gcs, gcs_bucket, s3, dest_bucket, key, size):
    """Download from GCS and upload to S3. Streams large objects."""
    resp = gcs.get_object(Bucket=gcs_bucket, Key=key)
    body = resp["Body"]
    if size > LARGE_OBJECT:
        s3.upload_fileobj(body, dest_bucket, key)
    else:
        s3.put_object(Bucket=dest_bucket, Key=key, Body=body.read())
    return size


def lambda_handler(event, context):
    cfg = _get_config()
    access_key, secret_key = _fetch_ssm_params(cfg["ssm_region"])

    gcs = _build_gcs_client(access_key, secret_key)
    s3 = boto3.client("s3")

    # Build destination inventory.
    print(f"Listing destination s3://{cfg['dest_bucket']}/{cfg['gcs_prefix']} ...")
    dest_map = _list_dest_keys(s3, cfg["dest_bucket"], cfg["gcs_prefix"])
    print(f"Destination has {len(dest_map)} objects.")

    # Diff against GCS source.
    print(f"Listing source gs://{cfg['gcs_bucket']}/{cfg['gcs_prefix']} ...")
    to_copy = []
    listed = 0
    for key, size in _list_gcs_keys(gcs, cfg["gcs_bucket"], cfg["gcs_prefix"]):
        listed += 1
        if dest_map.get(key) != size:
            to_copy.append((key, size))
        if listed % 500 == 0:
            print(f"  listed {listed}, queued {len(to_copy)}")

    already = listed - len(to_copy)
    print(f"Listed {listed} source objects: {already} present, {len(to_copy)} to copy.")

    copied = 0
    bytes_copied = 0
    errors = []
    timed_out = False

    def _do_copy(key, size):
        return key, _copy_object(gcs, cfg["gcs_bucket"], s3, cfg["dest_bucket"], key, size)

    with ThreadPoolExecutor(max_workers=cfg["workers"]) as pool:
        futures = {}
        idx = 0
        # Submit initial batch.
        while idx < len(to_copy) and len(futures) < cfg["workers"]:
            key, size = to_copy[idx]
            futures[pool.submit(_do_copy, key, size)] = key
            idx += 1

        while futures:
            # Wait for next completed future.
            done_iter = as_completed(futures)
            fut = next(done_iter)
            key = futures.pop(fut)
            try:
                _, nbytes = fut.result()
                copied += 1
                bytes_copied += nbytes
                if copied % 100 == 0:
                    print(f"  copied {copied} ({bytes_copied:,} bytes)")
            except Exception as exc:
                if len(errors) < MAX_ERRORS:
                    errors.append(f"{key}: {exc}")

            # Submit more work unless time is running out.
            if idx < len(to_copy):
                remaining = context.get_remaining_time_in_millis()
                if remaining < TIME_GUARD_MS:
                    print(f"Time guard hit ({remaining} ms left), draining {len(futures)} in-flight.")
                    timed_out = True
                else:
                    key, size = to_copy[idx]
                    futures[pool.submit(_do_copy, key, size)] = key
                    idx += 1

        # Drain remaining in-flight if we timed out (they're already submitted).
        # The while-loop above already drained them since we stopped submitting.

    done = not timed_out and len(errors) == 0 and idx >= len(to_copy)
    result = {
        "listed": listed,
        "already_present": already,
        "copied": copied,
        "bytes_copied": bytes_copied,
        "errors": errors,
        "done": done,
    }
    print(json.dumps(result))
    return result
