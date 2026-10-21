"""Guard publication inputs, authenticated history recovery and safe extraction."""
import hashlib
import io
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import urllib.error

import prepare_apt_site as site


class AptSiteTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='kilo-apt-site-tests-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.packages = self.root / 'packages'
        self.packages.mkdir()
        self.names = sorted(f'kilo-proxy-{kind}_1.2.3_{arch}.deb'
                            for kind in ('desktop', 'headless') for arch in ('amd64', 'arm64'))
        for name in self.names:
            (self.packages / name).write_bytes(name.encode())
        self.manifest = self.packages / 'DEBIAN-SHA256SUMS.txt'
        self.lines = [hashlib.sha256((self.packages / name).read_bytes()).hexdigest() + '  ./' + name
                      for name in self.names]
        self.manifest.write_text('\n'.join(self.lines) + '\n')

    def test_tested_package_manifest_rejects_changed_bytes(self):
        site.verify_packages(self.packages, '1.2.3')
        (self.packages / self.names[0]).write_bytes(b'changed after testing')
        with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
            site.verify_packages(self.packages, '1.2.3')

    def test_manifest_rejects_duplicate_missing_extra_and_unsafe_entries(self):
        for lines in (self.lines[:-1], self.lines + [self.lines[0]],
                      self.lines + ['0' * 64 + '  ../escape.deb'],
                      self.lines + ['0' * 64 + '  unrelated.deb']):
            self.manifest.write_text('\n'.join(lines) + '\n')
            with self.subTest(lines=lines), self.assertRaises(ValueError):
                site.verify_packages(self.packages, '1.2.3')

    def test_wrong_version_and_prerelease_are_not_published(self):
        for version in ('1.2.4', '1.2.3-beta.1', 'v1.2.3', '1.2.3\nunsafe'):
            with self.subTest(version=version), self.assertRaises(ValueError):
                site.verify_packages(self.packages, version)

    def test_package_symlink_is_not_a_tested_input(self):
        path = self.packages / self.names[0]
        original = self.root / 'original'
        path.rename(original)
        try:
            path.symlink_to(original)
        except OSError:
            self.skipTest('Host does not allow symlink fixtures')
        with self.assertRaises(ValueError):
            site.verify_packages(self.packages, '1.2.3')

    def test_snapshot_cannot_escape_or_install_links(self):
        for name, kind in (('../escape', tarfile.REGTYPE), ('/escape', tarfile.REGTYPE),
                           ('pool/link', tarfile.SYMTYPE), ('pool/link', tarfile.LNKTYPE)):
            archive = self.root / 'unsafe.tar.gz'
            with tarfile.open(archive, 'w:gz') as out:
                entry = tarfile.TarInfo(name)
                entry.type = kind
                entry.linkname = '/outside'
                out.addfile(entry, io.BytesIO())
            destination = self.root / 'output'
            destination.mkdir(exist_ok=True)
            with self.subTest(name=name, kind=kind), self.assertRaises(ValueError):
                site.extract_snapshot(archive, destination)
            self.assertEqual(list(destination.iterdir()), [])

    def test_verified_snapshot_extracts_without_restoring_permissions(self):
        archive = self.root / 'safe.tar.gz'
        with tarfile.open(archive, 'w:gz') as out:
            entry = tarfile.TarInfo('pool/package.deb')
            entry.size = 4
            entry.mode = 0o6777
            out.addfile(entry, io.BytesIO(b'data'))
        destination = self.root / 'output'
        destination.mkdir()
        site.extract_snapshot(archive, destination)
        path = destination / 'pool/package.deb'
        self.assertEqual(path.read_bytes(), b'data')
        if site.os.name == 'posix':
            self.assertEqual(path.stat().st_mode & 0o7777, 0o644)

    def test_missing_history_is_not_silent_bootstrap(self):
        absent = urllib.error.HTTPError('https://example.invalid/apt', 404, 'Not found', {}, None)
        with patch.object(site, 'download', side_effect=absent), self.assertRaises(urllib.error.HTTPError):
            site.previous_repository('https://example.invalid/apt', 'A' * 40, self.root, self.root, False)
        with patch.object(site, 'download', side_effect=absent):
            self.assertIsNone(site.previous_repository('https://example.invalid/apt', 'A' * 40,
                                                       self.root, self.root, True))

    def test_bootstrap_does_not_reset_existing_metadata(self):
        absent = urllib.error.HTTPError('https://example.invalid/apt', 404, 'Not found', {}, None)
        with patch.object(site, 'download', side_effect=[absent, None]), self.assertRaisesRegex(ValueError, 'bootstrap refused'):
            site.previous_repository('https://example.invalid/apt', 'A' * 40, self.root, self.root, True)

    def test_signed_archive_is_verified_before_extraction(self):
        with patch.object(site, 'download'), patch.object(site.subprocess, 'run', side_effect=[subprocess.CompletedProcess([], 0), RuntimeError('bad signature')]), \
                patch.object(site, 'extract_snapshot') as extract, self.assertRaises(RuntimeError):
            site.previous_repository('https://example.invalid/apt', 'A' * 40, self.root, self.root, False)
        extract.assert_not_called()

    def test_older_release_cannot_replace_newer_apt_repository(self):
        release = self.root / 'dists/noble/Release'
        release.parent.mkdir(parents=True)
        release.write_text('Origin: Kilo Proxy\nVersion: 1.10.0\n')
        site.check_previous_version(self.root, '1.10.0')
        site.check_previous_version(self.root, '1.11.0')
        with self.assertRaisesRegex(ValueError, 'older release'):
            site.check_previous_version(self.root, '1.9.9')
        release.write_text('Origin: Kilo Proxy\n')
        with self.assertRaises(ValueError):
            site.check_previous_version(self.root, '1.11.0')


    def test_v2_marker_and_paths_are_exact_before_any_extraction(self):
        cases = [
            [(site.FORMAT_NAME, b'unsupported-v3\n')],
            [(site.FORMAT_NAME, site.FORMAT_V2), ('pool/main/k/kilo-proxy/kilo-proxy-desktop_1.2.3_amd64.deb', b'x')],
            [(site.FORMAT_NAME, site.FORMAT_V2), ('unexpected', b'x')],
            [(site.FORMAT_NAME, site.FORMAT_V2), ('./dists/noble/Release', b'x')],
            [(site.FORMAT_NAME, site.FORMAT_V2), ('dists//noble/Release', b'x')],
            [(site.FORMAT_NAME, site.FORMAT_V2), ('dists/noble/../Release', b'x')],
            [(site.FORMAT_NAME, site.FORMAT_V2), ('dists\\noble\\Release', b'x')],
            [(site.FORMAT_NAME, site.FORMAT_V2), (site.FORMAT_NAME, site.FORMAT_V2)],
        ]
        for number, entries in enumerate(cases):
            archive = self.root / f'v2-{number}.tar.gz'
            with tarfile.open(archive, 'w:gz') as output:
                for name, data in entries:
                    entry = tarfile.TarInfo(name)
                    entry.size = len(data)
                    output.addfile(entry, io.BytesIO(data))
            destination = self.root / f'v2-{number}'
            destination.mkdir()
            with self.subTest(entries=entries), self.assertRaises(ValueError):
                site.extract_snapshot(archive, destination)
            self.assertEqual(list(destination.iterdir()), [])

    def test_snapshot_extraction_keeps_existing_400_mib_guard(self):
        archive = self.root / 'oversize.tar.gz'
        with tarfile.open(archive, 'w:gz') as output:
            entry = tarfile.TarInfo('data')
            entry.size = 4
            output.addfile(entry, io.BytesIO(b'data'))
        destination = self.root / 'oversize'
        destination.mkdir()
        with patch.object(site, 'MAX_SNAPSHOT', 3), self.assertRaisesRegex(ValueError, 'supported size'):
            site.extract_snapshot(archive, destination)
        self.assertEqual(list(destination.iterdir()), [])
        self.assertEqual(site.MAX_SNAPSHOT, 400 * 1024 * 1024)
        self.assertEqual(site.MAX_SITE, 900 * 1024 * 1024)

    def test_writer_checks_uncompressed_metadata_before_opening_tar(self):
        apt = self.root / 'site'
        apt.mkdir()
        (apt / 'kilo-proxy-archive-keyring.asc').write_bytes(b'four')
        output = self.root / 'metadata.tar.gz'
        with patch.object(site, 'MAX_SNAPSHOT', len(site.FORMAT_V2) + 3), patch.object(site.tarfile, 'open') as opened:
            with self.assertRaisesRegex(ValueError, 'snapshot metadata'):
                site.write_snapshot(apt, output, 1700000000)
        opened.assert_not_called()
        self.assertFalse(output.exists())

    def test_pool_download_publishes_only_exact_signed_bytes(self):
        url = 'https://example.invalid/apt/pool/main/k/kilo-proxy/kilo-proxy-desktop_1.2.3_amd64.deb'
        expected = b'tested package'
        for number, body in enumerate((expected, expected[:-1], expected + b'x', b'wrong contents')):
            response = io.BytesIO(body)
            response.geturl = lambda: url
            opener = unittest.mock.Mock()
            opener.open.return_value = response
            destination = self.root / f'pool-{number}' / 'package.deb'
            with patch.object(site.urllib.request, 'build_opener', return_value=opener):
                if body == expected:
                    site.download_pool(url, destination, len(expected), hashlib.sha256(expected).hexdigest())
                    self.assertEqual(destination.read_bytes(), expected)
                else:
                    with self.assertRaises(ValueError):
                        site.download_pool(url, destination, len(expected), hashlib.sha256(expected).hexdigest())
                    self.assertFalse(destination.exists())
                    self.assertEqual(list(destination.parent.iterdir()), [])
            self.assertEqual(opener.open.call_count, 1)

    def test_pool_redirect_or_missing_object_never_creates_payload(self):
        url = 'https://example.invalid/apt/pool/package.deb'
        self.assertIsNone(site.NoRedirect().redirect_request(None, None, 302, '', {}, 'https://other.invalid'))
        for code in (302, 404):
            opener = unittest.mock.Mock()
            opener.open.side_effect = urllib.error.HTTPError(url, code, 'blocked', {}, None)
            destination = self.root / f'http-{code}' / 'package.deb'
            with patch.object(site.urllib.request, 'build_opener', return_value=opener), self.assertRaises(urllib.error.HTTPError):
                site.download_pool(url, destination, 4, hashlib.sha256(b'data').hexdigest())
            self.assertFalse(destination.exists())
            self.assertEqual(list(destination.parent.iterdir()), [])

    def test_history_base_url_rejects_credentials_aliases_and_backslash(self):
        for url in ('http://example.invalid/apt', 'https://user:secret@example.invalid/apt',
                    'https://example.invalid/apt?query', 'https://example.invalid/a/../apt',
                    'https://example.invalid/apt\\path', 'https://example.invalid/apt%2fpath'):
            with self.subTest(url=url), patch.object(site, 'download') as download, self.assertRaises(ValueError):
                site.previous_repository(url, 'A' * 40, self.root, self.root, False)
            download.assert_not_called()


