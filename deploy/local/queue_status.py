#!/usr/bin/env python3
"""Read one local Collector Prometheus snapshot and report exporter queue state."""
import argparse
import json
import math
import re
import sys
from pathlib import Path
import urllib.request

LIMIT = 4 * 1024 * 1024
METRICS = {
    'otelcol_exporter_queue_size': 'depth',
    'otelcol_exporter_queue_capacity': 'capacity',
}
for signal, suffix in (('traces', 'spans'), ('metrics', 'metric_points'), ('logs', 'log_records')):
    for kind in ('enqueue_failed', 'send_failed', 'sent'):
        name = f'otelcol_exporter_{kind}_{suffix}'
        METRICS[name] = f'{signal}_{kind}_total'
        METRICS[name + '_total'] = METRICS[name]

SAMPLE = re.compile(r'^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(.*)\})?\s+(\S+)(?:\s+\S+)?$')
LABEL = re.compile(r'([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\[\\n"])*)"')
COMPONENT = re.compile(r'[A-Za-z0-9_.:/-]{1,160}')


def parse_labels(raw):
    labels = {}
    while raw:
        match = LABEL.match(raw)
        if not match or match[1] in labels:
            raise ValueError('invalid metric labels')
        labels[match[1]] = json.loads('"' + match[2] + '"')
        raw = raw[match.end():]
        if raw:
            if not raw.startswith(','):
                raise ValueError('invalid metric labels')
            raw = raw[1:]
            if not raw:
                raise ValueError('invalid metric labels')
    return labels


def summarize(text):
    if len(text.encode()) > LIMIT:
        raise ValueError('metric snapshot exceeds size limit')
    rows, duplicates = {}, set()
    for line in text.splitlines():
        if not line or line.startswith('#'):
            continue
        match = SAMPLE.fullmatch(line)
        if not match or match[1] not in METRICS:
            continue
        labels = parse_labels(match[2] or '')
        exporter = labels.get('exporter', '')
        signal = labels.get('data_type', '')
        if not COMPONENT.fullmatch(exporter) or signal not in ('', 'traces', 'metrics', 'logs'):
            continue
        value = float(match[3])
        if not math.isfinite(value) or value < 0:
            raise ValueError('invalid queue metric value')
        key = exporter, signal
        field = METRICS[match[1]]
        row = rows.setdefault(key, {'exporter': exporter, 'signal': signal or None})
        identity = key, field
        if field in row or identity in duplicates:
            row.pop(field, None)
            duplicates.add(identity)
        else:
            row[field] = value
    result = []
    for key in sorted(rows):
        row = rows[key]
        row['ambiguous_metrics'] = sorted(field for identity, field in duplicates if identity == key)
        depth, capacity = row.get('depth'), row.get('capacity')
        row['utilization'] = depth / capacity if depth is not None and capacity and depth <= capacity else None
        row['queue_observed'] = depth is not None and capacity is not None
        result.append(row)
    return {'exporters': result, 'counters': 'cumulative since Collector process start; retries may count repeatedly'}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--file', type=Path, help='read a saved snapshot instead of the local metrics endpoint')
    parser.add_argument('--port', type=int, default=8888)
    args = parser.parse_args()
    try:
        if not 1 <= args.port <= 65535:
            raise ValueError('invalid metrics port')
        if args.file:
            with args.file.open('rb') as source:
                body = source.read(LIMIT + 1)
        else:
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
            with opener.open(f'http://127.0.0.1:{args.port}/metrics', timeout=5) as response:
                body = response.read(LIMIT + 1)
        if len(body) > LIMIT:
            raise ValueError('metric snapshot exceeds size limit')
        print(json.dumps(summarize(body.decode('utf-8')), indent=2))
        return 0
    except (OSError, ValueError):
        print('Collector queue snapshot unavailable or invalid; raw response suppressed', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
