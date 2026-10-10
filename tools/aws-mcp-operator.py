#!/usr/bin/env python3
"""Run an AWS MCP script with credentials loaded from an owner-only env file."""

import argparse
import datetime
import errno
import hashlib
import hmac
import json
import os
import re
import shlex
import stat
import sys
import urllib.error
import urllib.parse
import urllib.request


ENDPOINT = "https://aws-mcp.us-east-1.api.aws/mcp"
ALLOWED_ENV = {
    "AWS_ACCESS_KEY_ID",
    "AWS_SECRET_ACCESS_KEY",
    "AWS_SESSION_TOKEN",
    "AWS_REGION",
    "AWS_DEFAULT_REGION",
}
IAM_USER_RE = re.compile(r"^[A-Za-z0-9+=,.@_-]{1,64}$")
SENSITIVE_KEYS = re.compile(
    r"authorization|access.?key|secret|session.?token|credential|password",
    re.IGNORECASE,
)


class TransportError(Exception):
    """An MCP request failed without exposing its response body."""


class ProtocolError(Exception):
    """An MCP response did not match the required JSON-RPC shape."""


class RemoteCallError(Exception):
    """The MCP server returned a definite tool or AWS operation error."""


def _open_secure_env(path):
    path = os.path.abspath(os.path.expanduser(path))
    flags = os.O_RDONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        fd = os.open(path, flags)
    except OSError as exc:
        if exc.errno == errno.ELOOP:
            raise ValueError("credentials file must not be a symlink") from None
        raise
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            raise ValueError("credentials file must be a regular file")
        if info.st_uid != os.getuid():
            raise ValueError("credentials file must be owned by the current user")
        if stat.S_IMODE(info.st_mode) & 0o077:
            raise ValueError("credentials file must not have group or other permissions")
        return os.fdopen(fd, "r", encoding="utf-8")
    except Exception:
        os.close(fd)
        raise


def _parse_value(value, line_number):
    lexer = shlex.shlex(value, posix=True)
    lexer.whitespace_split = True
    lexer.commenters = "#"
    try:
        parts = list(lexer)
    except ValueError as exc:
        raise ValueError(f"invalid quoted value on line {line_number}") from exc
    if not parts:
        return ""
    if len(parts) != 1:
        raise ValueError(f"invalid env value on line {line_number}")
    return parts[0]


def load_credentials(path):
    values = {}
    with _open_secure_env(path) as source:
        for line_number, raw in enumerate(source, 1):
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            if line.startswith("export "):
                line = line[7:].lstrip()
            if "=" not in line:
                continue
            name, value = line.split("=", 1)
            name = name.strip()
            if name not in ALLOWED_ENV:
                continue
            values[name] = _parse_value(value.strip(), line_number)
    if not values.get("AWS_ACCESS_KEY_ID") or not values.get("AWS_SECRET_ACCESS_KEY"):
        raise ValueError("credentials file lacks the required AWS access key fields")
    return values


def _sign(body, credentials):
    parsed = urllib.parse.urlparse(ENDPOINT)
    host = parsed.hostname or ""
    path = parsed.path or "/"
    now = datetime.datetime.now(datetime.timezone.utc)
    amz_date = now.strftime("%Y%m%dT%H%M%SZ")
    date = now.strftime("%Y%m%d")
    payload_hash = hashlib.sha256(body).hexdigest()
    headers = {
        "host": host,
        "x-amz-content-sha256": payload_hash,
        "x-amz-date": amz_date,
    }
    token = credentials.get("AWS_SESSION_TOKEN")
    if token:
        headers["x-amz-security-token"] = token
    signed_names = sorted(headers)
    canonical_headers = "".join(
        f"{name}:{headers[name].strip()}\n" for name in signed_names
    )
    signed_headers = ";".join(signed_names)
    canonical_request = (
        f"POST\n{path}\n\n{canonical_headers}\n{signed_headers}\n{payload_hash}"
    )
    scope = f"{date}/us-east-1/aws-mcp/aws4_request"
    string_to_sign = (
        "AWS4-HMAC-SHA256\n"
        f"{amz_date}\n{scope}\n"
        f"{hashlib.sha256(canonical_request.encode('utf-8')).hexdigest()}"
    )

    def digest(key, text):
        return hmac.new(key, text.encode("utf-8"), hashlib.sha256).digest()

    key_date = digest(
        ("AWS4" + credentials["AWS_SECRET_ACCESS_KEY"]).encode("utf-8"), date
    )
    key_region = digest(key_date, "us-east-1")
    key_service = digest(key_region, "aws-mcp")
    signing_key = digest(key_service, "aws4_request")
    signature = hmac.new(
        signing_key, string_to_sign.encode("utf-8"), hashlib.sha256
    ).hexdigest()
    headers["Authorization"] = (
        "AWS4-HMAC-SHA256 "
        f"Credential={credentials['AWS_ACCESS_KEY_ID']}/{scope}, "
        f"SignedHeaders={signed_headers}, Signature={signature}"
    )
    return headers


