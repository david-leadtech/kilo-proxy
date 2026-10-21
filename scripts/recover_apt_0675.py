#!/usr/bin/env python3
"""Validate a new main-only APT procedure using the unchanged 0.67.5 artifact."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import tempfile
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = 'rosseca/kilo-proxy'
REPOSITORY_ID = 1360386824
API = 'https://api.github.com/repos/' + REPOSITORY
WEB = 'https://github.com/' + REPOSITORY
GOLDEN_PATH = Path(__file__).with_name('apt_recovery_0675_golden.json')
GOLDEN_SHA256 = 'ac1e9818c77fac1d412e9d583d32e1f8642a832024ea69df57192d373e5b9a67'
ORIGINAL_HEAD = 'f1c2af894e5f365dc1f140888863967aa46c4f13'
PUBLISHER_BASE = 'f0f606df2223db01b7bf16fbee989cd414e5e3d6'
ORIGINAL_RUN = 39602441102
ORIGINAL_ARTIFACT = 12348047748
ORIGINAL_ZIP_BYTES = 31476759
ORIGINAL_ZIP_SHA256 = '4e514ae82c0e66ab088aa27ffb4a703fc2139ebac28d4b02b0e5de70a09f3555'
PRIMARY = '15AAF121DBFB06322EF1D7C8099722B1E76932AC'
PREPARE_SHA256 = '9a9e7fbf0b22cf56a872e693bc409ecc44cb64b9a4b7b2e84cfd3d6a6f2dd7d2'
CHECKSUM_MANIFEST = {'size': 418, 'sha256': '8eddae6731f533f8221a1be4a9232b4f9331d1f83e126208aa41a874501e2bba'}
BUILDER_SHA256 = '6218ab9f8e8407e6ed8830a07e86fba901c8502805049267c50033a305441c3b'
ALLOWED_PATHS = {
    'scripts/prepare_apt_site.py', 'scripts/test_prepare_apt_site.py', 'docs/ubuntu-apt.md',
    '.github/workflows/apt-recover-0675.yml', 'scripts/recover_apt_0675.py',
    'scripts/test_recover_apt_0675.py', 'scripts/apt_recovery_0675_golden.json',
    'docs/apt-recovery-0675.md', '.gitattributes',
}


# Authenticate both publisher modules before importing either module.
for relative, expected in (
        ('scripts/prepare_apt_site.py', PREPARE_SHA256),
        ('scripts/build_apt_repository.py', BUILDER_SHA256)):
    path = ROOT / relative
    if path.is_symlink() or hashlib.sha256(path.read_bytes()).hexdigest() != expected:
        raise ValueError('Pinned publisher module differs before import')
import prepare_apt_site as site


def digest(path):
    result = hashlib.sha256()
    with path.open('rb') as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b''):
            result.update(chunk)
    return result.hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def exact(actual, expected, label):
    require(type(actual) is type(expected), label + ' type differs')
    if isinstance(expected, dict):
        require(actual.keys() == expected.keys(), label + ' keys differ')
        for key, value in expected.items():
            exact(actual[key], value, label + '.' + str(key))
    elif isinstance(expected, list):
        require(len(actual) == len(expected), label + ' length differs')
        for number, value in enumerate(expected):
            exact(actual[number], value, label + '.' + str(number))
    else:
        require(actual == expected, label + ' differs')


def fields(actual, expected, label):
    require(type(actual) is dict, label + ' must be an object')
    for key, value in expected.items():
        require(key in actual, label + '.' + key + ' missing')
        if isinstance(value, dict):
            fields(actual[key], value, label + '.' + str(key))
        else:
            exact(actual[key], value, label + '.' + str(key))


def reject_constant(value):
    raise ValueError('Nonfinite JSON value: ' + value)


def unique_pairs(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'Duplicate JSON field')
        result[key] = value
    return result


def read_json(path, expected_sha256, maximum):
    require(re.fullmatch(r'[0-9a-f]{64}', expected_sha256 or '') is not None, 'External SHA256 required')
    require(path.is_file() and not path.is_symlink() and path.stat().st_size <= maximum,
            'Unsafe or oversized JSON input')
    raw = path.read_bytes()
    require(hashlib.sha256(raw).hexdigest() == expected_sha256, 'JSON SHA256 differs')
    return json.loads(raw, object_pairs_hook=unique_pairs, parse_constant=reject_constant)


def positive_id(value):
    require(type(value) is int and value > 0, 'Positive integer identifier required')
    return value


def sha40(value):
    require(type(value) is str and re.fullmatch(r'[0-9a-f]{40}', value), 'Full Git SHA required')
    return value


def validate_local_sources():
    require((ROOT / 'VERSION').read_bytes() == b'0.67.5\n', 'Recovery VERSION must remain 0.67.5')
    require(digest(ROOT / 'scripts/prepare_apt_site.py') == PREPARE_SHA256, 'Publisher source changed')
    require(digest(ROOT / 'scripts/build_apt_repository.py') == BUILDER_SHA256, 'Repository builder changed')


class GitHub:
    def get(self, route):
        if route.startswith('pulls/'):
            # Public PR metadata needs no additional token permission.
            request = urllib.request.Request(API + '/' + route, headers={
                'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28',
                'User-Agent': 'kilo-proxy-apt-recovery-0675'})
            with urllib.request.urlopen(request, timeout=60) as response:
                raw = response.read(8 * 1024 * 1024 + 1)
            require(len(raw) <= 8 * 1024 * 1024, 'Oversized GitHub response')
        else:
            raw = subprocess.check_output(
                ['gh', 'api', '--method', 'GET', 'repos/' + REPOSITORY + '/' + route], timeout=60)
            require(len(raw) <= 8 * 1024 * 1024, 'Oversized GitHub response')
        return json.loads(raw, object_pairs_hook=unique_pairs, parse_constant=reject_constant)

    def download(self, route, destination, maximum):
        with destination.open('xb') as output:
            child = subprocess.Popen(
                ['gh', 'api', '--allow-escape-sequences', '--method', 'GET', 'repos/' + REPOSITORY + '/' + route],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                total = 0
                while chunk := child.stdout.read(1024 * 1024):
                    total += len(chunk)
                    require(total <= maximum, 'GitHub download exceeds the approved size')
                    output.write(chunk)
                error = child.stderr.read()
                require(child.wait(timeout=60) == 0, 'GitHub download failed: ' + error.decode(errors='replace'))
            finally:
                if child.poll() is None:
                    child.kill()
                    child.wait()


class Git:
    def output(self, *args):
        return subprocess.check_output(['git', *args], cwd=ROOT, text=True).strip()

    def ancestor(self, first, second):
        result = subprocess.run(['git', 'merge-base', '--is-ancestor', first, second], cwd=ROOT)
        require(result.returncode in (0, 1), 'Cannot verify Git ancestry')
        return result.returncode == 0

    def changed_paths(self, first, second):
        raw = subprocess.check_output(['git', 'diff', '--no-renames', '--name-only', '-z', first, second], cwd=ROOT)
        return {part.decode() for part in raw.split(b'\0') if part}


def validate_environment(env, git, approved_sha):
    sha40(approved_sha)
    for key, wanted in {
        'GITHUB_REPOSITORY': REPOSITORY, 'GITHUB_REPOSITORY_ID': str(REPOSITORY_ID),
        'GITHUB_EVENT_NAME': 'workflow_dispatch', 'GITHUB_REF': 'refs/heads/main',
        'GITHUB_SHA': approved_sha, 'GITHUB_RUN_ATTEMPT': '1',
        'GITHUB_WORKFLOW_REF': REPOSITORY + '/.github/workflows/apt-recover-0675.yml@refs/heads/main',
    }.items():
        exact(env.get(key), wanted, key)
    exact(git.output('rev-parse', 'HEAD'), approved_sha, 'checkout SHA')


def validate_run(api, expected_run, expected_pairs, expected_names, label):
    rid = positive_id(expected_run['id'])
    run = api.get(f'actions/runs/{rid}')
    fields(run, expected_run, label + '.run')
    require(run['run_attempt'] == 1 and type(run['run_attempt']) is int, 'Only attempt 1 is approved')
    for key in ('repository', 'head_repository'):
        fields(run[key], {'id': REPOSITORY_ID, 'node_id': 'R_kgDOURXbCA',
                          'full_name': REPOSITORY, 'private': False}, label + '.' + str(key))
    jobs = api.get(f'actions/runs/{rid}/jobs?filter=latest&per_page=100')
    checks = api.get(f'check-suites/{run["check_suite_id"]}/check-runs?per_page=100')
    count = len(expected_pairs)
    require(count == len(expected_names) and len(set(expected_names)) == count, 'Invalid frozen name contract')
    for envelope, key in ((jobs, 'jobs'), (checks, 'check_runs')):
        exact(envelope.get('total_count'), count, label + '.' + key + '.total_count')
        rows = envelope.get(key)
        require(type(rows) is list and len(rows) == count, label + ' rows incomplete')
        require({row.get('name') for row in rows} == set(expected_names), label + ' names differ')
        require(len({positive_id(row.get('id')) for row in rows}) == count, label + ' duplicate IDs')
        require(len({row.get('node_id') for row in rows}) == count, label + ' duplicate nodes')
    by_job = {row['id']: row for row in jobs['jobs']}
    by_check = {row['id']: row for row in checks['check_runs']}
    expected_ids = {pair['job']['id'] for pair in expected_pairs}
    require(len(expected_ids) == count and set(by_job) == set(by_check) == expected_ids,
            label + ' exact job/check IDs differ')
    for pair in expected_pairs:
        jid = pair['job']['id']
        job, check = by_job[jid], by_check[jid]
        fields(job, pair['job'], label + '.job.' + str(jid))
        fields(check, pair['check'], label + '.check.' + str(jid))
        for key in ('id', 'node_id', 'name', 'head_sha', 'status', 'conclusion', 'html_url'):
            exact(check.get(key), job.get(key), label + '.pair.' + key)
        exact(job.get('check_run_url'), check.get('url'), label + '.check URL')
        exact(check.get('details_url'), job.get('html_url'), label + '.details URL')
        exact(check.get('check_suite', {}).get('id'), run['check_suite_id'], label + '.suite')
        fields(check.get('app'), {'id': 15368, 'slug': 'github-actions'}, label + '.app')
        fields(job, {'run_id': rid, 'run_attempt': 1, 'head_sha': run['head_sha'],
                     'head_branch': run['head_branch'], 'workflow_name': run['name'],
                     'run_url': API + f'/actions/runs/{rid}',
                     'url': API + f'/actions/jobs/{jid}',
                     'check_run_url': API + f'/check-runs/{jid}',
                     'html_url': WEB + f'/actions/runs/{rid}/job/{jid}'}, label + '.job binding')
        fields(check, {'head_sha': run['head_sha'], 'url': API + f'/check-runs/{jid}'},
               label + '.check binding')
        exact(job.get('status'), 'completed', label + '.terminal job')
        exact(check.get('status'), 'completed', label + '.terminal check')
    return run


def validate_original(api, golden):
    source = golden['source']
    run = validate_run(api, source['run'], source['pairs'], golden['source']['expectedReleaseJobNames'], 'original')
    exact(run['id'], ORIGINAL_RUN, 'original run')
    exact(run['head_sha'], ORIGINAL_HEAD, 'original head')
    fields(run, {'status': 'completed', 'conclusion': 'failure', 'workflow_id': 352417669,
                 'path': '.github/workflows/release.yml', 'event': 'push',
                 'head_branch': 'v0.67.5', 'check_suite_id': 107320836499,
                 'created_at': '2026-10-21T12:47:31Z'}, 'original run contract')
    conclusions = {pair['job']['id']: pair['job']['conclusion'] for pair in source['pairs']}
    exact(len(conclusions), 40, 'original40')
    require(sum(value == 'success' for value in conclusions.values()) == 38, 'Original 38 successes required')
    exact({key: value for key, value in conclusions.items() if value != 'success'},
          {119240033891: 'failure', 119240838104: 'skipped'}, 'sole original APT outcomes')
    for route, key in (
        ('git/ref/tags/v0.67.5', 'tagRef'),
        ('git/tags/2574a93945ba5733ac59007b1054f26920e168e2', 'tagObject'),
        ('git/commits/' + ORIGINAL_HEAD, 'commit'),
        ('pulls/72', 'pr'),
    ):
        fields(api.get(route), golden[key], 'original.' + key)
    fields(api.get('actions/artifacts/' + str(ORIGINAL_ARTIFACT)), golden['artifact'], 'artifact')
    artifacts = api.get(f'actions/runs/{ORIGINAL_RUN}/artifacts?per_page=100')
    require(type(artifacts.get('total_count')) is int and
            artifacts['total_count'] == len(artifacts.get('artifacts', [])) < 100,
            'Incomplete original artifact listing')
    candidates = [item for item in artifacts['artifacts'] if item.get('name') == 'debian-assets']
    require(len(candidates) == 1, 'Original Debian artifact name is ambiguous')
    fields(candidates[0], golden['artifact'], 'listed artifact')
    fields(golden['artifact'], {
        'id': ORIGINAL_ARTIFACT, 'name': 'debian-assets', 'size_in_bytes': ORIGINAL_ZIP_BYTES,
        'digest': 'sha256:' + ORIGINAL_ZIP_SHA256, 'expired': False,
        'workflow_run': {'id': ORIGINAL_RUN, 'head_sha': ORIGINAL_HEAD,
                         'repository_id': REPOSITORY_ID, 'head_repository_id': REPOSITORY_ID},
    }, 'fixed artifact contract')
    latest = api.get('releases/latest')
    release = api.get('releases/419571329')
    for record in (latest, release):
        fields(record, golden['release'], 'public release')
        fields(record, {'id': 419571329, 'tag_name': 'v0.67.5', 'draft': False, 'prerelease': False,
                        'html_url': WEB + '/releases/tag/v0.67.5'}, 'stable/latest')
    assets = api.get('releases/419571329/assets?per_page=100')
    require(type(assets) is list and len(assets) == 11, 'All 11 public assets required')
    expected = {item['id']: item for item in golden['assets']}
    require(len(expected) == 11, 'Invalid frozen public asset contract')
    for records in (assets, latest.get('assets'), release.get('assets')):
        require(type(records) is list and len(records) == 11, 'Incomplete public assets')
        observed = {positive_id(item.get('id')): item for item in records}
        require(len(observed) == 11 and observed.keys() == expected.keys(), 'Public asset IDs differ')
        for aid, wanted in expected.items():
            fields(observed[aid], wanted, 'public asset ' + str(aid))


def validate_approval(golden, approval, approved_sha, ci_run_id):
    require(type(approval) is dict, 'Approval must be an object')
    exact(set(approval), {'schemaVersion', 'purpose', 'goldenSha256', 'publisher'}, 'approval keys')
    exact(approval['schemaVersion'], 1, 'approval schema')
    exact(approval['purpose'], 'apt-recovery-0675-publisher-approval', 'approval purpose')
    exact(approval['goldenSha256'], GOLDEN_SHA256, 'approval original facts binding')
    publisher = approval['publisher']
    require(type(publisher) is dict, 'Publisher approval must be an object')
    exact(set(publisher), {'dispatchSha', 'headSha', 'treeSha', 'ciRun', 'pairs',
                           'pr', 'merge'}, 'publisher keys')
    exact(publisher['dispatchSha'], approved_sha, 'external publisher SHA')
    sha40(publisher['headSha'])
    sha40(publisher['treeSha'])
    require(ci_run_id not in (ORIGINAL_RUN, 39611487582), 'The recovery procedure needs its own new CI run')
    expected_run = publisher['ciRun']
    mandatory = {'id', 'node_id', 'run_attempt', 'created_at', 'head_sha', 'head_branch',
                 'check_suite_id', 'check_suite_node_id', 'workflow_id', 'path', 'name',
                 'event', 'status', 'conclusion', 'head_commit', 'repository', 'head_repository',
                 'url', 'html_url', 'jobs_url', 'check_suite_url', 'workflow_url'}
    require(mandatory <= set(expected_run), 'Publisher approval run identity incomplete')
    fields(expected_run, {
        'id': positive_id(ci_run_id), 'head_sha': publisher['headSha'],
        'run_attempt': 1, 'status': 'completed', 'conclusion': 'success',
        'path': '.github/workflows/build.yml', 'workflow_id': 352417668,
        'name': 'Test and package', 'event': 'pull_request', 'referenced_workflows': [],
        'head_commit': {'id': publisher['headSha'], 'tree_id': publisher['treeSha']},
        'url': API + f'/actions/runs/{ci_run_id}', 'html_url': WEB + f'/actions/runs/{ci_run_id}',
        'jobs_url': API + f'/actions/runs/{ci_run_id}/jobs',
        'check_suite_url': API + f'/check-suites/{positive_id(expected_run["check_suite_id"])}',
        'workflow_url': API + '/actions/workflows/352417668',
    }, 'publisher CI approval')
    require(len(publisher['pairs']) == 30 and len(golden['source']['expectedCiJobNames']) == 30,
            'Exactly 30 approved CI pairs required')
    for pair in publisher['pairs']:
        for kind in ('job', 'check'):
            fields(pair[kind], {'status': 'completed', 'conclusion': 'success'}, 'publisher approved pair')
            require({'id', 'node_id', 'name', 'head_sha', 'status', 'conclusion', 'html_url'} <=
                    pair[kind].keys(), 'Publisher pair identity incomplete')
    pr_expected = publisher['pr']
    require({'id', 'node_id', 'number', 'url', 'html_url', 'merged', 'merged_at',
             'state', 'draft', 'merge_commit_sha', 'base', 'head'} <= pr_expected.keys(),
            'Merged publisher PR approval incomplete')
    for endpoint in ('head', 'base'):
        require({'sha', 'ref', 'repo'} <= pr_expected[endpoint].keys(),
                'Publisher PR endpoint approval incomplete')
        sha40(pr_expected[endpoint]['sha'])
    fields(pr_expected, {'head': {'ref': expected_run['head_branch']},
                         'base': {'sha': PUBLISHER_BASE, 'ref': 'main'}}, 'approved PR branch and base')
    require({'sha', 'node_id', 'url', 'tree', 'parents'} <= publisher['merge'].keys(),
            'Approved merge identity incomplete')
    number = positive_id(pr_expected['number'])
    return publisher


def validate_publisher(api, git, golden, approval, approved_sha, ci_run_id):
    publisher = validate_approval(golden, approval, approved_sha, ci_run_id)
    expected_run = publisher['ciRun']
    validate_run(api, expected_run, publisher['pairs'], golden['source']['expectedCiJobNames'], 'publisher CI')
    main = api.get('commits/main')
    exact(main.get('sha'), approved_sha, 'fresh main SHA')
    merge = api.get('git/commits/' + approved_sha)
    fields(merge, publisher['merge'], 'publisher merge')
    exact(merge.get('sha'), approved_sha, 'publisher merge SHA')
    exact(merge.get('tree', {}).get('sha'), publisher['treeSha'], 'merge tree must equal tested tree')
    pr_expected = publisher['pr']
    number = positive_id(pr_expected['number'])
    pr = api.get('pulls/' + str(number))
    fields(pr, pr_expected, 'publisher merged PR')
    fields(pr, {'merged': True, 'state': 'closed', 'draft': False, 'merge_commit_sha': approved_sha,
                'head': {'sha': publisher['headSha'], 'repo': {'full_name': REPOSITORY,
                         'id': REPOSITORY_ID}},
                'base': {'ref': 'main', 'repo': {'full_name': REPOSITORY, 'id': REPOSITORY_ID}}},
           'publisher PR provenance')
    require(pr.get('merged_at') and pr.get('node_id') == pr_expected.get('node_id') and
            type(pr_expected.get('id')) is int and pr_expected['id'] > 0, 'Merged PR identity incomplete')
    exact([item.get('sha') for item in merge.get('parents', [])],
          [pr['base']['sha'], publisher['headSha']], 'publisher ordered merge parents')
    for first in (ORIGINAL_HEAD, publisher['headSha']):
        require(git.ancestor(first, approved_sha), 'Approved source is not included in main')
    require(not (git.changed_paths(ORIGINAL_HEAD, approved_sha) - ALLOWED_PATHS),
            'Application source differs from original 0.67.5')
    exact(git.output('rev-parse', publisher['headSha'] + '^{tree}'),
          publisher['treeSha'], 'local CI tree')
    exact(git.output('rev-parse', approved_sha + '^{tree}'),
          publisher['treeSha'], 'local publisher tree')
    exact(git.output('show', publisher['headSha'] + ':VERSION'), '0.67.5', 'tested VERSION')
    exact(git.output('rev-parse', 'refs/tags/v0.67.5'), '2574a93945ba5733ac59007b1054f26920e168e2',
          'local annotated tag')
    exact(git.output('rev-parse', 'refs/tags/v0.67.5^{commit}'), ORIGINAL_HEAD, 'local tag target')


def validate_all(api, git, golden, approval, approved_sha, ci_run_id):
    validate_approval(golden, approval, approved_sha, ci_run_id)
    validate_original(api, golden)
    validate_publisher(api, git, golden, approval, approved_sha, ci_run_id)


def verify_original_packages(packages, golden):
    site.verify_packages(packages, '0.67.5')
    expected = {item['name']: item for item in golden['originalDebs']}
    require(len(expected) == 4, 'Four frozen original Debian identities required')
    for name, wanted in expected.items():
        path = packages / name
        exact(path.stat().st_size, wanted['size'], 'original DEB size')
        exact(digest(path), wanted['sha256'], 'original DEB SHA256')
        _, metadata = site.repository.metadata(path)
        kind = 'desktop' if '-desktop_' in name else 'headless'
        arch = 'amd64' if name.endswith('_amd64.deb') else 'arm64'
        fields(metadata, {'package': 'kilo-proxy-' + kind, 'version': '0.67.5',
                          'architecture': arch}, 'original dpkg identity')


def extract_packages(archive, output, golden):
    expected = {item['name'] for item in golden['originalDebs']} | {'DEBIAN-SHA256SUMS.txt'}
    require(not output.exists() and not output.is_symlink(), 'Refusing to replace package output')
    with zipfile.ZipFile(archive) as source:
        members = source.infolist()
        require(len(members) == 5 and {item.filename for item in members} == expected,
                'ZIP must contain exactly the four original DEBs and manifest')
        require(sum(item.file_size for item in members) == sum(item['size'] for item in golden['originalDebs']) +
                CHECKSUM_MANIFEST['size'], 'ZIP uncompressed sizes differ')
        for item in members:
            require(not item.is_dir() and item.file_size >= 0 and
                    stat.S_IFMT(item.external_attr >> 16) in (0, stat.S_IFREG) and
                    '/' not in item.filename and chr(92) not in item.filename,
                    'Unsafe original artifact member')
        output.mkdir(parents=True)
        try:
            for item in members:
                with source.open(item) as inp, (output / item.filename).open('xb') as out:
                    shutil.copyfileobj(inp, out)
                (output / item.filename).chmod(0o644)
            manifest = output / 'DEBIAN-SHA256SUMS.txt'
            exact(digest(manifest), CHECKSUM_MANIFEST['sha256'], 'original checksum manifest')
            verify_original_packages(output, golden)
        except BaseException:
            shutil.rmtree(output)
            raise


def recover(api, git, golden, approval, approved_sha, ci_run_id, output):
    require(not output.exists() and not output.is_symlink(), 'Recovery package output already exists')
    validate_all(api, git, golden, approval, approved_sha, ci_run_id)
    with tempfile.TemporaryDirectory(prefix='kilo-apt-recovery-0675-') as temporary:
        log = Path(temporary) / 'original-failed-job.log'
        api.download('actions/jobs/119240033891/logs', log, 4 * 1024 * 1024)
        exact(digest(log), golden['failureLogSha256'], 'original full failed log')
        archive = Path(temporary) / 'debian-assets.zip'
        api.download(f'actions/artifacts/{ORIGINAL_ARTIFACT}/zip', archive, ORIGINAL_ZIP_BYTES)
        exact(archive.stat().st_size, ORIGINAL_ZIP_BYTES, 'original ZIP size before parsing')
        exact(digest(archive), ORIGINAL_ZIP_SHA256, 'original ZIP digest before parsing')
        validate_all(api, git, golden, approval, approved_sha, ci_run_id)
        extract_packages(archive, output, golden)


def verify_baseline(repository, golden):
    entries = golden['baseline']['entries']
    require(len(entries) == 168 and len({item['path'] for item in entries}) == 168,
            'Frozen original168 inventory invalid')
    for item in entries:
        require(re.fullmatch(site.repository.RETAINED_PATTERN, item['path']) is not None,
                'Unsafe frozen retained path')
        path = site.repository.safe_repository_path(repository, item['path'])
        exact(path.stat().st_size, item['size'], 'historical retained size')
        exact(digest(path), item['sha256'], 'historical retained SHA256')


def prepare_site(api, git, golden, approval, approved_sha, ci_run_id, packages, output, home):
    validate_all(api, git, golden, approval, approved_sha, ci_run_id)
    verify_original_packages(packages, golden)
    original_previous = site.previous_repository

    def retained_previous(*args, **kwargs):
        previous = original_previous(*args, **kwargs)
        require(previous is not None, 'Recovery must never bootstrap')
        verify_baseline(previous, golden)
        return previous

    # Keep the independently pinned publisher intact. Validate its authenticated
    # previous tree before it builds, and its complete result before any upload.
    site.previous_repository = retained_previous
    try:
        site.prepare(packages, output, PRIMARY, home,
                     'https://rosseca.github.io/kilo-proxy/apt', False,
                     int(git.output('log', '-1', '--format=%ct')))
    finally:
        site.previous_repository = original_previous
    verify_baseline(output / 'apt', golden)
    for item in golden['originalDebs']:
        path = output / 'apt/pool/main/k/kilo-proxy' / item['name']
        exact(path.stat().st_size, item['size'], 'prepared original DEB size')
        exact(digest(path), item['sha256'], 'prepared original DEB SHA256')
    require(sum(path.stat().st_size for path in output.rglob('*') if path.is_file()) <= site.MAX_SITE,
            'Complete recovery site exceeds900MiB')
    validate_all(api, git, golden, approval, approved_sha, ci_run_id)



def positive_argument(value):
    if re.fullmatch(r'[1-9][0-9]*', value) is None:
        raise argparse.ArgumentTypeError('Use a positive GitHub identifier')
    return int(value)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('validate', 'prepare-site'))
    parser.add_argument('--approval-file', type=Path, required=True)
    parser.add_argument('--approval-sha256', required=True)
    parser.add_argument('--approved-publisher-sha', required=True)
    parser.add_argument('--ci-run-id', type=positive_argument, required=True)
    parser.add_argument('--packages', type=Path, required=True)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    validate_local_sources()
    git, api = Git(), GitHub()
    validate_environment(os.environ, git, args.approved_publisher_sha)
    golden = read_json(GOLDEN_PATH, GOLDEN_SHA256, 512 * 1024)
    approval = read_json(args.approval_file, args.approval_sha256, 64 * 1024)
    if args.operation == 'validate':
        recover(api, git, golden, approval, args.approved_publisher_sha, args.ci_run_id, args.packages.absolute())
    else:
        require(args.output is not None and os.environ.get('GNUPGHOME'), 'Explicit isolated signing home required')
        prepare_site(api, git, golden, approval, args.approved_publisher_sha, args.ci_run_id,
                     args.packages.absolute(), args.output.absolute(), Path(os.environ['GNUPGHOME']))
    print(json.dumps({'operation': args.operation, 'version': '0.67.5', 'originalRunId': ORIGINAL_RUN,
                      'originalFailurePreserved': True, 'sourceSuccesses': 38,
                      'publicPublicationVerified': False, 'originalRelease40GateSatisfied': False}))


if __name__ == '__main__':
    main()
