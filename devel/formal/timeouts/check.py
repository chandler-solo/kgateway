"""Validate direct timeout observations; no instrumentation or Go refinement theorem."""
import copy
import hashlib
import json
from pathlib import Path
import shutil
import tempfile

PREFIX = 'type.googleapis.com/envoy.config.'
CDS = PREFIX + 'cluster.v3.Cluster'
EDS = PREFIX + 'endpoint.v3.ClusterLoadAssignment'
LDS = PREFIX + 'listener.v3.Listener'
RDS = PREFIX + 'route.v3.RouteConfiguration'
TYPES = {CDS, EDS, LDS, RDS}
FIELDS = {
    'profile': 'scenario snapshot_cache ordered disable_panic',
    'case': 'name fetch_timeout_ns eds_cache',
    'cache-set-result': 'phase error',
    'request': 'type version nonce names error',
    'response-callback': 'type version nonce',
    'send-attempt': 'type version nonce resources',
    'send-result': 'type version nonce status',
    'observation': 'phase config sha256 stats stats_sha256 ready status body',
    'end': 'failed',
}
PHASES = {
    'missing-cds': ['waiting', 'still-waiting', 'recovered'],
    'missing-lds': ['waiting', 'still-waiting', 'recovered'],
    'disabled-eds': ['waiting', 'still-waiting', 'recovered'],
    'local-cycle': ['waiting', 'still-waiting'],
    'missing-eds': ['waiting', 'degraded-active', 'recovered'],
    'missing-rds': ['waiting', 'degraded-active', 'recovered'],
    'explicit-empty': ['degraded-active', 'recovered'],
    'cached-rewarm': ['baseline', 'rewarming', 'cached-active'],
}


def require(value, message):
    if not value:
        raise ValueError(message)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def bootstrap_profile(bootstrap):
    dynamic = bootstrap['dynamic_resources']
    local = bootstrap['cluster_manager']['local_cluster_name']
    cluster = next(c for c in bootstrap['static_resources']['clusters'] if c['name'] == local)
    return {'cds': dynamic['cds_config'], 'lds': dynamic['lds_config'],
            'local_name': local, 'local_eds': cluster['eds_cluster_config']['eds_config'],
            'eds_cache': any(layer.get('static_layer', {}).get('envoy.restart_features.use_eds_cache_for_ads') is True
                             for layer in bootstrap['layered_runtime']['layers'])}


def check_bootstrap(bootstrap, case):
    profile = bootstrap_profile(bootstrap)
    for typ in ['cds', 'lds']:
        require(profile[typ] == {'ads': {}, 'resource_api_version': 'V3', 'initial_fetch_timeout': '0s'},
                'CDS/LDS timeout profile differs from explicit zero')
    require(profile['local_name'] == 'formal.formal', 'wrong bootstrap assignment identity')
    expected = '0s' if case == 'local-cycle' else '1s'
    require(profile['local_eds'] == {'ads': {}, 'resource_api_version': 'V3', 'initial_fetch_timeout': expected},
            'wrong local EDS timeout')
    require(profile['eds_cache'], 'EDS cache disabled')
    return profile


def artifact(directory, name, expected_hash):
    require(Path(name).name == name, 'unsafe artifact path')
    path = directory / name
    require(path.is_file() and digest(path) == expected_hash, 'artifact hash mismatch')
    return json.loads(path.read_text())


def admin_state(dump):
    configs = dump['configs']
    active = {c['cluster']['name']: c['cluster'] for cfg in configs for c in cfg.get('dynamic_active_clusters', [])}
    warming = {c['cluster']['name']: c['cluster'] for cfg in configs for c in cfg.get('dynamic_warming_clusters', [])}
    listeners = {c['name']: c for cfg in configs for c in cfg.get('dynamic_listeners', [])}
    routes = [r['route_config'] for cfg in configs for r in cfg.get('dynamic_route_configs', [])]
    endpoints = {r['endpoint_config']['cluster_name']: r['endpoint_config']
                 for cfg in configs for r in cfg.get('dynamic_endpoint_configs', [])}
    return active, warming, listeners, routes, endpoints


def hosts(assignment):
    return sum(len(locality.get('lb_endpoints', [])) for locality in assignment.get('endpoints', []))


def route_uses_a(routes):
    return any(r.get('route', {}).get('cluster') == 'a' for rc in routes
               for v in rc.get('virtual_hosts', []) for r in v.get('routes', []))