def _decode_response(raw, content_type):
    if not raw:
        return {}
    text = raw.decode("utf-8")
    if "text/event-stream" not in content_type:
        try:
            return json.loads(text)
        except json.JSONDecodeError as exc:
            raise ProtocolError("MCP response was not valid JSON") from exc
    events = []
    data_lines = []
    for line in text.splitlines() + [""]:
        if line == "":
            if data_lines:
                data = "\n".join(data_lines)
                data_lines = []
                if data != "[DONE]":
                    try:
                        events.append(json.loads(data))
                    except json.JSONDecodeError as exc:
                        raise ProtocolError("MCP event data was not valid JSON") from exc
            continue
        if line.startswith("data:"):
            data_lines.append(line[5:].lstrip())
    if not events:
        raise ProtocolError("MCP event stream contained no JSON response")
    return events[-1]


def post_mcp(payload, credentials, session_id=None, before_send=None):
    body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    request = urllib.request.Request(ENDPOINT, data=body, method="POST")
    request.add_header("Content-Type", "application/json")
    request.add_header("Accept", "application/json, text/event-stream")
    request.add_header("User-Agent", "BanhmiOperator/1.0")
    if session_id:
        request.add_header("Mcp-Session-Id", session_id)
    for name, value in _sign(body, credentials).items():
        request.add_header(name, value)
    try:
        if before_send:
            before_send()
        with urllib.request.urlopen(request, timeout=60) as response:
            raw = response.read()
            content_type = response.headers.get("Content-Type", "")
            next_session = response.headers.get("Mcp-Session-Id") or session_id
    except urllib.error.HTTPError as exc:
        raise TransportError(f"HTTP status {exc.code}") from None
    except urllib.error.URLError as exc:
        raise TransportError("network request failed") from exc
    except TimeoutError as exc:
        raise TransportError("network request timed out") from exc
    return _decode_response(raw, content_type), next_session


def _rpc_error(response):
    if isinstance(response, dict) and isinstance(response.get("error"), dict):
        return True
    return False


def initialize(credentials):
    response, session_id = post_mcp(
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2024-11-05",
                "capabilities": {},
                "clientInfo": {"name": "banhmi-operator", "version": "1.0"},
            },
        },
        credentials,
    )
    if _rpc_error(response):
        raise ProtocolError("MCP initialize was rejected")
    post_mcp(
        {"jsonrpc": "2.0", "method": "notifications/initialized"},
        credentials,
        session_id,
    )
    return session_id


def run_script(script, credentials, session_id, request_id=2, before_send=None):
    response, _ = post_mcp(
        {
            "jsonrpc": "2.0",
            "id": request_id,
            "method": "tools/call",
            "params": {
                "name": "aws___run_script",
                "arguments": {"code": script},
            },
        },
        credentials,
        session_id,
        before_send,
    )
    if not isinstance(response, dict):
        raise ProtocolError("MCP tool returned a non-object response")
    if _rpc_error(response):
        raise RemoteCallError("aws___run_script was rejected")
    if isinstance(response.get("result"), dict) and response["result"].get("isError"):
        raise RemoteCallError("aws___run_script reported an error")
    return response


def _envelopes(response):
    result = response.get("result") if isinstance(response, dict) else None
    if not isinstance(result, dict):
        raise ProtocolError("MCP tool response lacked a result")
    content = result.get("content")
    if not isinstance(content, list):
        raise ProtocolError("MCP tool response lacked a content list")
    envelopes = []
    for item in content:
        if not isinstance(item, dict) or item.get("type") != "text":
            raise ProtocolError("MCP tool returned a malformed content item")
        try:
            envelope = json.loads(item.get("text", ""))
        except (json.JSONDecodeError, TypeError) as exc:
            raise ProtocolError("MCP tool returned invalid JSON text") from exc
        if not isinstance(envelope, dict):
            raise ProtocolError("MCP tool returned a non-object envelope")
        if envelope.get("status") == "error":
            raise RemoteCallError("aws___run_script reported an AWS operation error")
        if envelope.get("status") != "success":
            raise ProtocolError("MCP tool returned a malformed envelope")
        envelopes.append(envelope)
    if not envelopes:
        raise ProtocolError("MCP tool returned no JSON envelope")
    return envelopes


