"""Credential redaction and inherited-environment regressions; no real network."""
import base64
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import urllib.error
from unittest.mock import patch

import verify


class CloudCheckTests(unittest.TestCase):
    def config(self, directory, extra=''):
        path = Path(directory)/'.env'
        path.write_text("OTEL_EXPORTER_OTLP_ENDPOINT='https://apm.example.invalid'\n"
                        "OTEL_EXPORTER_OTLP_HEADERS='Authorization=ApiKey%20synthetic-secret'\n"+extra)
        return path

    def test_failed_export_never_echoes_raw_output(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.config(directory)
            output = io.StringIO()
            process = subprocess.CompletedProcess([], 1, 'synthetic-secret', 'Authorization: ApiKey synthetic-secret')
            with patch('sys.argv', ['verify.py', '--env', str(path), '--target', 'apm']), patch.object(verify.subprocess, 'run', return_value=process), contextlib.redirect_stdout(output):
                self.assertEqual(verify.main(), 1)
            self.assertNotIn('synthetic-secret', output.getvalue())
            self.assertEqual(json.loads(output.getvalue())['result'], 'FAIL')

    def test_stale_otlp_settings_are_not_inherited(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.config(directory)
            diag = {name: {'SuccessfulBatches': 1, 'FailedBatches': 0, 'SuccessfulItems': 1} for name in ('Traces', 'Metrics', 'Logs')}
            process = subprocess.CompletedProcess([], 0, json.dumps({'trace_id': 'a'*32, 'diagnostics': diag}), '')
            with patch.dict(os.environ, {'OTEL_EXPORTER_OTLP_LOGS_HEADERS': 'stale-private-key', 'OTEL_EXPORTER_OTLP_TRACES_ENDPOINT': 'https://wrong.invalid'}), patch('sys.argv', ['verify.py', '--env', str(path), '--target', 'apm']), patch.object(verify.subprocess, 'run', return_value=process) as run, contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(verify.main(), 0)
                build, launch = run.call_args_list
                self.assertEqual(build.args[0][:2], ['go', 'build'])
                self.assertNotIn('OTEL_EXPORTER_OTLP_HEADERS', build.kwargs['env'])
                env = launch.kwargs['env']
                self.assertEqual(env['EDOT_GO_REQUIRE_SECURE_OTLP'], 'true')
                self.assertNotIn('OTEL_EXPORTER_OTLP_LOGS_HEADERS', env)
                self.assertNotIn('OTEL_EXPORTER_OTLP_TRACES_ENDPOINT', env)

    def test_file_is_data_and_does_not_execute_shell(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory)/'must-not-exist'
            path = self.config(directory, "OTEL_SERVICE_NAME='$(touch "+str(marker)+")'\n")
            values = verify.settings(path, 'apm')
            self.assertIn('$(touch ', values['OTEL_SERVICE_NAME'])
            self.assertFalse(marker.exists())

    def test_unsafe_endpoints_and_signal_overrides_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.config(directory, "OTEL_EXPORTER_OTLP_TRACES_HEADERS='alternate-key'\n")
            with self.assertRaises(ValueError):
                verify.settings(path, 'apm')
            path = self.config(directory, "OTEL_EXPORTER_OTLP_INSECURE=true\n")
            with self.assertRaises(ValueError):
                verify.settings(path, 'apm')
            path = self.config(directory)
            path.write_text(path.read_text().replace('https://apm.example.invalid', 'https://user:password@apm.example.invalid'))
            with self.assertRaises(ValueError):
                verify.settings(path, 'apm')

    def test_indexed_trace_log_transaction_link(self):
        values = {'ELASTICSEARCH_URL': 'https://search.example.invalid',
                  'OTEL_EXPORTER_OTLP_HEADERS': 'Authorization=ApiKey%20synthetic-secret',
                  'ELASTICSEARCH_API_KEY': base64.b64encode(b'synthetic:read').decode()}
        tid = 'a'*32
        replies = [
            {'hits': {'hits': [{'fields': {'transaction.id': ['b'*16]}}]}},
            {'hits': {'hits': [{'fields': {'span.id': ['b'*16]}}]}},
            {'hits': {'hits': [{'_id': 'synthetic-metric'}]}},
        ]
        with patch.object(verify, 'open_cloud_request', side_effect=[io.BytesIO(json.dumps(x).encode()) for x in replies]) as lookup:
            result = verify.indexed(values, 'apm', tid, '2026-11-09T00:00:00Z')
            self.assertTrue(result['verified'])
            queries = [json.loads(call.args[0].data) for call in lookup.call_args_list]
            self.assertEqual(queries[0]['query']['term']['trace.id'], tid)
            self.assertEqual(queries[1]['query']['term']['trace.id'], tid)
            self.assertEqual(queries[2]['query']['bool']['filter'][0]['term']['service.name'], 'edot-go-cloudcheck')

    def test_kibana_lookup_uses_only_read_only_search_proxy(self):
        values = {'KIBANA_URL': 'https://kibana.example.invalid',
                  'OTEL_EXPORTER_OTLP_HEADERS': 'Authorization=ApiKey%20synthetic-secret',
                  'ELASTICSEARCH_API_KEY': base64.b64encode(b'synthetic:read').decode()}
        replies = [{'hits': {'hits': [{'fields': {'span.id': ['b'*16]}}]}},
                   {'hits': {'hits': [{'fields': {'span.id': ['b'*16]}}]}},
                   {'hits': {'hits': [{'_id': 'synthetic-metric'}]}}]
        with patch.object(verify, 'open_cloud_request', side_effect=[io.BytesIO(json.dumps(x).encode()) for x in replies]) as lookup:
            self.assertTrue(verify.indexed(values, 'apm', 'a'*32, '2026-11-09T00:00:00Z')['verified'])
            for call in lookup.call_args_list:
                req = call.args[0]
                self.assertIn('/api/console/proxy?', req.full_url)
                self.assertIn('%2F_search', req.full_url)
                self.assertEqual(req.get_header('Kbn-xsrf'), 'cloudcheck')

    def test_lookup_key_without_read_access_stays_unverified(self):
        values = {'ELASTICSEARCH_URL': 'https://search.example.invalid',
                  'ELASTICSEARCH_API_KEY': base64.b64encode(b'synthetic:read').decode()}
        denied = urllib.error.HTTPError('https://search.example.invalid', 403, 'Forbidden', {}, None)
        with patch.object(verify, 'open_cloud_request', side_effect=denied):
            result = verify.indexed(values, 'apm', 'a'*32, '2026-11-09T00:00:00Z')
            self.assertFalse(result['verified'])
            self.assertIn('no Elasticsearch read access', result['reason'])

    def test_ingestion_key_is_never_used_for_lookup(self):
        values = {'ELASTICSEARCH_URL': 'https://search.example.invalid',
                  'OTEL_EXPORTER_OTLP_HEADERS': 'Authorization=ApiKey%20synthetic-secret'}
        with patch.object(verify, 'open_cloud_request') as request:
            self.assertIsNone(verify.observe_version(values))
            result = verify.indexed(values, 'apm', 'a'*32, '2026-11-09T00:00:00Z')
            self.assertFalse(result['verified'])
            self.assertIn('ELASTICSEARCH_API_KEY', result['reason'])
            request.assert_not_called()




if __name__ == '__main__':
    unittest.main()
