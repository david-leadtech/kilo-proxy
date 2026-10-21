"""Exercise frozen source identity, publisher approval and artifact boundaries offline."""
import copy
import hashlib
import io
import json
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import recover_apt_0675 as recovery


class FixtureGit:
    head = 'b' * 40
    merge = 'c' * 40
    tree = 'd' * 40
    base = 'f0f606df2223db01b7bf16fbee989cd414e5e3d6'

    def output(self, *args):
        if args == ('rev-parse', 'HEAD'):
            return self.merge
        if args[0] == 'rev-parse' and args[1].endswith('^{tree}'):
            return self.tree
        if args[0] == 'show':
            return '0.67.5'
        if args == ('rev-parse', 'refs/tags/v0.67.5'):
            return '2574a93945ba5733ac59007b1054f26920e168e2'
        if args == ('rev-parse', 'refs/tags/v0.67.5^{commit}'):
            return recovery.ORIGINAL_HEAD
        if args[0] == 'log':
            return '1700000000'
        raise AssertionError(args)

    def ancestor(self, first, second):
        return True

    def changed_paths(self, first, second):
        return set(recovery.ALLOWED_PATHS)


class FixtureAPI:
    def __init__(self, golden, approval):
        self.data = {}
        self.downloads = []
        self.reads = []
        self.install_run(golden['source']['run'], golden['source']['pairs'])
        self.install_run(approval['publisher']['ciRun'], approval['publisher']['pairs'])
        for route, key in (('git/ref/tags/v0.67.5', 'tagRef'),
                           ('git/tags/2574a93945ba5733ac59007b1054f26920e168e2', 'tagObject'),
                           ('git/commits/' + recovery.ORIGINAL_HEAD, 'commit'), ('pulls/72', 'pr')):
            self.data[route] = copy.deepcopy(golden[key])
        self.data['actions/artifacts/' + str(recovery.ORIGINAL_ARTIFACT)] = copy.deepcopy(golden['artifact'])
        self.data[f'actions/runs/{recovery.ORIGINAL_RUN}/artifacts?per_page=100'] = {
            'total_count': 1, 'artifacts': [copy.deepcopy(golden['artifact'])]}
        release = dict(copy.deepcopy(golden['release']), assets=copy.deepcopy(golden['assets']))
        self.data['releases/latest'] = copy.deepcopy(release)
        self.data['releases/419571329'] = copy.deepcopy(release)
        self.data['releases/419571329/assets?per_page=100'] = copy.deepcopy(golden['assets'])
        self.data['commits/main'] = {'sha': FixtureGit.merge}
        self.data['git/commits/' + FixtureGit.merge] = copy.deepcopy(approval['publisher']['merge'])
        self.data['pulls/74'] = copy.deepcopy(approval['publisher']['pr'])

    def install_run(self, run, pairs):
        run = copy.deepcopy(run)
        for name in ('repository', 'head_repository'):
            run[name]['private'] = False
        self.data[f'actions/runs/{run["id"]}'] = run
        self.data[f'actions/runs/{run["id"]}/jobs?filter=latest&per_page=100'] = {
            'total_count': len(pairs), 'jobs': [copy.deepcopy(pair['job']) for pair in pairs]}
        self.data[f'check-suites/{run["check_suite_id"]}/check-runs?per_page=100'] = {
            'total_count': len(pairs), 'check_runs': [copy.deepcopy(pair['check']) for pair in pairs]}

    def get(self, route):
        self.reads.append(route)
        return copy.deepcopy(self.data[route])

    def download(self, route, destination, maximum):
        self.downloads.append(route)
        destination.write_bytes(b'failed log' if route.endswith('/logs') else b'fixture zip')


