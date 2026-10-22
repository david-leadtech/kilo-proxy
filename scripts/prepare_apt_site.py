#!/usr/bin/env python3
"""Prepare a signed APT Pages artifact; never deploy or use a personal keyring."""
import argparse
import hashlib
import gzip
import io
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.error
import urllib.request
from urllib.parse import urlsplit

import build_apt_repository as repository

ROOT = Path(__file__).resolve().parents[1]
MAX_SNAPSHOT = 400 * 1024 * 1024
MAX_SITE = 900 * 1024 * 1024
KEY_NAME = 'kilo-proxy-archive-keyring.asc'
FORMAT_NAME = 'repository-state-format'
FORMAT_V2 = b'kilo-proxy-apt-snapshot-v2\n'
METADATA_PATTERN = (
    r'(?:kilo-proxy-archive-keyring\.asc|'
    r'dists/(?:noble|resolute)/(?:Release|Release\.gpg|InRelease|retained-files\.txt|'
    r'main/binary-(?:amd64|arm64)/(?:Packages|Packages\.gz|by-hash/SHA256/[0-9a-f]{64})))')


def verify_packages(directory, version):
    if not re.fullmatch(r'\d+\.\d+\.\d+', version):
        raise ValueError('APT publication requires a stable release version')
    expected = {f'kilo-proxy-{kind}_{version}_{arch}.deb'
                for kind in ('desktop', 'headless') for arch in ('amd64', 'arm64')}
    if {p.name for p in directory.glob('*.deb')} != expected:
        raise ValueError('Debian assets must contain exactly the four release packages')
    manifest = directory / 'DEBIAN-SHA256SUMS.txt'
    if manifest.is_symlink() or not manifest.is_file():
        raise ValueError('Missing Debian checksum manifest')
    seen = set()
    for line in manifest.read_text().splitlines():
        match = re.fullmatch(r'([0-9a-f]{64})  (?:\./)?([^/\\]+\.deb)', line)
        if not match or match[2] not in expected or match[2] in seen:
            raise ValueError('Invalid or duplicate Debian checksum entry')
        name = match[2]
        package = directory / name
        if package.is_symlink() or not package.is_file():
            raise ValueError('Unsafe Debian package input')
        digest = hashlib.sha256()
        with package.open('rb') as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b''):
                digest.update(chunk)
        if digest.hexdigest() != match[1]:
            raise ValueError('Debian package checksum mismatch')
        seen.add(name)
    if seen != expected:
        raise ValueError('Incomplete Debian checksum manifest')


def download(url, destination, maximum):
    # Public Pages objects need no GitHub token or other authentication.
    with urllib.request.urlopen(url, timeout=60) as response, destination.open('xb') as output:
        total = 0
        while chunk := response.read(1024 * 1024):
            total += len(chunk)
            if total > maximum:
                raise ValueError('Published APT history exceeds the supported size')
            output.write(chunk)