def validate(events, directory, case, workers):
    require(case in PHASES and events, 'unknown case or empty trace')
    last = -1
    for seq, e in enumerate(events, 1):
        kind = e.get('event')
        require(kind in FIELDS, 'unknown event kind')
        require(set(e) == set(FIELDS[kind].split()) | {'event', 'schema', 'seq', 'elapsed_ns'}, 'unknown or missing event fields')
        require(e['schema'] == 1 and e['seq'] == seq and type(e['elapsed_ns']) is int and e['elapsed_ns'] >= last,
                'invalid event sequence or time')
        last = e['elapsed_ns']
    def unique(kind):
        values = [e for e in events if e['event'] == kind]
        require(len(values) == 1, 'missing or repeated ' + kind)
        return values[0]
    profile, subject, end = unique('profile'), unique('case'), unique('end')
    require(profile['scenario'] == 'timeout-profile' and profile['snapshot_cache'] is True
            and profile['ordered'] is True and profile['disable_panic'] is False, 'wrong server profile')
    require(subject == {**subject, 'name': case, 'fetch_timeout_ns': -1000000000, 'eds_cache': True}, 'wrong timeout case')
    require(events[-1] is end and end['failed'] is False, 'failed or incomplete driver')
    actual = json.loads((directory / 'bootstrap.json').read_text())
    rendered = json.loads((directory / 'rendered-bootstrap.json').read_text())
    check_bootstrap(actual, case)
    check_bootstrap(rendered, 'missing-eds')
    require(actual['dynamic_resources']['cds_config'] == rendered['dynamic_resources']['cds_config']
            and actual['dynamic_resources']['lds_config'] == rendered['dynamic_resources']['lds_config'], 'rendered parent sources changed')
    info = json.loads((directory / 'server-info.json').read_text())
    require(info['command_line_options']['concurrency'] == workers, 'wrong worker count')
    attempts, sent, callbacks = {}, {}, {}
    for e in events:
        kind = e['event']
        if 'type' in e:
            require(e['type'] in TYPES, 'unknown resource type')
        if kind == 'response-callback':
            key = (e['type'], e['nonce'])
            require(key not in callbacks, 'reused callback nonce')
            callbacks[key] = e
        if kind == 'send-attempt':
            key = (e['type'], e['nonce'])
            require(key not in attempts and key in callbacks and callbacks[key]['version'] == e['version'], 'uncorrelated send')
            require(isinstance(e['resources'], list) and e['resources'], 'missing response payloads')
            require(all(r['@type'] == e['type'] for r in e['resources']), 'wrong response resource type')
            attempts[key] = e
            for r in e['resources']:
                if e['type'] == CDS:
                    source = r['edsClusterConfig']['edsConfig']
                    expected = {'ads': {}, 'resourceApiVersion': 'V3'}
                    if case == 'disabled-eds': expected['initialFetchTimeout'] = '0s'
                    require(source == expected, 'generated EDS timeout changed')
                if e['type'] == LDS:
                    for chain in r['filterChains']:
                        for f in chain['filters']:
                            source = f.get('typedConfig', {}).get('rds', {}).get('configSource')
                            if source is not None:
                                require(source == {'ads': {}, 'resourceApiVersion': 'V3'}, 'generated RDS timeout changed')
        if kind == 'send-result':
            key = (e['type'], e['nonce'])
            require(key in attempts and key not in sent and e['status'] == 'sent'
                    and e['version'] == attempts[key]['version'], 'failed or unpaired transport')
            sent[key] = e
        if kind == 'request':
            require(e['error'] is None, 'unexpected NACK')
            if e['nonce']:
                key = (e['type'], e['nonce'])
                require(key in sent and e['version'] == sent[key]['version'], 'ACK does not match prior transport')
    require(attempts.keys() == sent.keys() == callbacks.keys() and (attempts or case == 'local-cycle'), 'incomplete transport evidence')
    installs = [e for e in events if e['event'] == 'cache-set-result']
    require(installs and all(e['error'] is None for e in installs), 'failed or missing cache installation')
    observations = [e for e in events if e['event'] == 'observation']
    require([e['phase'] for e in observations] == PHASES[case], 'missing, repeated or reordered observation')
    snapshots = {}
    for e in observations:
        require(e['config'] == e['phase'] + '-config.json' and e['stats'] == e['phase'] + '-stats.json', 'wrong observation artifact')
        dump = artifact(directory, e['config'], e['sha256'])
        bootstraps = [c['bootstrap'] for c in dump['configs'] if 'bootstrap' in c]
        require(len(bootstraps) == 1, 'missing independently observed bootstrap')
        check_bootstrap(bootstraps[0], case)
        state = admin_state(dump)
        stats = {r['name']: r['value'] for r in artifact(directory, e['stats'], e['stats_sha256'])['stats']}
        require(stats.get('cluster_manager.cds.init_fetch_timeout', 0) == 0
                and stats.get('listener_manager.lds.init_fetch_timeout', 0) == 0, 'disabled parent expired')
        snapshots[e['phase']] = (e, state, stats)
        if e['phase'] in ('recovered', 'baseline', 'rewarming', 'cached-active'):
            require(e['ready'] == 200 and e['status'] == 200 and e['body'] == 'probe-upstream', 'HTTP forwarding did not corroborate state')
            active, _, listeners, routes, endpoints = state
            require('a' in active and listeners.get('front', {}).get('active_state') and route_uses_a(routes)
                    and hosts(endpoints.get('a', {})) == 1, 'admin does not corroborate forwarding')
    if case in ('missing-cds', 'missing-lds', 'disabled-eds', 'local-cycle'):
        first, _, _ = snapshots['waiting']
        last_obs, state, _ = snapshots['still-waiting']
        require(last_obs['elapsed_ns'] - first['elapsed_ns'] >= 16_000_000_000, 'silence window too short')
        require(first['ready'] != 200 and last_obs['ready'] != 200
                and last_obs['status'] == 0, 'disabled source unexpectedly initialized')
        typ = {'missing-cds': CDS, 'missing-lds': LDS, 'disabled-eds': EDS, 'local-cycle': EDS}[case]
        require(not any(e['event'] == 'send-attempt' and e['type'] == typ and e['seq'] < last_obs['seq'] for e in events), 'withheld type was sent')
        if case == 'local-cycle':
            require(not any(e['event'] == 'request' and e['type'] == CDS and e['seq'] < last_obs['seq'] for e in events), 'local cycle did not block CDS request')
    elif case == 'cached-rewarm':
        baseline, _, initial_stats = snapshots['baseline']
        warming, state, _ = snapshots['rewarming']
        active, final_state, stats = snapshots['cached-active']
        require(state[1].get('a', {}).get('connect_timeout') == '3s' and state[0]['a']['connect_timeout'] == '1s', 'old/new cluster states not distinguished')
        require(final_state[0]['a']['connect_timeout'] == '3s' and 'a' not in final_state[1], 'candidate did not activate')
        require(stats.get('cluster.a.assignment_use_cached', 0) > initial_stats.get('cluster.a.assignment_use_cached', 0)
                and stats.get('cluster.a.init_fetch_timeout', 0) > initial_stats.get('cluster.a.init_fetch_timeout', 0), 'cache fallback/timeout not independently observed')
        require(not any(e['event'] == 'send-attempt' and e['type'] == EDS and e['seq'] > baseline['seq'] for e in events), 'cache fallback received a fresh EDS response')
    else:
        obs, state, stats = snapshots['degraded-active']
        active, warming, listeners, routes, endpoints = state
        require(obs['ready'] == 200 and 'a' in active and 'a' not in warming
                and listeners.get('front', {}).get('active_state'), 'degraded configuration is not active')
        expected = 404 if case == 'missing-rds' else 503
        require(obs['status'] == expected, 'wrong degraded HTTP result')
        typ = RDS if case == 'missing-rds' else EDS
        if case == 'explicit-empty':
            require(stats.get('cluster.a.init_fetch_timeout', 0) == 0, 'explicit empty mistaken for timeout')
            require(any(e['event'] == 'send-attempt' and e['type'] == EDS and e['seq'] < obs['seq']
                        and any(r.get('clusterName') == 'a' and not r.get('endpoints') for r in e['resources']) for e in events), 'no explicit empty CLA delivered')
        else:
            counter = 'http.probe.rds.routes.init_fetch_timeout' if case == 'missing-rds' else 'cluster.a.init_fetch_timeout'
            require(stats.get(counter, 0) > 0, 'no independent fetch-timeout counter')
            require(not any(e['event'] == 'send-attempt' and e['type'] == typ and e['seq'] < obs['seq'] for e in events), 'missing resource was delivered before expiry')
        if case != 'missing-rds':
            require(route_uses_a(routes) and hosts(endpoints.get('a', {})) == 0, 'empty backend state missing')
        else:
            require(not route_uses_a(routes), 'missing RDS unexpectedly contains the route')
    return {'case': case, 'observations': len(observations), 'transport_responses': len(sent),
            'evidence_level': 'controlled cache/gRPC/Envoy admin, counters and HTTP; no verified refinement'}