def _redact(value, credentials):
    secret_values = {
        item
        for key, item in credentials.items()
        if key in {
            "AWS_ACCESS_KEY_ID",
            "AWS_SECRET_ACCESS_KEY",
            "AWS_SESSION_TOKEN",
        }
        and item
    }
    if isinstance(value, dict):
        return {
            key: "[REDACTED]" if SENSITIVE_KEYS.search(str(key)) else _redact(item, credentials)
            for key, item in value.items()
        }
    if isinstance(value, list):
        return [_redact(item, credentials) for item in value]
    if isinstance(value, str):
        redacted = value
        for secret in secret_values:
            redacted = redacted.replace(secret, "[REDACTED]")
        return redacted
    return value


def _find_access_key(value):
    if isinstance(value, dict):
        candidate = value.get("AccessKey")
        if isinstance(candidate, dict):
            key_id = candidate.get("AccessKeyId")
            secret = candidate.get("SecretAccessKey")
            token = candidate.get("SessionToken")
            if isinstance(key_id, str) and isinstance(secret, str):
                return key_id, secret, token if isinstance(token, str) else ""
        for item in value.values():
            found = _find_access_key(item)
            if found:
                return found
    elif isinstance(value, list):
        for item in value:
            found = _find_access_key(item)
            if found:
                return found
    return None


def _validate_output_parent(parent):
    current = os.path.sep
    for part in parent.split(os.path.sep)[1:]:
        current = os.path.join(current, part)
        try:
            info = os.lstat(current)
        except FileNotFoundError:
            if current != parent:
                raise ValueError("all ancestors of the destination parent must exist")
            os.mkdir(parent, 0o700)
            info = os.lstat(parent)
        if stat.S_ISLNK(info.st_mode):
            raise ValueError("destination path must not contain symlinks")
        if not stat.S_ISDIR(info.st_mode):
            raise ValueError("destination parent ancestry must contain only directories")
    info = os.lstat(parent)
    if info.st_uid != os.getuid():
        raise ValueError("destination parent must be owned by the current user")
    if stat.S_IMODE(info.st_mode) != 0o700:
        raise ValueError("destination parent mode must be 0700")


def _create_output(path):
    path = os.path.abspath(os.path.expanduser(path))
    parent = os.path.dirname(path)
    name = os.path.basename(path)
    if not name:
        raise ValueError("credential destination must name a file")
    _validate_output_parent(parent)
    parent_flags = os.O_RDONLY | os.O_DIRECTORY
    if hasattr(os, "O_NOFOLLOW"):
        parent_flags |= os.O_NOFOLLOW
    parent_fd = os.open(parent, parent_flags)
    parent_info = os.fstat(parent_fd)
    if parent_info.st_uid != os.getuid() or stat.S_IMODE(parent_info.st_mode) != 0o700:
        os.close(parent_fd)
        raise ValueError("destination parent changed during validation")
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        fd = os.open(name, flags, 0o600, dir_fd=parent_fd)
    except Exception:
        os.close(parent_fd)
        raise
    try:
        os.fchmod(fd, 0o600)
    except Exception:
        os.close(fd)
        try:
            os.unlink(name, dir_fd=parent_fd)
        finally:
            os.close(parent_fd)
        raise
    return path, name, parent_fd, fd


def _path_is_reserved(name, parent_fd, fd):
    try:
        path_info = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
    except FileNotFoundError:
        return False
    file_info = os.fstat(fd)
    return (
        stat.S_ISREG(path_info.st_mode)
        and path_info.st_dev == file_info.st_dev
        and path_info.st_ino == file_info.st_ino
    )


def _remove_reserved(name, parent_fd, fd):
    try:
        reserved = _path_is_reserved(name, parent_fd, fd)
    except OSError:
        return False
    if not reserved:
        return False
    try:
        os.unlink(name, dir_fd=parent_fd)
    except OSError:
        return False
    return True


