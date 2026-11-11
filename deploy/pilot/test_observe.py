"""Observer safety: sanitized snapshots and incomplete-run reporting."""
from datetime import datetime, timezone, timedelta
import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import observe


class ObserverTests(unittest.TestCase):
    def data(self, now):
        return {'pid': os.getpid(), 'time': now.isoformat(), 'sdk_version': '0.5.1', 'go_version': 'go1.26.10',
                'heap_alloc_bytes': 1024, 'goroutines': 12,
                'diagnostics': {**{signal: {key: 0 for key in observe.COUNTERS} for signal in ('Traces', 'Metrics', 'Logs')},
                                'QueueDepthAvailable': False, 'QueueOverflowCountAvailable': False},
                'api_key': 'synthetic-secret-do-not-copy'}

    def test_whitelist_and_partial_trailing_write(self):
        now = datetime.now(timezone.utc)
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)/'snapshots.jsonl'
            path.write_text(json.dumps(self.data(now))+'\n'+ '{"unfinished":"synthetic-secret"')
            sample = observe.read_snapshot(path, now+timedelta(seconds=1))
            self.assertTrue(sample['available'])
            self.assertFalse(sample['queue_overflow_available'])
            self.assertNotIn('synthetic-secret', json.dumps(sample))
            self.assertFalse(observe.read_snapshot(path, now+timedelta(seconds=1), os.getpid()+1)['available'])

    def test_stale_future_and_invalid_values_are_not_measurements(self):
        now = datetime.now(timezone.utc)
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)/'snapshots.jsonl'
            for data in (self.data(now-timedelta(seconds=121)), self.data(now+timedelta(seconds=5)),
                         dict(self.data(now), heap_alloc_bytes=True), dict(self.data(now), sdk_version='private-value')):
                path.write_text(json.dumps(data)+'\n')
                self.assertFalse(observe.read_snapshot(path, now)['available'])

    def test_go_nanosecond_timestamp(self):
        now = datetime(2026, 11, 10, 5, 0, 1, tzinfo=timezone.utc)
        data = self.data(now)
        data['time'] = '2026-11-10T05:00:00.123456789Z'
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)/'snapshots.jsonl';path.write_text(json.dumps(data)+'\n')
            self.assertTrue(observe.read_snapshot(path, now)['available'])

    def test_interruption_never_reports_24h_completion(self):
        with tempfile.TemporaryDirectory() as root:
            output = Path(root)/'observation'
            args = ['observe.py', '--pid', str(os.getpid()), '--snapshots', str(Path(root)/'not-created'), '--output-dir', str(output)]
            with patch('sys.argv', args), patch.object(observe.signal, 'signal', side_effect=lambda number, handler: handler()), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(observe.main(), 1)
            summary = json.loads((output/'summary.json').read_text())
            self.assertFalse(summary['observation_complete'])
            self.assertEqual(summary['stop_reason'], 'observer_interrupted')
            self.assertEqual((output/'summary.json').stat().st_mode & 0o777, 0o600)

    def test_process_sample_does_not_read_command_arguments(self):
        sample = observe.process_sample(os.getpid())
        self.assertTrue(sample['available'])
        self.assertGreater(sample['rss_bytes'], 0)
        self.assertNotIn('command', sample)
