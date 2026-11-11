#!/usr/bin/env python3
"""Regenerate Go sampler fixtures with the pinned Java consistent56 implementation."""
import hashlib
import json
import os
from pathlib import Path
import random
import subprocess
import tempfile
import urllib.request

REPO = Path(__file__).resolve().parents[1]
REFERENCE = REPO / 'testdata' / 'consistent56'


def main():
    metadata = json.loads((REFERENCE / 'reference.json').read_text())
    cases = []
    rng = random.Random(metadata['seed'])
    states = ['', 'th:8', 'th:8;rv:ffffffffffffff;x:keep',
              'th:8;rv:00000000000000', 'th:INVALID;x:keep', 'th:8;',
              'th:', 'th:1;th:f', 'rv:00000000000001', 'rv:invalid;x:keep']
    for ratio in (0, 2**-56, 0.1, 0.25, 0.5, 0.9, 1):
        for parent in (-1, 0, 1):
            for state in states:
                cases.append({'ratio': ratio, 'trace_id': f'{rng.getrandbits(128):032x}',
                              'parent_sampled': None if parent == -1 else bool(parent),
                              'parent_ot': state if parent != -1 else ''})
    with tempfile.TemporaryDirectory(prefix='consistent56-reference-') as directory:
        work = Path(directory)
        for artifact_info in metadata['artifacts']:
            name, expected = artifact_info['file'], artifact_info['sha512']
            if name.startswith('opentelemetry-consistent-sampling-'):
                group = 'io/opentelemetry/contrib'
                artifact, version = 'opentelemetry-consistent-sampling', '1.61.0-alpha'
            else:
                group = 'io/opentelemetry'
                version = metadata['java_otel']
                artifact = name.removesuffix('-' + version + '.jar')
            url = f'https://repo.maven.apache.org/maven2/{group}/{artifact}/{version}/{name}'
            with urllib.request.urlopen(url, timeout=30) as response:
                data = response.read(16 * 1024 * 1024)
            if hashlib.sha512(data).hexdigest() != expected:
                raise ValueError('Java reference artifact checksum mismatch')
            (work / name).write_bytes(data)
        source = work / 'Oracle.java'
        source.write_text((REFERENCE / 'Oracle.java').read_text())
        subprocess.run(['javac', '-cp', str(work / '*'), str(source)], check=True)
        lines = []
        for case in cases:
            parent = case['parent_sampled']
            lines.append('\t'.join((str(case['ratio']), case['trace_id'],
                                    str(-1 if parent is None else int(parent)), case['parent_ot'])))
        output = subprocess.run(['java', '-cp', str(work / '*') + os.pathsep + str(work), 'Oracle'],
                                input='\n'.join(lines) + '\n', capture_output=True,
                                text=True, check=True, timeout=30).stdout.splitlines()
    if len(output) != metadata['cases'] or len(output) != len(cases):
        raise ValueError('Java reference case count mismatch')
    for case, line in zip(cases, output):
        decision, state = line.split('\t')
        case.update(decision=decision, expected_ot=state)
    (REPO / 'testdata' / 'consistent56-java.json').write_text(json.dumps(cases, indent=2) + '\n')
    print(f'Generated {len(cases)} Java consistent56 reference cases')


if __name__ == '__main__':
    main()
