"""Version-aware ingestion profiles, separate from the version-neutral Go SDK."""
import re


class CompatibilityError(ValueError):
    """Safe, credential-free configuration error."""


def parse_version(text):
    if not isinstance(text, str) or not re.fullmatch(r'[0-9]+\.[0-9]+(?:\.[0-9]+)?', text):
        raise CompatibilityError('Elastic version must be major.minor or major.minor.patch')
    parts = tuple(int(x) for x in text.split('.'))
    return parts


def same_version(declared, observed):
    wanted, actual = parse_version(declared), parse_version(observed)
    return wanted == actual[:len(wanted)]


def resolve_profile(version=None, deployment='hosted', target=None):
    if deployment not in ('hosted', 'serverless'):
        raise CompatibilityError('deployment must be hosted or serverless')
    if target not in (None, 'apm', 'managed-otlp'):
        raise CompatibilityError('target must be apm or managed-otlp')
    result = {'deployment': deployment, 'elastic_version': version, 'target': target,
              'validated_cloud_versions': [], 'notes': []}
    if deployment == 'serverless':
        if version:
            raise CompatibilityError('Serverless has no user-selected Elastic Stack version; omit elastic-version')
        if target == 'apm':
            raise CompatibilityError('Serverless profile requires managed-otlp')
        result.update(profile='serverless-managed', target='managed-otlp', metric_ingestion_expected='native', data_model='otel-native')
    elif not version:
        target = target or 'managed-otlp'
        result.update(profile='unversioned-'+target, target=target, metric_ingestion_expected='unknown', data_model='ecs' if target == 'apm' else 'otel-native')
        result['notes'].append('No Stack version verified; ingestion target is explicit or the legacy default.')
    else:
        parsed = parse_version(version)
        major, minor = parsed[:2]
        if major not in (8, 9):
            raise CompatibilityError('This compatibility matrix covers Elastic Stack 8.x and 9.x only')
        if major == 8:
            if target == 'managed-otlp':
                raise CompatibilityError('Hosted 8.x cannot use the 9.x Managed OTLP profile; select apm')
            result.update(profile='8.x-apm', target='apm', metric_ingestion_expected='apm-translation', data_model='ecs', validated_cloud_versions=['8.19.16'])
            result['notes'].append('APM is the existing upstream-OTLP compatibility path; this unofficial SDK is not official EDOT support.')
        elif target == 'apm':
            result.update(profile='9.x-apm-explicit', target='apm', metric_ingestion_expected='apm-translation', data_model='ecs')
            result['notes'].append('Explicit legacy APM route; Managed OTLP is the normal Hosted 9.x profile. Cloud 9.x APM was not tested.')
        elif parsed[:2] == (9, 5) and len(parsed) == 2:
            raise CompatibilityError('Hosted 9.5 needs a patch version to distinguish 9.5.0-9.5.2 from 9.5.3+')
        elif (major, minor, parsed[2] if len(parsed) == 3 else 0) < (9, 5, 3):
            result.update(profile='9.0-9.5.2-managed', target='managed-otlp', metric_ingestion_expected='bulk-compatibility', data_model='otel-native')
            result['notes'].append('Use DELTA for histograms on the compatibility route; cumulative histograms are not supported on this route.')
        else:
            result.update(profile='9.5.3+-managed', target='managed-otlp', metric_ingestion_expected='native', data_model='otel-native')
        if major == 9:
            result['notes'].append('Expected route is inferred from documentation, not observed ingestion internals. Documented capability; Managed OTLP availability must be checked in the deployment. No Cloud 9.x integration run is claimed.')
    result['index_patterns'] = ['traces-apm*', 'logs-apm*', 'metrics-apm*'] if result['target'] == 'apm' else ['traces-*.otel-*', 'logs-*.otel-*', 'metrics-*.otel-*']
    return result
