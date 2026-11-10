"""Local dummy-credential regression: redirects must not receive auth headers."""
import http.server
import json
import base64
import contextlib
import io
import importlib.util
from pathlib import Path
from unittest.mock import patch
import threading
import unittest

import verify


class RedirectSecurityTests(unittest.TestCase):
    def test_version_probe_does_not_follow_redirect_or_forward_auth(self):
        captured = []

        class Destination(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                captured.append(self.headers.get('Authorization'))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(json.dumps({'version': {'number': '8.19.16'}}).encode())
            def log_message(self, *args):
                pass

        destination = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Destination)
        location = 'http://127.0.0.1:'+str(destination.server_port)+'/'

        class Redirect(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(302)
                self.send_header('Location', location)
                self.end_headers()
            def log_message(self, *args):
                pass

        source = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Redirect)
        for server in (source, destination):
            threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            values = {'ELASTICSEARCH_URL': 'http://127.0.0.1:'+str(source.server_port),
                      'OTEL_EXPORTER_OTLP_HEADERS': 'Authorization=ApiKey%20dummy-secret-only'}
            self.assertIsNone(verify.observe_version(values))
            self.assertEqual(len(captured), 0, 'redirect destination received an authenticated request')
            spec = importlib.util.spec_from_file_location('local_verify_security', Path(__file__).parents[1]/'local'/'verify.py')
            local = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(local)
            with patch.object(local, 'load_settings', return_value={'ES_LOCAL_PASSWORD': 'dummy-local-secret', 'ES_LOCAL_PORT': str(source.server_port)}):
                with self.assertRaises(verify.urllib.error.HTTPError):
                    local.request('/')
            self.assertEqual(len(captured), 0, 'local Basic auth followed a redirect')
        finally:
            for server in (source, destination):
                server.shutdown()
                server.server_close()


class LookupCredentialSeparationTests(unittest.TestCase):
    def test_lookup_requests_use_read_key_not_ingest_key(self):
        key = base64.b64encode(b'read-id:synthetic-read-secret').decode()
        values = {'ELASTICSEARCH_URL': 'https://search.example.invalid', 'OTEL_EXPORTER_OTLP_HEADERS': 'Authorization=ApiKey%20synthetic-ingest-secret', 'ELASTICSEARCH_API_KEY': key}
        response = io.BytesIO(json.dumps({'version': {'number': '8.19.16'}}).encode())
        with patch.object(verify, 'open_cloud_request', return_value=response) as lookup:
            self.assertEqual(verify.observe_version(values), '8.19.16')
            self.assertEqual(lookup.call_args.args[0].get_header('Authorization'), 'ApiKey '+key)

    def test_read_key_not_inherited_by_sdk_subprocess(self):
        import tempfile
        import os
        key = base64.b64encode(b'read-id:synthetic-read-secret').decode()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/'.env'
            path.write_text("OTEL_EXPORTER_OTLP_ENDPOINT='https://apm.example.invalid'\nOTEL_EXPORTER_OTLP_HEADERS='Authorization=ApiKey%20synthetic-ingest-secret'\nELASTICSEARCH_API_KEY='"+key+"'\n")
            data = {'trace_id': 'a'*32, 'diagnostics': {name: {'SuccessfulBatches': 1, 'FailedBatches': 0, 'SuccessfulItems': 1} for name in ('Traces', 'Metrics', 'Logs')}}
            with patch.dict(os.environ, {'ELASTICSEARCH_API_KEY': key}), patch('sys.argv', ['verify.py', '--env', str(path), '--target', 'apm']), patch.object(verify.subprocess, 'run') as run, contextlib.redirect_stdout(io.StringIO()) as output:
                run.return_value.returncode = 0
                run.return_value.stdout = json.dumps(data)
                self.assertEqual(verify.main(), 0)
                self.assertNotIn('ELASTICSEARCH_API_KEY', run.call_args.kwargs['env'])
                self.assertNotIn(key, output.getvalue())
