#!/usr/bin/env python3
"""Generate local-only secrets without printing or committing them."""
from pathlib import Path
import json, os, platform, re, secrets, tempfile

from verify import load_settings

root=Path(__file__).resolve().parent
target=root/'.env'
if target.exists():
    print('Keeping existing local .env')
else:
    version='9.5.6'
    if platform.machine().lower() in ('arm64','aarch64'):version+='-arm64'
    fields={
        'START_LOCAL_VERSION':'0.14.0','ES_LOCAL_VERSION':version,
        'ES_LOCAL_CONTAINER_NAME':'es-local-dev-edot-go-local',
        'KIBANA_LOCAL_CONTAINER_NAME':'kibana-local-dev-edot-go-local',
        'KIBANA_LOCAL_SETTINGS_CONTAINER_NAME':'kibana-local-settings-edot-go-local',
        'EDOT_LOCAL_CONTAINER_NAME':'edot-collector-edot-go-local',
        'ES_LOCAL_PASSWORD':secrets.token_hex(24),'KIBANA_LOCAL_PASSWORD':secrets.token_hex(24),
        'KIBANA_ENCRYPTION_KEY':secrets.token_hex(32),
        'ES_LOCAL_PORT':'9200','KIBANA_LOCAL_PORT':'5601',
        'ES_LOCAL_URL':'http://127.0.0.1:9200','ES_LOCAL_DISK_SPACE_REQUIRED':'1gb',
        'ES_LOCAL_JAVA_OPTS':'-Xms2g -Xmx2g',
    }
    fd=os.open(target,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,'w') as f:
        for key,value in fields.items():f.write(f'{key}="{value}"\n')
    print('Generated local .env (mode 0600); credentials were not printed')

# Keep the existing password for persistent Elasticsearch volumes.
values = load_settings()
elastic = values['ES_LOCAL_PASSWORD']
kibana = values['KIBANA_LOCAL_PASSWORD']
if not all(re.fullmatch(r'[A-Za-z0-9_-]+', value) for value in (elastic, kibana)):
    raise SystemExit('Local passwords must use letters, digits, underscore or hyphen')
secret_dir = root / '.env.secrets'
secret_dir.mkdir(mode=0o700, exist_ok=True)
if secret_dir.is_symlink() or secret_dir.stat().st_mode & 0o077:
    raise SystemExit('Local secret directory must be private and must not be a symlink')

files = {
    'elastic-password': elastic,
    'elastic-netrc': f'machine elasticsearch login elastic password {elastic}\n',
    'kibana-config': json.dumps({
        'server.host': '0.0.0.0',
        'server.shutdownTimeout': '5s',
        'elasticsearch.password': kibana,
        'xpack.encryptedSavedObjects.encryptionKey': values['KIBANA_ENCRYPTION_KEY'],
    }),
    'kibana-password-json': json.dumps({'password': kibana}),
    # JSON is valid YAML. The Collector merges this fragment with its base config.
    'collector-auth': json.dumps({
        'extensions': {'apmconfig': {'source': {'elasticsearch': {
            'endpoint': f'http://elastic:{elastic}@elasticsearch:9200',
        }}}},
        'exporters': {'elasticsearch/otel': {'password': elastic}},
    }),
}
for name, content in files.items():
    destination = secret_dir / name
    if destination.is_file() and not destination.is_symlink() and destination.read_text() == content:
        if destination.stat().st_mode & 0o077:
            raise SystemExit('Local secret files must be private')
        continue
    fd, temporary = tempfile.mkstemp(dir=secret_dir)
    try:
        with os.fdopen(fd, 'w') as output:
            output.write(content)
        os.replace(temporary, secret_dir / name)
    finally:
        Path(temporary).unlink(missing_ok=True)
