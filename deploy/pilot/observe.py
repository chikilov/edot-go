#!/usr/bin/env python3
"""Observe an explicitly selected service PID and its sanitized JSONL snapshots.
Does not start/stop/restart a service, generate traffic, or change credentials.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import time


COUNTERS = ('ExportCalls', 'SuccessfulBatches', 'FailedBatches', 'SuccessfulItems',
            'ItemsInFailedBatches', 'DisabledDroppedItems', 'InFlightBatches')


def read_snapshot(path, now, expected_pid=None):
    try:
        with path.open('rb') as source:
            size = source.seek(0, 2)
            source.seek(max(0, size-65536))
            content = source.read(65536)
        # Ignore an incomplete trailing write. Never emit input lines on errors.
        lines = content.split(b'\n')[:-1]
        for line in reversed(lines):
            try:
                data = json.loads(line)
                if expected_pid is not None and data.get('pid') != expected_pid:
                    continue
                stamp_text = re.sub(r'\.(\d+)(?=Z|[+-])', lambda m: '.'+(m.group(1)+'000000')[:6], data['time'])
                stamp = datetime.fromisoformat(stamp_text.replace('Z', '+00:00'))
                lag = (now-stamp).total_seconds()
                if not 0 <= lag <= 120:
                    continue
                sdk, go = data['sdk_version'], data['go_version']
                if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', sdk) or not re.fullmatch(r'go[0-9]+\.[0-9]+\.[0-9]+', go):
                    continue
                heap, goroutines = data['heap_alloc_bytes'], data['goroutines']
                if type(heap) is not int or heap < 0 or type(goroutines) is not int or goroutines < 0:
                    continue
                diag = data['diagnostics']
                signals = {}
                for name in ('Traces', 'Metrics', 'Logs'):
                    values = diag[name]
                    if any(type(values[k]) is not int or values[k] < 0 for k in COUNTERS):
                        raise ValueError('invalid counter')
                    signals[name] = {k: values[k] for k in COUNTERS}
                return {'available': True, 'time': stamp.isoformat(), 'age_seconds': lag,
                        'sdk_version': sdk, 'go_version': go, 'heap_alloc_bytes': heap,
                        'goroutines': goroutines, 'signals': signals,
                        'queue_depth_available': diag.get('QueueDepthAvailable') is True,
                        'queue_overflow_available': diag.get('QueueOverflowCountAvailable') is True}
            except (ValueError, TypeError, KeyError, AttributeError):
                continue
    except OSError:
        pass
    return {'available': False, 'reason': 'missing, stale, incomplete or invalid sanitized snapshot'}


def process_sample(pid):
    env = dict(os.environ, LC_ALL='C')
    result = subprocess.run(['ps', '-p', str(pid), '-o', '%cpu=', '-o', 'rss=', '-o', 'lstart='],
                            capture_output=True, text=True, timeout=5, env=env)
    if result.returncode:
        return {'available': False}
    fields = result.stdout.split()
    if len(fields) < 7:
        return {'available': False}
    try:
        return {'available': True, 'cpu_percent_lifetime_average': float(fields[0]),
                'rss_bytes': int(fields[1])*1024, 'start_identity': ' '.join(fields[2:])}
    except ValueError:
        return {'available': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--pid', required=True, type=int)
    parser.add_argument('--snapshots', required=True, type=Path)
    parser.add_argument('--duration-seconds', type=int, default=86400)
    parser.add_argument('--interval-seconds', type=int, default=60)
    parser.add_argument('--output-dir', required=True, type=Path)
    args = parser.parse_args()
    if args.pid <= 1 or args.duration_seconds < 10 or not 1 <= args.interval_seconds <= 60:
        parser.error('pid >1, duration >=10s, interval 1..60s required')
    args.output_dir.mkdir(parents=True, exist_ok=False, mode=0o700)
    events = args.output_dir/'samples.jsonl'
    fd = os.open(events, os.O_WRONLY|os.O_CREAT|os.O_EXCL, 0o600)
    stopped = False
    def stop(*unused):
        nonlocal stopped
        stopped = True
    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    start = time.monotonic()
    identity = None
    samples = missing = 0
    reason = 'duration_complete'
    with os.fdopen(fd, 'w') as output:
        while True:
            elapsed = time.monotonic()-start
            if stopped:
                reason = 'observer_interrupted'
                break
            now = datetime.now(timezone.utc)
            process = process_sample(args.pid)
            if not process['available']:
                reason = 'target_process_exited_or_unavailable'
                break
            if identity is None:
                identity = process['start_identity']
            elif process['start_identity'] != identity:
                reason = 'pid_reused_by_different_process'
                break
            snapshot = read_snapshot(args.snapshots, now, args.pid)
            if not snapshot['available']:
                missing += 1
            item = {'time': now.isoformat(), 'elapsed_seconds': elapsed,
                    'process': process, 'sdk': snapshot}
            output.write(json.dumps(item)+'\n')
            output.flush()
            samples += 1
            if elapsed >= args.duration_seconds:
                break
            deadline = min(start+args.duration_seconds, time.monotonic()+args.interval_seconds)
            while not stopped and time.monotonic() < deadline:
                time.sleep(min(.5, max(0, deadline-time.monotonic())))
    summary = {'requested_seconds': args.duration_seconds, 'observed_seconds': time.monotonic()-start,
               'samples': samples, 'missing_sdk_samples': missing, 'stop_reason': reason,
               'target_pid': args.pid,
               'duration_complete': reason == 'duration_complete' and time.monotonic()-start >= args.duration_seconds,
               'observation_complete': reason == 'duration_complete' and time.monotonic()-start >= args.duration_seconds and samples > 0 and missing == 0,
               'assessment': 'requires service SLO/baseline, latency and queue-observation review; not an automatic production PASS',
               'credentials_changed': False, 'service_managed': False}
    fd = os.open(args.output_dir/'summary.json', os.O_WRONLY|os.O_CREAT|os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as output:
        json.dump(summary, output, indent=2)
        output.write('\n')
    print(json.dumps(summary))
    return 0 if summary['observation_complete'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
