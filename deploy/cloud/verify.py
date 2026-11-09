#!/usr/bin/env python3
"""Load a local dotenv file without executing it; run sanitized Cloud checks."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import time
from datetime import datetime, timezone
import urllib.request
import urllib.error
import sys
import urllib.parse

from compatibility import CompatibilityError, resolve_profile, same_version

REPO = Path(__file__).resolve().parents[2]
PREFIXES = ('OTEL_', 'EDOT_GO_', 'ELASTIC_OTEL_JAVA_')


def settings(path, target=None):
    values = {}
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith('#'):
            continue
        if '=' not in line:
            raise ValueError('expected KEY=value lines in Cloud configuration')
        key, value = line.split('=', 1)
        if (not key.startswith(PREFIXES) and key not in ('ELASTICSEARCH_URL', 'KIBANA_URL', 'ELASTIC_STACK_VERSION', 'ELASTIC_DEPLOYMENT')) or not key.replace('_', '').isalnum():
            raise ValueError('unsupported Cloud configuration variable')
        value = value.strip()
        if value[:1] in ('"', "'"):
            if len(value) < 2 or value[-1] != value[0]:
                raise ValueError('unclosed configuration quote')
            value = value[1:-1]
        if key in values:
            raise ValueError('duplicate Cloud configuration variable')
        values[key] = value
    endpoint = values.get('OTEL_EXPORTER_OTLP_ENDPOINT', '')
    if not endpoint.isascii():
        raise ValueError('replace the endpoint example with the ASCII URL copied from Cloud')
    parsed = urllib.parse.urlsplit(endpoint)
    if parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError('an HTTPS Cloud OTLP endpoint is required')
    headers = urllib.parse.unquote(values.get('OTEL_EXPORTER_OTLP_HEADERS', ''))
    allowed = ('authorization=apikey ',) if target == 'managed-otlp' else ('authorization=apikey ', 'authorization=bearer ')
    if not headers.lower().startswith(allowed) or not headers.split(' ', 1)[1].strip():
        raise ValueError('a matching APM API Key or secret token header is required')
    if any(marker in (endpoint+' '+headers).upper() for marker in ('YOUR_', '<', '>')):
        raise ValueError('replace Cloud endpoint and API Key placeholders in the local file')
    # A clean process environment avoids stale per-signal credentials/routes.
    for signal in ('TRACES', 'METRICS', 'LOGS'):
        if any(k.startswith('OTEL_EXPORTER_OTLP_'+signal+'_') and k != 'OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE' for k in values):
            raise ValueError('use common exporter settings for this Cloud connectivity check')
    for url_key in ('ELASTICSEARCH_URL', 'KIBANA_URL'):
        if values.get(url_key):
            if not values[url_key].isascii():
                raise ValueError('replace the lookup URL example with the URL copied from Cloud')
            es = urllib.parse.urlsplit(values[url_key])
            if es.scheme != 'https' or not es.hostname or es.username or es.password or es.query or es.fragment or es.path not in ('', '/'):
                raise ValueError('lookup requires an HTTPS origin URL without embedded credentials or UI paths')
    return values


def observe_version(values):
    if not values.get('ELASTICSEARCH_URL') and not values.get('KIBANA_URL'):
        return None
    auth = urllib.parse.unquote(values['OTEL_EXPORTER_OTLP_HEADERS']).split('=', 1)[1]
    if not auth.lower().startswith('apikey '):
        return None
    headers = {'Authorization': auth}
    if values.get('ELASTICSEARCH_URL'):
        url, method = values['ELASTICSEARCH_URL'].rstrip('/')+'/', 'GET'
    else:
        url = values['KIBANA_URL'].rstrip('/')+'/api/console/proxy?'+urllib.parse.urlencode({'path': '/', 'method': 'GET'})
        method = 'POST'
        headers['kbn-xsrf'] = 'cloudcheck'
    try:
        req = urllib.request.Request(url, headers=headers, method=method)
        with urllib.request.urlopen(req, timeout=15) as response:
            version = json.loads(response.read(2*1024*1024))['version']['number']
        # Do not expose arbitrary server-returned text in the report.
        from compatibility import parse_version
        if len(parse_version(version)) != 3:
            return None
        return version
    except (OSError, ValueError, KeyError, TypeError):
        return None


def indexed(values, target, tid, started):
    if not values.get('ELASTICSEARCH_URL') and not values.get('KIBANA_URL'):
        return {'verified': False, 'reason': 'Elasticsearch or Kibana URL not configured'}
    auth = urllib.parse.unquote(values['OTEL_EXPORTER_OTLP_HEADERS']).split('=', 1)[1]
    if not auth.startswith('ApiKey '):
        return {'verified': False, 'reason': 'APM Bearer token cannot query Elasticsearch'}
    patterns = ('traces-apm*', 'logs-apm*', 'metrics-apm*') if target == 'apm' else ('traces-*.otel-*', 'logs-*.otel-*', 'metrics-*.otel-*')

    def search(pattern, body):
        headers = {'Authorization': auth, 'Content-Type': 'application/json'}
        if values.get('ELASTICSEARCH_URL'):
            url = values['ELASTICSEARCH_URL'].rstrip('/')+'/'+pattern+'/_search'
        else:
            url = values['KIBANA_URL'].rstrip('/')+'/api/console/proxy?'+urllib.parse.urlencode({'path': pattern+'/_search', 'method': 'POST'})
            headers['kbn-xsrf'] = 'cloudcheck'
        req = urllib.request.Request(url, data=json.dumps(body).encode(), headers=headers)
        with urllib.request.urlopen(req, timeout=15) as response:
            return json.loads(response.read(2*1024*1024))['hits']['hits']

    until = time.monotonic()+90
    while True:
        try:
            query = {'size': 2, 'query': {'term': {'trace.id': tid}}, '_source': False, 'fields': ['span.id', 'transaction.id']}
            traces = search(patterns[0], query)
            logs = search(patterns[1], query)
            metrics = search(patterns[2], {'size': 1, '_source': False, 'query': {'bool': {'filter': [
                {'term': {'service.name': 'edot-go-cloudcheck'}}, {'range': {'@timestamp': {'gte': started}}}],
                'should': [{'exists': {'field': 'cloudcheck.requests'}}, {'exists': {'field': 'metrics.cloudcheck.requests'}}], 'minimum_should_match': 1}}})
            if len(traces) == 1 and len(logs) == 1 and metrics:
                trace_fields, log_fields = traces[0].get('fields', {}), logs[0].get('fields', {})
                trace_ids = set(trace_fields.get('span.id', [])+trace_fields.get('transaction.id', []))
                log_ids = set(log_fields.get('span.id', [])+log_fields.get('transaction.id', []))
                if not trace_ids.intersection(log_ids):
                    return {'verified': False, 'reason': 'indexed trace/log correlation mismatch'}
                return {'verified': True, 'trace_documents': 1, 'correlated_log_documents': 1, 'metric_document_found': True}
        except urllib.error.HTTPError as error:
            if error.code in (401, 403):
                return {'verified': False, 'reason': 'existing ingestion key has no Elasticsearch read access'}
            if error.code not in (404, 429, 500, 502, 503, 504):
                return {'verified': False, 'reason': 'Elasticsearch query rejected'}
        except (OSError, ValueError, KeyError):
            return {'verified': False, 'reason': 'Elasticsearch lookup failed; details suppressed'}
        if time.monotonic() >= until:
            return {'verified': False, 'reason': 'indexed data not found within 90 seconds'}
        time.sleep(2)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env', type=Path, default=Path(__file__).with_name('.env'))
    parser.add_argument('--protocol', action='append', choices=('http/protobuf', 'grpc', 'http/json'), help='repeat to test multiple protocols; default http/protobuf')
    parser.add_argument('--target', choices=('managed-otlp', 'apm'), help='explicit route; by default resolve from the Stack version')
    parser.add_argument('--elastic-version', help='expected Stack major.minor or major.minor.patch; Hosted 9.5 requires patch')
    parser.add_argument('--deployment', choices=('hosted', 'serverless'), help='default hosted, or ELASTIC_DEPLOYMENT from the file')
    parser.add_argument('--list-profiles', action='store_true', help='list version profiles without credentials or network requests')
    parser.add_argument('--preflight', action='store_true', help='validate config without sending data')
    args = parser.parse_args()
    if args.list_profiles:
        print(json.dumps([resolve_profile(v, d) for v, d in [('8.19.16', 'hosted'), ('9.1.0', 'hosted'), ('9.5.3', 'hosted'), (None, 'serverless')]], indent=2))
        return 0
    try:
        values = settings(args.env, args.target)
    except (OSError, ValueError):
        print('Cloud configuration is missing or incomplete. Fill the local .env using .env.example; no credentials were printed.', file=sys.stderr)
        return 2
    declared = args.elastic_version or values.get('ELASTIC_STACK_VERSION')
    deployment = args.deployment or values.get('ELASTIC_DEPLOYMENT', 'hosted')
    observed = None
    try:
        # Reject invalid combinations without sending credentials anywhere.
        plan = resolve_profile(declared, deployment, args.target)
        if not args.preflight and deployment == 'hosted':
            observed = observe_version(values)
            if declared and observed and not same_version(declared, observed):
                raise CompatibilityError('Configured Elastic version does not match the observed server version; no telemetry sent')
            if observed:
                plan = resolve_profile(observed, deployment, args.target)
        if plan['target'] == 'managed-otlp':
            auth = urllib.parse.unquote(values['OTEL_EXPORTER_OTLP_HEADERS']).lower()
            if not auth.startswith('authorization=apikey '):
                raise CompatibilityError('Managed OTLP requires ApiKey authentication')
    except CompatibilityError as error:
        print(str(error), file=sys.stderr)
        return 2
    plan.update(declared_version=declared, observed_version=observed, version_verified=observed is not None)
    if args.preflight:
        print(json.dumps({'result': 'PREFLIGHT_PASS', 'compatibility': plan, 'telemetry_sent': False}))
        return 0
    target = plan['target']
    env = {k: v for k, v in os.environ.items() if not k.startswith(PREFIXES)}
    env.update({k: v for k, v in values.items() if k.startswith(PREFIXES)})
    protocols = args.protocol or [values.get('OTEL_EXPORTER_OTLP_PROTOCOL', 'http/protobuf')]
    if any(p not in ('http/protobuf', 'grpc', 'http/json') for p in protocols):
        print('Unsupported Cloud check protocol.', file=sys.stderr)
        return 2
    failed = False
    for protocol in protocols:
        env['OTEL_EXPORTER_OTLP_PROTOCOL'] = protocol
        try:
            started = datetime.now(timezone.utc).isoformat()
            process = subprocess.run(['go', 'run', './examples/cloudcheck'], cwd=REPO, env=env, capture_output=True, text=True, timeout=90)
            # Never echo raw stdout/stderr from exporters: URLs or payloads can appear.
            data = json.loads(process.stdout) if process.returncode == 0 else None
            if not isinstance(data, dict) or not isinstance(data.get('diagnostics'), dict):
                raise ValueError('check failed')
            diag = data['diagnostics']
            signals = {}
            for name in ('Traces', 'Metrics', 'Logs'):
                d = diag[name]
                signals[name] = {k: d[k] for k in ('SuccessfulBatches', 'FailedBatches', 'SuccessfulItems')}
                if not d['SuccessfulItems'] or d['FailedBatches']:
                    raise ValueError('signal failed')
            tid = data['trace_id']
            if len(tid) != 32 or any(c not in '0123456789abcdef' for c in tid):
                raise ValueError('invalid trace ID')
            storage = indexed(values, target, tid, started)
            print(json.dumps({'target': target, 'compatibility': plan, 'protocol': protocol, 'result': 'PASS', 'trace_id': tid, 'signals': signals, 'indexed_data_verified': storage['verified'], 'storage': storage}))
            if (values.get('ELASTICSEARCH_URL') or values.get('KIBANA_URL')) and not storage['verified']:
                failed = True
        except (subprocess.TimeoutExpired, OSError, ValueError, KeyError, TypeError):
            print(json.dumps({'target': target, 'compatibility': plan, 'protocol': protocol, 'result': 'FAIL', 'detail': 'check endpoint, API Key privileges, TLS and protocol support locally; raw exporter output suppressed'}))
            failed = True
    return int(failed)


if __name__ == '__main__':
    sys.exit(main())