def mutation_checks(events, directory, case, workers):
    mutations = [
        ('terminal failure', lambda es: es[-1].update(failed=True)),
        ('missing observation', lambda es: es.remove(next(e for e in es if e['event'] == 'observation'))),
        ('wrong HTTP result', lambda es: next(e for e in es if e['event'] == 'observation' and e['phase'] in ('recovered', 'baseline', 'still-waiting')).update(status=404)),
        ('wrong admin hash', lambda es: next(e for e in es if e['event'] == 'observation').update(sha256='wrong')),
        ('invented timer arm', lambda es: es.insert(0, {'event': 'arm-from-wire', 'schema': 1, 'elapsed_ns': 0})),
    ]
    if any(e['event'] == 'send-result' for e in events):
        mutations.append(('missing transport result', lambda es: es.remove(next(e for e in es if e['event'] == 'send-result'))))
    if any(e['event'] == 'request' and e['nonce'] for e in events):
        mutations.append(('wrong ACK nonce', lambda es: next(e for e in es if e['event'] == 'request' and e['nonce']).update(nonce='unrelated')))
    rejected = []
    for name, mutate in mutations:
        changed = copy.deepcopy(events)
        mutate(changed)
        for seq, e in enumerate(changed, 1): e['seq'] = seq
        try: validate(changed, directory, case, workers)
        except (ValueError, KeyError): rejected.append(name)
        else: raise ValueError('checker accepted mutation: ' + name)
    with tempfile.TemporaryDirectory(prefix='timeout-checker-') as tmp:
        dest = Path(tmp)
        for path in directory.iterdir():
            if path.is_file(): shutil.copyfile(path, dest / path.name)
        boot = json.loads((dest / 'bootstrap.json').read_text())
        del boot['dynamic_resources']['cds_config']['initial_fetch_timeout']
        (dest / 'bootstrap.json').write_text(json.dumps(boot))
        try: validate(events, dest, case, workers)
        except ValueError as error:
            require('CDS/LDS' in str(error), 'bootstrap mutation failed for unrelated reason')
            rejected.append('CDS silently defaulted to 15s')
        else: raise ValueError('checker accepted wrong parent timeout')
    # Semantic corruptions update artifact hashes too, so digest checks alone
    # cannot satisfy these negative controls.
    with tempfile.TemporaryDirectory(prefix='timeout-semantics-') as tmp:
        dest = Path(tmp)
        for path in directory.iterdir():
            if path.is_file(): shutil.copyfile(path, dest / path.name)
        changed = copy.deepcopy(events)
        obs = next(e for e in changed if e['event'] == 'observation')
        path = dest / obs['config']
        dump = json.loads(path.read_text())
        for cfg in dump['configs']:
            if 'bootstrap' in cfg:
                cfg['bootstrap']['dynamic_resources']['lds_config']['initial_fetch_timeout'] = '15s'
        path.write_text(json.dumps(dump))
        obs['sha256'] = digest(path)
        try: validate(changed, dest, case, workers)
        except ValueError as error:
            require('CDS/LDS' in str(error), 'admin mutation failed for unrelated reason')
            rejected.append('wrong observed bootstrap with recomputed hash')
        else: raise ValueError('checker inferred application from HTTP alone')
        for path in directory.iterdir():
            if path.is_file(): shutil.copyfile(path, dest / path.name)
        changed = copy.deepcopy(events)
        obs = next(e for e in changed if e['event'] == 'observation' and e['phase'] == PHASES[case][-1 if case == 'cached-rewarm' else -2])
        path = dest / obs['stats']
        stats = json.loads(path.read_text())
        key, value = 'cluster_manager.cds.init_fetch_timeout', 1
        if case in ('missing-eds', 'cached-rewarm'): key, value = 'cluster.a.init_fetch_timeout', 0
        if case == 'missing-rds': key, value = 'http.probe.rds.routes.init_fetch_timeout', 0
        for row in stats['stats']:
            if row['name'] == key: row['value'] = value
        path.write_text(json.dumps(stats))
        obs['stats_sha256'] = digest(path)
        try: validate(changed, dest, case, workers)
        except ValueError as error:
            require('timeout' in str(error) or 'expired' in str(error), 'counter mutation failed for unrelated reason')
            rejected.append('wrong timeout counter with recomputed hash')
        else: raise ValueError('checker inferred timeout from elapsed time alone')
    return rejected
