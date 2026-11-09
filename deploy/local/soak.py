#!/usr/bin/env python3
"""Opt-in real Collector/network recovery soak. Keeps secrets out of reports.

Stops/restarts only this compose project's EDOT Collector; ES/Kibana stay up.
Temporary raw TCP relays allow a second, independent network disconnection.
"""
import argparse
import json
import os
from pathlib import Path
import select
import socket
import socketserver
import subprocess
import tempfile
import threading
import time
import uuid

from verify import LOCAL, REPO, request, wait_for


class Relay(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, port):
        self.target = port
        self.enabled = threading.Event()
        self.enabled.set()
        self.lock = threading.Lock()
        self.connections = set()
        super().__init__(('127.0.0.1', 0), Handler)

    def disconnect(self):
        self.enabled.clear()
        with self.lock:
            for conn in tuple(self.connections):
                try:
                    conn.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass


class Handler(socketserver.BaseRequestHandler):
    def handle(self):
        relay = self.server
        if not relay.enabled.is_set():
            return
        upstream = None
        try:
            upstream = socket.create_connection(('127.0.0.1', relay.target), timeout=2)
            pair = (self.request, upstream)
            with relay.lock:
                relay.connections.update(pair)
            for conn in pair:
                conn.settimeout(2)
            while relay.enabled.is_set():
                ready, _, _ = select.select(pair, [], [], .5)
                for src in ready:
                    data = src.recv(65536)
                    if not data:
                        return
                    (upstream if src is self.request else self.request).sendall(data)
        except (OSError, ValueError):
            pass
        finally:
            with relay.lock:
                relay.connections.discard(self.request)
                if upstream is not None:
                    relay.connections.discard(upstream)
            if upstream is not None:
                upstream.close()


def compose(*args):
    subprocess.run(['docker', 'compose', '--project-directory', str(LOCAL), *args],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=120)


