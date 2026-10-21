#!/usr/bin/env python3
"""Run timeout histories on immutable chart source and digest-pinned Envoy images."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile

from check import PHASES, check_bootstrap, digest, mutation_checks, require, validate

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]


def outside_repository(path):
    return path != ROOT and ROOT not in path.parents


def helpers():
    spec = importlib.util.spec_from_file_location('timeout_regressions', HERE.parent / 'regressions/check.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def render(binary, source, dest):
    dest.mkdir(parents=True, exist_ok=True)
    target = dest / 'rendered-bootstrap.json'
    subprocess.run([str(binary), '-render-chart', str(source / 'pkg/kgateway/helm/envoy'),
                    '-render-values', str(HERE / 'render-values.yaml'), '-render-output', str(target)],
                   check=True, timeout=60)
    check_bootstrap(json.loads(target.read_text()), 'missing-eds')
    return target


def main():
    profiles = json.loads((HERE / 'profiles.json').read_text())
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--profile', choices=list(profiles['images']), action='append')
    parser.add_argument('--case', choices=profiles['cases'], action='append')
    parser.add_argument('--workers', choices=(1, 2), type=int, action='append')
    parser.add_argument('--jobs', type=int, default=1)
    parser.add_argument('--artifacts', type=Path)
    args = parser.parse_args()
    out = (args.artifacts or Path(tempfile.mkdtemp(prefix='xds-timeouts-'))).resolve()
    if not outside_repository(out) or args.jobs not in range(1, 5):
        parser.error('artifacts must be outside the repository; jobs must be 1..4')
    out.mkdir(parents=True, exist_ok=True)
    if any(out.iterdir()): parser.error('artifacts must be fresh')
    require(set(profiles['cases']) == set(PHASES), 'runner and checker case inventory differ')
    print('Timeout artifacts: ' + str(out), flush=True)
    binary = out / 'envoyprobe'
    subprocess.run(['make', 'formal-envoyprobe-build', 'FORMAL_ENVOYPROBE_BIN=' + str(binary)], cwd=ROOT, check=True)
    source_dir = out / 'subject'
    source_dir.mkdir()
    source = helpers().materialize(profiles['source'], source_dir)
    for path, expected in profiles['source']['sha256'].items():
        require(digest(source / path) == expected, 'pinned source contract changed: ' + path)
    bootstrap = render(binary, source, out / 'render')
    receipt = {'evidence_level': 'direct pinned cache/server + real Envoy; supplied translated resources and adapted probe transport',
               'tools': {name: subprocess.check_output(command, cwd=ROOT, text=True).strip()
                         for name, command in {'helm': ['helm', 'version', '--short'], 'go': ['go', 'version']}.items()},
               'profiles_sha256': digest(HERE / 'profiles.json'),
               'render_values_sha256': digest(HERE / 'render-values.yaml'),
               'source': profiles['source'], 'archive_sha256': digest(source_dir / 'source.tar.gz'),
               'render_sha256': digest(bootstrap), 'binary_sha256': digest(binary),
               'go_mod_sha256': digest(ROOT / 'go.mod'), 'go_sum_sha256': digest(ROOT / 'go.sum'),
               'probe_sources': {p.name: digest(p) for p in (ROOT / 'devel/formal/cmd/envoyprobe').glob('*.go') if not p.name.endswith('_test.go')},
               'runner_sources': {p.name: digest(p) for p in HERE.glob('*.py')},
               'images': {}, 'runs': [],
               'limits': ['finite observation windows', 'no exact internal timer-arm instrumentation',
                          'no model-to-Go/Envoy refinement proof', 'no full Kubernetes controller execution']}
    jobs = []
    for profile in args.profile or profiles['images']:
        image = profiles['images'][profile]['image']
        with (out / (profile + '-pull.log')).open('w') as log:
            subprocess.run(['docker', 'pull', image], stdout=log, stderr=subprocess.STDOUT, check=True, timeout=240)
        info = json.loads(subprocess.check_output(['docker', 'image', 'inspect', image]))[0]
        receipt['images'][profile] = {'requested': image, 'id': info['Id'], 'digests': info['RepoDigests'], 'architecture': info['Architecture']}
        for workers in args.workers or profiles['workers']:
            for case in args.case or profiles['cases']:
                jobs.append((profile, image, workers, case))
    def execute(job):
        profile, image, workers, case = job
        dest = out / profile / ('workers-' + str(workers)) / case
        dest.mkdir(parents=True)
        command = [str(binary), '-scenario', 'timeout-profile', '-timeout-case', case,
                   '-snapshot-cache', '-ordered', '-eds-cache', '-resource-fetch-timeout=-1s',
                   '-workers', str(workers), '-image', image, '-bootstrap', str(bootstrap), '-out', str(dest)]
        with (dest / 'driver.log').open('w') as log:
            result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, timeout=140)
        require(result.returncode == 0, 'driver failure is not a counterexample: ' + str(dest))
        events = [json.loads(line) for line in (dest / 'wire.jsonl').read_text().splitlines()]
        record = validate(events, dest, case, workers)
        record.update(profile=profile, workers=workers, mutation_checks=mutation_checks(events, dest, case, workers),
                      artifacts={p.name: digest(p) for p in dest.iterdir() if p.is_file()})
        print(f'PASS {profile}/workers-{workers}/{case}', flush=True)
        return record
    with ThreadPoolExecutor(max_workers=args.jobs) as pool:
        for record in pool.map(execute, jobs):
            receipt['runs'].append(record)
            (out / 'progress.json').write_text(json.dumps(receipt, indent=2) + '\n')
    for path, expected in profiles['source']['sha256'].items():
        require(digest(source / path) == expected, 'source changed during rendering')
    receipt['outcome'] = 'all selected timeout histories and checker mutations passed'
    (out / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
    print('PASS timeout histories: ' + str(out), flush=True)


if __name__ == '__main__':
    main()
