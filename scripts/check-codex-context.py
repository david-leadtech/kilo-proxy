#!/usr/bin/env python3
"""Check installed Codex context presets against a disposable local gateway.

Usage: python3 scripts/check-codex-context.py /absolute/path/to/codex
All model responses and usage are synthetic. No personal profile or paid API.
"""
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path(__file__).resolve().parents[1]


def main():
    records = []

    class Gateway(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            records.append(body)
            assert self.headers.get('Authorization') == 'Bearer context-fixture'
            message = {'id': 'msg_' + str(len(records)), 'type': 'message', 'role': 'assistant',
                       'status': 'completed', 'content': [{'type': 'output_text', 'text': 'Synthetic context check complete.', 'annotations': []}]}
            # A full low-window response forces auto-compaction before the next
            # user turn. Later responses are small so the fixture terminates.
            usage = 120000 if len(records) == 2 else 100
            response = {'id': 'resp_' + str(len(records)), 'object': 'response', 'status': 'completed',
                        'model': body.get('model'), 'output': [message],
                        'usage': {'input_tokens': usage, 'output_tokens': 5, 'total_tokens': usage + 5,
                                  'input_tokens_details': {'cached_tokens': 0}}}
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.end_headers()
            for event in [{'type': 'response.created', 'response': {**response, 'status': 'in_progress', 'output': []}},
                          {'type': 'response.output_item.added', 'output_index': 0, 'item': message},
                          {'type': 'response.output_item.done', 'output_index': 0, 'item': message},
                          {'type': 'response.completed', 'response': response}]:
                self.wfile.write(('data: ' + json.dumps(event) + '\n\n').encode())
                self.wfile.flush()

    with ThreadingHTTPServer(('127.0.0.1', 0), Gateway) as server, tempfile.TemporaryDirectory(prefix='kilo-context-runtime-') as directory:
        threading.Thread(target=server.serve_forever, daemon=True).start()
        profile = Path(directory)
        generated = subprocess.check_output(['node', '--input-type=module', '-e', """
import {codexCatalog} from './ui/codex-catalog.mjs';
console.log(JSON.stringify(codexCatalog([
 {id:'fixture/recommended', contextWindow:1050000, contextPreset:'recommended'},
 {id:'fixture/low', contextWindow:1050000, contextPreset:'low'}
], 'fixture/recommended')));
"""], cwd=ROOT, text=True, timeout=15)
        (profile / 'models.json').write_text(generated)
        (profile / 'config.toml').write_text(f'''model = "fixture/recommended"
model_provider = "kilo-local"
model_catalog_json = "models.json"
cli_auth_credentials_store = "file"
[model_providers.kilo-local]
name = "Synthetic local context fixture"
base_url = "http://127.0.0.1:{server.server_port}/v1"
env_key = "KILO_LOCAL_API_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
[analytics]
enabled = false
''')
        env = {k: v for k, v in os.environ.items() if not k.startswith(('CODEX_', 'OPENAI_', 'KILO_'))}
        env.update(CODEX_HOME=directory, KILO_LOCAL_API_KEY='context-fixture')
        process = subprocess.Popen([sys.argv[1], 'app-server', '--stdio'], cwd=directory, env=env,
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        events, errors, observed = queue.Queue(), [], []

        def read():
            for line in process.stdout:
                events.put(json.loads(line))

        def read_errors():
            for line in process.stderr:
                errors.append(line)

        threading.Thread(target=read, daemon=True).start()
        threading.Thread(target=read_errors, daemon=True).start()

        def send(message):
            process.stdin.write(json.dumps(message) + '\n')
            process.stdin.flush()

        def wait_for(predicate):
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                try:
                    event = events.get(timeout=.2)
                except queue.Empty:
                    assert process.poll() is None, ''.join(errors[-8:])
                    continue
                observed.append(event)
                assert 'error' not in event, event
                assert event.get('method') != 'configWarning', event
                if predicate(event):
                    return event
            raise AssertionError('Codex event timed out: ' + ''.join(errors[-5:]))

        try:
            send({'id': 1, 'method': 'initialize', 'params': {'clientInfo': {'name': 'context_fixture', 'version': '1'}, 'capabilities': {'experimentalApi': True}}})
            wait_for(lambda event: event.get('id') == 1)
            send({'method': 'initialized'})
            send({'id': 2, 'method': 'thread/start', 'params': {'cwd': directory, 'ephemeral': True, 'approvalPolicy': 'never', 'sandbox': 'read-only', 'model': 'fixture/recommended'}})
            thread = wait_for(lambda event: event.get('id') == 2)['result']['thread']['id']
            windows = []
            for i, model in enumerate(['fixture/recommended', 'fixture/low', 'fixture/low']):
                start = len(observed)
                send({'id': 10 + i, 'method': 'turn/start', 'params': {'threadId': thread, 'model': model,
                      'input': [{'type': 'text', 'text': 'Reply briefly for this local test.'}]}})
                wait_for(lambda event: event.get('method') == 'turn/completed')
                usage_events = [event['params']['tokenUsage'] for event in observed[start:] if event.get('method') == 'thread/tokenUsage/updated']
                assert usage_events, observed[start:]
                windows.append(usage_events[-1]['modelContextWindow'])
            assert windows == [258400, 121600, 121600], windows
            assert any(event.get('params', {}).get('item', {}).get('type') == 'contextCompaction' for event in observed), 'No actual auto-compaction observed'
            assert len(records) >= 4, 'Expected the compaction request as well as three user turns'
            print('PASS: installed Codex switched 272K → 128K, reported 95% usable windows, and automatically compacted synthetic history. No paid requests.')
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            server.shutdown()


if __name__ == '__main__':
    main()