def _empty_reserved(name, parent_fd, fd):
    os.ftruncate(fd, 0)
    os.fsync(fd)
    return _path_is_reserved(name, parent_fd, fd)


def _write_credentials(fd, credentials):
    key_id, secret, token = credentials
    lines = [
        f"AWS_ACCESS_KEY_ID={shlex.quote(key_id)}\n",
        f"AWS_SECRET_ACCESS_KEY={shlex.quote(secret)}\n",
    ]
    if token:
        lines.append(f"AWS_SESSION_TOKEN={shlex.quote(token)}\n")
    payload = "".join(lines).encode("utf-8")
    offset = 0
    while offset < len(payload):
        offset += os.write(fd, payload[offset:])
    os.fsync(fd)


def install_key(args, source_credentials):
    if not IAM_USER_RE.fullmatch(args.install_key_for):
        raise ValueError("IAM user name is invalid")
    session_id = initialize(source_credentials)
    destination, name, parent_fd, fd = _create_output(args.write_credentials)
    dispatched = False

    def mark_dispatched():
        nonlocal dispatched
        dispatched = True

    try:
        script = (
            "result = await call_boto3(service_name='iam', "
            "operation_name='CreateAccessKey', params={'UserName': "
            f"{args.install_key_for!r}}})\nresult"
        )
        response = run_script(
            script,
            source_credentials,
            session_id,
            before_send=mark_dispatched,
        )
        key = _find_access_key(_envelopes(response))
        if not key:
            raise ProtocolError("CreateAccessKey returned no usable access key")
        if not _path_is_reserved(name, parent_fd, fd):
            raise ProtocolError("reserved credential destination changed")
        _write_credentials(fd, key)
        if not _path_is_reserved(name, parent_fd, fd):
            raise ProtocolError("credential destination changed while writing")
    except Exception:
        if not dispatched:
            if not _remove_reserved(name, parent_fd, fd):
                raise RuntimeError(
                    "request was not sent; reserved destination could not be removed"
                ) from None
            raise
        try:
            retained = _empty_reserved(name, parent_fd, fd)
            state = (
                "remains as an empty sentinel"
                if retained
                else "no longer names the reserved file"
            )
        except OSError:
            state = "may contain partial data"
        raise RuntimeError(
            "key creation may have occurred; do not retry; "
            f"destination {state}"
        ) from None
    finally:
        os.close(fd)
        os.close(parent_fd)
    print(f"created credentials for {args.install_key_for} at {destination}")


def parse_args():
    parser = argparse.ArgumentParser(
        description=(
            "Run an AWS MCP script with runtime credentials. Caller scripts must "
            "not return secret values."
        )
    )
    parser.add_argument(
        "--credentials-file",
        default="~/.config/banhmi/aws-operator.env",
        help="owner-only env file used for SigV4 credentials",
    )
    parser.add_argument("--script", help="path to the Python script for aws___run_script")
    parser.add_argument("--install-key-for", metavar="IAM_USER")
    parser.add_argument("--write-credentials", metavar="NEW_ENV")
    args = parser.parse_args()
    installing = args.install_key_for is not None or args.write_credentials is not None
    if installing:
        if not args.install_key_for or not args.write_credentials or args.script:
            parser.error(
                "install mode requires --install-key-for and --write-credentials, "
                "and does not accept --script"
            )
    elif not args.script:
        parser.error("normal mode requires --script")
    return args


def main():
    args = parse_args()
    installing = args.install_key_for is not None
    try:
        credentials = load_credentials(args.credentials_file)
        if installing:
            install_key(args, credentials)
            return 0
        with open(args.script, "r", encoding="utf-8") as source:
            script = source.read()
        session_id = initialize(credentials)
        response = run_script(script, credentials, session_id)
        safe = _redact(_envelopes(response), credentials)
        print(json.dumps(safe, indent=2, sort_keys=True))
        return 0
    except urllib.error.HTTPError as error:
        error_type = type(error).__name__
        message = f"HTTP status {error.code}"
    except OSError as error:
        error_type = type(error).__name__
        message = f"local OS error {error.errno}"
    except (
        ValueError,
        TransportError,
        ProtocolError,
        RemoteCallError,
        RuntimeError,
    ) as error:
        error_type = type(error).__name__
        message = str(error) if str(error) else error_type
    if installing:
        print(f"aws-mcp-operator: {error_type}: {message}", file=sys.stderr)
    else:
        print(f"aws-mcp-operator: {message}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
