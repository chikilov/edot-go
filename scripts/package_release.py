#!/usr/bin/env python3
"""Package committed source only; private env files/untracked workspace stay out."""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
import zipfile

REPO = Path(__file__).resolve().parents[1]


def git(*args):
    return subprocess.check_output(['git', '-C', str(REPO), *args])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ref', default='HEAD')
    parser.add_argument('--output-dir', required=True, type=Path)
    args = parser.parse_args()
    commit = git('rev-parse', '--verify', args.ref+'^{commit}').decode().strip()
    source = git('show', commit+':sdk.go').decode()
    match = re.search(r'^const Version = "(\d+\.\d+\.\d+)"$', source, re.M)
    if not match:
        raise SystemExit('could not determine committed SDK version')
    version = 'v'+match.group(1)
    if re.fullmatch(r'v\d+\.\d+\.\d+', args.ref) and args.ref != version:
        raise SystemExit('tag name does not match SDK Version')
    go_source = git('show', commit+':go.mod').decode()
    go_version = re.search(r'^go (\S+)$', go_source, re.M).group(1)
    prefix = 'edot-go-'+version+'/'
    # Archive from the immutable commit, never from the live working directory.
    tar = git('archive', '--format=tar', '--prefix='+prefix, commit)
    zipped = git('archive', '--format=zip', '--prefix='+prefix, commit)
    with tarfile.open(fileobj=io.BytesIO(tar)) as archive:
        names = [entry.name for entry in archive.getmembers()]
    with zipfile.ZipFile(io.BytesIO(zipped)) as archive:
        if sorted(name.rstrip('/') for name in archive.namelist()) != sorted(name.rstrip('/') for name in names):
            raise SystemExit('archive manifests differ')
    for name in names:
        parts = Path(name).parts
        base = Path(name).name
        private_env = (base.startswith('.env') or base.endswith('.env')) and not base.endswith('.example')
        if private_env or '.idea' in parts or '__pycache__' in parts:
            raise SystemExit('private/development-only file tracked in release commit')
    args.output_dir.mkdir(parents=True, exist_ok=True)
    payloads = {
        'edot-go-'+version+'-source.tar.gz': gzip.compress(tar, mtime=0),
        'edot-go-'+version+'-source.zip': zipped,
        'release.json': (json.dumps({'version': version, 'commit': commit, 'go': '>='+go_version,
                                    'distribution': 'unofficial partial EDOT Java portable SDK port',
                                    'artifact': 'source-only Go library'}, indent=2, sort_keys=True)+'\n').encode(),
    }
    for name, body in payloads.items():
        (args.output_dir/name).write_bytes(body)
    checksum = ''.join(hashlib.sha256(body).hexdigest()+'  '+name+'\n' for name, body in sorted(payloads.items()))
    (args.output_dir/'SHA256SUMS').write_text(checksum)
    print(json.dumps({'version': version, 'commit': commit, 'files': sorted(payloads)+['SHA256SUMS']}))


if __name__ == '__main__':
    main()