def recovery_checks(samples, events):
    checks = {}
    for outage, restored, next_outage in (
        ('collector_down', 'collector_recovered', 'network_down'),
        ('network_down', 'network_recovered', None),
    ):
        begin = next(e['seconds'] for e in events if e['event'] == outage)
        end = next(e['seconds'] for e in events if e['event'] == restored)
        stop = next(e['seconds'] for e in events if e['event'] == next_outage) if next_outage else float('inf')
        before = [s for s in samples if s['seconds'] < begin][-1]
        during = [s for s in samples if s['seconds'] < end][-1]
        after = next(s for s in samples if s['seconds'] > end+10)
        later = [s for s in samples if s['seconds'] < stop][-1]
        for signal in ('Traces', 'Metrics', 'Logs'):
            if during['diagnostics'][signal]['FailedBatches'] <= before['diagnostics'][signal]['FailedBatches']:
                raise RuntimeError('missing signal failures during '+outage)
            if later['diagnostics'][signal]['SuccessfulBatches'] <= after['diagnostics'][signal]['SuccessfulBatches']:
                raise RuntimeError('missing signal success after '+restored)
        checks[restored] = True
    return checks


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--duration', type=int, default=300, help='seconds, >=300; use 3600 for hour soak')
    parser.add_argument('--rate', type=int, default=25, help='requests/second per protocol')
    parser.add_argument('--output', type=Path, required=True, help='JSON report path')
    args = parser.parse_args()
    if args.duration < 300 or not 1 <= args.rate <= 10000:
        parser.error('duration >= 300 and rate 1..10000 required')
    request('/_cluster/health')
    run_id = uuid.uuid4().hex[:10]
    reports, processes, files, relays, events = {}, [], [], [], []
    collector_stopped = False
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='edot-soak-') as temp:
        temp = Path(temp)
        binary = temp/'soak'
        subprocess.run(['go', 'build', '-o', str(binary), './examples/soak'], cwd=REPO, check=True)
        phase = temp/'phase'
        phase.write_text('warmup')
        for port in (4317, 4318):
            relay = Relay(port)
            threading.Thread(target=relay.serve_forever, daemon=True).start()
            relays.append(relay)
        started = time.monotonic()

        def event(name):
            phase.write_text(name)
            item = {'event': name, 'seconds': round(time.monotonic()-started, 2)}
            events.append(item)
            print(json.dumps(item), flush=True)

        def pause(seconds):
            until = time.monotonic()+seconds
            while time.monotonic() < until:
                if any(p.poll() is not None for p in processes):
                    raise RuntimeError('workload exited before soak completed')
                time.sleep(min(1, max(0, until-time.monotonic())))

        try:
            for protocol in ('http/protobuf', 'grpc', 'http/json'):
                name = 'edot-go-soak-'+run_id+'-'+protocol.replace('/', '-')
                env = {k: v for k, v in os.environ.items() if not k.startswith(('OTEL_', 'EDOT_GO_', 'ELASTIC_OTEL_JAVA_'))}
                relay = relays[0 if protocol == 'grpc' else 1]
                env.update(OTEL_SERVICE_NAME=name, OTEL_EXPORTER_OTLP_PROTOCOL=protocol,
                           OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:'+str(relay.server_address[1]),
                           OTEL_EXPORTER_OTLP_TIMEOUT='2000', OTEL_BSP_SCHEDULE_DELAY='1000',
                           OTEL_BSP_EXPORT_TIMEOUT='2500', OTEL_BLRP_SCHEDULE_DELAY='1000',
                           OTEL_BLRP_EXPORT_TIMEOUT='2500', OTEL_METRIC_EXPORT_INTERVAL='1000',
                           OTEL_METRIC_EXPORT_TIMEOUT='2500')
                out = open(temp/(protocol.replace('/', '-')+'.jsonl'), 'w+')
                err = open(temp/(protocol.replace('/', '-')+'.stderr'), 'w+')
                files.extend([out, err])
                p = subprocess.Popen([str(binary), '-duration', str(args.duration)+'s', '-rate', str(args.rate), '-phase-file', str(phase)],
                                     env=env, stdout=out, stderr=err)
                processes.append(p)
                reports[protocol] = {'service': name, 'file': out, 'process': p}
            event('warmup')
            pause(60)
            event('collector_down')
            # Set the restoration guard before mutating external state.
            collector_stopped = True
            compose('stop', '-t', '5', 'edot-collector')
            pause(30)
            compose('up', '-d', '--wait', '--wait-timeout', '90', 'edot-collector')
            collector_stopped = False
            event('collector_recovered')
            pause(60)
            event('network_down')
            for relay in relays:
                relay.disconnect()
            pause(30)
            for relay in relays:
                relay.enabled.set()
            event('network_recovered')
            for p in processes:
                if p.wait(timeout=args.duration+30) != 0:
                    raise RuntimeError('workload failed; raw exporter errors retained only in temporary private logs')
            summary = {}
            for protocol, data in reports.items():
                data['file'].seek(0)
                samples = [json.loads(line) for line in data['file']]
                final = samples[-1]
                baseline = [s for s in samples if 20 <= s['seconds'] <= 50][-1]
                if final['kind'] != 'final':
                    raise RuntimeError('missing final sample')
                heap_growth = final['heap_bytes']-baseline['heap_bytes']
                goroutine_growth = final['goroutines']-baseline['goroutines']
                if heap_growth > 32*1024*1024 or goroutine_growth > 20:
                    raise RuntimeError('resource growth exceeded soak guardrails')
                if final['emitted'] < args.duration*args.rate*.9 or final['max_emit_micros'] > 250000:
                    raise RuntimeError('application throughput/latency guardrail failed')
                recovered = recovery_checks(samples, events)
                tid = final['trace_ids']['network_recovered']
                def indexed():
                    records = []
                    for stream in ('traces', 'logs'):
                        hits = request('/'+stream+'-generic.otel-default/_search', {'size': 2, 'query': {'term': {'trace.id': tid}}, '_source': False, 'fields': ['span.id']})['hits']['hits']
                        if len(hits) != 1:
                            return None
                        records.append(hits[0]['fields']['span.id'][0])
                    return records if records[0] == records[1] else None
                wait_for(indexed, 60)
                def metric_indexed():
                    return request('/metrics-generic.otel-default/_search', {'size': 0, 'query': {'bool': {'filter': [{'term': {'service.name': data['service']}}, {'exists': {'field': 'metrics.soak.requests'}}]}}})['hits']['total']['value']
                wait_for(metric_indexed, 60)
                summary[protocol] = {'service': data['service'], 'emitted': final['emitted'], 'heap_growth_bytes': heap_growth,
                                     'goroutine_growth': goroutine_growth, 'max_emit_micros': final['max_emit_micros'],
                                     'recoveries': recovered, 'post_recovery_trace_id': tid, 'post_recovery_indexed': True, 'samples': samples}
            result = {'run_id': run_id, 'duration_seconds': args.duration, 'rate_per_protocol': args.rate, 'events': events,
                      'protocols': summary, 'result': 'PASS', 'scope': 'bounded local soak, not proof of absence of leaks or zero-loss delivery'}
            args.output.write_text(json.dumps(result, indent=2)+'\n')
            print(json.dumps({'result': 'PASS', 'run_id': run_id, 'report': str(args.output)}))
        except Exception as error:
            available = {}
            for protocol, data in reports.items():
                data['file'].flush()
                data['file'].seek(0)
                available[protocol] = []
                for line in data['file']:
                    try:
                        available[protocol].append(json.loads(line))
                    except ValueError:
                        pass
            args.output.write_text(json.dumps({'run_id': run_id, 'result': 'FAIL', 'error_type': type(error).__name__, 'events': events, 'samples': available}, indent=2)+'\n')
            raise
        finally:
            for relay in relays:
                relay.enabled.set()
            restore_failed = False
            if collector_stopped:
                try:
                    compose('up', '-d', '--wait', '--wait-timeout', '90', 'edot-collector')
                except Exception:
                    restore_failed = True
            for p in processes:
                if p.poll() is None:
                    p.terminate()
                    try:
                        p.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        p.kill()
                        p.wait()
            for relay in relays:
                relay.disconnect()
                relay.shutdown()
                relay.server_close()
            for file in files:
                file.close()
            if restore_failed:
                raise RuntimeError('Collector restoration failed; run deploy/local/start.sh')


if __name__ == '__main__':
    main()
