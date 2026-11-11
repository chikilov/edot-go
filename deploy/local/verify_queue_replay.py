"""Isolated Docker Desktop persistent-queue recovery smoke test with synthetic data."""
import json, platform, subprocess, tempfile, threading, time, urllib.request, uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

image = 'docker.elastic.co/elastic-agent/elastic-otel-collector:9.5.6'
if platform.machine().lower() in ('arm64', 'aarch64'):
    image += '-arm64'
name = 'edot-replay-' + uuid.uuid4().hex[:8]
failed, received, online = threading.Event(), threading.Event(), threading.Event()

class Backend(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers['Content-Length']))
        if b'replay-fixture' in body:
            (received if online.is_set() else failed).set()
        self.send_response(200 if online.is_set() else 503)
        self.end_headers()

    def log_message(self, *_):
        pass

def docker(*args):
    return subprocess.run(['docker', *args], check=True, capture_output=True, text=True).stdout.strip()

backend = ThreadingHTTPServer(('127.0.0.1', 0), Backend)
threading.Thread(target=backend.serve_forever, daemon=True).start()
try:
    with tempfile.TemporaryDirectory() as directory:
        config = Path(directory) / 'collector.yaml'
        config.write_text(f'''
extensions:
  file_storage:
    directory: /queue
    fsync: true
receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
exporters:
  otlphttp:
    endpoint: http://host.docker.internal:{backend.server_port}
    compression: none
    sending_queue:
      storage: file_storage
    retry_on_failure:
      initial_interval: 10s
service:
  telemetry:
    metrics:
      readers:
        - pull:
            exporter:
              prometheus:
                host: 0.0.0.0
                port: 8888
  extensions: [file_storage]
  pipelines:
    logs:
      receivers: [otlp]
      exporters: [otlphttp]
''')
        docker('volume', 'create', name)
        docker('run', '--rm', '--user', '0:0', '-v', name+':/queue', '--entrypoint', 'sh', image,
               '-ec', 'chown 1000:1000 /queue && chmod 0700 /queue')
        docker('run', '-d', '--name', name, '-p', '127.0.0.1::4318', '-p', '127.0.0.1::8888', '-v', name+':/queue',
               '-v', str(config)+':/test.yaml:ro', image, '--config=/test.yaml')
        port = docker('port', name, '4318/tcp').rsplit(':', 1)[1]
        payload = {'resourceLogs': [{'scopeLogs': [{'logRecords': [{'body': {'stringValue': 'replay-fixture'}}]}]}]}
        req = urllib.request.Request('http://127.0.0.1:'+port+'/v1/logs', data=json.dumps(payload).encode(),
                                     headers={'Content-Type': 'application/json'})
        for attempt in range(30):
            try:
                with urllib.request.urlopen(req, timeout=2) as response:
                    assert response.status == 200
                break
            except OSError:
                if attempt == 29:
                    raise
                time.sleep(1)
        assert failed.wait(15), 'backend did not receive the original request'
        import sys
        sys.path.insert(0, str(Path(__file__).resolve().parent))
        from queue_status import summarize
        metrics_port = docker('port', name, '8888/tcp').rsplit(':', 1)[1]
        with urllib.request.urlopen('http://127.0.0.1:'+metrics_port+'/metrics', timeout=5) as response:
            snapshot = summarize(response.read(4*1024*1024).decode())
        assert any(row['queue_observed'] for row in snapshot['exporters']), 'queue metrics missing'
        print(json.dumps(snapshot), flush=True)
        docker('kill', name)
        online.set()
        docker('start', name)
        assert received.wait(25), 'queued request did not replay after restart'
        print('Persistent queue replayed the accepted log after SIGKILL and restart')
finally:
    subprocess.run(['docker', 'rm', '-f', name], capture_output=True)
    subprocess.run(['docker', 'volume', 'rm', name], capture_output=True)
    backend.shutdown()