def publisher_fixture(golden):
    run_id, suite = 9001, 9002
    run_api = recovery.API + f'/actions/runs/{run_id}'
    run = copy.deepcopy(golden['source']['run'])
    run.update(id=run_id, node_id='TEST_RUN', check_suite_id=suite, check_suite_node_id='TEST_SUITE',
               head_sha=FixtureGit.head, head_branch='fix/apt-recovery-0675', event='pull_request',
               path='.github/workflows/build.yml', name='Test and package', workflow_id=352417668,
               status='completed', conclusion='success', referenced_workflows=[], pull_requests=[],
               url=run_api, html_url=recovery.WEB + f'/actions/runs/{run_id}',
               jobs_url=run_api + '/jobs', check_suite_url=recovery.API + f'/check-suites/{suite}',
               workflow_url=recovery.API + '/actions/workflows/352417668',
               head_commit={'id': FixtureGit.head, 'tree_id': FixtureGit.tree})
    pairs = []
    for number, old in enumerate(golden['source']['pairs']):
        if not old['job']['name'].startswith('build / '):
            continue
        pair = copy.deepcopy(old)
        jid = 9100 + number
        name = pair['job']['name'].removeprefix('build / ')
        web = recovery.WEB + f'/actions/runs/{run_id}/job/{jid}'
        for row in pair.values():
            row.update(id=jid, node_id='TEST_JOB_' + str(number), name=name, status='completed',
                       conclusion='success', head_sha=FixtureGit.head, html_url=web)
        pair['job'].update(run_id=run_id, run_attempt=1, run_url=run_api,
                           head_branch=run['head_branch'], workflow_name=run['name'],
                           url=recovery.API + f'/actions/jobs/{jid}',
                           check_run_url=recovery.API + f'/check-runs/{jid}')
        pair['check'].update(url=recovery.API + f'/check-runs/{jid}', details_url=web,
                             check_suite={'id': suite})
        pairs.append(pair)
    assert len(pairs) == 30
    pr = copy.deepcopy(golden['pr'])
    pr.update(number=74, id=90074, node_id='TEST_PR', merge_commit_sha=FixtureGit.merge,
              html_url=recovery.WEB + '/pull/74', url=recovery.API + '/pulls/74')
    pr['head'].update(sha=FixtureGit.head, ref='fix/apt-recovery-0675')
    pr['base']['sha'] = FixtureGit.base
    merge = {'sha': FixtureGit.merge, 'node_id': 'TEST_MERGE',
             'url': recovery.API + '/git/commits/' + FixtureGit.merge,
             'tree': {'sha': FixtureGit.tree},
             'parents': [{'sha': FixtureGit.base}, {'sha': FixtureGit.head}]}
    return {'schemaVersion': 1, 'purpose': 'apt-recovery-0675-publisher-approval',
            'goldenSha256': recovery.GOLDEN_SHA256,
            'publisher': {'dispatchSha': FixtureGit.merge, 'headSha': FixtureGit.head,
                          'treeSha': FixtureGit.tree, 'ciRun': run, 'pairs': pairs,
                          'pr': pr, 'merge': merge}}


class RecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='apt-recovery-0675-tests-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.golden = recovery.read_json(recovery.GOLDEN_PATH, recovery.GOLDEN_SHA256, 512 * 1024)
        self.approval = publisher_fixture(self.golden)
        self.api = FixtureAPI(self.golden, self.approval)
        self.git = FixtureGit()

    def validate(self):
        recovery.validate_all(self.api, self.git, self.golden, self.approval, self.git.merge, 9001)

    def test_frozen_original40_and_explicit_publisher30_validate_without_download(self):
        self.validate()
        self.assertEqual(self.api.downloads, [])
        self.assertEqual(self.golden['source']['successCount'], 38)
        self.assertFalse(self.golden['originalRelease40GateSatisfied'])
        self.assertEqual(set(self.golden['source']['expectedCiJobNames']),
                         {pair['job']['name'].removeprefix('build / ')
                          for pair in self.golden['source']['pairs']
                          if pair['job']['name'].startswith('build / ')})
        recovery.validate_local_sources()

    def test_each_original_or_publisher_binding_mutation_is_rejected(self):
        r = recovery.ORIGINAL_RUN
        routes = [
            (f'actions/runs/{r}', 'run_attempt', 2),
            (f'actions/runs/{r}', 'head_sha', 'e' * 40),
            (f'actions/runs/{r}', 'conclusion', 'success'),
            (f'actions/runs/{r}', 'workflow_id', True),
            ('actions/runs/9001', 'run_attempt', 2),
            ('actions/runs/9001', 'conclusion', 'failure'),
            ('actions/runs/9001', 'check_suite_id', 42),
            ('commits/main', 'sha', 'e' * 40),
            ('git/ref/tags/v0.67.5', 'ref', 'refs/tags/other'),
            ('pulls/72', 'merge_commit_sha', 'e' * 40),
            ('pulls/74', 'merge_commit_sha', 'e' * 40),
            ('pulls/74', 'merged', False),
            ('actions/artifacts/12348047748', 'size_in_bytes', 1),
            ('actions/artifacts/12348047748', 'digest', 'sha256:' + '0' * 64),
            ('actions/artifacts/12348047748', 'expired', True),
            ('releases/latest', 'id', 42),
            ('releases/latest', 'draft', True),
            ('releases/419571329', 'tag_name', 'v0.67.6'),
        ]
        for route, key, value in routes:
            original = copy.deepcopy(self.api.data[route])
            self.api.data[route][key] = value
            with self.subTest(route=route, key=key), self.assertRaises(ValueError):
                self.validate()
            self.api.data[route] = original
        self.assertEqual(self.api.downloads, [])

    def test_exact_job_check_sets_pairing_and_all_conclusions_are_mandatory(self):
        for rid, suite in ((recovery.ORIGINAL_RUN, 107320836499), (9001, 9002)):
            for kind, key in (('jobs', 'jobs'), ('checks', 'check_runs')):
                route = (f'actions/runs/{rid}/jobs?filter=latest&per_page=100'
                         if kind == 'jobs' else f'check-suites/{suite}/check-runs?per_page=100')
                clean = copy.deepcopy(self.api.data[route])
                for field, value in (('id', True), ('node_id', 'wrong'), ('name', 'unapproved'),
                                     ('conclusion', 'skipped'), ('html_url', 'https://other.invalid'),
                                     ('head_sha', 'e' * 40)):
                    self.api.data[route] = copy.deepcopy(clean)
                    self.api.data[route][key][0][field] = value
                    with self.subTest(rid=rid, kind=kind, field=field), self.assertRaises(ValueError):
                        self.validate()
                self.api.data[route] = copy.deepcopy(clean)
                self.api.data[route][key].append(copy.deepcopy(clean[key][0]))
                with self.assertRaises(ValueError):
                    self.validate()
                self.api.data[route] = clean

    def test_public_assets_and_application_diff_are_fixed(self):
        assets = self.api.data['releases/419571329/assets?per_page=100']
        assets[0]['digest'] = 'sha256:' + '0' * 64
        with self.assertRaises(ValueError):
            self.validate()
        assets[0]['digest'] = self.golden['assets'][0]['digest']
        with patch.object(self.git, 'changed_paths', return_value={'proxy.go'}), self.assertRaises(ValueError):
            self.validate()
        with patch.object(self.git, 'ancestor', return_value=False), self.assertRaises(ValueError):
            self.validate()

    def test_approval_sha_precedes_json_and_no_duplicate_json_or_boolean_id(self):
        path = self.root / 'approval.json'
        path.write_bytes(b'not JSON')
        with self.assertRaisesRegex(ValueError, 'SHA256'):
            recovery.read_json(path, '0' * 64, 1024)
        path.write_bytes(b'{"id":1,"id":2}')
        with self.assertRaisesRegex(ValueError, 'Duplicate'):
            recovery.read_json(path, hashlib.sha256(path.read_bytes()).hexdigest(), 1024)
        with self.assertRaises(ValueError):
            recovery.positive_id(True)
        for change in (None, False, 0):
            approval = copy.deepcopy(self.approval)
            approval['schemaVersion'] = change
            with self.assertRaises(ValueError):
                recovery.validate_publisher(self.api, self.git, self.golden, approval, self.git.merge, 9001)

    def test_invalid_approval_is_rejected_before_any_network(self):
        approval = copy.deepcopy(self.approval)
        approval['schemaVersion'] = False
        with self.assertRaises(ValueError):
            recovery.validate_all(self.api, self.git, self.golden, approval, self.git.merge, 9001)
        self.assertEqual(self.api.reads, [])
        self.assertEqual(self.api.downloads, [])

    def test_nonfinite_json_is_not_an_approval_value(self):
        path = self.root / 'nonfinite.json'
        path.write_bytes(b'{"value":NaN}')
        with self.assertRaises(ValueError):
            recovery.read_json(path, hashlib.sha256(path.read_bytes()).hexdigest(), 1024)

    def test_manual_main_exact_sha_first_attempt_environment(self):
        env = {'GITHUB_REPOSITORY': recovery.REPOSITORY, 'GITHUB_REPOSITORY_ID': str(recovery.REPOSITORY_ID),
               'GITHUB_EVENT_NAME': 'workflow_dispatch', 'GITHUB_REF': 'refs/heads/main',
               'GITHUB_SHA': self.git.merge, 'GITHUB_RUN_ATTEMPT': '1',
               'GITHUB_WORKFLOW_REF': recovery.REPOSITORY + '/.github/workflows/apt-recover-0675.yml@refs/heads/main'}
        recovery.validate_environment(env, self.git, self.git.merge)
        for key in env:
            bad = dict(env)
            bad[key] = 'unapproved'
            with self.subTest(key=key), self.assertRaises(ValueError):
                recovery.validate_environment(bad, self.git, self.git.merge)

    def test_zip_size_digest_and_source_recheck_precede_any_zip_parser(self):
        golden = copy.deepcopy(self.golden)
        golden['failureLogSha256'] = hashlib.sha256(b'failed log').hexdigest()
        output = self.root / 'packages'
        for size, digest in ((1, hashlib.sha256(b'fixture zip').hexdigest()),
                             (len(b'fixture zip'), '0' * 64)):
            with patch.object(recovery, 'validate_all'), patch.object(recovery, 'extract_packages') as extract, \
                    patch.object(recovery, 'ORIGINAL_ZIP_BYTES', size), \
                    patch.object(recovery, 'ORIGINAL_ZIP_SHA256', digest), self.assertRaises(ValueError):
                recovery.recover(self.api, self.git, golden, self.approval, self.git.merge, 9001, output)
            extract.assert_not_called()
        with patch.object(recovery, 'validate_all', side_effect=[None, ValueError('main moved')]), \
                patch.object(recovery, 'extract_packages') as extract, \
                patch.object(recovery, 'ORIGINAL_ZIP_BYTES', len(b'fixture zip')), \
                patch.object(recovery, 'ORIGINAL_ZIP_SHA256', hashlib.sha256(b'fixture zip').hexdigest()), \
                self.assertRaisesRegex(ValueError, 'main moved'):
            recovery.recover(self.api, self.git, golden, self.approval, self.git.merge, 9001, output)
        extract.assert_not_called()
        self.assertFalse(output.exists())

    def test_full_failed_log_sha_is_exact_without_fuzzy_fallback(self):
        with patch.object(recovery, 'validate_all'), patch.object(recovery, 'extract_packages') as extract, \
                self.assertRaisesRegex(ValueError, 'failed log'):
            recovery.recover(self.api, self.git, self.golden, self.approval, self.git.merge, 9001,
                             self.root / 'packages')
        extract.assert_not_called()
        self.assertEqual(self.api.downloads, ['actions/jobs/119240033891/logs'])


    def package_fixture(self):
        golden = copy.deepcopy(self.golden)
        bodies = {}
        for item in golden['originalDebs']:
            body = ('fixture-' + item['name']).encode()
            item.update(size=len(body), sha256=hashlib.sha256(body).hexdigest())
            bodies[item['name']] = body
        manifest = ''.join(hashlib.sha256(body).hexdigest() + '  ./' + name + '\n'
                           for name, body in sorted(bodies.items())).encode()
        bodies['DEBIAN-SHA256SUMS.txt'] = manifest
        expected_manifest = {'size': len(manifest), 'sha256': hashlib.sha256(manifest).hexdigest()}
        return golden, bodies, expected_manifest

    def test_original_zip_extraction_keeps_exact_bytes_and_checks_dpkg_identity(self):
        golden, bodies, manifest = self.package_fixture()
        archive = self.root / 'original.zip'
        with zipfile.ZipFile(archive, 'w') as output:
            for name, body in bodies.items():
                output.writestr(name, body)

        def metadata(path):
            kind = 'desktop' if '-desktop_' in path.name else 'headless'
            arch = 'amd64' if path.name.endswith('_amd64.deb') else 'arm64'
            return {}, {'package': 'kilo-proxy-' + kind, 'architecture': arch, 'version': '0.67.5'}

        with patch.object(recovery, 'CHECKSUM_MANIFEST', manifest), \
                patch.object(recovery.site.repository, 'metadata', side_effect=metadata):
            recovery.extract_packages(archive, self.root / 'good', golden)
            for name, body in bodies.items():
                self.assertEqual((self.root / 'good' / name).read_bytes(), body)
            for wrong in ({'package': 'kilo-proxy-desktop', 'architecture': 'amd64', 'version': '0.67.4'},
                          {'package': 'kilo-proxy-desktop', 'architecture': 'riscv64', 'version': '0.67.5'}):
                with patch.object(recovery.site.repository, 'metadata', return_value=({}, wrong)), \
                        self.assertRaises(ValueError):
                    recovery.extract_packages(archive, self.root / 'bad-dpkg', golden)
                self.assertFalse((self.root / 'bad-dpkg').exists())

    def test_zip_duplicate_traversal_links_and_changed_package_fail_closed(self):
        golden, bodies, manifest = self.package_fixture()
        first = next(name for name in bodies if name.endswith('.deb'))
        for case in ('duplicate', 'traversal', 'link', 'changed'):
            archive = self.root / (case + '.zip')
            with zipfile.ZipFile(archive, 'w') as output:
                for name, body in bodies.items():
                    if name == first and case == 'traversal':
                        output.writestr('../' + name, body)
                    elif name == first and case == 'link':
                        item = zipfile.ZipInfo(name)
                        item.external_attr = (stat.S_IFLNK | 0o777) << 16
                        output.writestr(item, body)
                    else:
                        output.writestr(name, b'x' * len(body) if name == first and case == 'changed' else body)
                if case == 'duplicate':
                    with self.assertWarns(UserWarning):
                        output.writestr(first, bodies[first])
            destination = self.root / case
            with patch.object(recovery, 'CHECKSUM_MANIFEST', manifest), self.subTest(case=case), \
                    self.assertRaises(ValueError):
                recovery.extract_packages(archive, destination, golden)
            self.assertFalse(destination.exists())

    def test_successful_recovery_downloads_original_zip_once_then_extracts_after_recheck(self):
        golden = copy.deepcopy(self.golden)
        golden['failureLogSha256'] = hashlib.sha256(b'failed log').hexdigest()
        with patch.object(recovery, 'validate_all') as validate, patch.object(recovery, 'extract_packages') as extract, \
                patch.object(recovery, 'ORIGINAL_ZIP_BYTES', len(b'fixture zip')), \
                patch.object(recovery, 'ORIGINAL_ZIP_SHA256', hashlib.sha256(b'fixture zip').hexdigest()):
            recovery.recover(self.api, self.git, golden, self.approval, self.git.merge, 9001,
                             self.root / 'packages')
        self.assertEqual(validate.call_count, 2)
        extract.assert_called_once()
        self.assertEqual(self.api.downloads, ['actions/jobs/119240033891/logs',
                                             'actions/artifacts/12348047748/zip'])

    def test_prepare_requires_all168_before_build_and_after_build_and_restores_reader(self):
        packages = self.root / 'packages'
        previous = self.root / 'previous'
        original_reader = recovery.site.previous_repository
        for stage in ('before', 'after'):
            output = self.root / stage
            called = []

            def prepare(*args):
                actual_previous = recovery.site.previous_repository()
                self.assertEqual(actual_previous, previous)
                called.append('build')
                (output / 'apt').mkdir(parents=True)

            with patch.object(recovery, 'validate_all'), patch.object(recovery, 'verify_original_packages'), \
                    patch.object(recovery.site, 'previous_repository', return_value=previous) as reader, \
                    patch.object(recovery.site, 'prepare', side_effect=prepare), \
                    patch.object(recovery, 'verify_baseline',
                                 side_effect=([ValueError('missing old path')] if stage == 'before' else
                                              [None, ValueError('changed old path')])), \
                    self.assertRaises(ValueError):
                recovery.prepare_site(self.api, self.git, self.golden, self.approval, self.git.merge, 9001,
                                      packages, output, self.root)
                self.assertIs(recovery.site.previous_repository, reader)
            self.assertEqual(called, [] if stage == 'before' else ['build'])
        self.assertIs(recovery.site.previous_repository, original_reader)

    def test_publisher_module_hash_is_checked_before_any_import_side_effect(self):
        root = self.root / 'untrusted-checkout'
        scripts = root / 'scripts'
        scripts.mkdir(parents=True)
        (scripts / 'recover_apt_0675.py').write_bytes(Path(recovery.__file__).read_bytes())
        marker = self.root / 'must-not-execute'
        (scripts / 'prepare_apt_site.py').write_text('from pathlib import Path\nPath(' + repr(str(marker)) + ').touch()\n')
        (scripts / 'build_apt_repository.py').write_text('raise RuntimeError("must not import")\n')
        result = subprocess.run([sys.executable, '-B', str(scripts / 'recover_apt_0675.py'), '--help'],
                                capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('before import', result.stderr)
        self.assertFalse(marker.exists())

    def test_baseline_all168_paths_sizes_and_hashes_are_required(self):
        golden = copy.deepcopy(self.golden)
        for item in golden['baseline']['entries']:
            path = self.root / 'previous' / item['path']
            path.parent.mkdir(parents=True, exist_ok=True)
            body = item['path'].encode()
            path.write_bytes(body)
            item.update(size=len(body), sha256=hashlib.sha256(body).hexdigest())
        recovery.verify_baseline(self.root / 'previous', golden)
        path = self.root / 'previous' / golden['baseline']['entries'][0]['path']
        path.write_bytes(b'tampered')
        with self.assertRaises(ValueError):
            recovery.verify_baseline(self.root / 'previous', golden)
        path.unlink()
        with self.assertRaises(ValueError):
            recovery.verify_baseline(self.root / 'previous', golden)


if __name__ == '__main__':
    unittest.main()