def extract_snapshot(archive, destination):
    with tarfile.open(archive, 'r:gz') as source:
        members = source.getmembers()
        paths = set()
        total = 0
        for member in members:
            path = PurePosixPath(member.name)
            if (not member.name or path.is_absolute() or '..' in path.parts or
                    '\\' in member.name or member.name in paths or
                    not (member.isfile() or member.isdir())):
                raise ValueError('Unsafe APT history archive entry')
            if path.as_posix() != member.name or member.size < 0 or chr(92) in member.name:
                raise ValueError('Noncanonical APT history archive entry')
            paths.add(member.name)
            total += member.size
            if total > MAX_SNAPSHOT:
                raise ValueError('Extracted APT history exceeds the supported size')
        marker = next((member for member in members if member.name == FORMAT_NAME), None)
        version_two = marker is not None
        if version_two:
            if not marker.isfile() or marker.size != len(FORMAT_V2) or source.extractfile(marker).read() != FORMAT_V2:
                raise ValueError('Unsupported APT snapshot format')
            files = {member.name for member in members if member.isfile()}
            parents = {str(parent) for name in files for parent in PurePosixPath(name).parents
                       if str(parent) != '.'}
            for member in members:
                if ((member.isfile() and member.name != FORMAT_NAME and
                     not re.fullmatch(METADATA_PATTERN, member.name)) or
                        (member.isdir() and member.name not in parents)):
                    raise ValueError('Unsupported metadata-only APT snapshot path')
        by_name = {member.name: member for member in members}
        for member in members:
            for parent in PurePosixPath(member.name).parents:
                ancestor = by_name.get(str(parent))
                if ancestor is not None and not ancestor.isdir():
                    raise ValueError('APT history file conflicts with a directory')
        # Copy explicitly: do not restore archive owners or arbitrary modes.
        for member in members:
            target = destination.joinpath(*PurePosixPath(member.name).parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with source.extractfile(member) as inp, target.open('xb') as out:
                    shutil.copyfileobj(inp, out)
                target.chmod(0o644)
        return version_two


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        return None


def download_pool(url, destination, size, expected_digest):
    """Download one immutable signed path; never accept redirects or partial bytes."""
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(prefix='.retained-', dir=destination.parent) as temporary:
        digest = hashlib.sha256()
        total = 0
        request = urllib.request.Request(url, headers={'Accept-Encoding': 'identity'})
        with opener.open(request, timeout=60) as response:
            if response.geturl() != url:
                raise ValueError('Retained APT package redirected')
            while chunk := response.read(min(1024 * 1024, size - total + 1)):
                total += len(chunk)
                if total > size:
                    raise ValueError('Retained APT package exceeds its signed size')
                digest.update(chunk)
                temporary.write(chunk)
        if total != size or digest.hexdigest() != expected_digest:
            raise ValueError('Retained APT package size or checksum mismatch')
        temporary.flush()
        os.chmod(temporary.name, 0o644)
        # Link publishes only complete verified bytes and refuses an existing path.
        os.link(temporary.name, destination)


def materialize_pool(previous, url, fingerprint, home):
    """Authenticate metadata and budget the entire retained tree before pool GETs."""
    listing = repository.gpg(home, '--with-colons', '--show-keys', previous / KEY_NAME).stdout.decode()
    primary = []
    pending = False
    for line in listing.splitlines():
        fields = line.split(':')
        if fields[0] == 'pub':
            pending = True
        elif fields[0] == 'fpr' and pending:
            primary.append(fields[9])
            pending = False
    if primary != [fingerprint]:
        raise ValueError('Exported repository key does not match the pinned key')
    manifests = []
    indexed_packages = []
    for suite in repository.SUITES:
        root = previous / 'dists' / suite
        repository.verify_signature(home, fingerprint, root / 'Release.gpg', root / 'Release')
        repository.verify_signature(home, fingerprint, root / 'InRelease')
        if repository.gpg(home, '--decrypt', root / 'InRelease').stdout != (root / 'Release').read_bytes():
            raise ValueError('InRelease payload differs from Release')
        fields = repository.parse_control((root / 'Release').read_text())
        if (fields.get('Origin') != 'Kilo Proxy' or fields.get('Label') != 'Kilo Proxy' or
                fields.get('Suite') != suite or fields.get('Codename') != suite or
                fields.get('Architectures') != ' '.join(repository.ARCHITECTURES) or
                fields.get('Components') != 'main' or fields.get('Acquire-By-Hash') != 'yes' or
                not re.fullmatch(repository.VERSION_PATTERN, fields.get('Version', ''))):
            raise ValueError('Incorrect metadata-only Release identity')
        expected = {f'main/binary-{architecture}/{name}' for architecture in repository.ARCHITECTURES
                    for name in ('Packages', 'Packages.gz')} | {repository.RETAINED_MANIFEST}
        seen = set()
        for line in fields.get('SHA256', '').splitlines():
            if not line.strip():
                continue
            match = re.fullmatch(r' ([0-9a-f]{64}) +([0-9]+) (\S+)', line)
            if not match or match[3] not in expected or match[3] in seen:
                raise ValueError('Invalid or duplicate Release checksum entry')
            seen.add(match[3])
            path = repository.safe_repository_path(root, match[3])
            if path.stat().st_size != int(match[2]) or repository.digest(path) != match[1]:
                raise ValueError('Release index size or checksum mismatch')
            if match[3] != repository.RETAINED_MANIFEST:
                by_hash = path.parent / 'by-hash/SHA256' / match[1]
                if not by_hash.is_file() or by_hash.read_bytes() != path.read_bytes():
                    raise ValueError('Missing or incorrect by-hash index')
        if seen != expected:
            raise ValueError('Incomplete metadata-only Release indexes')
        for architecture in repository.ARCHITECTURES:
            index = root / f'main/binary-{architecture}/Packages'
            with gzip.open(index.with_suffix('.gz'), 'rb') as compressed:
                if compressed.read(index.stat().st_size + 1) != index.read_bytes():
                    raise ValueError('Compressed Packages differs from plain index')
            found = set()
            for paragraph in index.read_text().strip().split('\n\n'):
                record = repository.parse_control(paragraph)
                package = record.get('Package')
                if (package not in repository.PACKAGES or package in found or
                        record.get('Architecture') != architecture or record.get('Version') != fields['Version']):
                    raise ValueError('Unexpected or duplicate indexed package identity')
                found.add(package)
                expected_name = f'pool/main/k/kilo-proxy/{package}_{fields["Version"]}_{architecture}.deb'
                if record.get('Filename') != expected_name:
                    raise ValueError('Incorrect package pool filename')
                indexed_packages.append(record)
            if found != set(repository.PACKAGES):
                raise ValueError('Incomplete metadata-only package matrix')
        manifests.append((fields['Version'], (root / repository.RETAINED_MANIFEST).read_text()))
    if manifests[0] != manifests[1]:
        raise ValueError('Suite retained-file manifests or versions disagree')
    entries = {}
    for line in manifests[0][1].splitlines():
        match = re.fullmatch(r'([0-9a-f]{64}) ([0-9]+) (\S+)', line)
        if (not match or not re.fullmatch(repository.RETAINED_PATTERN, match[3]) or
                match[3] in entries or int(match[2]) <= 0):
            raise ValueError('Invalid or duplicate retained repository file')
        entries[match[3]] = (int(match[2]), match[1])
    actual = {path.relative_to(previous).as_posix() for path in repository.retained_files(previous)}
    indexes = {name for name in entries if not name.startswith('pool/')}
    if actual != indexes:
        raise ValueError('Unsigned or missing metadata-only retained index')
    for name in indexes:
        size, digest = entries[name]
        path = repository.safe_repository_path(previous, name)
        if path.name != digest or path.stat().st_size != size or repository.digest(path) != digest:
            raise ValueError('Retained by-hash index size or checksum mismatch')
    pool = {name: entry for name, entry in entries.items() if name.startswith('pool/')}
    if not pool:
        raise ValueError('Metadata-only snapshot has no retained packages')
    for record in indexed_packages:
        entry = pool.get(record['Filename'])
        if entry is None or (str(entry[0]), entry[1]) != (record.get('Size'), record.get('SHA256')):
            raise ValueError('Indexed package differs from the retained manifest')
    metadata_bytes = sum(path.stat().st_size for path in previous.rglob('*') if path.is_file())
    if metadata_bytes + sum(size for size, digest in pool.values()) > MAX_SITE:
        raise ValueError('Materialized APT history exceeds its supported hosting size')
    for name, (size, digest) in sorted(pool.items()):
        download_pool(url + '/' + name, previous / name, size, digest)
    repository.verify_repository(previous, fingerprint, home)


def write_snapshot(apt, snapshot, epoch):
    """Keep every deployed payload, but authenticate only metadata in the snapshot."""
    metadata = []
    total = len(FORMAT_V2)
    for path in sorted(apt.rglob('*')):
        relative = path.relative_to(apt).as_posix()
        if path.is_symlink() or (not path.is_dir() and not path.is_file()):
            raise ValueError('Unsafe APT snapshot metadata')
        if relative == 'pool' or relative.startswith('pool/'):
            continue
        if path.is_file():
            if not re.fullmatch(METADATA_PATTERN, relative):
                raise ValueError('Unsupported APT snapshot metadata path')
            total += path.stat().st_size
            if total > MAX_SNAPSHOT:
                raise ValueError('APT snapshot metadata exceeds its supported size')
            metadata.append((path, relative))
    if total > MAX_SNAPSHOT:
        raise ValueError('APT snapshot metadata exceeds its supported size')
    with tarfile.open(snapshot, 'w:gz') as archive:
        for path, relative in metadata:
            archive.add(path, arcname=relative, recursive=False)
        marker = tarfile.TarInfo(FORMAT_NAME)
        marker.size = len(FORMAT_V2)
        marker.mode = 0o644
        marker.mtime = epoch
        archive.addfile(marker, io.BytesIO(FORMAT_V2))


def previous_repository(url, fingerprint, home, temporary, allow_bootstrap):
    parsed = urlsplit(url)
    if (parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password or
            parsed.query or parsed.fragment or '%' in parsed.path or chr(92) in url or
            any(ord(character) <= 32 or ord(character) == 127 for character in url) or
            any(part in ('.', '..') for part in parsed.path.split('/'))):
        raise ValueError('APT history requires a plain HTTPS repository URL')
    url = url.rstrip('/')
    archive, signature = temporary / 'previous.tar.gz', temporary / 'previous.tar.gz.asc'
    try:
        download(url + '/repository-state.tar.gz', archive, MAX_SNAPSHOT)
    except urllib.error.HTTPError as error:
        if error.code != 404 or not allow_bootstrap:
            raise
        # A missing snapshot must not silently reset an existing repository.
        try:
            download(url + '/dists/noble/InRelease', temporary / 'existing-release', 1024 * 1024)
        except urllib.error.HTTPError as absent:
            if absent.code == 404:
                return None
            raise
        raise ValueError('Existing APT metadata has no history snapshot; bootstrap refused')
    download(url + '/repository-state.tar.gz.asc', signature, 1024 * 1024)
    keyring = temporary / 'trusted.gpg'
    with keyring.open('xb') as output:
        subprocess.run(['gpg', '--homedir', str(home), '--batch', '--export', fingerprint],
                       stdout=output, check=True)
    # Authenticate the entire archive before parsing or extracting it.
    subprocess.run(['gpgv', '--homedir', str(home), '--keyring', str(keyring),
                    str(signature), str(archive)], check=True)
    previous = temporary / 'previous'
    previous.mkdir()
    if extract_snapshot(archive, previous):
        materialize_pool(previous, url, fingerprint, home)
    return previous


def check_previous_version(previous, version):
    if previous is None:
        return
    fields = {}
    for line in (previous / 'dists/noble/Release').read_text().splitlines():
        if ': ' in line and not line.startswith(' '):
            key, value = line.split(': ', 1)
            if key in fields:
                raise ValueError('Duplicate previous repository field')
            fields[key] = value
    current = fields.get('Version', '')
    if not re.fullmatch(r'\d+\.\d+\.\d+', current):
        raise ValueError('Previous APT repository has no stable release identity')
    if tuple(map(int, version.split('.'))) < tuple(map(int, current.split('.'))):
        raise ValueError('Refusing to replace APT with an older release')


def prepare(packages, output, fingerprint, home, previous_url, allow_bootstrap, epoch):
    version = (ROOT / 'VERSION').read_text().strip()
    verify_packages(packages, version)
    if not re.fullmatch(r'(?:[0-9A-Fa-f]{40}|[0-9A-Fa-f]{64})', fingerprint):
        raise ValueError('Supply the complete APT signing fingerprint')
    fingerprint = fingerprint.upper()
    if not home.is_dir() or home.is_symlink() or (home.stat().st_mode & 0o077):
        raise ValueError('Use an explicitly selected private GNUPGHOME with mode 0700')
    if output.exists() or output.is_symlink():
        raise ValueError('Refusing to overwrite an existing site')
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='kilo-apt-site-', dir=output.parent) as temporary_name:
        temporary = Path(temporary_name)
        previous = previous_repository(previous_url, fingerprint, home, temporary, allow_bootstrap)
        check_previous_version(previous, version)
        site = temporary / 'site'
        site.mkdir()
        command = [sys.executable, str(ROOT / 'scripts/build_apt_repository.py'),
                   '--packages-directory', str(packages), '--output', str(site / 'apt'),
                   '--signing-key', fingerprint, '--gnupg-home', str(home),
                   '--source-date-epoch', str(epoch)]
        if previous:
            command += ['--previous-repository', str(previous)]
        subprocess.run(command, check=True)
        apt = site / 'apt'
        # Snapshot excludes itself; its detached signature authenticates history
        # on the next deployment, while APT verifies InRelease and package hashes.
        snapshot = temporary / 'repository-state.tar.gz'
        write_snapshot(apt, snapshot, epoch)
        if snapshot.stat().st_size > MAX_SNAPSHOT:
            raise ValueError('APT history needs explicit archival maintenance before publishing')
        shutil.move(snapshot, apt / snapshot.name)
        subprocess.run(['gpg', '--homedir', str(home), '--batch', '--yes',
                        '--local-user', fingerprint, '--armor', '--detach-sign',
                        str(apt / snapshot.name)], check=True)
        (site / '.nojekyll').touch()
        (site / 'index.html').write_text(
            '<!doctype html><html lang="en"><meta charset="utf-8">'
            '<title>Kilo Proxy APT repository</title><h1>Kilo Proxy APT repository</h1>'
            '<p>Desktop and headless packages for Ubuntu 24.04 and 26.04 LTS, amd64 and arm64.</p>'
            '<p><a href="https://github.com/rosseca/kilo-proxy/blob/main/docs/ubuntu-apt.md">'
            'Installation and signature verification instructions</a></p></html>\n')
        if sum(p.stat().st_size for p in site.rglob('*') if p.is_file()) > MAX_SITE:
            raise ValueError('APT site exceeds its supported hosting size')
        site.rename(output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--packages-directory', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--signing-key', required=True)
    parser.add_argument('--gnupg-home', type=Path, default=os.environ.get('GNUPGHOME'))
    parser.add_argument('--previous-url', required=True)
    parser.add_argument('--allow-bootstrap', action='store_true')
    parser.add_argument('--source-date-epoch', type=int, default=None)
    args = parser.parse_args()
    if args.gnupg_home is None:
        parser.error('An isolated --gnupg-home or GNUPGHOME is required')
    epoch = args.source_date_epoch
    if epoch is None:
        epoch = int(subprocess.check_output(['git', 'log', '-1', '--format=%ct'], cwd=ROOT, text=True))
    if epoch < 0:
        parser.error('source-date-epoch must be nonnegative')
    prepare(args.packages_directory.resolve(), args.output.absolute(), args.signing_key,
            Path(args.gnupg_home).resolve(), args.previous_url, args.allow_bootstrap, epoch)


if __name__ == '__main__':
    main()
