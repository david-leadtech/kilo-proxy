"""Private Omnigent 0.18.0 integration. Imported code stays in the vendor package.

This module is also used by sitecustomize in owned runner processes. Importing
it alone performs no package imports, network requests, or credential lookups.
"""
from __future__ import annotations

import argparse
import asyncio
import copy
import dataclasses
import datetime
import contextvars
import errno
import re
from urllib.parse import urlsplit
import hashlib
import hmac
import importlib
import json
import os
from pathlib import Path
import secrets
import signal
import stat
import socket
import subprocess
import sys
import threading
import tempfile
import shutil
import time
import urllib.error
import urllib.request
import webbrowser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

_ACTIVE = None
_NATIVE_ENV = {}
_KILO_TOKENS = {}
_CREDENTIAL_PID = None
_SESSION = contextvars.ContextVar("kilo_omnigent_session", default=None)
_SDK_TURN = contextvars.ContextVar("kilo_omnigent_sdk_turn", default=None)
_FAMILIES = {"codex": "codex", "codex-native": "codex", "claude": "claude", "claude-sdk": "claude", "claude-native": "claude"}
_EFFORTS = {"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}


def _read_regular(path, limit):
    flags = os.O_RDONLY | getattr(os, "O_NONBLOCK", 0) | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise ValueError("Unsafe private runtime file")
        with os.fdopen(fd, "rb", closefd=False) as stream:
            raw = stream.read(limit + 1)
        if len(raw) > limit:
            raise ValueError("Private runtime file exceeds its limit")
        return raw
    finally:
        os.close(fd)


def _json(path):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError("Duplicate private runtime JSON field")
            result[key] = value
        return result
    return json.loads(_read_regular(path, 2 << 20), object_pairs_hook=pairs)


def _digest(path):
    return hashlib.sha256(_read_regular(path, 16 << 20)).hexdigest()


def _inside(path, root):
    p, r = Path(path), Path(root)
    return p.is_absolute() and (p == r or r in p.parents)


def load_descriptor(path):
    d = _json(path)
    if d.get("version") != 1 or d.get("packageVersion") != "0.18.0":
        raise ValueError("Unsupported private Omnigent runtime")
    root = Path(d["root"])
    if not root.is_absolute() or root.is_symlink() or not root.is_dir():
        raise ValueError("Unsafe private runtime root")
    for key in ("configPath", "inferencePath"):
        if not _inside(d[key], root) or _digest(d[key]) != d[key.removesuffix("Path") + "Hash"]:
            raise ValueError("Private Omnigent settings changed; prepare again")
    prefs = d["preferences"]
    if prefs.get("version") != 1:
        raise ValueError("Unsupported private Omnigent preferences")
    for family in ("codex", "claude"):
        if type(prefs[family]) is not bool or prefs[family + "Connection"] not in ("native", "kilo"):
            raise ValueError("Invalid private Omnigent connection")
        if not prefs[family] or prefs[family + "Connection"] != "kilo":
            continue
        library = d["modelLibraries"][family]
        ids = [row["id"] for row in library["models"]]
        if not ids or len(set(ids)) != len(ids) or library["defaultModel"] not in ids:
            raise ValueError("Invalid private Omnigent model selection")
        for row in library["models"]:
            levels = row["reasoningLevels"]
            if not isinstance(levels, list) or len(set(levels)) != len(levels) or any(x not in _EFFORTS for x in levels):
                raise ValueError("Invalid private Omnigent reasoning levels")
            if row["defaultReasoning"] and row["defaultReasoning"] not in levels:
                raise ValueError("Invalid private Omnigent reasoning default")
    return d


def verify_sources(d):
    root = Path(d["sourceRoot"])
    if root.is_symlink() or not root.is_dir():
        raise ValueError("Omnigent source is unavailable")
    for relative, expected in d["sourceHashes"].items():
        source = root / relative
        if not _inside(source, root) or _digest(source) != expected:
            raise ValueError("The selected Omnigent installation changed")
    # PathFinder inspects package resolution without importing the application.
    from importlib.machinery import PathFinder
    spec = PathFinder.find_spec("omnigent")
    if spec is None or spec.origin is None or Path(spec.origin).parent.resolve() != root.resolve():
        raise ValueError("Omnigent interpreter and package do not match")
    metadata = root.parent / ("omnigent-" + d["packageVersion"] + ".dist-info") / "METADATA"
    text = _read_regular(metadata, 1 << 20).decode("utf-8")
    if "\nVersion: " + d["packageVersion"] + "\n" not in "\n" + text:
        raise ValueError("Omnigent interpreter and package do not match")


def _family(d, harness):
    family = _FAMILIES.get(harness)
    if family is None or not d["preferences"][family]:
        raise ValueError("This harness is disabled in the private Omnigent workspace")
    return family


def _kilo(d, harness):
    family = _family(d, harness)
    return d["preferences"][family + "Connection"] == "kilo"


def model_policy(d, harness, model=None):
    family = _family(d, harness)
    if not _kilo(d, harness):
        return None
    library = d["modelLibraries"][family]
    selected = library["defaultModel"] if model is None or model == "" else model
    for row in library["models"]:
        if row["id"] == selected:
            return row
    raise ValueError("This exact model is not selected for the Omnigent connection; prepare again")


def effort_policy(d, harness, model, effort):
    row = model_policy(d, harness, model)
    if row is None:
        return effort
    if effort in (None, "", "default", "reset", "off"):
        return row["defaultReasoning"] or None
    if effort not in row["reasoningLevels"]:
        raise ValueError("This reasoning level is not selected for the Omnigent model")
    return effort


def model_rows(d, harness):
    family = _family(d, harness)
    if not _kilo(d, harness):
        return None
    library = d["modelLibraries"][family]
    return [{"id": row["id"], "model": row["id"], "displayName": row["name"] or row["id"],
             "name": row["name"] or row["id"], "isDefault": row["id"] == library["defaultModel"],
             "supportedReasoningEfforts": [{"reasoningEffort": level} for level in row["reasoningLevels"]],
             "defaultReasoningEffort": row["defaultReasoning"], "kiloOmnigent": True}
            for row in library["models"]]


def validate_binding_config(d, config):
    bindings = config.get("inference", {}).get("harnesses", {})
    if not isinstance(bindings, dict):
        raise ValueError("Invalid Omnigent inference binding")
    for harness, binding in bindings.items():
        family = _family(d, harness)
        if not _kilo(d, harness):
            raise ValueError("A native Omnigent connection cannot use a Kilo binding")
        library = d["modelLibraries"][family]
        if binding.get("default_model") != library["defaultModel"] or binding.get("model_allowlist") != [row["id"] for row in library["models"]]:
            raise ValueError("The Omnigent binding differs from its prepared selection")
    for harness in ("codex", "codex-native", "claude-sdk", "claude-native"):
        family = _FAMILIES[harness]
        if d["preferences"][family] and d["preferences"][family + "Connection"] == "kilo" and harness not in bindings:
            raise ValueError("The Omnigent connection is missing its prepared binding")
    return config


def _uses_kilo(d):
    return any(d["preferences"][family] and d["preferences"][family + "Connection"] == "kilo" for family in ("codex", "claude"))


def _prepared_inference_config(d):
    raw = _read_regular(d["inferencePath"], 16 << 20)
    if hashlib.sha256(raw).hexdigest() != d["inferenceHash"]:
        raise ValueError("The private Omnigent inference configuration changed")
    config = json.loads(raw)
    if not isinstance(config, dict):
        raise ValueError("Invalid private Omnigent inference configuration")
    validate_binding_config(d, config)
    return {key: copy.deepcopy(config.get(key, {})) for key in ("providers", "inference")}


def _validate_session_inference(d, config):
    expected = _prepared_inference_config(d)
    if not isinstance(config, dict) or {key: config.get(key, {}) for key in ("providers", "inference")} != expected:
        raise ValueError("The Omnigent session snapshot differs from its prepared connection")
    validate_binding_config(d, config)
    return config


def _kilo_token(d, family):
    if not _kilo(d, family):
        raise ValueError("A native connection cannot use a Kilo credential")
    value = _KILO_TOKENS.get(family) or os.environ.get("KILO_OMNIGENT_" + family.upper() + "_TOKEN")
    if not isinstance(value, str) or not value or any(c in value for c in "\r\n\x00"):
        raise ValueError("The private Omnigent scoped credential is unavailable")
    return value


def _native_credentials(d, family):
    if _kilo(d, family) or family not in _NATIVE_ENV:
        raise ValueError("This native account was not authorized for the Omnigent child")
    return dict(_NATIVE_ENV[family])


def _consume_private_credentials(d):
    global _NATIVE_ENV, _KILO_TOKENS, _CREDENTIAL_PID
    # A fork inherits module memory but receives a replacement environment.
    # Never retain any credential from the zygote or another child payload.
    _NATIVE_ENV, _KILO_TOKENS, _CREDENTIAL_PID = {}, {}, None
    native, tokens = {}, {}
    dispatch = os.environ.pop("KILO_OMNIGENT_DISPATCH_TOKENS", "")
    values = json.loads(dispatch) if dispatch else {}
    if not isinstance(values, dict) or any(k not in ("codex", "claude") or not isinstance(v, str) or not v or any(c in v for c in "\r\n\x00") for k, v in values.items()):
        raise ValueError("Invalid private Omnigent dispatch credentials")
    for family in ("codex", "claude"):
        value = os.environ.pop("KILO_OMNIGENT_" + family.upper() + "_TOKEN", "") or values.get(family)
        if value and (not isinstance(value, str) or any(c in value for c in "\r\n\x00")):
            raise ValueError("Invalid private Omnigent dispatch credentials")
        if d["preferences"][family] and d["preferences"][family + "Connection"] == "kilo" and value:
            tokens[family] = value
    for family in ("codex", "claude"):
        raw = os.environ.pop("KILO_OMNIGENT_NATIVE_" + family.upper() + "_ENV", "")
        if raw:
            values = json.loads(raw)
            prefixes = ("OPENAI_", "CODEX_") if family == "codex" else ("ANTHROPIC_", "CLAUDE_CODE_")
            if not isinstance(values, dict) or any(not isinstance(k, str) or (not k.startswith(prefixes) and not (family == "claude" and k == "CLAUDE_SECURESTORAGE_CONFIG_DIR")) or not isinstance(v, str) or "\x00" in v for k, v in values.items()):
                raise ValueError("Invalid transient native Omnigent environment")
            if d["preferences"][family] and d["preferences"][family + "Connection"] == "native":
                native[family] = values
    _NATIVE_ENV, _KILO_TOKENS = native, tokens
    _CREDENTIAL_PID = os.getpid()


def _refresh_fork_credentials(d):
    global _NATIVE_ENV, _KILO_TOKENS, _CREDENTIAL_PID
    if _CREDENTIAL_PID == os.getpid():
        return
    _NATIVE_ENV, _KILO_TOKENS, _CREDENTIAL_PID = {}, {}, None
    expected = str(Path(d["root"]) / "runtime" / "descriptor.json")
    if os.environ.get("KILO_OMNIGENT_DESCRIPTOR") != expected or load_descriptor(expected) != d:
        raise ValueError("Conflicting private Omnigent fork descriptor")
    _configure_owned_socket_root(d, required=True)
    _consume_private_credentials(d)


def _patch_fork_entries(d):
    # The zygote is executed as __main__ with -m, so patching its named module
    # would miss that namespace. Both pinned fork bodies call these named main
    # functions after _apply_child_env has replaced the child's environment.
    for name in ("omnigent.runner._entry", "omnigent.runtime.harnesses._runner"):
        module = importlib.import_module(name)
        original = module.main
        def entry(*args, _original=original, **kwargs):
            _refresh_fork_credentials(d)
            return _original(*args, **kwargs)
        module.main = entry


def _open_owned_socket_root(d, path):
    raw = str(path)
    if not re.fullmatch(r"/tmp/ko-[a-z0-9_]{8}", raw):
        raise ValueError("The owned Omnigent temporary root is invalid")
    fd = os.open(raw, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        directory = os.fstat(fd)
        if not stat.S_ISDIR(directory.st_mode) or directory.st_uid != os.getuid() or stat.S_IMODE(directory.st_mode) != 0o700:
            raise ValueError("The owned Omnigent temporary root is invalid")
        marker_fd = os.open(".kilo-omnigent-owner.json", os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW, dir_fd=fd)
        try:
            marker = os.fstat(marker_fd)
            if not stat.S_ISREG(marker.st_mode) or marker.st_uid != os.getuid() or stat.S_IMODE(marker.st_mode) != 0o600 or marker.st_size > 4096:
                raise ValueError("The owned Omnigent temporary root is invalid")
            raw_marker = os.read(marker_fd, 4097)
        finally:
            os.close(marker_fd)
        def pairs(items):
            result = {}
            for key, value in items:
                if key in result:
                    raise ValueError("Invalid owned Omnigent temporary marker")
                result[key] = value
            return result
        owner = json.loads(raw_marker, object_pairs_hook=pairs)
        if not isinstance(owner, dict) or type(owner.get("creatorPID")) is not int or owner["creatorPID"] < 1 or owner != {"version": 1, "root": d["root"], "fingerprint": d["fingerprint"], "creatorPID": owner["creatorPID"]}:
            raise ValueError("The owned Omnigent temporary root does not match this workspace")
        identity = {"path": raw, "device": directory.st_dev, "inode": directory.st_ino,
                    "markerDevice": marker.st_dev, "markerInode": marker.st_ino,
                    "markerHash": hashlib.sha256(raw_marker).hexdigest(), "creatorPID": owner["creatorPID"]}
        return fd, identity
    except BaseException:
        os.close(fd)
        raise


def _create_owned_socket_root(d):
    # This fixed short POSIX parent avoids macOS's long per-user TMPDIR.
    # The official UUID socket suffix is 79 bytes; /tmp/ko-XXXXXXXX totals 95.
    path = Path(tempfile.mkdtemp(prefix="ko-", dir="/tmp"))
    path.chmod(0o700)
    _write_json(path / ".kilo-omnigent-owner.json", {"version": 1, "root": d["root"], "fingerprint": d["fingerprint"], "creatorPID": os.getpid()})
    fd, identity = _open_owned_socket_root(d, path)
    os.close(fd)
    return path, identity


def _configure_owned_socket_root(d, *, required=False):
    owned = os.environ.get("KILO_OMNIGENT_OWN_TMP")
    if owned:
        fd, _ = _open_owned_socket_root(d, owned)
        os.close(fd)
        os.environ["OMNIGENT_HARNESS_TMP_PARENT"] = owned
        os.environ["TMPDIR"] = owned
        tempfile.tempdir = owned
    else:
        if required:
            raise ValueError("The owned Omnigent temporary root is required")
        os.environ.pop("OMNIGENT_HARNESS_TMP_PARENT", None)


def _owned_sockets_stopped(directory, root):
    # A detached tmux server can outlive every supervised process group. Probe
    # only sockets in this already validated private tree before removing any
    # path. AF_UNIX connect has no O_NOFOLLOW; checks on both sides are
    # conservative, while traversal and deletion remain directory-FD anchored.
    deadline = time.monotonic() + 0.5
    count = sockets = 0
    def unchanged(before, after):
        return (before.st_dev, before.st_ino, before.st_uid, before.st_mode) == (after.st_dev, after.st_ino, after.st_uid, after.st_mode)
    def visit(fd, path, depth):
        nonlocal count, sockets
        if depth > 32 or time.monotonic() >= deadline:
            return False
        initial = os.fstat(fd)
        if initial.st_uid != os.getuid() or stat.S_IMODE(initial.st_mode) & 0o022:
            return False
        with os.scandir(fd) as entries:
            for entry in entries:
                count += 1
                if count > 65536 or time.monotonic() >= deadline:
                    return False
                before = os.stat(entry.name, dir_fd=fd, follow_symlinks=False)
                if stat.S_ISLNK(before.st_mode):
                    continue  # The existing cleanup unlinks this leaf only.
                if stat.S_ISDIR(before.st_mode):
                    child = os.open(entry.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                    try:
                        if not unchanged(before, os.fstat(child)) or not visit(child, path / entry.name, depth + 1):
                            return False
                        if not unchanged(before, os.stat(entry.name, dir_fd=fd, follow_symlinks=False)):
                            return False
                    finally:
                        os.close(child)
                elif stat.S_ISSOCK(before.st_mode):
                    sockets += 1
                    if sockets > 128 or before.st_uid != os.getuid() or stat.S_IMODE(before.st_mode) & 0o022:
                        return False
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        return False
                    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as probe:
                        probe.settimeout(min(0.05, remaining))
                        try:
                            probe.connect(str(path / entry.name))
                            return False  # Live; no bytes are sent.
                        except OSError as exc:
                            if exc.errno not in (errno.ENOENT, errno.ECONNREFUSED):
                                return False
                    try:
                        after = os.stat(entry.name, dir_fd=fd, follow_symlinks=False)
                    except FileNotFoundError:
                        continue
                    if not unchanged(before, after):
                        return False
        final = os.fstat(fd)
        return unchanged(initial, final) and initial.st_mtime_ns == final.st_mtime_ns and time.monotonic() < deadline
    return visit(directory, Path(root), 0)


def _cleanup_owned_socket_root(d, identity):
    fd = parent = None
    try:
        fd, current = _open_owned_socket_root(d, identity["path"])
        if current != identity or current["creatorPID"] != os.getpid():
            return False
        if not _owned_sockets_stopped(fd, identity["path"]):
            return False
        verified, after_probe = _open_owned_socket_root(d, identity["path"])
        os.close(verified)
        if after_probe != identity:
            return False
        def clear(directory):
            for name in os.listdir(directory):
                info = os.stat(name, dir_fd=directory, follow_symlinks=False)
                if stat.S_ISDIR(info.st_mode):
                    child = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=directory)
                    try:
                        opened = os.fstat(child)
                        if (opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino):
                            raise ValueError("Owned temporary directory changed during cleanup")
                        clear(child)
                        now = os.stat(name, dir_fd=directory, follow_symlinks=False)
                        if (now.st_dev, now.st_ino) != (opened.st_dev, opened.st_ino):
                            raise ValueError("Owned temporary directory changed during cleanup")
                        os.rmdir(name, dir_fd=directory)
                    finally:
                        os.close(child)
                else:
                    # Symlinks are unlinked without following their targets.
                    os.unlink(name, dir_fd=directory)
        clear(fd)
        parent = os.open("/tmp", os.O_RDONLY | os.O_DIRECTORY)
        name = Path(identity["path"]).name
        now = os.stat(name, dir_fd=parent, follow_symlinks=False)
        if (now.st_dev, now.st_ino) != (identity["device"], identity["inode"]):
            return False
        os.rmdir(name, dir_fd=parent)
        return True
    except (OSError, ValueError):
        # Changed ownership/identity is retained, never recursively adopted.
        return False
    finally:
        if fd is not None:
            os.close(fd)
        if parent is not None:
            os.close(parent)


def _private_environment(d):
    _consume_private_credentials(d)

    root = Path(d["root"])
    family_prefixes = {"codex": ("OPENAI_", "CODEX_"), "claude": ("ANTHROPIC_", "CLAUDE_")}
    for family, prefixes in family_prefixes.items():
        for key in list(os.environ):
            if key.startswith(prefixes):
                os.environ.pop(key, None)
    # Do not give unrelated providers or external Omnigent services a foothold.
    for key in list(os.environ):
        if key.startswith(("DATABRICKS_", "AWS_", "AZURE_", "GOOGLE_", "GEMINI_", "MUSE_", "OPENCODE_")) or (key.startswith("HARNESS_") and not key.startswith({"codex": "HARNESS_CODEX_", "claude": "HARNESS_CLAUDE_SDK_"}.get(os.environ.get("KILO_OMNIGENT_CHILD_FAMILY"), "!"))):
            os.environ.pop(key, None)
    os.environ.pop("KILO_LOCAL_API_KEY", None)
    for key in ("HTTP_PROXY","HTTPS_PROXY","ALL_PROXY","http_proxy","https_proxy","all_proxy"):
        os.environ.pop(key,None)
    os.environ["NO_PROXY"]="127.0.0.1,localhost,::1"
    home = root / "home"
    home.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.environ.update({"HOME": str(home), "USERPROFILE": str(home), "XDG_CONFIG_HOME": str(root / "xdg" / "config"),
                       "XDG_DATA_HOME": str(root / "xdg" / "data"), "XDG_STATE_HOME": str(root / "xdg" / "state"), "XDG_CACHE_HOME": str(root / "xdg" / "cache"),
                       "APPDATA": str(root / "xdg" / "config"), "LOCALAPPDATA": str(root / "xdg" / "data"), "CLAUDE_SECURESTORAGE_CONFIG_DIR": str(root / "profiles" / "claude-kilo"),
                       "OMNIGENT_CONFIG_HOME": str(Path(d["configPath"]).parent),
                       "OMNIGENT_DATA_DIR": str(root / "data"), "OMNIGENT_INFERENCE_CONFIG": d["inferencePath"],
                       "OMNIGENT_LOCAL_SINGLE_USER": "1", "OMNIGENT_AUTH_PROVIDER": "header", "OMNIGENT_AUTH_ENABLED": "0",
                       "OMNIGENT_HOST_NO_OPEN": "1", "OMNIGENT_ACCOUNTS_AUTO_OPEN": "0", "OMNIGENT_WEB_UI_DIST": str(root / "runtime" / "web-ui"),
                       "PYTHON_KEYRING_BACKEND": "keyring.backends.null.Keyring", "DISABLE_TELEMETRY": "1", "DO_NOT_TRACK": "1",
                       "PYTHONPATH": str(root / "runtime"), "KILO_OMNIGENT_DESCRIPTOR": str(root / "runtime" / "descriptor.json")})
    _configure_owned_socket_root(d)
    for family, key, normal_key, path_key in (("codex", "CODEX_HOME", "normalCodexHome", "codexPath"), ("claude", "CLAUDE_CONFIG_DIR", "normalClaudeHome", "claudePath")):
        native = d["preferences"][family] and d["preferences"][family + "Connection"] == "native"
        selected_home = Path(d[normal_key]) if native else root / "profiles" / (family + "-kilo")
        if not native:
            selected_home.mkdir(mode=0o700, parents=True, exist_ok=True)
        os.environ[key] = str(selected_home)
        if d["preferences"][family]:
            os.environ["OMNIGENT_" + family.upper() + "_PATH"] = d[path_key]
    # The selected binary never falls back to an ambient HARNESS_* override.


def _patch_selected_cli(d):
    platform = importlib.import_module("omnigent._platform")
    original_resolve = platform.resolve_cli_binary
    paths = {family: d[family + "Path"] for family in ("codex", "claude")}
    def selected(family):
        if not d["preferences"][family]:
            return None
        path = Path(paths[family])
        if not path.is_absolute():
            raise ValueError("The selected Omnigent CLI is unavailable; refresh its executable settings")
        fd = os.open(path, os.O_RDONLY | getattr(os, "O_NONBLOCK", 0))
        try:
            if not stat.S_ISREG(os.fstat(fd).st_mode) or not os.access(path, os.X_OK):
                raise ValueError("The selected Omnigent CLI is unavailable; refresh its executable settings")
        finally:
            os.close(fd)
        return str(path)
    def resolve(name, *, env_var=None, which=None):
        family = name if name in paths else next((kind for kind,path in paths.items() if name == path), None)
        if family is not None:
            return selected(family)
        return original_resolve(name, env_var=env_var, which=which)
    platform.resolve_cli_binary = resolve
    # These stable modules cache direct imports. Rebind those precise aliases,
    # leaving unrelated command resolution and all probe verdicts unchanged.
    modules = {}
    for name in ("omnigent.onboarding.harness_install", "omnigent.onboarding.harness_readiness",
                 "omnigent.inner.codex_executor", "omnigent.inner.claude_sdk_executor",
                 "omnigent.runner.native.orchestration"):
        module = importlib.import_module(name)
        if hasattr(module, "resolve_cli_binary"):
            module.resolve_cli_binary = resolve
        modules[name] = module
    install = modules["omnigent.onboarding.harness_install"]
    original_spec = install.harness_install_spec
    def spec(key):
        family = {"openai": "codex", "anthropic": "claude"}.get(key)
        value = original_spec(key)
        if family is None or value is None:
            return value
        if not d["preferences"][family]:
            return None
        return dataclasses.replace(value, binary=selected(family))
    install.harness_install_spec = spec
    readiness = modules["omnigent.onboarding.harness_readiness"]
    readiness.harness_install_spec = spec
    for name in ("_harness_availability", "_harness_availability_core"):
        original = getattr(readiness, name)
        def availability(harness, _original=original):
            canonical = readiness._canonical_harness(harness)
            canonical = {"native-codex": "codex-native", "native-claude": "claude-native"}.get(canonical, canonical)
            family = _FAMILIES.get(canonical)
            if family is None or not d["preferences"][family]:
                return False
            return _original(canonical)
        setattr(readiness, name, availability)
    original_subprocess = install.subprocess
    class ProbeSubprocess:
        def __getattr__(self, name):
            return getattr(original_subprocess, name)
        def run(self, argv, *args, **kwargs):
            family = next((kind for kind,path in paths.items() if isinstance(argv, (list,tuple)) and argv and argv[0] == path), None)
            if family is None:
                return original_subprocess.run(argv, *args, **kwargs)
            selected(family)
            env = dict(kwargs.get("env") or os.environ)
            for key in list(env):
                if key.startswith(("OPENAI_", "CODEX_", "ANTHROPIC_", "CLAUDE_", "KILO_OMNIGENT_NATIVE_")) or key in ("KILO_LOCAL_API_KEY", "KILO_OMNIGENT_DISPATCH_TOKENS", "KILO_OMNIGENT_CODEX_TOKEN", "KILO_OMNIGENT_CLAUDE_TOKEN"):
                    env.pop(key, None)
            if _kilo(d, family):
                env["HOME"] = str(Path(d["root"]) / "home")
                env["CODEX_HOME" if family == "codex" else "CLAUDE_CONFIG_DIR"] = str(Path(d["root"]) / "profiles" / (family + "-kilo"))
                token = _kilo_token(d, family)
                env["KILO_OMNIGENT_" + family.upper() + "_TOKEN"] = token
                config = _json(d["inferencePath"])
                binding = config["inference"]["harnesses"]["codex" if family == "codex" else "claude-native"]
                endpoint = config["providers"][binding["provider"]]["openai" if family == "codex" else "anthropic"]["base_url"]
                if family == "claude":
                    env.update({"ANTHROPIC_BASE_URL":endpoint, "ANTHROPIC_AUTH_TOKEN":token, "CLAUDE_SECURESTORAGE_CONFIG_DIR":str(Path(d["root"])/"profiles"/"claude-kilo"), "CLAUDE_CODE_USE_BEDROCK":"0", "CLAUDE_CODE_USE_VERTEX":"0", "CLAUDE_CODE_USE_FOUNDRY":"0"})
                else:
                    env.update({"OPENAI_BASE_URL":endpoint, "OPENAI_API_KEY":token})
            else:
                native = _native_credentials(d, family)
                env.update(native)
                env["HOME"] = d["normalHome"]
                env["CODEX_HOME" if family == "codex" else "CLAUDE_CONFIG_DIR"] = d["normalCodexHome" if family == "codex" else "normalClaudeHome"]
                if family == "claude":
                    env["CLAUDE_SECURESTORAGE_CONFIG_DIR"] = native.get("CLAUDE_SECURESTORAGE_CONFIG_DIR", d["normalClaudeHome"])
            kwargs["env"] = env
            return original_subprocess.run(argv, *args, **kwargs)
    install.subprocess = ProbeSubprocess()


def _patch_bindings(d):
    module = importlib.import_module("omnigent.inference_config")
    original_load = module.load_runtime_inference_config
    original_model = module.resolve_bound_model
    original_provider = module.resolve_bound_provider
    original_snapshot = module.snapshot_runtime_config
    def load(base_config=None):
        return validate_binding_config(d, original_load(base_config))
    def model(config, harness, selected_model=None):
        policy = model_policy(d, harness, selected_model)
        if policy is None:
            return original_model(config, harness, selected_model)
        validate_binding_config(d, config)
        result = original_model(config, harness, selected_model)
        if result != policy["id"]:
            raise ValueError("Omnigent resolved an unexpected model")
        return result
    def provider(config, harness, auth=None, **kwargs):
        _family(d, harness)
        validate_binding_config(d, config)
        return original_provider(config, harness, auth, **kwargs)
    def snapshot(saved):
        result = original_snapshot(saved)
        if not _uses_kilo(d):
            return result
        if saved is None:
            return _prepared_inference_config(d)
        _validate_session_inference(d, result)
        return result
    providers = importlib.import_module("omnigent.onboarding.provider_config")
    original_secret = providers.resolve_secret
    def secret(reference):
        for family in ("codex", "claude"):
            if reference == "env:KILO_OMNIGENT_" + family.upper() + "_TOKEN":
                return _kilo_token(d, family)
        return original_secret(reference)
    providers.resolve_secret = secret
    module.load_runtime_inference_config, module.resolve_bound_model, module.resolve_bound_provider = load, model, provider
    module.snapshot_runtime_config = snapshot


def _patch_registry(d):
    registry = importlib.import_module("omnigent.harness_plugins")
    enabled = {h for h, family in _FAMILIES.items() if h != "claude" and d["preferences"][family]}
    contribution = registry._BUILTIN_CONTRIBUTION
    fields = {}
    for field in dataclasses.fields(contribution):
        value = getattr(contribution, field.name)
        if field.name in ("valid_harnesses", "native_harnesses"):
            fields[field.name] = frozenset(h for h in value if h in enabled)
        elif field.name == "aliases":
            fields[field.name] = {alias: h for alias, h in value.items() if h in enabled}
        elif field.name in ("native_agents", "native_providers"):
            fields[field.name] = tuple(row for row in value if row.key in (family for family in ("codex", "claude") if d["preferences"][family]))
        elif isinstance(value, dict):
            fields[field.name] = {h: entry for h, entry in value.items() if h in enabled or h in (family for family in ("codex", "claude") if d["preferences"][family])}
    registry._BUILTIN_CONTRIBUTION = dataclasses.replace(contribution, **fields)
    registry._entry_points = lambda: ()
    registry._state = None


def _patch_catalogs(d):
    claude = importlib.import_module("omnigent.harnesses.claude_native.main")
    codex = importlib.import_module("omnigent.harnesses.codex_native.app_server")
    original_claude = claude.claude_launch_catalog
    async def claude_catalog(config):
        rows = model_rows(d, "claude-native")
        return rows if rows is not None else await original_claude(config)
    original_codex = codex.list_codex_model_options
    async def codex_catalog(client, **kwargs):
        rows = model_rows(d, "codex-native")
        return rows if rows is not None else await original_codex(client, **kwargs)
    claude.claude_launch_catalog, codex.list_codex_model_options = claude_catalog, codex_catalog


def _patch_native_controls(d):
    controls = importlib.import_module("omnigent.runner.native_controls")
    build = controls.build_native_controls
    def wrapped_build(**kwargs):
        original = build(**kwargs)
        claude_selections = {}
        async def selected(conv, harness):
            value = await kwargs["_fetch_session_model_override"](conv)
            row = model_policy(d, harness, value)
            return row["id"] if row is not None else value
        async def codex_models(conv):
            rows = model_rows(d, "codex-native")
            return rows if rows is not None else await original.codex_native_model_options(conv)
        async def codex_settings(conv, settings, **options):
            if not _kilo(d, "codex-native"):
                return await original.handle_codex_native_settings_update(conv, settings, **options)
            settings = dict(settings)
            model = settings["model"] if "model" in settings else await selected(conv, "codex-native")
            row = model_policy(d, "codex-native", model)
            if "model" in settings:
                # Native RPC null retains its live model; reset must name the
                # prepared default explicitly, unlike an effort-only update.
                settings["model"] = row["id"]
            if "model" in settings or "effort" in settings:
                resolved = effort_policy(d, "codex-native", model, settings.get("effort"))
                # Native RPC null retains a previous effort; "none" clears it.
                # This transport value never adds a level to the prepared catalog.
                settings["effort"] = resolved if row["reasoningLevels"] else "none"
            return await original.handle_codex_native_settings_update(conv, settings, **options)
        async def claude_model(conv, model):
            if not _kilo(d, "claude-native"):
                return await original.handle_claude_native_model_change(conv, model)
            row = model_policy(d, "claude-native", model)
            current = claude_selections.get(conv)
            requested = current["effort"] if current is not None and current["model"] == row["id"] else None
            resolved = effort_policy(d, "claude-native", row["id"], requested) if row["reasoningLevels"] else None
            def refused():
                # A combined PATCH may have confirmed B's effort before B's
                # model switch fails. Restore only that still-current candidate.
                if claude_selections.get(conv) is current and current is not None and current["model"] == row["id"] and "previous" in current:
                    previous = current["previous"]
                    if previous is None:
                        claude_selections.pop(conv, None)
                    else:
                        claude_selections[conv] = previous
            try:
                await asyncio.to_thread(register_claude_effort, d, conv, row["id"], resolved)
                response = await original.handle_claude_native_model_change(conv, row["id"])
            except BaseException:
                refused()
                raise
            if 200 <= response.status_code < 300:
                if claude_selections.get(conv) is current:
                    claude_selections[conv] = {"model": row["id"], "effort": resolved}
                if resolved in {"low", "medium", "high", "xhigh", "max"}:
                    await original.handle_claude_native_effort_change(conv, resolved)
            else:
                refused()
            return response
        async def claude_effort(conv, effort):
            if not _kilo(d, "claude-native"):
                return await original.handle_claude_native_effort_change(conv, effort)
            model = await selected(conv, "claude-native")
            resolved = effort_policy(d, "claude-native", model, effort)
            current = claude_selections.get(conv)
            await asyncio.to_thread(register_claude_effort, d, conv, model, resolved)
            if resolved not in {"low", "medium", "high", "xhigh", "max"}:
                from starlette.responses import Response
                response = Response(status_code=204)
            else:
                response = await original.handle_claude_native_effort_change(conv, resolved)
            if 200 <= response.status_code < 300 and claude_selections.get(conv) is current:
                candidate = {"model": model, "effort": resolved}
                if current is None or current["model"] != model:
                    candidate["previous"] = current
                elif "previous" in current:
                    candidate["previous"] = current["previous"]
                claude_selections[conv] = candidate
            return response
        return dataclasses.replace(original, codex_native_model_options=codex_models,
                                   handle_codex_native_settings_update=codex_settings,
                                   handle_claude_native_model_change=claude_model,
                                   handle_claude_native_effort_change=claude_effort)
    controls.build_native_controls = wrapped_build


def _patch_host(d):
    module = importlib.import_module("omnigent.host.connect")
    original = module._build_runner_env
    def environment(*args, **kwargs):
        harness = kwargs.get("harness")
        if harness is not None:
            _family(d, harness)
        if _uses_kilo(d) and kwargs.get("inference_config") is not None:
            _validate_session_inference(d, kwargs["inference_config"])
        env = original(*args, **kwargs)
        for key in list(env):
            if key.startswith(("OPENAI_", "CODEX_", "ANTHROPIC_", "CLAUDE_", "KILO_OMNIGENT_NATIVE_")):
                env.pop(key, None)
        if harness in _FAMILIES:
            family = _FAMILIES[harness]
            if not _kilo(d, harness) and family in _NATIVE_ENV:
                env["KILO_OMNIGENT_NATIVE_" + family.upper() + "_ENV"] = json.dumps(_NATIVE_ENV[family])
        for key in ("KILO_OMNIGENT_DESCRIPTOR", "KILO_OMNIGENT_OWN_TMP", "OMNIGENT_HARNESS_TMP_PARENT", "TMPDIR", "PYTHONPATH", "OMNIGENT_CONFIG_HOME", "OMNIGENT_DATA_DIR", "OMNIGENT_INFERENCE_CONFIG", "PYTHON_KEYRING_BACKEND", "OMNIGENT_CODEX_PATH", "OMNIGENT_CLAUDE_PATH", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"):
            if key in os.environ:
                env[key] = os.environ[key]
        for name in ("KILO_OMNIGENT_CODEX_TOKEN", "KILO_OMNIGENT_CLAUDE_TOKEN", "KILO_OMNIGENT_DISPATCH_TOKENS"):
            env.pop(name, None)
        env["KILO_OMNIGENT_DISPATCH_TOKENS"] = json.dumps(_KILO_TOKENS)
        if harness in _FAMILIES:
            env["KILO_OMNIGENT_CHILD_FAMILY"] = _FAMILIES[harness]
        if "KILO_LOCAL_API_KEY" in env:
            raise ValueError("A root Kilo key must not enter an Omnigent runner")
        return env
    module._build_runner_env = environment
    original_models = module.HostProcess._handle_model_options
    async def model_options(self, frame):
        harness = module.canonicalize_harness(frame.harness) or frame.harness
        family = _FAMILIES.get(harness)
        if family is None or not d["preferences"][family]:
            return module.HostModelOptionsResultFrame(request_id=frame.request_id, status="failed",
                error="This harness is disabled in the private Omnigent workspace")
        if not _kilo(d, harness):
            return await original_models(self, frame)
        rows = model_rows(d, harness)
        return module.HostModelOptionsResultFrame(request_id=frame.request_id, status="ok",
            models=module._with_model_configuration_source(rows, harness=harness),
            routable_models=[row["id"] for row in rows])
    module.HostProcess._handle_model_options = model_options


def register_claude_effort(d, session_id, model, effort):
    if not isinstance(session_id, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}", session_id):
        raise ValueError("The private Claude session identity is unavailable")
    selected = model_policy(d, "claude-native", model)
    resolved = effort_policy(d, "claude-native", selected["id"], effort)
    chosen = resolved or "none"
    config = _json(d["inferencePath"])
    binding = config["inference"]["harnesses"]["claude-native"]
    base = config["providers"][binding["provider"]]["anthropic"]["base_url"]
    parsed = urlsplit(base)
    if parsed.scheme != "http" or parsed.hostname != "127.0.0.1" or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path != "/omnigent/claude" or not parsed.port or parsed.port < 1024:
        raise ValueError("The private Claude endpoint is invalid")
    token = _kilo_token(d, "claude")
    if not token:
        raise ValueError("The private Claude credential is unavailable")
    body = {"sessionId": session_id, "model": selected["id"], "effort": chosen}
    request = urllib.request.Request(base + "/v1/session-effort", data=json.dumps(body).encode(), method="POST",
                                     headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
    try:
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=5) as response:
            result = json.loads(response.read(16384))
            if response.status != 200:
                status = response.status
                detail = "HTTP " + str(status) if type(status) is int and 100 <= status <= 599 else "unexpected confirmation"
                raise ValueError("Claude reasoning was not confirmed (" + detail + "); retry its selection")
            if result != {"ok": True, **body}:
                raise ValueError("Claude reasoning was not confirmed (unexpected confirmation); retry its selection")
    except urllib.error.HTTPError as error:
        status = error.code
        detail = "HTTP " + str(status) if type(status) is int and 100 <= status <= 599 else "HTTPError"
        raise ValueError("Claude reasoning was not confirmed (" + detail + "); retry its selection") from error
    except (OSError, urllib.error.URLError, json.JSONDecodeError) as error:
        cause = error.reason if isinstance(error, urllib.error.URLError) else error
        detail = "TimeoutError" if isinstance(cause, TimeoutError) else "JSONDecodeError" if isinstance(error, json.JSONDecodeError) else "URLError" if isinstance(error, urllib.error.URLError) else "OSError"
        raise ValueError("Claude reasoning was not confirmed (" + detail + "); retry its selection") from error
    return resolved


def _credential_command(d, family="claude"):
    import shlex
    return " ".join(shlex.quote(x) for x in (d["pythonPath"], "-I", str(Path(d["root"]) / "runtime" / "omnigent_runtime.py"), "--credential", family))


def _claude_gateway_base(d):
    config = _json(d["inferencePath"])
    binding = config["inference"]["harnesses"]["claude-native"]
    return config["providers"][binding["provider"]]["anthropic"]["base_url"]


def _claude_owned_env(d, session_id, base=None):
    env = {"ANTHROPIC_BASE_URL": base or _claude_gateway_base(d),
           "ANTHROPIC_CUSTOM_HEADERS": "X-Kilo-Omnigent-Session: " + session_id,
           "CLAUDE_CONFIG_DIR": str(Path(d["root"]) / "profiles" / "claude-kilo"),
           "CLAUDE_SECURESTORAGE_CONFIG_DIR": str(Path(d["root"]) / "profiles" / "claude-kilo"),
           "CLAUDE_CODE_USE_BEDROCK": "0", "CLAUDE_CODE_USE_VERTEX": "0", "CLAUDE_CODE_USE_FOUNDRY": "0"}
    return env


def _patch_claude_launch(d):
    native = importlib.import_module("omnigent.harnesses.claude_native.main")
    orchestration = importlib.import_module("omnigent.runner.native.orchestration")
    original_auto = orchestration._auto_create_claude_terminal
    original_args = orchestration._build_claude_native_base_args
    original_env = native.build_native_claude_terminal_env
    original_resolve = native.resolve_native_claude_config
    async def auto(session_id, *args, **kwargs):
        resolver=kwargs.get("resolve_launch_config")
        if resolver is not None and _kilo(d,"claude-native"):
            async def scoped_resolver():
                result=await resolver()
                if result is None:
                    raise ValueError("The private Claude provider is unavailable")
                return dataclasses.replace(result,api_key_helper=_credential_command(d))
            kwargs["resolve_launch_config"]=scoped_resolver
        token = _SESSION.set({"session": session_id})
        try:
            return await original_auto(session_id, *args, **kwargs)
        finally:
            _SESSION.reset(token)
    def resolve(*args, **kwargs):
        result = original_resolve(*args, **kwargs)
        if result is not None and _kilo(d, "claude-native"):
            return dataclasses.replace(result, api_key_helper=_credential_command(d))
        return result
    def arguments(**kwargs):
        if not _kilo(d, "claude-native"):
            return original_args(**kwargs)
        context = _SESSION.get()
        if context is None:
            raise ValueError("A Claude launch must have its own private session")
        passthrough = list(kwargs.get("terminal_launch_args") or [])
        model = kwargs.get("model_override")
        effort = kwargs.get("reasoning_effort")
        filtered = []
        index = 0
        while index < len(passthrough):
            arg = passthrough[index]
            if arg in ("--model", "--effort"):
                if index + 1 >= len(passthrough):
                    raise ValueError("Missing private Claude launch argument")
                if arg == "--model":
                    model = passthrough[index + 1]
                else:
                    effort = passthrough[index + 1]
                index += 2
                continue
            if arg.startswith("--model="):
                model = arg.split("=", 1)[1]
            elif arg.startswith("--effort="):
                effort = arg.split("=", 1)[1]
            else:
                filtered.append(arg)
            index += 1
        policy = model_policy(d, "claude-native", model)
        resolved = register_claude_effort(d, context["session"], policy["id"], effort)
        context.update({"model": policy["id"], "effort": resolved})
        kwargs.update({"model_override": policy["id"], "reasoning_effort": resolved if resolved in {"low", "medium", "high", "xhigh", "max"} else None,
                       "terminal_launch_args": filtered})
        settings = {"env": _claude_owned_env(d, context["session"]), "apiKeyHelper": _credential_command(d)}
        return (*original_args(**kwargs), "--settings", json.dumps(settings, separators=(",", ":")))
    def environment(config):
        result = original_env(config)
        if _kilo(d, "claude-native"):
            context = _SESSION.get()
            if context is None or "model" not in context:
                raise ValueError("Claude launch reasoning was not confirmed")
            result.update(_claude_owned_env(d, context["session"]))
            result["KILO_OMNIGENT_CLAUDE_TOKEN"] = _kilo_token(d, "claude")
        else:
            result.update(_native_credentials(d, "claude"))
            result["HOME"] = d["normalHome"]
            result["CLAUDE_CONFIG_DIR"] = d["normalClaudeHome"]
            result["CLAUDE_SECURESTORAGE_CONFIG_DIR"] = _NATIVE_ENV.get("claude", {}).get("CLAUDE_SECURESTORAGE_CONFIG_DIR", d["normalClaudeHome"])
        return result
    orchestration._auto_create_claude_terminal = auto
    orchestration._build_claude_native_base_args = arguments
    native.resolve_native_claude_config = resolve
    # orchestration imports this resolver eagerly; keep that exact call site bound.
    orchestration.resolve_native_claude_config = resolve
    native.build_native_claude_terminal_env = environment


def _patch_sdk_env(d):
    workflow = importlib.import_module("omnigent.runtime.workflow")
    for family, name in (("codex", "_build_codex_spawn_env"), ("claude", "_build_claude_sdk_spawn_env")):
        original = getattr(workflow, name)
        def builder(*args, _family_name=family, _original=original, **kwargs):
            _family(d, _family_name)
            env = _original(*args, **kwargs)
            for key in list(env):
                if key.startswith(("OPENAI_", "CODEX_", "ANTHROPIC_", "CLAUDE_")):
                    env.pop(key, None)
            if not _kilo(d, _family_name):
                env.update(_native_credentials(d, _family_name))
                env["HOME"] = d["normalHome"]
                env["CODEX_HOME" if _family_name == "codex" else "CLAUDE_CONFIG_DIR"] = d["normalCodexHome" if _family_name == "codex" else "normalClaudeHome"]
            else:
                env["CODEX_HOME" if _family_name == "codex" else "CLAUDE_CONFIG_DIR"] = str(Path(d["root"]) / "profiles" / (_family_name + "-kilo"))
            env["KILO_OMNIGENT_DESCRIPTOR"] = str(Path(d["root"]) / "runtime" / "descriptor.json")
            env["PYTHONPATH"] = str(Path(d["root"]) / "runtime")
            env.pop("KILO_LOCAL_API_KEY", None)
            for key in list(env):
                if key.startswith("KILO_OMNIGENT_NATIVE_"):
                    env.pop(key, None)
            # Only its own credential is carried into this harness worker.
            for other in ("CODEX", "CLAUDE"):
                env.pop("KILO_OMNIGENT_" + other + "_TOKEN", None)
            if _kilo(d, _family_name):
                key = "KILO_OMNIGENT_" + _family_name.upper() + "_TOKEN"
                env[key] = _kilo_token(d, _family_name)
            else:
                env["KILO_OMNIGENT_NATIVE_" + _family_name.upper() + "_ENV"] = json.dumps(_native_credentials(d, _family_name))
            env["KILO_OMNIGENT_CHILD_FAMILY"] = _family_name
            env.pop("KILO_OMNIGENT_DISPATCH_TOKENS", None)
            return env
        setattr(workflow, name, builder)
    executor = importlib.import_module("omnigent.inner.claude_sdk_executor")
    original_turn = executor.ClaudeSDKExecutor.run_turn
    async def turn(self, messages, tools, system_prompt, config=None):
        if not _kilo(d, "claude-sdk"):
            async for event in original_turn(self, messages, tools, system_prompt, config):
                yield event
            return
        cfg = config if config is not None else executor.ExecutorConfig()
        row = model_policy(d, "claude-sdk", cfg.model or self._model_override)
        selected = effort_policy(d, "claude-sdk", row["id"], cfg.extra.get("reasoning_effort")) if row["reasoningLevels"] else None
        extra = dict(cfg.extra)
        if selected in {"low", "medium", "high", "xhigh", "max"}:
            extra["reasoning_effort"] = selected
        else:
            extra.pop("reasoning_effort", None)
        forwarded = dataclasses.replace(cfg, model=row["id"], extra=extra)
        turn_context = {"session_key": self._session_key(messages), "model": row["id"], "effort": selected}
        events = original_turn(self, messages, tools, system_prompt, forwarded)
        try:
            while True:
                context = _SDK_TURN.set(turn_context)
                try:
                    try:
                        event = await events.__anext__()
                    finally:
                        _SDK_TURN.reset(context)
                except StopAsyncIteration:
                    return
                # An async generator may be closed by another task/context.
                # Keep tokens local to awaits, never across an outward yield.
                yield event
        finally:
            context = _SDK_TURN.set(turn_context)
            try:
                await events.aclose()
            finally:
                _SDK_TURN.reset(context)
    executor.ClaudeSDKExecutor.run_turn = turn
    original_client = executor.ClaudeSDKExecutor._get_or_create_client
    async def client(self, sdk, *, session_key, options, model):
        if not _kilo(d, "claude-sdk"):
            env = dict(getattr(options, "env", {}) or {})
            env.update(_native_credentials(d, "claude")); env["HOME"] = d["normalHome"]
            env["CLAUDE_CONFIG_DIR"] = d["normalClaudeHome"]
            options.env = env
            return await original_client(self, sdk, session_key=session_key, options=options, model=model)
        row = model_policy(d, "claude-sdk", model)
        session_id = "sdk_" + hashlib.sha256(str(session_key).encode()).hexdigest()
        context = _SDK_TURN.get()
        if context is not None and (context["session_key"] != session_key or context["model"] != row["id"]):
            raise ValueError("The private Claude turn does not match its session and model")
        requested = context["effort"] if context is not None else getattr(options, "effort", None)
        selected = await asyncio.to_thread(register_claude_effort, d, session_id, row["id"], requested)
        options.effort = selected if selected in {"low", "medium", "high", "xhigh", "max"} else None
        env = dict(getattr(options, "env", {}) or {})
        env.update(_claude_owned_env(d, session_id)); env["KILO_OMNIGENT_CLAUDE_TOKEN"] = _kilo_token(d, "claude")
        options.env = env
        # Preserve the owned SDK transport shim; then pin the same local route
        # in highest-priority CLI settings so a project cannot redirect it.
        await self._route_options_through_gateway_shim(options)
        env = dict(options.env or {})
        current = getattr(options, "settings", None)
        settings = json.loads(current) if isinstance(current, str) and current else dict(current or {})
        settings["apiKeyHelper"] = _credential_command(d)
        settings["env"] = {**dict(settings.get("env", {})), **_claude_owned_env(d, session_id, env.get("ANTHROPIC_BASE_URL"))}
        options.settings = json.dumps(settings, separators=(",", ":"))
        return await original_client(self, sdk, session_key=session_key, options=options, model=row["id"])
    executor.ClaudeSDKExecutor._get_or_create_client = client


def _patch_claude_gateway_disconnect(d):
    if not d["preferences"]["claude"] or d["preferences"]["claudeConnection"] != "kilo":
        return
    config = _prepared_inference_config(d)
    provider = config["inference"]["harnesses"]["claude-sdk"]["provider"]
    expected_base = config["providers"][provider]["anthropic"]["base_url"].rstrip("/")
    module = importlib.import_module("omnigent.inner.claude_gateway_shim")
    original = module.ClaudeGatewayShim._asgi_app
    async def forward(self, scope, receive, send):
        if scope["type"] != "http" or self._upstream_base_url != expected_base:
            return await original(self, scope, receive, send)
        body_done = asyncio.Event()
        disconnected = asyncio.Event()
        async def body_receive():
            message = await receive()
            if message["type"] == "http.disconnect":
                disconnected.set()
                raise asyncio.CancelledError()
            if message["type"] == "http.request" and not message.get("more_body", False):
                body_done.set()
            return message
        async def watch_disconnect():
            # The original forwarder alone owns all request-body reads.
            await body_done.wait()
            while True:
                if (await receive())["type"] == "http.disconnect":
                    disconnected.set()
                    return
        request = asyncio.create_task(original(self, scope, body_receive, send))
        watcher = asyncio.create_task(watch_disconnect())
        try:
            done, _ = await asyncio.wait((request, watcher), return_when=asyncio.FIRST_COMPLETED)
            if request in done:
                try:
                    return await request
                except asyncio.CancelledError:
                    if not disconnected.is_set():
                        raise
                    return
            await watcher
            request.cancel()
            try:
                await request
            except asyncio.CancelledError:
                pass
        finally:
            for task in (request, watcher):
                if not task.done():
                    task.cancel()
            await asyncio.gather(request, watcher, return_exceptions=True)
    module.ClaudeGatewayShim._asgi_app = forward


def _patch_private_runner_shutdown(d):
    # The original runner closes detached terminals after its process manager.
    # Close them first so a slow SDK shutdown cannot exhaust the unchanged
    # parent-death watchdog before tmux receives its own close request.
    from importlib.abc import MetaPathFinder
    from importlib.machinery import PathFinder, SourceFileLoader
    name = "omnigent.runner._entry"
    if name in sys.modules:
        raise ValueError("The private Omnigent runner was imported before its shutdown policy")
    source = Path(d["sourceRoot"]) / "runner" / "_entry.py"
    anchor = "        await pm.shutdown()\n        await _terminal_registry.shutdown()"
    replacement = "        await _terminal_registry.shutdown()\n        await pm.shutdown()"
    class Loader(SourceFileLoader):
        def get_code(self, fullname):
            raw = _read_regular(source, 16 << 20)
            if fullname != name or hashlib.sha256(raw).hexdigest() != d["sourceHashes"].get("runner/_entry.py"):
                raise ValueError("The selected Omnigent runner shutdown contract changed")
            text = raw.decode("utf-8")
            if text.count(anchor) != 1:
                raise ValueError("The selected Omnigent runner shutdown contract changed")
            return compile(text.replace(anchor, replacement, 1), str(source), "exec")
    class Finder(MetaPathFinder):
        def find_spec(self, fullname, path=None, target=None):
            if fullname != name:
                return None
            spec = PathFinder.find_spec(fullname, path, target)
            if spec is None or not isinstance(spec.loader, SourceFileLoader) or spec.origin != str(source) or source.is_symlink():
                raise ValueError("The selected Omnigent runner shutdown loader changed")
            spec.loader = Loader(fullname, str(source))
            return spec
    sys.meta_path.insert(0, Finder())


def _patch_private_runner_efforts(d):
    # Transform only exact pinned request-closure anchors in memory, leaving
    # the installed source and shared utilities intact.
    from importlib.abc import MetaPathFinder
    from importlib.machinery import PathFinder, SourceFileLoader
    name = "omnigent.runner.app"
    if name in sys.modules:
        raise ValueError("The private Omnigent runner was imported before its reasoning policy")
    _patch_private_runner_shutdown(d)
    source = Path(d["sourceRoot"]) / "runner" / "app.py"
    anchor = "from omnigent.util.reasoning_effort import efforts_for_harness, format_supported"
    replacement = "from omnigent.util.reasoning_effort import format_supported; efforts_for_harness = _kilo_omnigent_efforts_for_harness"
    codex_anchor = ('                if harness == "codex-native":\n'
                    '                    if model is None or not model.strip():\n'
                    '                        return Response(status_code=204)\n'
                    '                    settings: _JsonObject = {"model": model.strip()}')
    codex_replacement = codex_anchor.replace(
        '                    if model is None or not model.strip():',
        '                    model = _kilo_omnigent_native_codex_model(model)\n'
        '                    if model is None or not model.strip():', 1)
    def efforts(harness):
        original = importlib.import_module("omnigent.util.reasoning_effort").efforts_for_harness(harness)
        if harness in ("claude", "claude-sdk") and d["preferences"]["claude"] and d["preferences"]["claudeConnection"] == "kilo" and original is not None:
            return frozenset(original) | {"none"}
        return original
    def codex_model(model):
        if d["preferences"]["codex"] and d["preferences"]["codexConnection"] == "kilo" and (model is None or isinstance(model, str) and not model.strip()):
            return model_policy(d, "codex-native", None)["id"]
        return model
    class Loader(SourceFileLoader):
        def get_code(self, fullname):
            raw = _read_regular(source, 16 << 20)
            if fullname != name or hashlib.sha256(raw).hexdigest() != d["sourceHashes"].get("runner/app.py"):
                raise ValueError("The selected Omnigent runner reasoning contract changed")
            text = raw.decode("utf-8")
            if text.count(anchor) != 1 or text.count(codex_anchor) != 1:
                raise ValueError("The selected Omnigent runner reasoning contract changed")
            text = text.replace(anchor, replacement, 1).replace(codex_anchor, codex_replacement, 1)
            return compile(text, str(source), "exec")
        def exec_module(self, module):
            module.__dict__["_kilo_omnigent_efforts_for_harness"] = efforts
            module.__dict__["_kilo_omnigent_native_codex_model"] = codex_model
            return super().exec_module(module)
    class Finder(MetaPathFinder):
        def find_spec(self, fullname, path=None, target=None):
            if fullname != name:
                return None
            spec = PathFinder.find_spec(fullname, path, target)
            if spec is None or not isinstance(spec.loader, SourceFileLoader) or spec.origin != str(source) or source.is_symlink():
                raise ValueError("The selected Omnigent runner reasoning loader changed")
            spec.loader = Loader(fullname, str(source))
            return spec
    sys.meta_path.insert(0, Finder())


def _patch_codex_environment(d):
    module = importlib.import_module("omnigent.inner.codex_executor")
    native = importlib.import_module("omnigent.harnesses.codex_native.app_server")
    original = module._clean_codex_env
    def environment(*args,**kwargs):
        result=original(*args,**kwargs)
        for key in list(result):
            if key.startswith(("ANTHROPIC_","CLAUDE_","KILO_OMNIGENT_CLAUDE")):
                result.pop(key,None)
        if not _kilo(d,"codex-native"):
            result.update(_native_credentials(d,"codex"));result["HOME"]=d["normalHome"];result["CODEX_HOME"]=d["normalCodexHome"]
        else:
            result["CODEX_HOME"]=str(Path(d["root"])/"profiles"/"codex-kilo")
            result["KILO_OMNIGENT_CODEX_TOKEN"]=_kilo_token(d, "codex")
        return result
    module._clean_codex_env=environment
    native._clean_codex_env=environment
    original_overrides=module._provider_codex_config_overrides
    def overrides(**kwargs):
        if _kilo(d,"codex-native"):
            kwargs["auth_command"]=_credential_command(d,"codex")
            kwargs["model"]=model_policy(d,"codex-native",kwargs.get("model"))["id"]
        return original_overrides(**kwargs)
    module._provider_codex_config_overrides=overrides
    native._provider_codex_config_overrides=overrides


class LoopbackBoundary:
    def __init__(self, app, port):
        self.app,self.port=app,str(port)
    async def __call__(self,scope,receive,send):
        if scope.get("type") not in ("http","websocket"):
            return await self.app(scope,receive,send)
        headers={k.decode("latin-1").lower():v.decode("latin-1") for k,v in scope.get("headers",[])}
        origin=headers.get("origin","")
        # The official runner sends this non-browser sentinel on its HTTP
        # server_client as well as WebSocket handshakes. This boundary only
        # admits the origin; session/tunnel authorization remains downstream.
        valid= headers.get("host")=="127.0.0.1:"+self.port and (not origin or origin=="http://127.0.0.1:"+self.port or origin=="omnigent://internal")
        if not valid:
            if scope["type"]=="websocket":
                await send({"type":"websocket.close","code":1008});return
            body=b'{"error":"invalid_local_origin"}'
            await send({"type":"http.response.start","status":403,"headers":[(b"content-type",b"application/json"),(b"content-length",str(len(body)).encode())]})
            await send({"type":"http.response.body","body":body});return
        return await self.app(scope,receive,send)


def _patch_health(d):
    module = importlib.import_module("omnigent.server.app")
    create = module.create_app
    def wrapped(*args, **kwargs):
        app = create(*args, **kwargs)
        port=int(os.environ["KILO_OMNIGENT_SERVER_PORT"])
        from starlette.middleware.trustedhost import TrustedHostMiddleware
        app.add_middleware(TrustedHostMiddleware, allowed_hosts=["127.0.0.1"])
        app.add_middleware(LoopbackBoundary,port=port)
        from starlette.responses import JSONResponse
        async def health(request):
            token = os.environ.get("KILO_OMNIGENT_CONTROL_TOKEN", "")
            authorization = request.headers.get("authorization", "")
            if not token or not hmac.compare_digest(authorization, "Bearer " + token):
                return JSONResponse({"error": "unauthorized"}, status_code=401)
            return JSONResponse({"version": 1, "pid": os.getpid(), "fingerprint": d["fingerprint"]})
        from starlette.routing import Route
        app.router.routes.insert(0, Route("/kilo-omnigent/health", health, methods=["GET"]))
        return app
    module.create_app = wrapped


def install(d):
    global _ACTIVE
    _configure_owned_socket_root(d, required=True)
    if _ACTIVE is not None:
        if _ACTIVE["fingerprint"] != d["fingerprint"]:
            raise ValueError("Conflicting private Omnigent runtime")
        return
    verify_sources(d)
    _private_environment(d)
    _patch_private_runner_efforts(d)
    _patch_registry(d)
    _patch_bindings(d)
    _patch_selected_cli(d)
    _patch_catalogs(d)
    _patch_native_controls(d)
    _patch_host(d)
    _patch_fork_entries(d)
    _patch_claude_launch(d)
    _patch_sdk_env(d)
    _patch_claude_gateway_disconnect(d)
    _patch_codex_environment(d)
    _patch_health(d)
    _ACTIVE = d


def install_from_environment():
    path = os.environ.get("KILO_OMNIGENT_DESCRIPTOR")
    if path:
        install(load_descriptor(path))


def _write_json(path, data):
    path = Path(path)
    if path.is_symlink():
        raise ValueError("Unsafe private runtime record")
    temporary = path.with_name(path.name + ".tmp-" + secrets.token_hex(8))
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(data, stream, separators=(",", ":")); stream.write("\n")
            stream.flush(); os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def _http_json(url, token=None, timeout=1):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    headers = {"Authorization": "Bearer " + token} if token else {}
    with opener.open(urllib.request.Request(url, headers=headers), timeout=timeout) as response:
        return json.loads(response.read(1 << 20))


def _stop_owned(children):
    # A leader can exit while its runner descendants still close detached
    # terminal servers. Keep their socket root until the owned groups are gone.
    processes = list(reversed(children))
    safe = True
    deadline = time.monotonic() + 8
    for process in processes:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        except OSError as error:
            if not (sys.platform == "darwin" and isinstance(error, PermissionError) and error.errno == errno.EPERM):
                safe = False
    pending = processes
    while pending:
        alive = []
        for process in pending:
            process.poll()  # Reap our leader before probing its group.
            try:
                os.killpg(process.pid, 0)
            except ProcessLookupError:
                pass
            except OSError as error:
                if not (sys.platform == "darwin" and isinstance(error, PermissionError) and error.errno == errno.EPERM):
                    safe = False
                alive.append(process)
            else:
                alive.append(process)
        pending = alive
        remaining = deadline - time.monotonic()
        if not pending or remaining <= 0:
            break
        time.sleep(min(0.05, remaining))
    if pending:
        # A forced exit cannot promise that detached terminals closed. Retain
        # their owned sockets for diagnosis instead of unlinking them first.
        safe = False
        for process in pending:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            except OSError:
                safe = False
    hard_deadline = time.monotonic() + 3
    for process in processes:
        try:
            process.wait(timeout=max(0, hard_deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            safe = False
    return safe


def _open_existing(d, root):
    record = _json(root / "runtime.json")
    if record.get("version") != 1 or record.get("fingerprint") != d["fingerprint"] or type(record.get("pid")) is not int or record["pid"] < 1:
        raise ValueError("A different private Omnigent workspace is already running")
    for key in ("serverPort", "controlPort"):
        if type(record.get(key)) is not int or not 1024 <= record[key] <= 65535:
            raise ValueError("The private Omnigent runtime record is invalid")
    if not isinstance(record.get("controlToken"), str) or not 32 <= len(record["controlToken"]) <= 256:
        raise ValueError("The private Omnigent runtime record is invalid")
    os.kill(record["pid"], 0)
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        ready = _http_json(f'http://127.0.0.1:{record["controlPort"]}/ready', record["controlToken"])
        expected = {"version": 1, "pid": record["pid"], "fingerprint": d["fingerprint"], "ready": True}
        if ready == expected:
            break
        if ready != {**expected, "ready": False}:
            raise ValueError("The private Omnigent runtime changed")
        time.sleep(0.1)
    else:
        raise ValueError("The private Omnigent runtime is not ready")
    health = _http_json(f'http://127.0.0.1:{record["serverPort"]}/kilo-omnigent/health', record["controlToken"])
    if health.get("fingerprint") != d["fingerprint"]:
        raise ValueError("The private Omnigent server changed")
    webbrowser.open(f'http://127.0.0.1:{record["serverPort"]}/')


def supervise(d, descriptor):
    import fcntl
    root = Path(d["root"])
    lock = root / "runtime.lock"
    if lock.is_symlink():
        raise ValueError("Unsafe private runtime lock")
    lock_file = open(lock, "a+b")
    os.chmod(lock, 0o600)
    try:
        fcntl.flock(lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        lock_file.close()
        _open_existing(d, root)
        return
    children, logs = [], []
    owned_tmp, owned_tmp_identity, control, reservation = None, None, None, None
    ready_event, stop = threading.Event(), threading.Event()
    record_path = root / "runtime.json"
    status_path = root / "runtime" / "status.json"
    token = secrets.token_urlsafe(32)
    failed = False
    def status(phase, message):
        _write_json(status_path, {"version": 1, "fingerprint": d["fingerprint"], "phase": phase, "message": message})
    class Control(BaseHTTPRequestHandler):
        def boundary(self):
            origin = self.headers.get("Origin", "")
            local = "127.0.0.1:" + str(self.server.server_port)
            return self.headers.get("Host") == local and (not origin or origin == "http://" + local)
        def authorized(self):
            return self.boundary() and hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + token)
        def do_GET(self):
            if self.path != "/ready" or not self.authorized():
                self.send_error(403); return
            payload = {"version": 1, "pid": os.getpid(), "fingerprint": d["fingerprint"], "ready": ready_event.is_set()}
            self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(json.dumps(payload).encode())
        def do_POST(self):
            if self.path != "/stop" or not self.authorized():
                self.send_error(403); return
            self.send_response(200); self.end_headers(); self.wfile.write(b'{"stopping":true}\n'); stop.set()
        def log_message(self, *args):
            pass
    try:
        status("starting", "Starting the private Omnigent server and host.")
        owned_tmp, owned_tmp_identity = _create_owned_socket_root(d)
        control = ThreadingHTTPServer(("127.0.0.1", 0), Control)
        control.daemon_threads = True
        threading.Thread(target=control.serve_forever, daemon=True).start()
        reservation = socket.socket()
        reservation.bind(("127.0.0.1", d["serverPort"]))
        port = reservation.getsockname()[1]
        env = dict(os.environ)
        env["KILO_OMNIGENT_OWN_TMP"] = str(owned_tmp)
        for family, values in _NATIVE_ENV.items():
            env["KILO_OMNIGENT_NATIVE_" + family.upper() + "_ENV"] = json.dumps(values)
        env["KILO_OMNIGENT_DISPATCH_TOKENS"] = json.dumps(_KILO_TOKENS)
        env["KILO_OMNIGENT_SERVER_PORT"] = str(port)
        env["KILO_OMNIGENT_CONTROL_TOKEN"] = token
        host_id = hashlib.sha256(str(root).encode()).hexdigest()[:32]
        env["OMNIGENT_HOST_ID"], env["OMNIGENT_HOST_NAME"] = host_id, "Kilo Proxy"
        record = {"version": 1, "pid": os.getpid(), "startedAt": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
                  "serverPort": port, "controlPort": control.server_port, "controlToken": token, "fingerprint": d["fingerprint"]}
        _write_json(record_path, record)
        script = str(root / "runtime" / "omnigent_runtime.py")
        def launch(role):
            log_path = root / "runtime" / (role + ".log")
            if log_path.is_symlink():
                raise ValueError("Unsafe private runtime log")
            fd = os.open(log_path, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
            log = os.fdopen(fd, "ab"); logs.append(log)
            process = subprocess.Popen([d["pythonPath"], "-I", script, "--child", role, descriptor], cwd=str(root), env=env,
                                       stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
            children.append(process)
            return process
        signal.signal(signal.SIGTERM, lambda *_: stop.set())
        signal.signal(signal.SIGINT, lambda *_: stop.set())
        reservation.close()
        server = launch("server")
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline and not stop.is_set():
            if server.poll() is not None:
                raise ValueError("The private Omnigent server exited during startup")
            try:
                health = _http_json(f"http://127.0.0.1:{port}/kilo-omnigent/health", token)
                if health != {"version": 1, "pid": server.pid, "fingerprint": d["fingerprint"]}:
                    raise ValueError("The selected port belongs to a different server")
                break
            except (OSError, urllib.error.URLError):
                stop.wait(0.1)
        else:
            if stop.is_set():
                return
            raise ValueError("The private Omnigent server did not become ready")
        if stop.is_set():
            return
        host = launch("host")
        while time.monotonic() < deadline and not stop.is_set():
            if server.poll() is not None or host.poll() is not None:
                raise ValueError("The private Omnigent host exited during startup")
            try:
                result = _http_json(f"http://127.0.0.1:{port}/v1/hosts")
                rows = result if isinstance(result, list) else result.get("hosts", [])
                if any(row.get("host_id") == host_id and row.get("status") == "online" for row in rows):
                    break
            except (OSError, urllib.error.URLError):
                pass
            stop.wait(0.1)
        else:
            if stop.is_set():
                return
            raise ValueError("The private Omnigent host did not register")
        ready_event.set()
        status("ready", "The private Omnigent workspace is ready.")
        webbrowser.open(f"http://127.0.0.1:{port}/")
        while not stop.wait(0.25):
            if server.poll() is not None or host.poll() is not None:
                raise ValueError("The private Omnigent runtime stopped unexpectedly")
    except BaseException:
        failed = True
        status("failed", "The private Omnigent runtime failed. Review its own logs and prepare again.")
        raise
    finally:
        ready_event.clear()
        if reservation is not None:
            reservation.close()
        cleanup_safe = False
        try:
            cleanup_safe = _stop_owned(children)
        finally:
            if control is not None:
                control.shutdown(); control.server_close()
            if record_path.is_file() and not record_path.is_symlink():
                try:
                    current = _json(record_path)
                    if current.get("pid") == os.getpid() and current.get("controlToken") == token:
                        record_path.unlink()
                except (OSError, ValueError):
                    pass
            if not cleanup_safe:
                status("failed", "The private Omnigent processes did not finish stopping. Their temporary directory was kept.")
            elif not failed:
                status("stopped", "The private Omnigent workspace has stopped.")
            for log in logs:
                log.close()
            if owned_tmp is not None and cleanup_safe:
                if not _cleanup_owned_socket_root(d, owned_tmp_identity):
                    status("failed", "The private Omnigent temporary cleanup could not be confirmed. Its directory was kept.")
            fcntl.flock(lock_file, fcntl.LOCK_UN)
            lock_file.close()


def main():
    if len(sys.argv) == 3 and sys.argv[1] == "--credential" and sys.argv[2] in ("claude","codex"):
        token = os.environ.get("KILO_OMNIGENT_"+sys.argv[2].upper()+"_TOKEN", "")
        if not token or "\n" in token or "\r" in token:
            raise ValueError("The private Claude credential is unavailable")
        sys.stdout.write(token)
        return
    parser = argparse.ArgumentParser()
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--supervise", action="store_true")
    group.add_argument("--child", choices=("server", "host"))
    parser.add_argument("descriptor")
    args = parser.parse_args()
    d = load_descriptor(args.descriptor)
    if args.supervise:
        # The supervisor stays standard-library-only until its own control and
        # record exist; application imports occur exclusively in owned children.
        try:
            verify_sources(d)
            _private_environment(d)
            supervise(d, args.descriptor)
        except BaseException:
            _write_json(Path(d["root"]) / "runtime" / "status.json", {"version": 1, "fingerprint": d["fingerprint"], "phase": "failed", "message": "The private Omnigent runtime failed. Prepare its workspace again."})
            raise
    else:
        install(d)
        from omnigent.cli import main as omni_main
        port = int(os.environ["KILO_OMNIGENT_SERVER_PORT"])
        command = ["server", "--host", "127.0.0.1", "--port", str(port), "--no-open"] if args.child == "server" else ["host", "--server", f"http://127.0.0.1:{port}", "--no-open", "--non-interactive"]
        sys.argv = [d["omniPath"], *command]
        omni_main()


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except BaseException:
        # Never print vendor exceptions which may contain headers or credentials.
        sys.stderr.write("The private Omnigent runtime failed. Prepare its workspace again.\n")
        raise SystemExit(78)