@unittest.skipUnless(shutil.which('gpg') and shutil.which('gpgv') and shutil.which('dpkg-deb'),
                     'Real isolated signing and Debian tools required')
class AptSiteIntegrationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        import test_apt_repository as fixtures
        cls.fixtures = fixtures.RepositoryIntegrationTests
        cls.fixtures.setUpClass()

    @classmethod
    def tearDownClass(cls):
        cls.fixtures.tearDownClass()

    def setUp(self):
        self.fixture = self.fixtures()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.root = self.fixture.case
        self.source = self.root / 'source'
        (self.source / 'scripts').mkdir(parents=True)
        shutil.copyfile(site.ROOT / 'scripts/build_apt_repository.py', self.source / 'scripts/build_apt_repository.py')
        self.identity = self.fixture.fingerprint

    def write_candidate(self, version):
        (self.source / 'VERSION').write_text(version)
        self.fixture.write_packages(version)
        packages = self.fixture.packages
        (packages / 'DEBIAN-SHA256SUMS.txt').write_text(''.join(
            hashlib.sha256(path.read_bytes()).hexdigest() + '  ./' + path.name + '\n'
            for path in sorted(packages.glob('*.deb'))))

    def publish_candidate(self, name, previous=None):
        def public_download(url, destination, maximum):
            if previous is None:
                raise urllib.error.HTTPError(url, 404, 'Not found', {}, None)
            source = previous / 'apt' / url.split('/apt/', 1)[1]
            shutil.copyfile(source, destination)
        output = self.root / name
        def pool_response(request, timeout):
            url = request.full_url
            source = previous / 'apt' / url.split('/apt/', 1)[1]
            self.pool_requests.append(url)
            response = io.BytesIO(source.read_bytes())
            response.geturl = lambda: url
            return response
        self.pool_requests = []
        opener = unittest.mock.Mock()
        opener.open.side_effect = pool_response
        with patch.object(site, 'ROOT', self.source), patch.object(site, 'download', side_effect=public_download), \
                patch.object(site.urllib.request, 'build_opener', return_value=opener):
            site.prepare(self.fixture.packages, output, self.identity, self.fixture.home,
                         'https://example.invalid/apt', previous is None, 1700000000)
        return output

    def test_real_site_bootstrap_and_authenticated_history_upgrade(self):
        self.write_candidate('1.2.3')
        initial = self.publish_candidate('initial')
        self.assertTrue((initial / 'apt/repository-state.tar.gz.asc').is_file())
        self.write_candidate('1.2.4')
        updated = self.publish_candidate('updated', initial)
        self.assertEqual(len(list((updated / 'apt/pool').rglob('*.deb'))), 8)
        self.assertIn('Version: 1.2.4\n', (updated / 'apt/dists/noble/Release').read_text())
        self.write_candidate('1.2.3')
        with self.assertRaisesRegex(ValueError, 'older release'):
            self.publish_candidate('downgrade', updated)
        self.assertFalse((self.root / 'downgrade').exists())


    def sign_snapshot(self, apt, legacy=False):
        archive = apt / 'repository-state.tar.gz'
        signature = apt / 'repository-state.tar.gz.asc'
        archive.unlink(missing_ok=True)
        signature.unlink(missing_ok=True)
        temporary = self.root / 'resigned.tar.gz'
        if legacy:
            with tarfile.open(temporary, 'w:gz') as output:
                for child in sorted(apt.iterdir()):
                    output.add(child, arcname=child.name)
        else:
            site.write_snapshot(apt, temporary, 1700000000)
        temporary.rename(archive)
        site.repository.gpg(self.fixture.home, '--local-user', self.identity,
                            '--armor', '--detach-sign', archive)

    def resign_metadata(self, apt, rebuild_manifest=False):
        if rebuild_manifest:
            manifest = site.repository.retained_manifest(apt)
            for suite in site.repository.SUITES:
                (apt / 'dists' / suite / 'retained-files.txt').write_text(manifest)
        for suite in site.repository.SUITES:
            root = apt / 'dists' / suite
            release = root / 'Release'
            release.write_text(site.repository.release_text(suite, root, 1700000000, '1.2.3'))
            site.repository.gpg(self.fixture.home, '--yes', '--local-user', self.identity, '--armor',
                                '--output', root / 'InRelease', '--clearsign', release)
            site.repository.gpg(self.fixture.home, '--yes', '--local-user', self.identity, '--armor',
                                '--output', root / 'Release.gpg', '--detach-sign', release)

    def retained_bytes(self, apt):
        return {path.relative_to(apt).as_posix(): path.read_bytes()
                for path in site.repository.retained_files(apt)}

    def test_new_snapshot_excludes_payloads_but_site_keeps_all_packages(self):
        self.write_candidate('1.2.3')
        initial = self.publish_candidate('metadata-only')
        self.assertEqual(len(list((initial / 'apt/pool').rglob('*.deb'))), 4)
        with tarfile.open(initial / 'apt/repository-state.tar.gz') as archive:
            self.assertFalse(any(name.startswith('pool') for name in archive.getnames()))

    def test_v1_to_v2_to_v2_preserves_every_old_path_and_byte(self):
        self.write_candidate('1.2.3')
        initial = self.publish_candidate('legacy')
        old = self.retained_bytes(initial / 'apt')
        self.sign_snapshot(initial / 'apt', legacy=True)
        with tarfile.open(initial / 'apt/repository-state.tar.gz') as archive:
            self.assertNotIn(site.FORMAT_NAME, archive.getnames())
            self.assertTrue(any(name.startswith('pool/') for name in archive.getnames()))
        self.write_candidate('1.2.4')
        updated = self.publish_candidate('v2', initial)
        self.assertEqual(self.pool_requests, [])
        for name, data in old.items():
            self.assertEqual((updated / 'apt' / name).read_bytes(), data)
        with tarfile.open(updated / 'apt/repository-state.tar.gz') as archive:
            self.assertEqual(archive.extractfile(site.FORMAT_NAME).read(), site.FORMAT_V2)
            self.assertFalse(any(name.startswith('pool') for name in archive.getnames()))
        all_old = self.retained_bytes(updated / 'apt')
        self.write_candidate('1.2.5')
        newest = self.publish_candidate('next-v2', updated)
        self.assertEqual(len(self.pool_requests), 8)
        for name, data in all_old.items():
            self.assertEqual((newest / 'apt' / name).read_bytes(), data)
        self.assertEqual(len(list((newest / 'apt/pool').rglob('*.deb'))), 12)
        site.repository.verify_repository(newest / 'apt', self.identity, self.fixture.home)

    def test_authenticated_bad_v2_metadata_fails_before_pool_requests(self):
        self.write_candidate('1.2.3')
        initial = self.publish_candidate('clean')
        for case in ('duplicate', 'traversal', 'zero-size', 'suite-disagreement', 'missing-index', 'missing-byhash', 'extra-byhash', 'gzip-mismatch', 'wrong-key'):
            previous = self.root / case
            shutil.copytree(initial, previous)
            apt = previous / 'apt'
            manifest = apt / 'dists/noble/retained-files.txt'
            if case == 'duplicate':
                text = manifest.read_text()
                for suite in site.repository.SUITES:
                    (apt / 'dists' / suite / 'retained-files.txt').write_text(text + text.splitlines()[0] + '\n')
            elif case == 'traversal':
                text = manifest.read_text().replace('pool/main/', 'pool/../main/', 1)
                for suite in site.repository.SUITES:
                    (apt / 'dists' / suite / 'retained-files.txt').write_text(text)
            elif case == 'zero-size':
                lines = manifest.read_text().splitlines()
                number = next(i for i, line in enumerate(lines) if ' pool/' in line)
                fields = lines[number].split(' ')
                fields[1] = '0'
                lines[number] = ' '.join(fields)
                for suite in site.repository.SUITES:
                    (apt / 'dists' / suite / 'retained-files.txt').write_text('\n'.join(lines) + '\n')
            elif case == 'missing-byhash':
                next((apt / 'dists/noble/main/binary-amd64/by-hash/SHA256').iterdir()).unlink()
            elif case == 'extra-byhash':
                (apt / 'dists/noble/main/binary-amd64/by-hash/SHA256' / ('a' * 64)).write_bytes(b'unsigned')
            elif case == 'suite-disagreement':
                manifest.write_text(manifest.read_text() + manifest.read_text().splitlines()[0] + '\n')
            elif case == 'gzip-mismatch':
                for suite in site.repository.SUITES:
                    path = apt / 'dists' / suite / 'main/binary-amd64/Packages.gz'
                    path.write_bytes(__import__('gzip').compress(b'different plain index', mtime=0))
                    by_hash = path.parent / 'by-hash/SHA256' / hashlib.sha256(path.read_bytes()).hexdigest()
                    by_hash.write_bytes(path.read_bytes())
                self.resign_metadata(apt, rebuild_manifest=True)
            if case not in ('missing-index', 'wrong-key'):
                self.resign_metadata(apt)
            if case == 'missing-index':
                (apt / 'dists/noble/main/binary-amd64/Packages').unlink()
            if case == 'wrong-key':
                (apt / site.KEY_NAME).write_bytes(b'not a public key')
            self.sign_snapshot(apt)
            self.write_candidate('1.2.4')
            with self.subTest(case=case), self.assertRaises((ValueError, subprocess.CalledProcessError)):
                self.publish_candidate('rejected-' + case, previous)
            self.assertEqual(self.pool_requests, [])
            self.assertFalse((self.root / ('rejected-' + case)).exists())
            self.write_candidate('1.2.3')

    def test_v2_total_materialization_budget_fails_before_any_pool_get(self):
        self.write_candidate('1.2.3')
        initial = self.publish_candidate('initial-budget')
        self.write_candidate('1.2.4')
        with patch.object(site, 'MAX_SITE', 1), self.assertRaisesRegex(ValueError, 'hosting size'):
            self.publish_candidate('over-budget', initial)
        self.assertEqual(self.pool_requests, [])
        self.assertFalse((self.root / 'over-budget').exists())

    def test_tampered_public_snapshot_never_reaches_extraction_or_output(self):
        self.write_candidate('1.2.3')
        initial = self.publish_candidate('initial')
        archive = initial / 'apt/repository-state.tar.gz'
        archive.write_bytes(archive.read_bytes() + b'tampered bytes')
        with patch.object(site, 'extract_snapshot') as extract, self.assertRaises(subprocess.CalledProcessError):
            self.publish_candidate('tampered', initial)
        self.assertEqual(self.pool_requests, [])
        extract.assert_not_called()
        self.assertFalse((self.root / 'tampered').exists())


if __name__ == '__main__':
    unittest.main()
