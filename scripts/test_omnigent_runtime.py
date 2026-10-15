"""Synthetic checks for the private runtime; never import a vendor SDK/app."""
import asyncio
import copy
import dataclasses
import importlib.util
import json
import os
import socket
import shutil
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "omnigent_runtime.py"
spec = importlib.util.spec_from_file_location("kilo_omnigent_test_runtime", SOURCE)
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


class RuntimePolicyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="kilo-omnigent-test-")
        self.root = Path(self.tmp.name)
        self.d = {"root": str(self.root), "pythonPath": "/synthetic/python", "omniPath": "/synthetic/omni", "normalHome": str(self.root / "normal"),
                  "normalCodexHome": str(self.root / "normal" / ".codex"), "normalClaudeHome": str(self.root / "normal" / ".claude"),
                  "codexPath": "/selected/codex", "claudePath": "/selected/claude", "configPath": str(self.root / "config" / "config.yaml"), "inferencePath": str(self.root / "inference.json"),
                  "fingerprint": "a" * 64, "preferences": {"version": 1, "codex": True, "claude": True, "codexConnection": "kilo", "claudeConnection": "kilo"},
                  "modelLibraries": {"codex": {"defaultModel": "openai/exact-a", "models": [{"id": "openai/exact-a", "name": "My Codex", "reasoningLevels": ["low", "high"], "defaultReasoning": "high"}]},
                                     "claude": {"defaultModel": "vendor/exact-b", "models": [{"id": "vendor/exact-b", "name": "My Claude", "reasoningLevels": ["none", "high"], "defaultReasoning": "none"}, {"id": "vendor/exact-c", "name": "No thinking", "reasoningLevels": [], "defaultReasoning": ""}]}}}
        self.bindings = {"providers": {"kilo-codex": {"openai": {"base_url": "http://127.0.0.1:8877/omnigent/codex/v1"}}, "kilo-claude": {"anthropic": {"base_url": "http://127.0.0.1:8877/omnigent/claude"}}}, "inference": {"harnesses": {}}}
        for harness, family in (("codex", "codex"), ("codex-native", "codex"), ("claude-sdk", "claude"), ("claude-native", "claude")):
            library = self.d["modelLibraries"][family]
            self.bindings["inference"]["harnesses"][harness] = {"provider": "kilo-" + family, "default_model": library["defaultModel"], "model_allowlist": [m["id"] for m in library["models"]]}
        (self.root / "inference.json").write_text(json.dumps(self.bindings))
        self.d["inferenceHash"] = runtime.hashlib.sha256((self.root / "inference.json").read_bytes()).hexdigest()
        self.old_tokens = runtime._KILO_TOKENS
        runtime._KILO_TOKENS = {}
        self.old_native = runtime._NATIVE_ENV
        runtime._NATIVE_ENV = {}
        self.old_credential_pid = runtime._CREDENTIAL_PID
        runtime._CREDENTIAL_PID = None
        self.old_temp = tempfile.tempdir

    def tearDown(self):
        runtime._KILO_TOKENS = self.old_tokens
        runtime._NATIVE_ENV = self.old_native
        runtime._CREDENTIAL_PID = self.old_credential_pid
        tempfile.tempdir = self.old_temp
        self.tmp.cleanup()

    def owned_socket_fixture(self):
        owned, identity = runtime._create_owned_socket_root(self.d)
        self.addCleanup(lambda: self.assertTrue(runtime._cleanup_owned_socket_root(self.d, identity)))
        return str(owned)

    def test_exact_ids_defaults_and_empty_reasoning(self):
        rows = runtime.model_rows(self.d, "claude-native")
        self.assertEqual([r["id"] for r in rows], ["vendor/exact-b", "vendor/exact-c"])
        self.assertEqual(rows[0]["displayName"], "My Claude")
        self.assertTrue(rows[0]["isDefault"])
        self.assertEqual(rows[1]["supportedReasoningEfforts"], [])
        self.assertEqual(runtime.model_policy(self.d, "claude-sdk")["id"], "vendor/exact-b")
        for spelling in ("exact-b", "vendor/exact-b-old", "openai/exact-a"):
            with self.assertRaises(ValueError):
                runtime.model_policy(self.d, "claude-native", spelling)
        self.assertEqual(runtime.effort_policy(self.d, "codex", None, None), "high")
        self.assertEqual(runtime.effort_policy(self.d, "claude-native", "vendor/exact-b", None), "none")
        self.assertIsNone(runtime.effort_policy(self.d, "claude-native", "vendor/exact-c", None))
        with self.assertRaises(ValueError):
            runtime.effort_policy(self.d, "claude-native", "vendor/exact-c", "high")

    def test_disabled_harnesses_and_binding_changes_fail_before_dispatch(self):
        self.assertIs(runtime.validate_binding_config(self.d, self.bindings), self.bindings)
        for mutate in (lambda c: c["inference"]["harnesses"].pop("claude-native"),
                       lambda c: c["inference"]["harnesses"]["claude-sdk"].update(model_allowlist=["vendor/other"]),
                       lambda c: c["inference"]["harnesses"].update({"opencode-native": {}})):
            changed = copy.deepcopy(self.bindings); mutate(changed)
            with self.assertRaises(ValueError):
                runtime.validate_binding_config(self.d, changed)
        self.d["preferences"]["codex"] = False
        for harness in ("codex", "codex-native", "opencode-native", "muse"):
            with self.assertRaises(ValueError):
                runtime.model_policy(self.d, harness, "openai/exact-a")

    def binding_modules_fixture(self):
        module = types.ModuleType("omnigent.inference_config")
        providers = types.ModuleType("omnigent.onboarding.provider_config")
        module.load_runtime_inference_config = lambda base=None: copy.deepcopy(self.bindings if base is None else base)
        module.resolve_bound_model = lambda config, harness, model=None: model
        module.resolve_bound_provider = lambda config, harness, auth=None, **kwargs: config
        module.snapshot_runtime_config = lambda saved: copy.deepcopy(saved["runtime_config"]) if saved is not None else None
        providers.resolve_secret = lambda reference: reference
        return module, providers

    def test_private_session_snapshot_is_exact_hashed_and_retains_native_connections(self):
        module, providers = self.binding_modules_fixture()
        with patch.dict(sys.modules, {m.__name__: m for m in (module, providers)}):
            runtime._patch_bindings(self.d)
            self.assertEqual(module.snapshot_runtime_config(None), self.bindings)
            saved = {"runtime_config": copy.deepcopy(self.bindings), "unrelatedMetadata": "preserve"}
            result = module.snapshot_runtime_config(saved)
            self.assertEqual(result, self.bindings)
            result["providers"].clear()
            self.assertEqual(saved["runtime_config"], self.bindings)
            for change in (lambda c: c["providers"]["kilo-codex"]["openai"].update(base_url="https://foreign.invalid/v1"),
                           lambda c: c["inference"]["harnesses"]["codex"].update(default_model="foreign/model")):
                changed = copy.deepcopy(self.bindings)
                change(changed)
                with self.assertRaisesRegex(ValueError, "snapshot differs"):
                    module.snapshot_runtime_config({"runtime_config": changed})
            path = self.root / "inference.json"
            original = path.read_bytes()
            path.write_bytes(original + b"\n")
            with self.assertRaisesRegex(ValueError, "configuration changed"):
                module.snapshot_runtime_config(None)
            path.write_bytes(original)
            # A mixed profile carries only its prepared Kilo family. It does
            # not manufacture a provider/binding for the native sibling.
            self.d["preferences"]["claudeConnection"] = "native"
            mixed = copy.deepcopy(self.bindings)
            mixed["providers"].pop("kilo-claude")
            for harness in ("claude-sdk", "claude-native"):
                mixed["inference"]["harnesses"].pop(harness)
            path.write_text(json.dumps(mixed))
            self.d["inferenceHash"] = runtime.hashlib.sha256(path.read_bytes()).hexdigest()
            self.assertEqual(module.snapshot_runtime_config(None), mixed)
            self.assertNotIn("kilo-claude", module.snapshot_runtime_config(None)["providers"])
            self.d["preferences"]["codexConnection"] = "native"
            path.unlink()
            self.assertIsNone(module.snapshot_runtime_config(None))
            native = {"runtime_config": {"nativeOnly": "unchanged-original-contract"}}
            self.assertEqual(module.snapshot_runtime_config(native), native["runtime_config"])
            self.d["preferences"]["codex"] = False
            self.d["preferences"]["claude"] = False
            self.assertIsNone(module.snapshot_runtime_config(None))

    def test_private_host_forwards_inference_and_rejects_foreign_frame_before_dispatch(self):
        host = types.ModuleType("omnigent.host.connect")
        calls = []
        def original(*args, **kwargs):
            calls.append((args, kwargs))
            return {"original": "retained"}
        host._build_runner_env = original
        class Host:
            async def _handle_model_options(self, frame): raise AssertionError("No catalog request")
        host.HostProcess = Host
        with patch.dict(sys.modules, {host.__name__: host}), patch.dict(os.environ, {"OMNIGENT_INFERENCE_CONFIG": self.d["inferencePath"], "UNRELATED_SECRET": "not-forwarded"}, clear=True):
            runtime._patch_host(self.d)
            result = host._build_runner_env(harness="codex", inference_config=None)
            self.assertEqual(result["OMNIGENT_INFERENCE_CONFIG"], self.d["inferencePath"])
            self.assertEqual(result["original"], "retained")
            self.assertNotIn("UNRELATED_SECRET", result)
            frame = copy.deepcopy(self.bindings)
            result = host._build_runner_env(harness="claude-native", inference_config=frame)
            self.assertIs(calls[-1][1]["inference_config"], frame)
            # Upstream writes an immutable session snapshot after this call.
            # Its later path override remains authoritative and is not reset.
            result["OMNIGENT_INFERENCE_CONFIG"] = "/own/session-inference/snapshot.json"
            self.assertEqual(result["OMNIGENT_INFERENCE_CONFIG"], "/own/session-inference/snapshot.json")
            changed = copy.deepcopy(frame)
            changed["providers"]["kilo-codex"]["openai"]["base_url"] = "https://foreign.invalid/v1"
            before = len(calls)
            with self.assertRaisesRegex(ValueError, "snapshot differs"):
                host._build_runner_env(harness="codex", inference_config=changed)
            self.assertEqual(len(calls), before)
            self.d["preferences"]["codex"] = False
            with self.assertRaises(ValueError): host._build_runner_env(harness="codex", inference_config=None)
            self.assertEqual(len(calls), before)
            self.d["preferences"].update(codex=True, codexConnection="native", claudeConnection="native")
            self.assertEqual(host._build_runner_env(harness="codex", inference_config={"native": "original"})["original"], "retained")
            self.assertEqual(calls[-1][1]["inference_config"], {"native": "original"})

    def test_native_model_catalog_is_preserved(self):
        self.d["preferences"]["claudeConnection"] = "native"
        self.assertIsNone(runtime.model_rows(self.d, "claude-native"))
        self.assertIsNone(runtime.model_policy(self.d, "claude-native", "native/whatever"))
        self.assertEqual(runtime.effort_policy(self.d, "claude-native", "native/whatever", "max"), "max")

    def test_transient_native_credentials_are_scrubbed_and_no_ambient_roots_survive(self):
        self.d["preferences"]["claudeConnection"] = "native"
        with patch.dict(os.environ, {"HOME": "/personal", "XDG_CONFIG_HOME": "/personal/config", "XDG_DATA_HOME": "/personal/data", "HTTP_PROXY": "http://foreign", "OPENAI_API_KEY": "wrong", "ANTHROPIC_API_KEY": "wrong", "KILO_LOCAL_API_KEY": "root-key", "KILO_OMNIGENT_NATIVE_CLAUDE_ENV": json.dumps({"ANTHROPIC_API_KEY": "synthetic-native"})}, clear=True):
            runtime._private_environment(self.d)
            self.assertNotIn("KILO_OMNIGENT_NATIVE_CLAUDE_ENV", os.environ)
            self.assertNotIn("ANTHROPIC_API_KEY", os.environ)
            self.assertNotIn("OPENAI_API_KEY", os.environ)
            self.assertNotIn("KILO_LOCAL_API_KEY", os.environ)
            self.assertNotIn("HTTP_PROXY", os.environ)
            self.assertEqual(runtime._NATIVE_ENV["claude"], {"ANTHROPIC_API_KEY": "synthetic-native"})
            self.assertEqual(os.environ["XDG_DATA_HOME"], str(self.root / "xdg" / "data"))
            self.assertEqual(os.environ["CODEX_HOME"], str(self.root / "profiles" / "codex-kilo"))
            self.assertEqual(os.environ["CLAUDE_CONFIG_DIR"], self.d["normalClaudeHome"])
            self.assertEqual(os.environ["PYTHON_KEYRING_BACKEND"], "keyring.backends.null.Keyring")

    def test_registration_requires_confirmation_and_never_proxies(self):
        requests = []
        class Response:
            status = 200
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def read(self, size): return json.dumps({"ok": True, "sessionId": "conv_1", "model": "vendor/exact-b", "effort": "none"}).encode()
        class Opener:
            def open(self, request, timeout): requests.append(request); return Response()
        with patch.dict(os.environ, {"KILO_OMNIGENT_CLAUDE_TOKEN": "synthetic-scoped"}, clear=True), patch.object(runtime.urllib.request, "build_opener", return_value=Opener()) as opener:
            self.assertEqual(runtime.register_claude_effort(self.d, "conv_1", "vendor/exact-b", None), "none")
            self.assertEqual(requests[0].full_url, "http://127.0.0.1:8877/omnigent/claude/v1/session-effort")
            self.assertEqual(requests[0].get_header("Authorization"), "Bearer synthetic-scoped")
            self.assertEqual(json.loads(requests[0].data)["effort"], "none")
            self.assertIsInstance(opener.call_args.args[0], runtime.urllib.request.ProxyHandler)
            with self.assertRaises(ValueError): runtime.register_claude_effort(self.d, "conv_wrong", "vendor/exact-b", "high")
            with self.assertRaises(ValueError): runtime.register_claude_effort(self.d, "conv_1", "vendor/not-selected", "high")

    def test_registration_errors_include_only_safe_status_or_class_and_keep_cause(self):
        secret = "synthetic-private-value-never-in-diagnostic"
        http_error = runtime.urllib.error.HTTPError("http://127.0.0.1/" + secret, 403, secret, {}, None)
        timeout_error = TimeoutError(secret)
        wrapped_timeout = runtime.urllib.error.URLError(timeout_error)
        transport_error = runtime.urllib.error.URLError(secret)
        io_error = OSError(secret)
        for original, detail in ((http_error, "HTTP 403"), (timeout_error, "TimeoutError"), (wrapped_timeout, "TimeoutError"), (transport_error, "URLError"), (io_error, "OSError")):
            with self.subTest(detail=detail, original=type(original).__name__):
                class Opener:
                    def open(self, request, timeout):
                        self_test.assertEqual(timeout, 5)
                        raise original
                self_test = self
                with patch.object(runtime.urllib.request, "build_opener", return_value=Opener()), patch.dict(os.environ, {"KILO_OMNIGENT_CLAUDE_TOKEN": secret}, clear=True):
                    with self.assertRaises(ValueError) as caught:
                        runtime.register_claude_effort(self.d, "conv_1", "vendor/exact-b", "none")
                self.assertEqual(str(caught.exception), "Claude reasoning was not confirmed (" + detail + "); retry its selection")
                self.assertIs(caught.exception.__cause__, original)
                self.assertNotIn(secret, str(caught.exception))
                self.assertNotIn("http", str(caught.exception))

    def test_registration_malformed_and_unexpected_confirmations_are_distinct_and_private(self):
        secret = "synthetic-private-body-never-in-diagnostic"
        for status, raw, detail, cause_type in ((200, secret.encode(), "JSONDecodeError", json.JSONDecodeError),
                                              (200, json.dumps({"ok": True, "sessionId": secret, "model": "vendor/exact-b", "effort": "none"}).encode(), "unexpected confirmation", None),
                                              (503, b'{}', "HTTP 503", None),
                                              ("http://" + secret, b'{}', "unexpected confirmation", None)):
            with self.subTest(status=status, detail=detail):
                class Response:
                    def __enter__(self): return self
                    def __exit__(self, *args): pass
                    def read(self, size):
                        self_test.assertEqual(size, 16384)
                        return raw
                response = Response()
                response.status = status
                class Opener:
                    def open(self, request, timeout):
                        self_test.assertEqual(timeout, 5)
                        return response
                self_test = self
                with patch.object(runtime.urllib.request, "build_opener", return_value=Opener()), patch.dict(os.environ, {"KILO_OMNIGENT_CLAUDE_TOKEN": secret}, clear=True):
                    with self.assertRaises(ValueError) as caught:
                        runtime.register_claude_effort(self.d, "conv_1", "vendor/exact-b", "none")
                self.assertEqual(str(caught.exception), "Claude reasoning was not confirmed (" + detail + "); retry its selection")
                if cause_type is None:
                    self.assertIsNone(caught.exception.__cause__)
                else:
                    self.assertIsInstance(caught.exception.__cause__, cause_type)
                    self.assertEqual(caught.exception.__cause__.doc, secret)
                self.assertNotIn(secret, str(caught.exception))
                self.assertNotIn("http", str(caught.exception))

    def test_loopback_boundary_checks_host_and_http_or_websocket_origin(self):
        accepted = []
        async def app(scope, receive, send): accepted.append(scope)
        async def run(host, origin, kind="http"):
            output = []
            async def send(item): output.append(item)
            async def receive(): return {"type": "http.disconnect"}
            headers = [(b"host", host.encode())]
            if origin is not None: headers.append((b"origin", origin.encode()))
            await runtime.LoopbackBoundary(app, 43210)({"type": kind, "headers": headers}, receive, send)
            return output
        for origin in (None, "http://127.0.0.1:43210", "omnigent://internal"):
            self.assertEqual(asyncio.run(run("127.0.0.1:43210", origin)), [])
        for host, origin in (("localhost:43210", None), ("attacker.invalid:43210", None), ("127.0.0.1:43210", "https://foreign"), ("127.0.0.1:43210", "null"), ("127.0.0.1:43210", "omnigent://other"), ("127.0.0.1:43210", "omnigent://internal/"), ("127.0.0.1:43210", "Omnigent://internal")):
            self.assertEqual(asyncio.run(run(host, origin))[0]["status"], 403)
        self.assertEqual(asyncio.run(run("127.0.0.1:43210", "omnigent://internal", "websocket")), [])
        self.assertEqual(asyncio.run(run("127.0.0.1:43210", "https://foreign", "websocket"))[0]["code"], 1008)
        self.assertEqual(len(accepted), 4)

    def test_internal_http_origin_preserves_upstream_runner_authorization(self):
        calls=[]
        async def authorized_app(scope,receive,send):
            calls.append(scope)
            headers=dict(scope["headers"])
            token=headers.get(b"x-omnigent-runner-tunnel-token")
            status=200 if token==b"synthetic-correct-bound-token" else 401 if token is None else 403
            await send({"type":"http.response.start","status":status,"headers":[]})
            await send({"type":"http.response.body","body":b"synthetic upstream verdict"})
        async def request(token,origin="omnigent://internal",host="127.0.0.1:43210"):
            output=[]
            async def receive():return {"type":"http.disconnect"}
            async def send(value):output.append(value)
            headers=[(b"host",host.encode()),(b"origin",origin.encode())]
            if token is not None:headers.append((b"x-omnigent-runner-tunnel-token",token))
            await runtime.LoopbackBoundary(authorized_app,43210)({"type":"http","method":"GET","path":"/v1/sessions/own-session/agent/contents","headers":headers},receive,send)
            return output
        for token,expected in ((b"synthetic-correct-bound-token",200),(None,401),(b"synthetic-wrong-token",403)):
            self.assertEqual(asyncio.run(request(token))[0]["status"],expected)
        self.assertEqual(len(calls),3)
        for origin,host in (("https://foreign.example","127.0.0.1:43210"),("null","127.0.0.1:43210"),("omnigent://other","127.0.0.1:43210"),("omnigent://internal","foreign.example:43210"),("omnigent://internal","127.0.0.1:43211")):
            self.assertEqual(asyncio.run(request(b"synthetic-correct-bound-token",origin,host))[0]["status"],403)
        self.assertEqual(len(calls),3)

    def test_console_entry_keeps_its_zero_argument_contract(self):
        observed = []
        module = types.ModuleType("omnigent.cli")
        def main(): observed.append(list(sys.argv))
        module.main = main
        with patch.dict(sys.modules, {"omnigent.cli": module}), patch.object(runtime, "load_descriptor", return_value=self.d), patch.object(runtime, "install"), patch.dict(os.environ, {"KILO_OMNIGENT_SERVER_PORT": "43210"}), patch.object(sys, "argv", [str(SOURCE), "--child", "server", "own-descriptor.json"]):
            runtime.main()
        self.assertEqual(observed, [["/synthetic/omni", "server", "--host", "127.0.0.1", "--port", "43210", "--no-open"]])

    def test_native_launch_context_header_and_default_exist_before_cli(self):
        native = types.ModuleType("omnigent.harnesses.claude_native.main")
        orchestration = types.ModuleType("omnigent.runner.native.orchestration")
        @dataclasses.dataclass(frozen=True)
        class Config: api_key_helper: str = "printf synthetic-scoped"
        native.resolve_native_claude_config = lambda **kwargs: Config()
        native.build_native_claude_terminal_env = lambda config: {"ENABLE_TOOL_SEARCH": "true"}
        orchestration._build_claude_native_base_args = lambda **kwargs: ("--model", kwargs["model_override"])
        async def original_auto(session, *args, **kwargs):
            config = await kwargs["resolve_launch_config"]()
            args = orchestration._build_claude_native_base_args(reasoning_effort=None, model_override="vendor/exact-b", terminal_launch_args=[])
            env = native.build_native_claude_terminal_env(config)
            return config, args, env
        orchestration._auto_create_claude_terminal = original_auto
        async def resolve(): return Config()
        calls = []
        def registered(*args): calls.append(args[1:]); return "none"
        with patch.dict(sys.modules, {native.__name__: native, orchestration.__name__: orchestration}), patch.object(runtime, "register_claude_effort", side_effect=registered), patch.dict(os.environ, {"KILO_OMNIGENT_CLAUDE_TOKEN": "synthetic-scoped"}, clear=True):
            runtime._patch_claude_launch(self.d)
            config, args, env = asyncio.run(orchestration._auto_create_claude_terminal("conv_1", resolve_launch_config=resolve))
        self.assertEqual(calls, [("conv_1", "vendor/exact-b", None)])
        self.assertEqual(env["ANTHROPIC_CUSTOM_HEADERS"], "X-Kilo-Omnigent-Session: conv_1")
        self.assertNotIn("synthetic-scoped", " ".join(args))
        self.assertNotIn("synthetic-scoped", config.api_key_helper)
        self.assertIn("--credential", config.api_key_helper)
        settings = json.loads(args[-1])
        self.assertEqual(settings["env"]["ANTHROPIC_BASE_URL"], "http://127.0.0.1:8877/omnigent/claude")
        self.assertIsNone(runtime._SESSION.get())

    def test_dispatch_tokens_are_memory_only_and_workers_get_destination_credentials(self):
        host = types.ModuleType("omnigent.host.connect")
        host._build_runner_env = lambda *args, **kwargs: dict(os.environ)
        class Host:
            async def _handle_model_options(self, frame): raise AssertionError("No catalog lookup in this credential test")
        host.HostProcess = Host
        workflow = types.ModuleType("omnigent.runtime.workflow")
        workflow._build_codex_spawn_env = lambda *args, **kwargs: {"HARNESS_CODEX_MODEL": "openai/exact-a", "ANTHROPIC_API_KEY": "foreign", "KILO_LOCAL_API_KEY": "root"}
        workflow._build_claude_sdk_spawn_env = lambda *args, **kwargs: {"HARNESS_CLAUDE_SDK_MODEL": "vendor/exact-b", "OPENAI_API_KEY": "foreign"}
        executor = types.ModuleType("omnigent.inner.claude_sdk_executor")
        class Executor:
            async def run_turn(self, *args, **kwargs):
                yield None
            async def _get_or_create_client(self, *args, **kwargs): return kwargs["options"]
        executor.ClaudeSDKExecutor = Executor
        with patch.dict(os.environ, {"KILO_OMNIGENT_CODEX_TOKEN": "scoped-codex", "KILO_OMNIGENT_CLAUDE_TOKEN": "scoped-claude"}, clear=True), patch.dict(sys.modules, {m.__name__:m for m in (host, workflow, executor)}):
            runtime._private_environment(self.d)
            self.assertNotIn("KILO_OMNIGENT_CODEX_TOKEN", os.environ)
            self.assertNotIn("KILO_OMNIGENT_CLAUDE_TOKEN", os.environ)
            self.assertEqual(runtime._KILO_TOKENS, {"codex":"scoped-codex", "claude":"scoped-claude"})
            runtime._patch_host(self.d)
            runner = host._build_runner_env(harness="codex-native")
            self.assertEqual(json.loads(runner["KILO_OMNIGENT_DISPATCH_TOKENS"]), runtime._KILO_TOKENS)
            self.assertNotIn("KILO_OMNIGENT_CLAUDE_TOKEN", runner)
            runtime._patch_sdk_env(self.d)
            for family, build in (("codex",workflow._build_codex_spawn_env),("claude",workflow._build_claude_sdk_spawn_env)):
                child = build(None)
                own = "KILO_OMNIGENT_"+family.upper()+"_TOKEN"
                sibling = "KILO_OMNIGENT_"+("CLAUDE" if family=="codex" else "CODEX")+"_TOKEN"
                self.assertEqual(child[own], "scoped-"+family)
                self.assertNotIn(sibling,child)
                self.assertNotIn("KILO_OMNIGENT_DISPATCH_TOKENS",child)
                self.assertNotIn("KILO_LOCAL_API_KEY",child)
                self.assertNotIn("ANTHROPIC_API_KEY",child)
                self.assertNotIn("OPENAI_API_KEY",child)

    def test_host_catalog_keeps_exact_kilo_sdk_and_native_rows_and_native_behavior(self):
        host = types.ModuleType("omnigent.host.connect")
        host._build_runner_env = lambda *args, **kwargs: {}
        calls = []
        provenance = []
        native_result = object()
        class Host:
            async def _handle_model_options(self, frame):
                calls.append(frame)
                return native_result
        @dataclasses.dataclass
        class Result:
            request_id: str
            status: str
            models: list = dataclasses.field(default_factory=list)
            error: str = None
            routable_models: list = dataclasses.field(default_factory=list)
        def with_source(rows, *, harness):
            provenance.append(harness)
            return [{**row, "source": {"syntheticHarness": harness}} for row in rows]
        host.HostProcess = Host
        host.HostModelOptionsResultFrame = Result
        host.canonicalize_harness = lambda name: {"codex-native-ui": "codex-native", "claude-code-native-ui": "claude-native"}.get(name, name)
        host._with_model_configuration_source = with_source
        with patch.dict(sys.modules, {host.__name__: host}):
            runtime._patch_host(self.d)
            for harness, family in (("codex", "codex"), ("codex-native", "codex"), ("codex-native-ui", "codex"), ("claude-sdk", "claude"), ("claude-native", "claude"), ("claude-code-native-ui", "claude")):
                with self.subTest(harness=harness):
                    result = asyncio.run(Host()._handle_model_options(types.SimpleNamespace(request_id="exact-request", harness=harness)))
                    expected = runtime.model_rows(self.d, host.canonicalize_harness(harness))
                    self.assertEqual(result.status, "ok")
                    self.assertEqual(result.request_id, "exact-request")
                    self.assertEqual(result.models, with_source(expected, harness=host.canonicalize_harness(harness)))
                    self.assertEqual(result.routable_models, [m["id"] for m in self.d["modelLibraries"][family]["models"]])
                    self.assertTrue(result.models[0]["isDefault"])
            self.assertEqual(result.models[0]["defaultReasoningEffort"], "none")
            self.assertEqual(result.models[0]["supportedReasoningEfforts"], [{"reasoningEffort": "none"}, {"reasoningEffort": "high"}])
            self.assertEqual(result.models[1]["supportedReasoningEfforts"], [])
            self.assertEqual(calls, [])
            for family, harness in (("codex", "codex"), ("claude", "claude-sdk")):
                self.d["preferences"][family + "Connection"] = "native"
                frame = types.SimpleNamespace(request_id="native", harness=harness)
                self.assertIs(asyncio.run(Host()._handle_model_options(frame)), native_result)
                self.assertIs(calls[-1], frame)
            original_count = len(calls)
            self.d["preferences"]["codex"] = False
            for harness in ("codex", "codex-native-ui", "opencode-native", "muse", "foreign"):
                result = asyncio.run(Host()._handle_model_options(types.SimpleNamespace(request_id="rejected", harness=harness)))
                self.assertEqual(result.status, "failed")
                self.assertEqual(result.models, [])
                self.assertEqual(result.routable_models, [])
            self.assertEqual(len(calls), original_count)

    def test_native_secure_storage_and_worker_overrides_survive_only_authorized_family(self):
        self.d["preferences"]["claudeConnection"] = "native"
        native = {"ANTHROPIC_API_KEY":"own-native", "CLAUDE_SECURESTORAGE_CONFIG_DIR":"/synthetic/secure"}
        with patch.dict(os.environ, {"KILO_OMNIGENT_NATIVE_CLAUDE_ENV":json.dumps(native), "KILO_OMNIGENT_CHILD_FAMILY":"claude", "HARNESS_CLAUDE_SDK_MODEL":"native/model", "HARNESS_CODEX_MODEL":"foreign/model"}, clear=True):
            runtime._private_environment(self.d)
            self.assertEqual(runtime._native_credentials(self.d,"claude"),native)
            self.assertEqual(os.environ["HARNESS_CLAUDE_SDK_MODEL"],"native/model")
            self.assertNotIn("HARNESS_CODEX_MODEL",os.environ)
            self.assertNotIn("ANTHROPIC_API_KEY",os.environ)
            self.assertNotIn("KILO_OMNIGENT_NATIVE_CLAUDE_ENV",os.environ)
            runtime._NATIVE_ENV.clear()
            with self.assertRaises(ValueError): runtime._native_credentials(self.d,"claude")

    def test_fork_entries_restore_payload_after_environment_replacement(self):
        owned = self.owned_socket_fixture()
        calls = []
        modules = []
        for name in ("omnigent.runner._entry", "omnigent.runtime.harnesses._runner"):
            module = types.ModuleType(name)
            def original(*args, _name=name, **kwargs):
                calls.append((_name, args, kwargs, dict(runtime._KILO_TOKENS)))
                return "original-result"
            module.main = original
            modules.append(module)
        descriptor = str(self.root / "runtime" / "descriptor.json")
        with patch.dict(sys.modules, {m.__name__: m for m in modules}), patch.object(runtime, "load_descriptor", return_value=self.d) as read:
            runtime._patch_fork_entries(self.d)
            for index, module in enumerate(modules):
                with self.subTest(entry=module.__name__), patch.object(runtime.os, "getpid", return_value=200 + index), patch.dict(os.environ, {"KILO_OMNIGENT_DESCRIPTOR": descriptor, "KILO_OMNIGENT_OWN_TMP": owned, "KILO_OMNIGENT_DISPATCH_TOKENS": json.dumps({"codex": "fork-codex", "claude": "fork-claude"})}, clear=True):
                    runtime._CREDENTIAL_PID = 100
                    runtime._KILO_TOKENS = {}
                    self.assertEqual(module.main(["--exact", "argument"], chosen=True), "original-result")
                    self.assertEqual(runtime._KILO_TOKENS, {"codex": "fork-codex", "claude": "fork-claude"})
                    self.assertNotIn("KILO_OMNIGENT_DISPATCH_TOKENS", os.environ)
                    self.assertEqual(runtime._CREDENTIAL_PID, 200 + index)
                    # Another named entry in the same process must retain its
                    # consumed payload rather than interpreting absence as reset.
                    self.assertEqual(module.main(), "original-result")
            self.assertEqual(read.call_count, 2)
        self.assertEqual(calls[0][1:3], ((["--exact", "argument"],), {"chosen": True}))
        self.assertEqual(len(calls), 4)

    def test_fork_refresh_revokes_stale_scopes_and_native_accounts(self):
        owned = self.owned_socket_fixture()
        self.d["preferences"]["claudeConnection"] = "native"
        descriptor = str(self.root / "runtime" / "descriptor.json")
        payload = {"KILO_OMNIGENT_DESCRIPTOR": descriptor, "KILO_OMNIGENT_OWN_TMP": owned, "KILO_OMNIGENT_DISPATCH_TOKENS": json.dumps({"codex": "first-codex"}), "KILO_OMNIGENT_NATIVE_CLAUDE_ENV": json.dumps({"ANTHROPIC_API_KEY": "first-native"})}
        with patch.object(runtime, "load_descriptor", return_value=self.d), patch.object(runtime.os, "getpid", return_value=201), patch.dict(os.environ, payload, clear=True):
            runtime._refresh_fork_credentials(self.d)
            self.assertEqual(runtime._kilo_token(self.d, "codex"), "first-codex")
            self.assertEqual(runtime._native_credentials(self.d, "claude"), {"ANTHROPIC_API_KEY": "first-native"})
            self.assertNotIn("KILO_OMNIGENT_NATIVE_CLAUDE_ENV", os.environ)
        with patch.object(runtime, "load_descriptor", return_value=self.d), patch.object(runtime.os, "getpid", return_value=202), patch.dict(os.environ, {"KILO_OMNIGENT_DESCRIPTOR": descriptor, "KILO_OMNIGENT_OWN_TMP": owned}, clear=True):
            runtime._refresh_fork_credentials(self.d)
            with self.assertRaises(ValueError): runtime._kilo_token(self.d, "codex")
            with self.assertRaises(ValueError): runtime._native_credentials(self.d, "claude")
            self.assertEqual(runtime._KILO_TOKENS, {})
            self.assertEqual(runtime._NATIVE_ENV, {})

    def test_fork_refresh_rejects_foreign_descriptor_before_original_entry(self):
        module = types.ModuleType("omnigent.runner._entry")
        harness = types.ModuleType("omnigent.runtime.harnesses._runner")
        called = []
        module.main = harness.main = lambda: called.append(True)
        with patch.dict(sys.modules, {m.__name__: m for m in (module, harness)}):
            runtime._patch_fork_entries(self.d)
            for path, changed in (("/foreign/descriptor.json", self.d), (str(self.root / "runtime" / "descriptor.json"), {**self.d, "fingerprint": "b" * 64})):
                runtime._CREDENTIAL_PID = 100
                runtime._KILO_TOKENS = {"codex": "stale"}
                runtime._NATIVE_ENV = {"claude": {"ANTHROPIC_API_KEY": "stale"}}
                with patch.object(runtime.os, "getpid", return_value=201), patch.object(runtime, "load_descriptor", return_value=changed), patch.dict(os.environ, {"KILO_OMNIGENT_DESCRIPTOR": path}, clear=True):
                    with self.assertRaises(ValueError): module.main()
                    self.assertEqual(runtime._KILO_TOKENS, {})
                    self.assertEqual(runtime._NATIVE_ENV, {})
        self.assertEqual(called, [])

    def test_direct_entry_keeps_already_consumed_destination_scope(self):
        owned = self.owned_socket_fixture()
        module = types.ModuleType("omnigent.runner._entry")
        harness = types.ModuleType("omnigent.runtime.harnesses._runner")
        module.main = harness.main = lambda: runtime._kilo_token(self.d, "codex")
        with patch.dict(sys.modules, {m.__name__: m for m in (module, harness)}), patch.dict(os.environ, {"KILO_OMNIGENT_CODEX_TOKEN": "own-direct-codex", "KILO_OMNIGENT_OWN_TMP": owned}, clear=True), patch.object(runtime, "load_descriptor") as read:
            runtime._private_environment(self.d)
            runtime._patch_fork_entries(self.d)
            self.assertEqual(module.main(), "own-direct-codex")
            self.assertEqual(harness.main(), "own-direct-codex")
            self.assertEqual(runtime._KILO_TOKENS, {"codex": "own-direct-codex"})
            read.assert_not_called()

    def test_fork_without_owned_socket_root_rejects_before_credentials_and_original_entry(self):
        module = types.ModuleType("omnigent.runner._entry")
        harness = types.ModuleType("omnigent.runtime.harnesses._runner")
        called = []
        module.main = harness.main = lambda: called.append(True)
        payload = {"KILO_OMNIGENT_DESCRIPTOR": str(self.root / "runtime" / "descriptor.json"), "KILO_OMNIGENT_DISPATCH_TOKENS": json.dumps({"codex": "not-consumed"})}
        with patch.dict(sys.modules, {m.__name__: m for m in (module, harness)}), patch.object(runtime, "load_descriptor", return_value=self.d), patch.object(runtime.os, "getpid", return_value=201), patch.dict(os.environ, payload, clear=True):
            runtime._patch_fork_entries(self.d)
            for entry in (module, harness):
                runtime._CREDENTIAL_PID = 100; runtime._KILO_TOKENS = {"codex": "stale"}
                with self.assertRaisesRegex(ValueError, "temporary root is required"): entry.main()
                self.assertEqual(runtime._KILO_TOKENS, {})
                self.assertIn("KILO_OMNIGENT_DISPATCH_TOKENS", os.environ)
        self.assertEqual(called, [])

    def test_install_without_owned_socket_root_rejects_before_vendor_or_credentials(self):
        with patch.dict(os.environ, {"KILO_OMNIGENT_CODEX_TOKEN": "not-consumed"}, clear=True), patch.object(runtime, "_ACTIVE", None), patch.object(runtime, "verify_sources") as verify, patch.object(runtime.importlib, "import_module") as imports:
            with self.assertRaisesRegex(ValueError, "temporary root is required"): runtime.install(self.d)
            verify.assert_not_called()
            imports.assert_not_called()
            self.assertEqual(runtime._KILO_TOKENS, {})
            self.assertEqual(os.environ["KILO_OMNIGENT_CODEX_TOKEN"], "not-consumed")

    @unittest.skipUnless(os.name == "posix", "The Omnigent runtime is POSIX-only")
    def test_short_owned_socket_root_ignores_long_tmpdir_and_binds_official_suffix(self):
        with patch.dict(os.environ, {"TMPDIR": str(self.root / ("long-" * 30))}):
            owned, identity = runtime._create_owned_socket_root(self.d)
        try:
            self.assertRegex(str(owned), r"^/tmp/ko-[a-z0-9_]{8}$")
            instance = owned / ("ap-" + "a" * 32)
            instance.mkdir(mode=0o700)
            path = instance / ("conv-" + "b" * 32 + ".sock")
            self.assertLessEqual(len(str(path).encode()), 103)
            self.assertEqual(len(str(path).encode()), 95)
            old = "/tmp/kilo-omnigent-" + "a" * 12 + "-" + "b" * 8 + "/ap-" + "a" * 32 + "/conv-" + "b" * 32 + ".sock"
            self.assertEqual(len(old.encode()), 119)
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
                with self.assertRaisesRegex(OSError, "AF_UNIX path too long"):
                    connection.bind(old)
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
                connection.bind(str(path))
            self.assertTrue(path.exists())
            with patch.dict(os.environ, {"KILO_OMNIGENT_OWN_TMP": str(owned), "TMPDIR": "/foreign/long", "OMNIGENT_HARNESS_TMP_PARENT": "/foreign"}, clear=True):
                runtime._configure_owned_socket_root(self.d)
                self.assertEqual(os.environ["TMPDIR"], str(owned))
                self.assertEqual(os.environ["OMNIGENT_HARNESS_TMP_PARENT"], str(owned))
                self.assertEqual(tempfile.tempdir, str(owned))
        finally:
            self.assertTrue(runtime._cleanup_owned_socket_root(self.d, identity))
        self.assertFalse(owned.exists())

    @unittest.skipUnless(os.name == "posix", "The Omnigent runtime is POSIX-only")
    def test_owned_socket_root_rejects_foreign_marker_permissions_and_symlinks(self):
        owned, identity = runtime._create_owned_socket_root(self.d)
        marker = owned / ".kilo-omnigent-owner.json"
        raw = marker.read_bytes()
        try:
            marker.write_text(json.dumps({"version": 1, "root": self.d["root"], "fingerprint": "foreign", "creatorPID": os.getpid()}))
            with self.assertRaises(ValueError): runtime._open_owned_socket_root(self.d, owned)
            self.assertFalse(runtime._cleanup_owned_socket_root(self.d, identity))
            self.assertTrue(owned.exists())
            marker.write_bytes(raw)
            for target, mode, restore in ((owned, 0o755, 0o700), (marker, 0o644, 0o600)):
                target.chmod(mode)
                with self.assertRaises(ValueError): runtime._open_owned_socket_root(self.d, owned)
                target.chmod(restore)
            with patch.object(runtime.os, "getuid", return_value=os.getuid() + 1):
                with self.assertRaises(ValueError): runtime._open_owned_socket_root(self.d, owned)
            link = self.root / "owned-link"
            link.symlink_to(owned)
            with self.assertRaises(ValueError): runtime._open_owned_socket_root(self.d, link)
            moved = owned.with_name(owned.name + "-saved")
            owned.rename(moved); owned.symlink_to(moved)
            with self.assertRaises(OSError): runtime._open_owned_socket_root(self.d, owned)
            owned.unlink(); moved.rename(owned)
            marker.rename(owned / ".original-marker")
            foreign_marker = self.root / "foreign-marker.json"
            foreign_marker.write_bytes(raw); foreign_marker.chmod(0o600)
            marker.symlink_to(foreign_marker)
            with self.assertRaises(OSError): runtime._open_owned_socket_root(self.d, owned)
            self.assertFalse(runtime._cleanup_owned_socket_root(self.d, identity))
            marker.unlink(); marker.write_bytes(raw); marker.chmod(0o600)
            # Marker replacement changes inode: cleanup must retain this root.
            self.assertFalse(runtime._cleanup_owned_socket_root(self.d, identity))
            fd, restored = runtime._open_owned_socket_root(self.d, owned); os.close(fd)
            self.assertTrue(runtime._cleanup_owned_socket_root(self.d, restored))
        finally:
            if owned.exists(): shutil.rmtree(owned)

    @unittest.skipUnless(os.name == "posix", "The Omnigent runtime is POSIX-only")
    def test_owned_socket_cleanup_preserves_sibling_and_symlink_target(self):
        owned, identity = runtime._create_owned_socket_root(self.d)
        sibling, sibling_identity = runtime._create_owned_socket_root(self.d)
        outside = self.root / "keep.txt"; outside.write_text("keep")
        (owned / "outside-link").symlink_to(outside)
        (owned / "nested").mkdir(); (owned / "nested" / "own.txt").write_text("own")
        try:
            self.assertTrue(runtime._cleanup_owned_socket_root(self.d, identity))
            self.assertFalse(owned.exists())
            self.assertTrue(sibling.exists())
            self.assertEqual(outside.read_text(), "keep")
        finally:
            self.assertTrue(runtime._cleanup_owned_socket_root(self.d, sibling_identity))

    @unittest.skipUnless(os.name == "posix", "The Omnigent runtime is POSIX-only")
    def test_owned_socket_cleanup_retains_live_endpoint_before_any_unlink(self):
        owned, identity = runtime._create_owned_socket_root(self.d)
        marker = owned / ".kilo-omnigent-owner.json"
        original_marker = marker.read_bytes()
        keep = owned / "keep.txt"; keep.write_text("retained")
        endpoint = owned / "owned.sock"
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            server.bind(str(endpoint)); server.listen(1)
            self.assertFalse(runtime._cleanup_owned_socket_root(self.d, identity))
            self.assertTrue(endpoint.exists())
            self.assertEqual(marker.read_bytes(), original_marker)
            self.assertEqual(keep.read_text(), "retained")
            fd, observed = runtime._open_owned_socket_root(self.d, owned); os.close(fd)
            self.assertEqual(observed, identity)
            server.close()
            # A stale pathname left by a closed listener is safe to remove.
            self.assertTrue(endpoint.exists())
            self.assertTrue(runtime._cleanup_owned_socket_root(self.d, identity))
            self.assertFalse(owned.exists())
        finally:
            server.close()
            if owned.exists(): shutil.rmtree(owned)

    @unittest.skipUnless(os.name == "posix", "The Omnigent runtime is POSIX-only")
    def test_owned_socket_cleanup_retains_uncertain_permissions_and_deadline(self):
        owned, identity = runtime._create_owned_socket_root(self.d)
        endpoint = owned / "owned.sock"
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        server.bind(str(endpoint)); server.close()
        marker = owned / ".kilo-omnigent-owner.json"; original_marker = marker.read_bytes()
        try:
            endpoint.chmod(0o666)
            self.assertFalse(runtime._cleanup_owned_socket_root(self.d, identity))
            endpoint.chmod(0o600)
            probe = unittest.mock.MagicMock()
            probe.__enter__.return_value = probe
            for failure in (TimeoutError("synthetic timeout"), PermissionError("synthetic denied"), OSError(runtime.errno.EPROTOTYPE, "synthetic protocol")):
                with self.subTest(error=type(failure).__name__):
                    probe.connect.side_effect = failure
                    with patch.object(runtime.socket, "socket", return_value=probe):
                        self.assertFalse(runtime._cleanup_owned_socket_root(self.d, identity))
                    self.assertEqual(marker.read_bytes(), original_marker)
                    probe.send.assert_not_called()
                    probe.sendall.assert_not_called()
            original_stat = runtime.os.stat
            def foreign_socket(name, *args, **kwargs):
                observed = original_stat(name, *args, **kwargs)
                if name == "owned.sock":
                    return types.SimpleNamespace(st_mode=observed.st_mode, st_uid=observed.st_uid + 1)
                return observed
            with patch.object(runtime.os, "stat", side_effect=foreign_socket), patch.object(runtime.socket, "socket") as probe:
                self.assertFalse(runtime._cleanup_owned_socket_root(self.d, identity))
                probe.assert_not_called()
            with patch.object(runtime.time, "monotonic", side_effect=[0, 1]):
                self.assertFalse(runtime._cleanup_owned_socket_root(self.d, identity))
            self.assertEqual(marker.read_bytes(), original_marker)
            self.assertTrue(endpoint.exists())
            self.assertTrue(runtime._cleanup_owned_socket_root(self.d, identity))
        finally:
            if owned.exists(): shutil.rmtree(owned)

    @unittest.skipUnless(os.name == "posix", "The Omnigent runtime is POSIX-only")
    def test_fork_revalidates_owned_socket_root_before_consuming_credentials(self):
        owned, identity = runtime._create_owned_socket_root(self.d)
        marker = owned / ".kilo-omnigent-owner.json"; raw = marker.read_bytes()
        payload = {"KILO_OMNIGENT_DESCRIPTOR": str(self.root / "runtime" / "descriptor.json"), "KILO_OMNIGENT_DISPATCH_TOKENS": json.dumps({"codex": "new-fork-scope"}), "KILO_OMNIGENT_OWN_TMP": str(owned), "TMPDIR": "/foreign"}
        try:
            marker.write_text(json.dumps({"version": 1, "root": "/foreign", "fingerprint": self.d["fingerprint"], "creatorPID": os.getpid()}))
            with patch.object(runtime, "load_descriptor", return_value=self.d), patch.object(runtime.os, "getpid", return_value=200), patch.dict(os.environ, payload, clear=True):
                runtime._CREDENTIAL_PID = 100; runtime._KILO_TOKENS = {"codex": "stale"}
                with self.assertRaises(ValueError): runtime._refresh_fork_credentials(self.d)
                self.assertEqual(runtime._KILO_TOKENS, {})
            marker.write_bytes(raw)
            with patch.object(runtime, "load_descriptor", return_value=self.d), patch.object(runtime.os, "getpid", return_value=200), patch.dict(os.environ, payload, clear=True):
                runtime._refresh_fork_credentials(self.d)
                self.assertEqual(runtime._KILO_TOKENS, {"codex": "new-fork-scope"})
                self.assertEqual(os.environ["TMPDIR"], str(owned))
                self.assertEqual(os.environ["OMNIGENT_HARNESS_TMP_PARENT"], str(owned))
                self.assertEqual(tempfile.tempdir, str(owned))
        finally:
            self.assertTrue(runtime._cleanup_owned_socket_root(self.d, identity))

    def test_supervisor_path_never_imports_vendor_before_control_record(self):
        calls=[]
        def supervise(d, path): calls.append("supervise")
        with patch.object(runtime,"load_descriptor",return_value=self.d), patch.object(runtime,"verify_sources",side_effect=lambda d:calls.append("verify")), patch.object(runtime,"_private_environment",side_effect=lambda d:calls.append("environment")), patch.object(runtime,"supervise",side_effect=supervise), patch.object(runtime,"install",side_effect=AssertionError("vendor import in supervisor")), patch.object(sys,"argv",[str(SOURCE),"--supervise","own.json"]):
            runtime.main()
        self.assertEqual(calls,["verify","environment","supervise"])

    def test_sdk_session_default_is_registered_before_destination_launch(self):
        workflow = types.ModuleType("omnigent.runtime.workflow")
        workflow._build_codex_spawn_env = lambda *a,**k:{}
        workflow._build_claude_sdk_spawn_env = lambda *a,**k:{}
        executor = types.ModuleType("omnigent.inner.claude_sdk_executor")
        calls=[]
        class Executor:
            async def run_turn(self, *args, **kwargs):
                yield None
            async def _route_options_through_gateway_shim(self, options):
                calls.append("shim"); options.env["ANTHROPIC_BASE_URL"]="http://127.0.0.1:54321"
            async def _get_or_create_client(self,sdk,**kwargs):
                calls.append("launch"); return kwargs["options"]
        executor.ClaudeSDKExecutor = Executor
        def register(d,session,model,effort):
            calls.append((session,model,effort));return "none"
        options=types.SimpleNamespace(env={},effort=None,settings=None)
        with patch.dict(sys.modules,{m.__name__:m for m in (workflow,executor)}), patch.dict(os.environ,{"KILO_OMNIGENT_CLAUDE_TOKEN":"synthetic-own"},clear=True), patch.object(runtime,"register_claude_effort",side_effect=register):
            runtime._patch_sdk_env(self.d)
            result=asyncio.run(Executor()._get_or_create_client(None,session_key="subagent-session",options=options,model="vendor/exact-b"))
        session="sdk_"+runtime.hashlib.sha256(b"subagent-session").hexdigest()
        self.assertEqual(calls,[(session,"vendor/exact-b",None),"shim","launch"])
        self.assertIsNone(result.effort)
        self.assertEqual(result.env["ANTHROPIC_CUSTOM_HEADERS"],"X-Kilo-Omnigent-Session: "+session)
        settings=json.loads(result.settings)
        self.assertEqual(settings["env"]["ANTHROPIC_BASE_URL"],"http://127.0.0.1:54321")
        self.assertNotIn("synthetic-own",settings["apiKeyHelper"])
        self.assertNotIn("KILO_OMNIGENT_CODEX_TOKEN",result.env)

    def runner_effort_fixture(self, text=None):
        source = self.root / "source" / "runner" / "app.py"
        source.parent.mkdir(parents=True, exist_ok=True)
        text = text or ("def admit(harness, effort):\n"
                        "    from omnigent.util.reasoning_effort import efforts_for_harness, format_supported\n"
                        "    supported = efforts_for_harness(harness)\n"
                        "    return supported is None or effort in supported\n"
                        "def native_dispatch_fixture():\n"
                        "    async def dispatch(harness, model, callback, Response):\n"
                        "        if True:\n"
                        "            if True:\n"
                        "                if model is not None and not isinstance(model, str):\n"
                        "                    return Response(status_code=400)\n"
                        "                if harness == \"codex-native\":\n"
                        "                    if model is None or not model.strip():\n"
                        "                        return Response(status_code=204)\n"
                        "                    settings: _JsonObject = {\"model\": model.strip()}\n"
                        "                    return await callback(\"a\", settings)\n"
                        "                return (harness, model)\n"
                        "    return dispatch\n")
        source.write_text(text)
        self.d.update(sourceRoot=str(source.parent.parent), sourceHashes={"runner/app.py": runtime.hashlib.sha256(source.read_bytes()).hexdigest()})
        original_path = list(sys.meta_path)
        self.addCleanup(lambda: sys.meta_path.__setitem__(slice(None), original_path))
        runtime._patch_private_runner_efforts(self.d)
        return source, sys.meta_path[0]

    def test_private_runner_effort_loader_is_exact_and_leaves_vendor_file_unchanged(self):
        from importlib.machinery import SourceFileLoader
        source, finder = self.runner_effort_fixture()
        original = source.read_bytes()
        name = "omnigent.runner.app"
        spec = importlib.util.spec_from_file_location(name, source)
        utility = types.ModuleType("omnigent.util.reasoning_effort")
        claude = frozenset({"low", "high"})
        codex = frozenset({"low", "high"})
        utility.efforts_for_harness = lambda harness: claude if harness in ("claude", "claude-sdk", "claude-native") else codex if harness == "codex" else None
        utility.format_supported = lambda levels: ",".join(levels)
        original_utility = utility.efforts_for_harness
        with patch("importlib.machinery.PathFinder.find_spec", return_value=spec), patch.dict(sys.modules, {utility.__name__: utility}):
            transformed = finder.find_spec(name, [str(source.parent)])
            self.assertIs(transformed, spec)
            self.assertEqual(transformed.origin, str(source))
            self.assertIsInstance(transformed.loader, SourceFileLoader)
            module = importlib.util.module_from_spec(transformed)
            transformed.loader.exec_module(module)
            self.assertTrue(module.admit("claude-sdk", "none"))
            self.assertTrue(module.admit("claude", "none"))
            self.assertFalse(module.admit("claude-native", "none"))
            self.assertFalse(module.admit("codex", "none"))
            self.assertTrue(module.admit("foreign", "none"))  # Original unrestricted result.
            self.d["preferences"]["claudeConnection"] = "native"
            self.assertFalse(module.admit("claude-sdk", "none"))
            self.d["preferences"]["claudeConnection"] = "kilo"
            self.d["preferences"]["claude"] = False
            self.assertFalse(module.admit("claude-sdk", "none"))
            self.assertIs(utility.efforts_for_harness, original_utility)
            self.assertNotIn("none", utility.efforts_for_harness("claude-sdk"))
            self.assertEqual(module.admit.__code__.co_filename, str(source))
        self.assertEqual(source.read_bytes(), original)
        self.assertIsNone(finder.find_spec("omnigent.runner.other"))

    def test_private_runner_effort_loader_rejects_changed_source_and_unexpected_loader(self):
        from importlib.machinery import ModuleSpec, SourceFileLoader
        source, finder = self.runner_effort_fixture()
        name = "omnigent.runner.app"
        for origin, loader in ((str(source.parent / "other.py"), SourceFileLoader(name, str(source))), (str(source), object())):
            with self.subTest(origin=origin, loader=type(loader).__name__):
                spec = ModuleSpec(name, loader, origin=origin)
                with patch("importlib.machinery.PathFinder.find_spec", return_value=spec):
                    with self.assertRaisesRegex(ValueError, "loader changed"):
                        finder.find_spec(name)
        with patch("importlib.machinery.PathFinder.find_spec", return_value=importlib.util.spec_from_file_location(name, source)):
            loader = finder.find_spec(name).loader
        for text, update_hash in (("def changed(): pass\n", False), ("def missing(): pass\n", True), (source.read_text() + source.read_text().replace("admit", "admit_again"), True)):
            with self.subTest(update_hash=update_hash, length=len(text)):
                source.write_text(text)
                if update_hash:
                    self.d["sourceHashes"]["runner/app.py"] = runtime.hashlib.sha256(source.read_bytes()).hexdigest()
                with self.assertRaisesRegex(ValueError, "contract changed"):
                    loader.get_code(name)
        with patch.dict(sys.modules, {name: types.ModuleType(name)}):
            with self.assertRaisesRegex(ValueError, "imported before"):
                runtime._patch_private_runner_efforts(self.d)

    def test_private_runner_native_codex_reset_reaches_settings_before_original_early_return(self):
        plain = {"id": "vendor/nested/plain", "name": "Plain", "reasoningLevels": [], "defaultReasoning": ""}
        self.d["modelLibraries"]["codex"]["models"].append(plain)
        source, finder = self.runner_effort_fixture()
        original_source = source.read_bytes()
        name = "omnigent.runner.app"
        spec = importlib.util.spec_from_file_location(name, source)
        with patch("importlib.machinery.PathFinder.find_spec", return_value=spec):
            transformed = finder.find_spec(name)
        module = importlib.util.module_from_spec(transformed)
        transformed.loader.exec_module(module)
        dispatch = module.native_dispatch_fixture()
        modules, controls_module, fetch, selected, calls, _, response, _ = self.native_controls_fixture()
        selected["a"] = plain["id"]
        Response = modules["starlette.responses"].Response

        async def run():
            controls = controls_module.build_native_controls(_fetch_session_model_override=fetch)
            callback = controls.handle_codex_native_settings_update
            for value in (None, "", " \t\n"):
                with self.subTest(reset=value):
                    calls.clear()
                    self.assertIs(await dispatch("codex-native", value, callback, Response), response)
                    self.assertEqual(calls, [("codex", "a", {"model": "openai/exact-a", "effort": "high"}, {})])
            calls.clear()
            self.assertIs(await dispatch("codex-native", plain["id"], callback, Response), response)
            self.assertEqual(calls, [("codex", "a", {"model": plain["id"], "effort": "none"}, {})])
            calls.clear()
            with self.assertRaises(ValueError):
                await dispatch("codex-native", "foreign/model", callback, Response)
            self.assertEqual(calls, [])
            for value in (42, [], {}):
                with self.subTest(invalid_type=type(value).__name__):
                    self.assertEqual((await dispatch("codex-native", value, callback, Response)).status_code, 400)
                    self.assertEqual(calls, [])
            self.assertEqual(await dispatch("claude-native", None, callback, Response), ("claude-native", None))
            self.assertEqual(calls, [])
            self.d["preferences"]["codexConnection"] = "native"
            for value in (None, "", " \t\n"):
                self.assertEqual((await dispatch("codex-native", value, callback, Response)).status_code, 204)
                self.assertEqual(calls, [])
            self.assertIs(await dispatch("codex-native", "native/model", callback, Response), response)
            self.assertEqual(calls[-1], ("codex", "a", {"model": "native/model"}, {}))
            self.d["preferences"].update(codex=False, codexConnection="kilo")
            calls.clear()
            self.assertEqual((await dispatch("codex-native", None, callback, Response)).status_code, 204)
            self.assertEqual(calls, [])

        with patch.dict(sys.modules, modules):
            runtime._patch_native_controls(self.d)
            asyncio.run(run())
        self.assertEqual(source.read_bytes(), original_source)

    def test_private_runner_loader_requires_unique_native_reset_anchor(self):
        source, finder = self.runner_effort_fixture()
        name = "omnigent.runner.app"
        with patch("importlib.machinery.PathFinder.find_spec", return_value=importlib.util.spec_from_file_location(name, source)):
            loader = finder.find_spec(name).loader
        original = source.read_text()
        extra = original[original.index("def native_dispatch_fixture"):].replace("native_dispatch_fixture", "native_dispatch_again", 1)
        for text in (original.replace('if harness == "codex-native":', 'if harness == "other-native":', 1), original + extra):
            with self.subTest(unique_native_reset=False):
                source.write_text(text)
                self.d["sourceHashes"]["runner/app.py"] = runtime.hashlib.sha256(source.read_bytes()).hexdigest()
                with self.assertRaisesRegex(ValueError, "contract changed"):
                    loader.get_code(name)

    def sdk_turn_fixture(self, *, client_session=None, client_model=None, fail=False, pause=None):
        workflow = types.ModuleType("omnigent.runtime.workflow")
        workflow._build_codex_spawn_env = lambda *a, **k: {}
        workflow._build_claude_sdk_spawn_env = lambda *a, **k: {}
        executor = types.ModuleType("omnigent.inner.claude_sdk_executor")
        @dataclasses.dataclass
        class Config:
            model: str = None
            extra: dict = dataclasses.field(default_factory=dict)
        observed = {"configs": [], "registrations": [], "finally": []}
        class Executor:
            _model_override = "vendor/exact-b"
            def _session_key(self, messages): return messages[0]
            async def run_turn(self, messages, tools, system_prompt, config=None):
                observed["configs"].append(config)
                try:
                    if pause is not None: await pause(messages[0])
                    cfg = config or Config()
                    # This models the unchanged official SDK enum boundary.
                    self_check = cfg.extra.get("reasoning_effort")
                    if self_check is not None and self_check not in {"low", "medium", "high", "xhigh", "max"}:
                        raise ValueError("synthetic official enum rejected")
                    options = types.SimpleNamespace(effort=self_check, env={}, settings=None)
                    await self._get_or_create_client(None, session_key=client_session or messages[0], options=options, model=client_model or cfg.model or self._model_override)
                    if fail: raise RuntimeError("synthetic original failure")
                    yield options
                    yield "done"
                finally:
                    observed["finally"].append(messages[0])
            async def _route_options_through_gateway_shim(self, options):
                options.env["ANTHROPIC_BASE_URL"] = "http://127.0.0.1:54321"
            async def _get_or_create_client(self, sdk, **kwargs): return kwargs["options"]
        executor.ExecutorConfig = Config
        executor.ClaudeSDKExecutor = Executor
        def register(d, session, model, effort):
            selected = runtime.effort_policy(d, "claude-sdk", model, effort)
            observed["registrations"].append((session, model, selected))
            return selected
        modules = {m.__name__: m for m in (workflow, executor)}
        return modules, Executor, Config, observed, register

    def test_sdk_explicit_none_survives_enum_filter_without_mutating_request(self):
        row = self.d["modelLibraries"]["claude"]["models"][0]
        row.update(reasoningLevels=["none", "low", "high"], defaultReasoning="low")
        modules, Executor, Config, observed, register = self.sdk_turn_fixture()
        async def run(config): return [event async for event in Executor().run_turn(["conversation-a"], [], "prompt", config)]
        with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register), patch.dict(os.environ, {"KILO_OMNIGENT_CLAUDE_TOKEN": "synthetic-own"}, clear=True):
            runtime._patch_sdk_env(self.d)
            for requested, expected, model in (("none", "none", "vendor/exact-b"), ("high", "high", "vendor/exact-b"), (None, "low", "vendor/exact-b"), ("reset", "low", "vendor/exact-b"), (None, None, "vendor/exact-c"), ("none", None, "vendor/exact-c"), ("high", None, "vendor/exact-c")):
                cfg = Config(model=model, extra={"reasoning_effort": requested, "preserved": 7})
                result = asyncio.run(run(cfg))
                self.assertEqual(result[1], "done")
                self.assertEqual(observed["registrations"][-1][1:], (model, expected))
                self.assertEqual(cfg.extra, {"reasoning_effort": requested, "preserved": 7})
                self.assertIsNot(observed["configs"][-1], cfg)
                self.assertEqual(observed["configs"][-1].extra.get("reasoning_effort"), expected if expected in ("low", "high") else None)
                self.assertIsNone(runtime._SDK_TURN.get())
            self.assertEqual(asyncio.run(run(None))[1], "done")
            self.assertEqual(observed["registrations"][-1][1:], ("vendor/exact-b", "low"))
            for cfg in (Config(model="foreign/model", extra={"reasoning_effort": "none"}), Config(model="vendor/exact-b", extra={"reasoning_effort": "max"})):
                before = len(observed["configs"])
                with self.assertRaises(ValueError): asyncio.run(run(cfg))
                self.assertEqual(len(observed["configs"]), before)
                self.assertIsNone(runtime._SDK_TURN.get())

    def test_sdk_turn_context_is_parallel_and_bound_to_session_and_model(self):
        row = self.d["modelLibraries"]["claude"]["models"][0]
        row.update(reasoningLevels=["none", "high"], defaultReasoning="high")
        async def pause(session): await asyncio.sleep(0)
        modules, Executor, Config, observed, register = self.sdk_turn_fixture(pause=pause)
        async def consume(session, effort):
            events = [event async for event in Executor().run_turn([session], [], "", Config(extra={"reasoning_effort": effort}))]
            self.assertIsNone(runtime._SDK_TURN.get())
            return events
        async def parallel(): return await asyncio.gather(consume("a", "none"), consume("b", "high"))
        with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register), patch.dict(os.environ, {"KILO_OMNIGENT_CLAUDE_TOKEN": "synthetic-own"}, clear=True):
            runtime._patch_sdk_env(self.d)
            results = asyncio.run(parallel())
        self.assertEqual([events[1] for events in results], ["done", "done"])
        self.assertEqual(set(observed["registrations"]), {("sdk_" + runtime.hashlib.sha256(s.encode()).hexdigest(), "vendor/exact-b", effort) for s, effort in (("a", "none"), ("b", "high"))})
        for session, model in (("foreign-session", None), (None, "vendor/exact-c")):
            modules, Executor, Config, observed, register = self.sdk_turn_fixture(client_session=session, client_model=model)
            async def mismatch(): return [event async for event in Executor().run_turn(["a"], [], "", Config(extra={"reasoning_effort": "none"}))]
            with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register):
                runtime._patch_sdk_env(self.d)
                with self.assertRaisesRegex(ValueError, "session and model"): asyncio.run(mismatch())
            self.assertEqual(observed["registrations"], [])
            self.assertIsNone(runtime._SDK_TURN.get())

    def test_sdk_turn_context_resets_on_failure_and_native_preserves_original_config(self):
        modules, Executor, Config, observed, register = self.sdk_turn_fixture(fail=True)
        async def consume():
            try:
                return [event async for event in Executor().run_turn(["a"], [], "", Config(extra={"reasoning_effort": "none"}))]
            finally:
                self.assertIsNone(runtime._SDK_TURN.get())
        with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register), patch.dict(os.environ, {"KILO_OMNIGENT_CLAUDE_TOKEN": "synthetic-own"}, clear=True):
            runtime._patch_sdk_env(self.d)
            with self.assertRaisesRegex(RuntimeError, "original failure"): asyncio.run(consume())
        self.assertIsNone(runtime._SDK_TURN.get())
        self.assertEqual(observed["finally"], ["a"])
        self.d["preferences"]["claudeConnection"] = "native"
        runtime._NATIVE_ENV["claude"] = {}
        modules, Executor, Config, observed, register = self.sdk_turn_fixture()
        cfg = Config(model="native/model", extra={"reasoning_effort": "max"})
        async def native(): return [event async for event in Executor().run_turn(["a"], [], "", cfg)]
        with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=AssertionError("native registration")):
            runtime._patch_sdk_env(self.d)
            self.assertEqual(asyncio.run(native())[1], "done")
        self.assertIs(observed["configs"][0], cfg)
        self.assertIsNone(runtime._SDK_TURN.get())

    def test_sdk_turn_context_closes_original_generator_and_resets_on_cancellation(self):
        modules, Executor, Config, observed, register = self.sdk_turn_fixture()
        async def close_early():
            stream = Executor().run_turn(["close"], [], "", Config(extra={"reasoning_effort": "none"}))
            await stream.__anext__()
            self.assertIsNone(runtime._SDK_TURN.get())
            await stream.aclose()
            self.assertIsNone(runtime._SDK_TURN.get())
            self.assertEqual(observed["finally"], ["close"])
        with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register), patch.dict(os.environ, {"KILO_OMNIGENT_CLAUDE_TOKEN": "synthetic-own"}, clear=True):
            runtime._patch_sdk_env(self.d)
            asyncio.run(close_early())
        async def cancelled():
            started = asyncio.Event()
            async def pause(session):
                started.set()
                await asyncio.Future()
            modules, Executor, Config, observed, register = self.sdk_turn_fixture(pause=pause)
            async def consume():
                try:
                    return [event async for event in Executor().run_turn(["cancel"], [], "", Config(extra={"reasoning_effort": "none"}))]
                finally:
                    self.assertIsNone(runtime._SDK_TURN.get())
            with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register):
                runtime._patch_sdk_env(self.d)
                task = asyncio.create_task(consume())
                await started.wait()
                task.cancel()
                with self.assertRaises(asyncio.CancelledError): await task
                self.assertEqual(observed["finally"], ["cancel"])
                self.assertEqual(observed["registrations"], [])
        asyncio.run(cancelled())

    def test_sdk_turn_context_can_advance_and_close_in_distinct_consumer_contexts(self):
        for advance_twice in (False, True):
            with self.subTest(advance_twice=advance_twice):
                modules, Executor, Config, observed, register = self.sdk_turn_fixture()
                async def cross_context():
                    stream = Executor().run_turn(["cross-context"], [], "", Config(extra={"reasoning_effort": "none"}))
                    consumers = []
                    async def advance(label):
                        outer = {"consumer": label}
                        token = runtime._SDK_TURN.set(outer)
                        try:
                            event = await stream.__anext__()
                            consumers.append(runtime._SDK_TURN.get())
                            return event
                        finally:
                            runtime._SDK_TURN.reset(token)
                    async def close():
                        outer = {"consumer": "close"}
                        token = runtime._SDK_TURN.set(outer)
                        try:
                            await stream.aclose()
                            consumers.append(runtime._SDK_TURN.get())
                        finally:
                            runtime._SDK_TURN.reset(token)
                    await asyncio.create_task(advance("first"))
                    if advance_twice:
                        self.assertEqual(await asyncio.create_task(advance("second")), "done")
                    await asyncio.create_task(close())
                    self.assertEqual(consumers, [{"consumer": label} for label in (["first", "second", "close"] if advance_twice else ["first", "close"])])
                    self.assertIsNone(runtime._SDK_TURN.get())
                with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register), patch.dict(os.environ, {"KILO_OMNIGENT_CLAUDE_TOKEN": "synthetic-own"}, clear=True):
                    runtime._patch_sdk_env(self.d)
                    asyncio.run(cross_context())
                self.assertEqual(observed["finally"], ["cross-context"])
                self.assertEqual(observed["registrations"], [("sdk_" + runtime.hashlib.sha256(b"cross-context").hexdigest(), "vendor/exact-b", "none")])

    def gateway_disconnect_fixture(self):
        module = types.ModuleType("omnigent.inner.claude_gateway_shim")
        class Shim:
            def __init__(self, base="http://127.0.0.1:8877/omnigent/claude"):
                self._upstream_base_url = base
                self.entered = asyncio.Event()
                self.release = asyncio.Event()
                self.closed = 0
                self.body = None
            async def _asgi_app(self, scope, receive, send):
                if scope["type"] != "http": return
                body = bytearray()
                while True:
                    message = await receive()
                    body.extend(message.get("body", b""))
                    if not message.get("more_body", False): break
                self.body = bytes(body)
                self.entered.set()
                try:
                    await send({"type": "http.response.start", "status": 201, "headers": [(b"content-type", b"text/event-stream")]})
                    await send({"type": "http.response.body", "body": b"first\x00chunk", "more_body": True})
                    await self.release.wait()
                    await send({"type": "http.response.body", "body": b"last\xffchunk", "more_body": True})
                    await send({"type": "http.response.body", "body": b""})
                finally:
                    self.closed += 1
        module.ClaudeGatewayShim = Shim
        return module, Shim

    def native_controls_fixture(self, *, model_status=204, effort_status=204, model_hook=None):
        controls = types.ModuleType("omnigent.runner.native_controls")
        responses = types.ModuleType("starlette.responses")
        calls = []
        selected = {}
        class Response:
            def __init__(self, status_code): self.status_code = status_code
        responses.Response = Response
        model_response, effort_response = Response(model_status), Response(effort_status)
        async def model(conv, value):
            calls.append(("model", conv, value))
            return await model_hook(conv, value) if model_hook is not None else model_response
        async def effort(conv, value): calls.append(("effort", conv, value)); return effort_response
        async def codex_settings(conv, settings, **options): calls.append(("codex", conv, settings, options)); return model_response
        async def unused(*args, **kwargs): raise AssertionError("unrelated native control")
        @dataclasses.dataclass
        class Controls:
            codex_native_model_options: object = unused
            handle_codex_native_settings_update: object = codex_settings
            handle_claude_native_model_change: object = model
            handle_claude_native_effort_change: object = effort
        controls.build_native_controls = lambda **kwargs: Controls()
        async def fetch(conv): return selected[conv]
        def register(d, conv, model, requested):
            resolved = runtime.effort_policy(d, "claude-native", model, requested)
            calls.append(("register", conv, model, resolved))
            register.selections[conv, model] = resolved
            return resolved
        register.selections = {}
        modules = {m.__name__: m for m in (controls, responses)}
        return modules, controls, fetch, selected, calls, register, model_response, effort_response

    def test_native_codex_empty_ladder_clears_rpc_without_advertising_or_accepting_extra_levels(self):
        plain = {"id": "vendor/nested/plain", "name": "Plain", "reasoningLevels": [], "defaultReasoning": ""}
        self.d["modelLibraries"]["codex"]["models"].append(plain)
        before = copy.deepcopy(self.d["modelLibraries"]["codex"])
        modules, module, fetch, selected, calls, register, response, _ = self.native_controls_fixture()
        async def run():
            controls = module.build_native_controls(_fetch_session_model_override=fetch, _session_reasoning_effort={"a": "high"})
            selected["a"] = plain["id"]
            for request, expected in (({"model": plain["id"]}, {"model": plain["id"], "effort": "none"}),
                                      ({"effort": None}, {"effort": "none"}),
                                      ({"model": "openai/exact-a"}, {"model": "openai/exact-a", "effort": "high"}),
                                      ({"model": "openai/exact-a", "effort": "low"}, {"model": "openai/exact-a", "effort": "low"})):
                with self.subTest(request=request):
                    untouched = copy.deepcopy(request); calls.clear()
                    self.assertIs(await controls.handle_codex_native_settings_update("a", request, legacy_server=True), response)
                    self.assertEqual(calls, [("codex", "a", expected, {"legacy_server": True})])
                    self.assertEqual(request, untouched)
                    self.assertIsNot(calls[0][2], request)
            calls.clear()
            for request in ({"model": plain["id"], "effort": "high"}, {"effort": "high"}, {"model": "foreign/model"}, {"model": "openai/exact-a", "effort": "none"}):
                with self.subTest(invalid=request):
                    with self.assertRaises(ValueError): await controls.handle_codex_native_settings_update("a", request)
                    self.assertEqual(calls, [])
            self.d["preferences"]["codexConnection"] = "native"
            request = {"model": "native/model", "effort": "native-level"}
            self.assertIs(await controls.handle_codex_native_settings_update("a", request, legacy_server=False), response)
            self.assertIs(calls[-1][2], request)
            self.assertEqual(calls[-1][3], {"legacy_server": False})
            self.d["preferences"].update(codex=False, codexConnection="kilo")
            calls.clear()
            with self.assertRaises(ValueError): await controls.handle_codex_native_settings_update("a", {"model": plain["id"]})
            self.assertEqual(calls, [])
        with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=AssertionError("Codex must not register Claude effort")):
            runtime._patch_native_controls(self.d)
            asyncio.run(run())
        self.assertEqual(self.d["modelLibraries"]["codex"], before)
        self.d["preferences"].update(codex=True, codexConnection="kilo")
        advertised = runtime.model_rows(self.d, "codex-native")[-1]
        self.assertEqual(advertised["supportedReasoningEfforts"], [])
        self.assertEqual(advertised["defaultReasoningEffort"], "")

    def test_native_claude_ordered_selection_keeps_effort_bound_to_current_model_and_session(self):
        rows = self.d["modelLibraries"]["claude"]["models"]
        rows[0].update(reasoningLevels=["none", "low", "high"], defaultReasoning="low")
        rows.append({"id": "vendor/high-default", "name": "High", "reasoningLevels": ["none", "low", "high"], "defaultReasoning": "high"})
        modules, module, fetch, selected, calls, register, _, _ = self.native_controls_fixture()
        async def run():
            controls = module.build_native_controls(_fetch_session_model_override=fetch, _session_reasoning_effort={"a": "low", "b": "none"})
            selected["a"] = "vendor/exact-b"
            # The official combined PATCH forwards effort before model.
            await controls.handle_claude_native_effort_change("a", "none")
            await controls.handle_claude_native_model_change("a", selected["a"])
            self.assertEqual(calls, [("register", "a", "vendor/exact-b", "none"), ("register", "a", "vendor/exact-b", "none"), ("model", "a", "vendor/exact-b")])
            calls.clear()
            selected["a"] = "vendor/high-default"
            await controls.handle_claude_native_model_change("a", selected["a"])
            self.assertEqual(calls, [("register", "a", "vendor/high-default", "high"), ("model", "a", "vendor/high-default"), ("effort", "a", "high")])
            calls.clear()
            selected["a"] = "vendor/exact-b"
            await controls.handle_claude_native_model_change("a", selected["a"])
            self.assertEqual(calls[0], ("register", "a", "vendor/exact-b", "low"))
            for model, requested, expected in (("vendor/high-default", "none", "none"), ("vendor/high-default", "low", "low"),
                                               ("vendor/high-default", "high", "high"), ("vendor/exact-b", None, "low"),
                                               ("vendor/exact-c", None, None)):
                with self.subTest(model=model, requested=requested):
                    calls.clear(); selected["a"] = model
                    await controls.handle_claude_native_effort_change("a", requested)
                    await controls.handle_claude_native_model_change("a", model)
                    self.assertEqual([c for c in calls if c[0] == "register"], [("register", "a", model, expected)] * 2)
                    self.assertEqual([c for c in calls if c[0] == "effort"], [("effort", "a", expected)] * 2 if expected in ("low", "high") else [])
            calls.clear(); selected["b"] = "vendor/exact-b"
            await controls.handle_claude_native_model_change("b", selected["b"])
            self.assertEqual(calls[0], ("register", "b", "vendor/exact-b", "low"))
            calls.clear(); selected["a"] = "vendor/exact-b"
            with self.assertRaises(ValueError): await controls.handle_claude_native_effort_change("a", "max")
            with self.assertRaises(ValueError): await controls.handle_claude_native_model_change("a", "foreign/model")
            self.assertEqual(calls, [])
        with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register):
            runtime._patch_native_controls(self.d)
            asyncio.run(run())

    def test_native_claude_new_empty_ladder_effort_rejects_before_registration(self):
        modules, module, fetch, selected, calls, register, _, _ = self.native_controls_fixture()
        async def run():
            controls = module.build_native_controls(_fetch_session_model_override=fetch, _session_reasoning_effort={"a": "high"})
            selected["a"] = "vendor/exact-c"
            for requested in ("none", "high"):
                with self.subTest(requested=requested):
                    with self.assertRaises(ValueError): await controls.handle_claude_native_effort_change("a", requested)
                    self.assertEqual(calls, [])
            response = await controls.handle_claude_native_effort_change("a", None)
            self.assertEqual(response.status_code, 204)
            self.assertEqual(calls, [("register", "a", "vendor/exact-c", None)])
            calls.clear()
            await controls.handle_claude_native_model_change("a", "vendor/exact-c")
            self.assertEqual(calls, [("register", "a", "vendor/exact-c", None), ("model", "a", "vendor/exact-c")])
        with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register):
            runtime._patch_native_controls(self.d)
            asyncio.run(run())

    def test_native_claude_refused_model_restores_pending_candidate_but_keeps_confirmed_same_model_effort(self):
        rows = self.d["modelLibraries"]["claude"]["models"]
        rows[0].update(reasoningLevels=["none", "low", "high"], defaultReasoning="low")
        rows.append({"id": "vendor/high-default", "name": "High", "reasoningLevels": ["none", "low", "high"], "defaultReasoning": "high"})
        modules, module, fetch, selected, calls, register, model_response, effort_response = self.native_controls_fixture()
        async def run():
            controls = module.build_native_controls(_fetch_session_model_override=fetch, _session_reasoning_effort={})
            selected["a"] = "vendor/exact-b"
            await controls.handle_claude_native_effort_change("a", "none")
            await controls.handle_claude_native_model_change("a", selected["a"])
            for combined in (False, True):
                with self.subTest(combined=combined):
                    selected["a"] = "vendor/high-default"
                    if combined: await controls.handle_claude_native_effort_change("a", "none")
                    model_response.status_code = 503
                    self.assertIs(await controls.handle_claude_native_model_change("a", selected["a"]), model_response)
                    self.assertEqual(register.selections["a", "vendor/exact-b"], "none")
                    selected["a"] = "vendor/exact-b"; model_response.status_code = 204; calls.clear()
                    await controls.handle_claude_native_model_change("a", selected["a"])
                    self.assertEqual(calls[0], ("register", "a", "vendor/exact-b", "none"))
            selected["a"] = "vendor/high-default"; calls.clear()
            await controls.handle_claude_native_model_change("a", selected["a"])
            self.assertEqual(calls[0], ("register", "a", "vendor/high-default", "high"))
            # A confirmed effort on the unchanged model survives /model refusal.
            await controls.handle_claude_native_effort_change("a", "none")
            model_response.status_code = 503
            await controls.handle_claude_native_model_change("a", selected["a"])
            model_response.status_code = 204; calls.clear()
            await controls.handle_claude_native_model_change("a", selected["a"])
            self.assertEqual(calls[0], ("register", "a", "vendor/high-default", "none"))
            effort_response.status_code = 503
            self.assertIs(await controls.handle_claude_native_effort_change("a", "high"), effort_response)
            effort_response.status_code = 204; calls.clear()
            await controls.handle_claude_native_model_change("a", selected["a"])
            self.assertEqual(calls[0], ("register", "a", "vendor/high-default", "none"))
        with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register):
            runtime._patch_native_controls(self.d)
            asyncio.run(run())

    def test_native_claude_late_refusal_does_not_overwrite_a_newer_selection(self):
        rows = self.d["modelLibraries"]["claude"]["models"]
        rows[0].update(reasoningLevels=["none", "low", "high"], defaultReasoning="low")
        rows.append({"id": "vendor/high-default", "name": "High", "reasoningLevels": ["none", "low", "high"], "defaultReasoning": "high"})
        async def run():
            started, release = asyncio.Event(), asyncio.Event()
            async def model_hook(conv, model):
                if model == "vendor/high-default":
                    started.set(); await release.wait()
                    return types.SimpleNamespace(status_code=503)
                return types.SimpleNamespace(status_code=204)
            modules, module, fetch, selected, calls, register, _, _ = self.native_controls_fixture(model_hook=model_hook)
            with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register):
                runtime._patch_native_controls(self.d)
                controls = module.build_native_controls(_fetch_session_model_override=fetch, _session_reasoning_effort={})
                selected["a"] = "vendor/exact-b"
                await controls.handle_claude_native_effort_change("a", "none")
                await controls.handle_claude_native_model_change("a", selected["a"])
                selected["a"] = "vendor/high-default"
                await controls.handle_claude_native_effort_change("a", "none")
                old = asyncio.create_task(controls.handle_claude_native_model_change("a", selected["a"]))
                await started.wait()
                selected["a"] = "vendor/exact-b"
                await controls.handle_claude_native_effort_change("a", "high")
                await controls.handle_claude_native_model_change("a", selected["a"])
                release.set()
                self.assertEqual((await old).status_code, 503)
                calls.clear()
                await controls.handle_claude_native_model_change("a", selected["a"])
                self.assertEqual(calls[0], ("register", "a", "vendor/exact-b", "high"))
        asyncio.run(run())

    def test_native_claude_model_exception_restores_only_pending_marker_and_preserves_exception(self):
        rows = self.d["modelLibraries"]["claude"]["models"]
        rows[0].update(reasoningLevels=["none", "low", "high"], defaultReasoning="low")
        rows.append({"id": "vendor/high-default", "name": "High", "reasoningLevels": ["none", "low", "high"], "defaultReasoning": "high"})
        for error in (TimeoutError("synthetic timeout"), asyncio.CancelledError()):
            with self.subTest(error=type(error).__name__):
                async def model_hook(conv, model):
                    if model == "vendor/high-default": raise error
                    return types.SimpleNamespace(status_code=204)
                modules, module, fetch, selected, calls, register, _, _ = self.native_controls_fixture(model_hook=model_hook)
                async def run():
                    controls = module.build_native_controls(_fetch_session_model_override=fetch, _session_reasoning_effort={})
                    selected["a"] = "vendor/exact-b"
                    await controls.handle_claude_native_effort_change("a", "none")
                    await controls.handle_claude_native_model_change("a", selected["a"])
                    selected["a"] = "vendor/high-default"
                    await controls.handle_claude_native_effort_change("a", "none")
                    try:
                        await controls.handle_claude_native_model_change("a", selected["a"])
                    except BaseException as caught:
                        self.assertIs(caught, error)
                    else:
                        self.fail("original exception was suppressed")
                    self.assertEqual(register.selections["a", "vendor/exact-b"], "none")
                    selected["a"] = "vendor/exact-b"; calls.clear()
                    await controls.handle_claude_native_model_change("a", selected["a"])
                    self.assertEqual(calls[0], ("register", "a", "vendor/exact-b", "none"))
                with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register):
                    runtime._patch_native_controls(self.d)
                    asyncio.run(run())

    def test_native_claude_ordered_selection_preserves_refusals_native_and_disabled_controls(self):
        modules, module, fetch, selected, calls, register, model_response, effort_response = self.native_controls_fixture(model_status=503, effort_status=503)
        async def run():
            controls = module.build_native_controls(_fetch_session_model_override=fetch, _session_reasoning_effort={})
            selected["a"] = "vendor/exact-b"
            self.assertIs(await controls.handle_claude_native_model_change("a", selected["a"]), model_response)
            self.assertEqual([c for c in calls if c[0] == "effort"], [])
            self.assertIs(await controls.handle_claude_native_effort_change("a", "high"), effort_response)
            self.d["preferences"]["claudeConnection"] = "native"
            calls.clear()
            self.assertIs(await controls.handle_claude_native_model_change("a", "native/model"), model_response)
            self.assertIs(await controls.handle_claude_native_effort_change("a", "native-effort"), effort_response)
            self.assertEqual(calls, [("model", "a", "native/model"), ("effort", "a", "native-effort")])
            self.d["preferences"].update(claude=False, claudeConnection="kilo")
            calls.clear()
            with self.assertRaises(ValueError): await controls.handle_claude_native_model_change("a", "vendor/exact-b")
            with self.assertRaises(ValueError): await controls.handle_claude_native_effort_change("a", "high")
            self.assertEqual(calls, [])
        with patch.dict(sys.modules, modules), patch.object(runtime, "register_claude_effort", side_effect=register):
            runtime._patch_native_controls(self.d)
            asyncio.run(run())

    def test_gateway_disconnect_waits_for_complete_body_and_closes_only_its_request(self):
        async def run():
            module, Shim = self.gateway_disconnect_fixture()
            with patch.dict(sys.modules, {module.__name__: module}):
                runtime._patch_claude_gateway_disconnect(self.d)
            queues = [asyncio.Queue(), asyncio.Queue()]
            shims = [Shim(), Shim()]
            sent = [[], []]
            reads = [[], []]
            async def receive(i):
                message = await queues[i].get()
                reads[i].append(message)
                return message
            async def send(i, message): sent[i].append(message)
            tasks_before = asyncio.all_tasks()
            tasks = [asyncio.create_task(shims[i]._asgi_app({"type": "http"}, lambda i=i: receive(i), lambda message, i=i: send(i, message))) for i in range(2)]
            for queue in queues:
                await queue.put({"type": "http.request", "body": b"part-one", "more_body": True})
            for _ in range(4): await asyncio.sleep(0)
            self.assertEqual([len(row) for row in reads], [1, 1])
            self.assertTrue(all(not shim.entered.is_set() for shim in shims))
            for queue in queues:
                await queue.put({"type": "http.request", "body": b"part-two", "more_body": False})
            for shim in shims: await shim.entered.wait()
            self.assertEqual([shim.body for shim in shims], [b"part-onepart-two"] * 2)
            await queues[0].put({"type": "http.disconnect"})
            await asyncio.wait_for(tasks[0], 1)
            self.assertEqual(shims[0].closed, 1)
            self.assertFalse(tasks[1].done())
            self.assertEqual(shims[1].closed, 0)
            shims[1].release.set()
            await asyncio.wait_for(tasks[1], 1)
            self.assertEqual(shims[1].closed, 1)
            self.assertEqual(sent[0], sent[1][:2])
            self.assertEqual(sent[1][2:], [{"type": "http.response.body", "body": b"last\xffchunk", "more_body": True}, {"type": "http.response.body", "body": b""}])
            self.assertEqual(asyncio.all_tasks(), tasks_before)
        asyncio.run(run())

    def test_gateway_disconnect_before_body_never_starts_upstream(self):
        async def run():
            module, Shim = self.gateway_disconnect_fixture()
            with patch.dict(sys.modules, {module.__name__: module}): runtime._patch_claude_gateway_disconnect(self.d)
            shim = Shim(); queue = asyncio.Queue(); sent = []
            queue.put_nowait({"type": "http.request", "body": b"partial", "more_body": True})
            queue.put_nowait({"type": "http.disconnect"})
            async def send(message): sent.append(message)
            tasks_before = asyncio.all_tasks()
            await shim._asgi_app({"type": "http"}, queue.get, send)
            self.assertFalse(shim.entered.is_set())
            self.assertEqual(shim.closed, 0)
            self.assertEqual(sent, [])
            self.assertEqual(asyncio.all_tasks(), tasks_before)
        asyncio.run(run())

    def test_gateway_disconnect_preserves_errors_and_outer_cancellation_without_tasks(self):
        async def run():
            for failure in ("receive", "send", "cancel"):
                with self.subTest(failure=failure):
                    module, Shim = self.gateway_disconnect_fixture()
                    with patch.dict(sys.modules, {module.__name__: module}): runtime._patch_claude_gateway_disconnect(self.d)
                    shim = Shim(); queue = asyncio.Queue()
                    queue.put_nowait({"type": "http.request", "body": b"exact-body"})
                    async def receive():
                        if failure == "receive": raise RuntimeError("synthetic receive failure")
                        return await queue.get()
                    async def send(message):
                        if failure == "send": raise RuntimeError("synthetic send failure")
                    tasks_before = asyncio.all_tasks()
                    task = asyncio.create_task(shim._asgi_app({"type": "http"}, receive, send))
                    if failure == "cancel":
                        await shim.entered.wait()
                        task.cancel()
                        with self.assertRaises(asyncio.CancelledError): await task
                    else:
                        with self.assertRaisesRegex(RuntimeError, "synthetic " + failure + " failure"): await task
                    self.assertEqual(shim.closed, 0 if failure == "receive" else 1)
                    self.assertEqual(asyncio.all_tasks(), tasks_before)
        asyncio.run(run())

    def test_gateway_disconnect_keeps_native_disabled_foreign_and_non_http_original(self):
        async def run():
            for connection, enabled, base, scope_type in (("native", True, "http://127.0.0.1:8877/omnigent/claude", "http"),
                                                          ("kilo", False, "http://127.0.0.1:8877/omnigent/claude", "http"),
                                                          ("kilo", True, "http://127.0.0.1:8877/foreign", "http"),
                                                          ("kilo", True, "http://127.0.0.1:8877/omnigent/claude-extra", "http"),
                                                          ("kilo", True, "http://127.0.0.1:8877/omnigent/claude", "lifespan")):
                with self.subTest(connection=connection, enabled=enabled, base=base, scope_type=scope_type):
                    self.d["preferences"].update(claude=enabled, claudeConnection=connection)
                    module, Shim = self.gateway_disconnect_fixture(); original = Shim._asgi_app
                    with patch.dict(sys.modules, {module.__name__: module}): runtime._patch_claude_gateway_disconnect(self.d)
                    if not enabled or connection == "native": self.assertIs(Shim._asgi_app, original)
                    shim = Shim(base); shim.release.set(); queue = asyncio.Queue(); sent = []
                    queue.put_nowait({"type": "http.request", "body": b"unchanged"})
                    queue.put_nowait({"type": "http.disconnect"})
                    async def send(message): sent.append(message)
                    tasks_before = asyncio.all_tasks()
                    await shim._asgi_app({"type": scope_type}, queue.get, send)
                    self.assertEqual(queue.qsize(), 2 if scope_type != "http" else 1)
                    self.assertEqual(len(sent), 0 if scope_type != "http" else 4)
                    self.assertEqual(asyncio.all_tasks(), tasks_before)
        asyncio.run(run())

    def test_stop_waits_for_owned_group_after_leader_exit(self):
        now = [0.0]; calls = []
        class Process:
            pid = 41001
            def poll(self): return 0  # Leader already exited; descendant remains.
            def wait(self, timeout): return 0
        def killpg(pid, sig):
            calls.append((pid, sig, now[0]))
            self.assertEqual(pid, Process.pid)
            if sig == 0 and now[0] >= 0.25: raise ProcessLookupError()
        with patch.object(runtime.os, "killpg", side_effect=killpg), patch.object(runtime.time, "monotonic", side_effect=lambda: now[0]), patch.object(runtime.time, "sleep", side_effect=lambda delay: now.__setitem__(0, now[0] + delay)):
            self.assertTrue(runtime._stop_owned([Process()]))
        self.assertEqual(calls[0][:2], (Process.pid, runtime.signal.SIGTERM))
        self.assertGreaterEqual(now[0], 0.25)
        self.assertLess(now[0], 8)
        self.assertFalse(any(sig == runtime.signal.SIGKILL for _, sig, _ in calls))

    def test_stop_forced_group_exit_and_permission_errors_are_not_cleanup_safe(self):
        for permission, leader_waits in ((False, False), (False, True), (True, False)):
            with self.subTest(permission=permission, leader_waits=leader_waits):
                now = [0.0]; calls = []; waits = []
                class Process:
                    pid = 41002
                    def poll(self): return None if leader_waits else 0
                    def wait(self, timeout):
                        waits.append(timeout)
                        if leader_waits:
                            now[0] += timeout
                            raise runtime.subprocess.TimeoutExpired("own child", timeout)
                        return 0
                def killpg(pid, sig):
                    calls.append((pid, sig, now[0]))
                    self.assertEqual(pid, Process.pid)
                    if permission: raise PermissionError("synthetic owned group probe denied")
                with patch.object(runtime.os, "killpg", side_effect=killpg), patch.object(runtime.time, "monotonic", side_effect=lambda: now[0]), patch.object(runtime.time, "sleep", side_effect=lambda delay: now.__setitem__(0, now[0] + delay)):
                    self.assertFalse(runtime._stop_owned([Process()]))
                self.assertEqual(calls[0][:2], (Process.pid, runtime.signal.SIGTERM))
                self.assertEqual([sig for _, sig, _ in calls].count(runtime.signal.SIGKILL), 1)
                self.assertEqual(next(t for _, sig, t in calls if sig == runtime.signal.SIGKILL), 8)
                self.assertLessEqual(now[0], 11)
                self.assertLessEqual(max(waits), 3)
        # killpg(0) cannot distinguish a zombie from a draining member. Such a
        # group must fail closed at the same budget, not be called cleanly gone.

    def test_stop_absent_groups_and_no_children_are_cleanup_safe(self):
        class Process:
            pid = 41003
            def poll(self): return 0
            def wait(self, timeout): return 0
        with patch.object(runtime.os, "killpg", side_effect=ProcessLookupError()) as kill:
            self.assertTrue(runtime._stop_owned([Process()]))
            self.assertEqual(kill.call_args_list, [unittest.mock.call(Process.pid, runtime.signal.SIGTERM), unittest.mock.call(Process.pid, 0)])
            kill.reset_mock()
            self.assertTrue(runtime._stop_owned([]))
            kill.assert_not_called()

    def test_supervisor_keeps_owned_socket_root_when_group_exit_is_unconfirmed(self):
        (self.root / "runtime").mkdir()
        self.d["serverPort"] = 0
        signals = {}; processes = []; owned = []
        original_create = runtime._create_owned_socket_root
        def create(d):
            result = original_create(d); owned.append(result); return result
        class Control:
            server_port = 43211
            def __init__(self, *args): pass
            def serve_forever(self): pass
            def shutdown(self): pass
            def server_close(self): pass
        class Socket:
            def bind(self, address): pass
            def getsockname(self): return ("127.0.0.1", 43210)
            def close(self): pass
        def launch(*args, **kwargs):
            process = types.SimpleNamespace(pid=42000+len(processes), poll=lambda: None)
            processes.append(process); return process
        def http(url, *args, **kwargs):
            if url.endswith("/kilo-omnigent/health"):
                return {"version": 1, "pid": processes[0].pid, "fingerprint": self.d["fingerprint"]}
            return {"hosts": [{"host_id": runtime.hashlib.sha256(str(self.root).encode()).hexdigest()[:32], "status": "online"}]}
        with patch.object(runtime, "_create_owned_socket_root", side_effect=create), patch.object(runtime, "ThreadingHTTPServer", Control), patch.object(runtime.socket, "socket", Socket), patch.object(runtime.threading.Thread, "start"), patch.object(runtime.subprocess, "Popen", side_effect=launch), patch.object(runtime.signal, "signal", side_effect=lambda sig, callback: signals.update({sig: callback})), patch.object(runtime, "_http_json", side_effect=http), patch.object(runtime.webbrowser, "open", side_effect=lambda url: signals[runtime.signal.SIGTERM]()), patch.object(runtime, "_stop_owned", return_value=False), patch.object(runtime, "_cleanup_owned_socket_root", side_effect=AssertionError("must retain socket root until group exit is confirmed")):
            runtime.supervise(self.d, "own.json")
        self.assertFalse((self.root / "runtime.json").exists())
        status = runtime._json(self.root / "runtime" / "status.json")
        self.assertEqual(status["phase"], "failed")
        self.assertEqual(status["message"], "The private Omnigent processes did not finish stopping. Their temporary directory was kept.")
        path, identity = owned[0]
        self.assertTrue(path.is_dir())
        fd, actual = runtime._open_owned_socket_root(self.d, path)
        os.close(fd)
        self.assertEqual(actual, identity)
        self.assertTrue(runtime._cleanup_owned_socket_root(self.d, identity))

    def test_supervisor_records_before_children_and_opens_only_own_ready_host(self):
        for foreign in (False,True):
            with self.subTest(foreign=foreign):
                (self.root/"runtime").mkdir(exist_ok=True)
                self.d["serverPort"]=0
                signals={}; launched=[]; cleaned=[]; opened=[]
                class Control:
                    server_port=43211
                    def __init__(self,*a): pass
                    def serve_forever(self): pass
                    def shutdown(self): pass
                    def server_close(self): pass
                class Socket:
                    def bind(self,address): self.address=address
                    def getsockname(self): return ("127.0.0.1",43210)
                    def close(self): pass
                class Process:
                    def __init__(self,pid): self.pid=pid
                    def poll(self): return None
                def launch(args,**kwargs):
                    record=runtime._json(self.root/"runtime.json")
                    self.assertEqual(record["pid"],os.getpid())
                    self.assertEqual(runtime._json(self.root/"runtime"/"status.json")["phase"],"starting")
                    self.assertTrue(record["startedAt"].endswith("Z"))
                    self.assertEqual(kwargs["cwd"],str(self.root))
                    self.assertTrue(kwargs["start_new_session"])
                    self.assertEqual(kwargs["env"]["OMNIGENT_HOST_NAME"],"Kilo Proxy")
                    launched.append(Process(40000+len(launched)))
                    return launched[-1]
                def http(url,token=None,timeout=1):
                    if url.endswith("/kilo-omnigent/health"):
                        return {"version":1,"pid": 999 if foreign else launched[0].pid,"fingerprint":self.d["fingerprint"]}
                    self.assertTrue(url.endswith("/v1/hosts"))
                    return {"hosts":[{"host_id":runtime.hashlib.sha256(str(self.root).encode()).hexdigest()[:32],"status":"online"}]}
                def browser(url):
                    opened.append(url)
                    self.assertEqual(len(launched),2)
                    self.assertEqual(runtime._json(self.root/"runtime"/"status.json")["phase"],"ready")
                    signals[runtime.signal.SIGTERM]()
                with patch.object(runtime,"ThreadingHTTPServer",Control),patch.object(runtime.socket,"socket",Socket),patch.object(runtime.threading.Thread,"start"),patch.object(runtime.subprocess,"Popen",side_effect=launch),patch.object(runtime.signal,"signal",side_effect=lambda sig,handler:signals.update({sig:handler})),patch.object(runtime,"_http_json",side_effect=http),patch.object(runtime.webbrowser,"open",side_effect=browser),patch.object(runtime,"_stop_owned",side_effect=lambda children:(cleaned.extend(p.pid for p in children),True)[1]):
                    if foreign:
                        with self.assertRaises(ValueError): runtime.supervise(self.d,"own.json")
                    else:
                        runtime.supervise(self.d,"own.json")
                self.assertFalse((self.root/"runtime.json").exists())
                self.assertEqual(cleaned,[p.pid for p in launched])
                self.assertEqual(len(launched),1 if foreign else 2)
                self.assertEqual(opened,[] if foreign else ["http://127.0.0.1:43210/"])
                self.assertEqual(runtime._json(self.root/"runtime"/"status.json")["phase"],"failed" if foreign else "stopped")

    @unittest.skipUnless(hasattr(os,"mkfifo"),"POSIX synthetic FIFO fixture")
    def test_special_files_fail_without_waiting_for_writer(self):
        fifo=self.root/"fifo"
        os.mkfifo(fifo,0o600)
        with self.assertRaises(ValueError): runtime._read_regular(fifo,1024)
        with self.assertRaises(ValueError): runtime._json(fifo)

    def selected_cli_modules(self):
        modules={name:types.ModuleType(name) for name in ("omnigent._platform","omnigent.onboarding.harness_install","omnigent.onboarding.harness_readiness","omnigent.inner.codex_executor","omnigent.inner.claude_sdk_executor","omnigent.runner.native.orchestration")}
        self.resolve_calls=[];self.probe_calls=[];self.availability_calls=[]
        def resolve(name,**kwargs): self.resolve_calls.append((name,kwargs));return "/foreign/"+name
        for module in modules.values(): module.resolve_cli_binary=resolve
        @dataclasses.dataclass(frozen=True)
        class Spec:
            binary:str
            status_args:tuple=("auth","status")
            min_version:str="1.0.0"
            max_version_exclusive:str="3.0.0"
            login_status_key:str="loggedIn"
        self.original_spec=Spec("ambient")
        install=modules["omnigent.onboarding.harness_install"]
        install.harness_install_spec=lambda key:self.original_spec
        self.probe_result=types.SimpleNamespace(returncode=7,stdout="synthetic status",stderr="")
        def run(argv,*args,**kwargs):self.probe_calls.append((argv,args,kwargs));return self.probe_result
        install.subprocess=types.SimpleNamespace(run=run,TimeoutExpired=runtime.subprocess.TimeoutExpired,DEVNULL=runtime.subprocess.DEVNULL)
        readiness=modules["omnigent.onboarding.harness_readiness"]
        readiness._canonical_harness=lambda name:{"ClaudeSdkExecutor":"claude-sdk","CodexExecutor":"codex"}.get(name,name)
        def availability(name):self.availability_calls.append(name);return "version-too-low" if "codex" in name.lower() else "needs-auth"
        readiness._harness_availability=availability;readiness._harness_availability_core=availability
        for family in ("codex","claude"):
            path=self.root/(family+"-selected")
            path.write_text("synthetic executable; never run")
            path.chmod(0o700)
            self.d[family+"Path"]=str(path)
        return modules

    def test_selected_cli_overrides_all_cached_aliases_without_path_fallback(self):
        modules=self.selected_cli_modules()
        with patch.dict(sys.modules,modules):
            runtime._patch_selected_cli(self.d)
            for module in modules.values():
                if not hasattr(module,"resolve_cli_binary"):continue
                for family in ("codex","claude"):
                    self.assertEqual(module.resolve_cli_binary(family,env_var="FORGED_PATH",which=lambda name:"/foreign/cli"),self.d[family+"Path"])
            self.assertEqual(self.resolve_calls,[])
            resolver=modules["omnigent._platform"].resolve_cli_binary
            self.assertEqual(resolver("git",env_var="OTHER",which=None),"/foreign/git")
            self.assertEqual(self.resolve_calls,[("git",{"env_var":"OTHER","which":None})])
            self.d["preferences"]["codex"]=False
            self.assertIsNone(resolver("codex"))
            Path(self.d["claudePath"]).unlink()
            with self.assertRaises(OSError):resolver("claude")
            self.assertEqual(len(self.resolve_calls),1)

    def test_selected_cli_specs_and_readiness_keep_version_auth_verdicts(self):
        modules=self.selected_cli_modules()
        with patch.dict(sys.modules,modules):
            runtime._patch_selected_cli(self.d)
            install=modules["omnigent.onboarding.harness_install"]
            for key,family in (("openai","codex"),("anthropic","claude")):
                spec=install.harness_install_spec(key)
                self.assertEqual(spec.binary,self.d[family+"Path"])
                self.assertEqual(dataclasses.replace(spec,binary="ambient"),self.original_spec)
            self.assertIs(install.harness_install_spec("pi"),self.original_spec)
            readiness=modules["omnigent.onboarding.harness_readiness"]
            self.assertEqual(readiness._harness_availability("codex-native"),"version-too-low")
            self.assertEqual(readiness._harness_availability_core("ClaudeSdkExecutor"),"needs-auth")
            self.assertFalse(readiness._harness_availability("opencode-native"))
            self.d["preferences"]["codex"]=False
            self.assertFalse(readiness._harness_availability_core("codex-native"))
            self.assertIsNone(install.harness_install_spec("openai"))
            self.assertEqual(self.availability_calls,["codex-native","claude-sdk"])

    def test_readiness_accepted_legacy_spellings_keep_original_verdict_for_every_cache_representative(self):
        modules = self.selected_cli_modules()
        with patch.dict(sys.modules, modules):
            runtime._patch_selected_cli(self.d)
            readiness = modules["omnigent.onboarding.harness_readiness"]
            for connection in ("kilo", "native"):
                self.d["preferences"]["codexConnection"] = connection
                self.d["preferences"]["claudeConnection"] = connection
                for method in (readiness._harness_availability, readiness._harness_availability_core):
                    for representative in ("codex", "codex-native", "native-codex"):
                        self.assertEqual(method(representative), "version-too-low")
                        self.assertEqual(self.availability_calls[-1], "codex-native" if representative == "native-codex" else representative)
                    for representative in ("claude-native", "native-claude"):
                        self.assertEqual(method(representative), "needs-auth")
                        self.assertEqual(self.availability_calls[-1], "claude-native")
                    count = len(self.availability_calls)
                    for unknown in ("native-openai", "native-codex-foreign", "Native-Codex", "native-muse", "opencode-native"):
                        self.assertFalse(method(unknown))
                    self.assertEqual(len(self.availability_calls), count)
            self.d["preferences"]["codex"] = False
            self.d["preferences"]["claude"] = False
            count = len(self.availability_calls)
            for method in (readiness._harness_availability, readiness._harness_availability_core):
                for disabled in ("codex", "codex-native", "native-codex", "claude-native", "native-claude"):
                    self.assertFalse(method(disabled))
            self.assertEqual(len(self.availability_calls), count)

    def test_selected_cli_probes_preserve_arguments_timeout_exit_and_own_context(self):
        modules=self.selected_cli_modules();self.d["preferences"]["claudeConnection"]="native"
        runtime._KILO_TOKENS={"codex":"scoped-codex"}
        runtime._NATIVE_ENV={"claude":{"ANTHROPIC_API_KEY":"own-native","CLAUDE_SECURESTORAGE_CONFIG_DIR":"/own/secure"}}
        with patch.dict(sys.modules,modules),patch.dict(os.environ,{"KILO_LOCAL_API_KEY":"root","KILO_OMNIGENT_CLAUDE_TOKEN":"sibling","OPENAI_API_KEY":"ambient","ANTHROPIC_API_KEY":"ambient","HOME":"/private/home"},clear=True),patch.object(runtime,"register_claude_effort",side_effect=AssertionError("auth probe must not register inference session")):
            runtime._patch_selected_cli(self.d)
            install=modules["omnigent.onboarding.harness_install"]
            for family in ("codex","claude"):
                argv=[self.d[family+"Path"],"auth","status"]
                result=install.subprocess.run(argv,check=False,timeout=10.0,stdin=install.subprocess.DEVNULL,capture_output=True,text=True)
                self.assertIs(result,self.probe_result)
                observed,args,kwargs=self.probe_calls[-1]
                self.assertEqual(observed,argv);self.assertEqual(args,())
                self.assertEqual({key:value for key,value in kwargs.items() if key!="env"},{"check":False,"timeout":10.0,"stdin":runtime.subprocess.DEVNULL,"capture_output":True,"text":True})
                env=kwargs["env"]
                self.assertNotIn("KILO_LOCAL_API_KEY",env);self.assertNotIn("KILO_OMNIGENT_CLAUDE_TOKEN",env)
                self.assertNotIn("ANTHROPIC_CUSTOM_HEADERS",env)
                if family=="codex":
                    self.assertEqual(env["KILO_OMNIGENT_CODEX_TOKEN"],"scoped-codex")
                    self.assertEqual(env["OPENAI_API_KEY"],"scoped-codex")
                    self.assertEqual(env["OPENAI_BASE_URL"],"http://127.0.0.1:8877/omnigent/codex/v1")
                    self.assertNotIn("ANTHROPIC_API_KEY",env)
                else:
                    self.assertEqual(env["ANTHROPIC_API_KEY"],"own-native")
                    self.assertEqual(env["HOME"],self.d["normalHome"])
                    self.assertEqual(env["CLAUDE_SECURESTORAGE_CONFIG_DIR"],"/own/secure")
                    self.assertNotIn("KILO_OMNIGENT_CODEX_TOKEN",env);self.assertNotIn("OPENAI_API_KEY",env)


    def test_native_codex_reset_forwards_prepared_default_instead_of_retaining_live_model(self):
        plain = {"id": "vendor/nested/plain", "name": "Plain", "reasoningLevels": [], "defaultReasoning": ""}
        self.d["modelLibraries"]["codex"]["models"].append(plain)
        modules, module, fetch, selected, calls, _, response, _ = self.native_controls_fixture()
        selected["a"] = plain["id"]
        async def run():
            controls = module.build_native_controls(_fetch_session_model_override=fetch)
            for value in (None, ""):
                with self.subTest(reset=value):
                    request = {"model": value, "effort": "reset"}
                    untouched = copy.deepcopy(request)
                    calls.clear()
                    self.assertIs(await controls.handle_codex_native_settings_update("a", request), response)
                    self.assertEqual(calls, [("codex", "a", {"model": "openai/exact-a", "effort": "high"}, {})])
                    self.assertEqual(request, untouched)
            # An effort-only change must preserve the currently selected model.
            calls.clear()
            await controls.handle_codex_native_settings_update("a", {"effort": None})
            self.assertEqual(calls, [("codex", "a", {"effort": "none"}, {})])
            self.d["preferences"]["codexConnection"] = "native"
            request = {"model": None, "effort": "reset"}
            await controls.handle_codex_native_settings_update("a", request)
            self.assertIs(calls[-1][2], request)
            self.assertIsNone(calls[-1][2]["model"])
        with patch.dict(sys.modules, modules):
            runtime._patch_native_controls(self.d)
            asyncio.run(run())


class RuntimeShutdownOrderTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="kilo-omnigent-stop-order-")
        self.root = Path(self.tmp.name)
        self.source = self.root / "runner" / "_entry.py"
        self.source.parent.mkdir()
        self.original = (
            "def fixture(events, pm, _terminal_registry, mcp_manager, server_client):\n"
            "    async def _stop_pm():\n"
            "        events.append('native-teardown')\n"
            "        await pm.shutdown()\n"
            "        await _terminal_registry.shutdown()\n"
            "        await mcp_manager.shutdown()\n"
            "        await server_client.aclose()\n"
            "    return _stop_pm\n"
        )
        self.source.write_text(self.original)
        self.d = {"sourceRoot": str(self.root), "sourceHashes": {"runner/_entry.py": runtime.hashlib.sha256(self.source.read_bytes()).hexdigest()}}
        self.old_path = list(sys.meta_path)

    def tearDown(self):
        sys.meta_path[:] = self.old_path
        self.tmp.cleanup()

    def loader(self):
        name = "omnigent.runner._entry"
        runtime._patch_private_runner_shutdown(self.d)
        finder = sys.meta_path[0]
        spec = importlib.util.spec_from_file_location(name, self.source)
        with patch("importlib.machinery.PathFinder.find_spec", return_value=spec):
            transformed = finder.find_spec(name)
        return transformed.loader

    def test_shutdown_order_loader_keeps_native_teardown_and_remaining_cleanup(self):
        name = "omnigent.runner._entry"
        loader = self.loader()
        module = types.ModuleType(name)
        loader.exec_module(module)
        events = []
        class Resource:
            def __init__(self, label): self.label = label
            async def shutdown(self): events.append(self.label)
            async def aclose(self): events.append(self.label)
        stop = module.fixture(events, Resource("process-manager"), Resource("terminals"), Resource("mcp"), Resource("client"))
        asyncio.run(stop())
        self.assertEqual(events, ["native-teardown", "terminals", "process-manager", "mcp", "client"])
        self.assertEqual(module.fixture.__code__.co_filename, str(self.source))
        self.assertEqual(self.source.read_text(), self.original)
        self.assertIsNone(sys.meta_path[0].find_spec("omnigent.runner.other"))

    def test_detached_terminal_closes_before_unchanged_two_second_watchdog(self):
        original = types.ModuleType("synthetic_original_entry")
        exec(compile(self.original, str(self.source), "exec"), original.__dict__)
        fixed = types.ModuleType("omnigent.runner._entry")
        self.loader().exec_module(fixed)
        async def run(module):
            closed = asyncio.Event()
            class Manager:
                async def shutdown(self): await asyncio.sleep(3)
            class Terminals:
                async def shutdown(self): closed.set()
            class Rest:
                async def shutdown(self): pass
                async def aclose(self): pass
            with self.assertRaises(asyncio.TimeoutError):
                await asyncio.wait_for(module.fixture([], Manager(), Terminals(), Rest(), Rest())(), timeout=2)
            return closed.is_set()
        self.assertFalse(asyncio.run(run(original)))
        self.assertTrue(asyncio.run(run(fixed)))

    def test_shutdown_loader_rejects_hash_missing_duplicate_origin_and_preload(self):
        from importlib.machinery import ModuleSpec, SourceFileLoader
        name = "omnigent.runner._entry"
        loader = self.loader()
        for text, repin in (("def changed(): pass\n", False), (self.original.replace("await pm.shutdown()", "await pm.other()"), True), (self.original + self.original.replace("fixture", "fixture_again"), True)):
            with self.subTest(repin=repin):
                self.source.write_text(text)
                if repin: self.d["sourceHashes"]["runner/_entry.py"] = runtime.hashlib.sha256(self.source.read_bytes()).hexdigest()
                with self.assertRaisesRegex(ValueError, "contract changed"):
                    loader.get_code(name)
        self.source.write_text(self.original)
        for origin, implementation in ((str(self.source.parent / "other.py"), SourceFileLoader(name, str(self.source))), (str(self.source), object())):
            with self.subTest(origin=origin):
                spec = ModuleSpec(name, implementation, origin=origin)
                with patch("importlib.machinery.PathFinder.find_spec", return_value=spec):
                    with self.assertRaisesRegex(ValueError, "loader changed"):
                        sys.meta_path[0].find_spec(name)
        with patch.dict(sys.modules, {name: types.ModuleType(name)}):
            with self.assertRaisesRegex(ValueError, "imported before"):
                runtime._patch_private_runner_shutdown(self.d)


