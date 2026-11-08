#!/usr/bin/env python3
"""Generate local-only secrets without printing or committing them."""
from pathlib import Path
import os,platform,secrets

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
