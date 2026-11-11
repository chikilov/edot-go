"""Version boundaries and safety gates; no production endpoint or credentials."""
import base64
import contextlib
import io
import json
import re
import urllib.error
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from compatibility import CompatibilityError, resolve_profile, same_version
import verify


class CompatibilityTests(unittest.TestCase):
    def test_version_boundaries(self):
        for version, profile, route in [
            ('8.18.0', '8.x-apm', 'apm-translation'),
            ('8.19.16', '8.x-apm', 'apm-translation'),
            ('9.0.0', '9.0-9.5.2-managed', 'bulk-compatibility'),
            ('9.4', '9.0-9.5.2-managed', 'bulk-compatibility'),
            ('9.5.2', '9.0-9.5.2-managed', 'bulk-compatibility'),
            ('9.5.3', '9.5.3+-managed', 'native'),
            ('9.5.6', '9.5.3+-managed', 'native'),
            ('9.6', '9.5.3+-managed', 'native'),
        ]:
            with self.subTest(version=version):
                result = resolve_profile(version)
                self.assertEqual(result['profile'], profile)
                self.assertEqual(result['metric_ingestion_expected'], route)
                self.assertEqual(result['target'], 'apm' if version.startswith('8.') else 'managed-otlp')
                self.assertEqual(result['index_patterns'][0], 'traces-apm*' if version.startswith('8.') else 'traces-*.otel-*')

    def test_invalid_and_incompatible_inputs(self):
        for version in ('7.17.0', '10.0.0', '9.5', '9', '9.5.3-SNAPSHOT', 'private-secret'):
            with self.subTest(version=version), self.assertRaises(CompatibilityError):
                resolve_profile(version)
        with self.assertRaises(CompatibilityError):
            resolve_profile('8.19.16', target='managed-otlp')
        with self.assertRaises(CompatibilityError):
            resolve_profile('9.5.3', deployment='serverless')
        with self.assertRaises(CompatibilityError):
            resolve_profile(deployment='serverless', target='apm')
        self.assertEqual(resolve_profile(deployment='serverless')['profile'], 'serverless-managed')
        self.assertEqual(resolve_profile('9.5.6', target='apm')['profile'], '9.x-apm-explicit')

    def test_expected_minor_and_exact_patch_comparison(self):
        self.assertTrue(same_version('8.19', '8.19.16'))
        self.assertTrue(same_version('8.19.16', '8.19.16'))
        self.assertFalse(same_version('8.19.15', '8.19.16'))
        self.assertFalse(same_version('9.5.3', '8.19.16'))

    def file(self, directory):
        path = Path(directory)/'.env'
        path.write_text("OTEL_EXPORTER_OTLP_ENDPOINT='https://apm.example.invalid'\n"
                        "OTEL_EXPORTER_OTLP_HEADERS='Authorization=ApiKey%20synthetic-secret'\n")
        return path

    def test_mismatch_aborts_before_export(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.file(directory)
            with patch('sys.argv', ['verify.py', '--env', str(path), '--elastic-version', '9.5.3']), patch.object(verify, 'observe_version', return_value='8.19.16'), patch.object(verify.subprocess, 'run') as export, contextlib.redirect_stderr(io.StringIO()) as output:
                self.assertEqual(verify.main(), 2)
                export.assert_not_called()
                self.assertNotIn('synthetic-secret', output.getvalue())

    def test_preflight_and_listing_never_probe_or_export(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.file(directory)
            with patch('sys.argv', ['verify.py', '--env', str(path), '--elastic-version', '8.19.16', '--preflight']), patch.object(verify, 'observe_version') as probe, patch.object(verify.subprocess, 'run') as export, contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(verify.main(), 0)
                probe.assert_not_called()
                export.assert_not_called()
                result = json.loads(output.getvalue())
                self.assertEqual(result['compatibility']['profile'], '8.x-apm')
                self.assertFalse(result['compatibility']['version_verified'])
            with patch('sys.argv', ['verify.py', '--list-profiles']), patch.object(verify, 'settings') as read, contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(verify.main(), 0)
                read.assert_not_called()
                self.assertEqual(len(json.loads(output.getvalue())), 4)

    def test_profile_templates_are_valid_offline_and_cli_version_wins(self):
        root = Path(__file__).with_name('profiles')
        expected = {'elastic-8-apm.env.example': '8.x-apm',
                    'elastic-9-compat-managed.env.example': '9.0-9.5.2-managed',
                    'elastic-9-native-managed.env.example': '9.5.3+-managed',
                    'serverless-managed.env.example': 'serverless-managed'}
        with tempfile.TemporaryDirectory() as directory:
            for name, profile in expected.items():
                text = (root/name).read_text()
                text = re.sub(r'https://YOUR_[A-Z_]+', 'https://ingest.example.invalid', text)
                text = re.sub(r'ApiKey%20YOUR_[A-Z_]+', 'ApiKey%20synthetic-secret', text)
                path = Path(directory)/name
                path.write_text(text)
                with self.subTest(name=name), patch('sys.argv', ['verify.py', '--env', str(path), '--preflight']), contextlib.redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(verify.main(), 0)
                    self.assertEqual(json.loads(output.getvalue())['compatibility']['profile'], profile)
            path = self.file(directory)
            path.write_text(path.read_text()+"ELASTIC_STACK_VERSION='8.19.16'\n")
            with patch('sys.argv', ['verify.py', '--env', str(path), '--elastic-version', '9.5.3', '--preflight']), contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(verify.main(), 0)
                self.assertEqual(json.loads(output.getvalue())['compatibility']['profile'], '9.5.3+-managed')

    def test_version_probe_never_exposes_arbitrary_server_content(self):
        values = {'ELASTICSEARCH_URL': 'https://search.example.invalid', 'ELASTICSEARCH_API_KEY': base64.b64encode(b'synthetic:read').decode()}
        with patch.object(verify, 'open_cloud_request', return_value=io.BytesIO(json.dumps({'version': {'number': 'private-server-secret'}}).encode())):
            self.assertIsNone(verify.observe_version(values))
        with patch.object(verify, 'open_cloud_request', side_effect=urllib.error.HTTPError('https://search.example.invalid', 403, 'denied', {}, None)):
            self.assertIsNone(verify.observe_version(values))

    def test_observed_version_selects_apm_before_export(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.file(directory)
            data = {'trace_id': 'a'*32, 'diagnostics': {name: {'SuccessfulBatches': 1, 'FailedBatches': 0, 'SuccessfulItems': 1} for name in ('Traces', 'Metrics', 'Logs')}}
            with patch('sys.argv', ['verify.py', '--env', str(path)]), patch.object(verify, 'observe_version', return_value='8.19.16'), patch.object(verify.subprocess, 'run') as export, contextlib.redirect_stdout(io.StringIO()) as output:
                export.return_value.returncode = 0
                export.return_value.stdout = json.dumps(data)
                self.assertEqual(verify.main(), 0)
                result = json.loads(output.getvalue())
                self.assertEqual(result['target'], 'apm')
                self.assertEqual(result['compatibility']['observed_version'], '8.19.16')
                self.assertTrue(result['compatibility']['version_verified'])