class RuntimeDarwinStopTests(unittest.TestCase):
    def stop_fixture(self, platform, plan, poll_values=(0,), wait_timeout=False):
        now = [0.0]; calls = []; waits = []; polls = []
        class Process:
            pid = 42001
            def poll(self):
                value = poll_values[min(len(polls), len(poll_values) - 1)]
                polls.append(value)
                return value
            def wait(self, timeout):
                waits.append(timeout)
                if wait_timeout:
                    raise runtime.subprocess.TimeoutExpired("own synthetic child", timeout)
                return 0
        actions = {sig: list(values) for sig, values in plan.items()}
        def killpg(pid, sig):
            self.assertEqual(pid, Process.pid)
            if sig == 0:
                self.assertTrue(polls, "poll the owned leader before confirming its group")
            calls.append((pid, sig, now[0]))
            values = actions.get(sig, [])
            action = values.pop(0) if len(values) > 1 else values[0] if values else "absent" if sig == 0 else "ok"
            if action == "absent": raise ProcessLookupError(runtime.errno.ESRCH, "synthetic group absent")
            if action == "eperm": raise PermissionError(runtime.errno.EPERM, "synthetic Darwin group transition")
            if action == "eacces": raise PermissionError(runtime.errno.EACCES, "synthetic access denied")
            if action == "unknown": raise PermissionError("synthetic unknown permission error")
            if action == "eio": raise OSError(runtime.errno.EIO, "synthetic I/O error")
            self.assertEqual(action, "ok")
        with patch.object(runtime.sys, "platform", platform), patch.object(runtime.os, "killpg", side_effect=killpg), patch.object(runtime.time, "monotonic", side_effect=lambda: now[0]), patch.object(runtime.time, "sleep", side_effect=lambda delay: now.__setitem__(0, now[0] + delay)):
            result = runtime._stop_owned([Process()])
        return result, now[0], calls, polls, waits

    def test_darwin_term_eperm_is_provisional_until_reaped_group_is_absent(self):
        result, elapsed, calls, polls, waits = self.stop_fixture("darwin", {runtime.signal.SIGTERM: ["eperm"]})
        self.assertTrue(result)
        self.assertEqual([sig for _, sig, _ in calls], [runtime.signal.SIGTERM, 0])
        self.assertEqual(polls, [0])
        self.assertEqual(waits, [3.0])
        self.assertEqual(elapsed, 0)

    def test_darwin_probe_eperm_waits_for_next_poll_and_group_absence(self):
        result, elapsed, calls, polls, waits = self.stop_fixture("darwin", {0: ["eperm", "absent"]}, poll_values=(None, -15))
        self.assertTrue(result)
        self.assertEqual(polls, [None, -15])
        self.assertEqual([sig for _, sig, _ in calls], [runtime.signal.SIGTERM, 0, 0])
        self.assertEqual(elapsed, 0.05)
        self.assertEqual(waits, [3.0])

    def test_darwin_provisional_term_error_does_not_skip_live_group(self):
        result, elapsed, calls, polls, waits = self.stop_fixture("darwin", {runtime.signal.SIGTERM: ["eperm"], 0: ["ok", "ok", "absent"]}, poll_values=(None, None, -15))
        self.assertTrue(result)
        self.assertEqual(polls, [None, None, -15])
        self.assertEqual(elapsed, 0.1)
        self.assertEqual(waits, [3.0])
        self.assertNotIn(runtime.signal.SIGKILL, [sig for _, sig, _ in calls])

    def test_darwin_permanent_eperm_and_live_groups_remain_unsafe_at_same_deadline(self):
        for action in ("eperm", "ok"):
            with self.subTest(action=action):
                result, elapsed, calls, polls, waits = self.stop_fixture("darwin", {0: [action]})
                self.assertFalse(result)
                self.assertEqual(next(t for _, sig, t in calls if sig == runtime.signal.SIGKILL), 8)
                self.assertEqual([sig for _, sig, _ in calls].count(runtime.signal.SIGKILL), 1)
                self.assertLessEqual(elapsed, 11)
                self.assertLessEqual(max(waits), 3)

    def test_darwin_other_errors_remain_sticky_after_group_disappears(self):
        for action in ("eio", "eacces", "unknown"):
            for sig in (runtime.signal.SIGTERM, 0):
                with self.subTest(action=action, signal=sig):
                    plan = {sig: [action, "absent"]} if sig == 0 else {sig: [action]}
                    self.assertFalse(self.stop_fixture("darwin", plan)[0])

    def test_non_darwin_eperm_behavior_is_unchanged(self):
        for platform in ("linux", "win32"):
            for sig in (runtime.signal.SIGTERM, 0):
                with self.subTest(platform=platform, signal=sig):
                    plan = {sig: ["eperm", "absent"]} if sig == 0 else {sig: ["eperm"]}
                    self.assertFalse(self.stop_fixture(platform, plan)[0])

    def test_darwin_group_absence_does_not_hide_leader_wait_timeout(self):
        result, elapsed, calls, polls, waits = self.stop_fixture("darwin", {runtime.signal.SIGTERM: ["eperm"]}, wait_timeout=True)
        self.assertFalse(result)
        self.assertEqual(polls, [0])
        self.assertEqual(waits, [3.0])

    def test_darwin_forced_kill_permission_failure_is_never_cleanup_safe(self):
        result, elapsed, calls, polls, waits = self.stop_fixture("darwin", {0: ["ok"], runtime.signal.SIGKILL: ["eperm"]})
        self.assertFalse(result)
        self.assertEqual(next(t for _, sig, t in calls if sig == runtime.signal.SIGKILL), 8)
        self.assertLessEqual(elapsed, 11)
        self.assertLessEqual(max(waits), 3)


if __name__ == "__main__":
    unittest.main()
