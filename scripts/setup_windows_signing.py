#!/usr/bin/env python3
"""Create a persistent, private Windows signing identity once; never rotate it implicitly."""
import argparse
import base64
import os
from pathlib import Path
import secrets
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PUBLIC_CERTIFICATE = ROOT / 'certificates' / 'kilo-proxy-windows.cer'


def openssl(*arguments, input=None):
    return subprocess.run(['openssl', *map(str, arguments)], check=True,
                          input=input, capture_output=True).stdout


def prepare_identity(directory, public_certificate):
    directory = directory.expanduser().resolve()
    public_certificate = public_certificate.resolve()
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    if os.name != 'nt':
        directory.chmod(0o700)
    pfx, password_file = directory / 'signing.pfx', directory / 'password.txt'
    backup = directory / 'signing.cer'
    if any(directory.iterdir()):
        if not all(p.is_file() and not p.is_symlink() for p in (pfx, password_file, backup)):
            raise ValueError('Incomplete private identity: restore its backup; do not generate a replacement.')
        password = password_file.read_text().strip()
        # Prove the password opens the PFX and that it contains the same certificate.
        pem = openssl('pkcs12', '-in', pfx, '-passin', f'file:{password_file}', '-clcerts', '-nokeys')
        with tempfile.TemporaryDirectory() as temporary:
            certificate = Path(temporary) / 'certificate.pem'
            certificate.write_bytes(pem)
            der = openssl('x509', '-in', certificate, '-outform', 'DER')
        if der != backup.read_bytes():
            raise ValueError('Private identity certificate mismatch; refusing to change the identity.')
    else:
        if public_certificate.exists():
            raise ValueError('Public identity already exists: restore its original PFX and password backup.')
        password = secrets.token_urlsafe(48)
        password_file.write_text(password + '\n')
        password_file.chmod(0o600)
        with tempfile.TemporaryDirectory(dir=directory) as temporary:
            key, pem = Path(temporary) / 'key.pem', Path(temporary) / 'certificate.pem'
            openssl('req', '-new', '-x509', '-newkey', 'rsa:3072', '-sha256', '-days', '3650',
                    '-subj', '/CN=Kilo Proxy Release Signing',
                    '-addext', 'basicConstraints=critical,CA:FALSE',
                    '-addext', 'keyUsage=critical,digitalSignature',
                    '-addext', 'extendedKeyUsage=codeSigning',
                    '-keyout', key, '-passout', f'file:{password_file}', '-out', pem)
            openssl('pkcs12', '-export', '-inkey', key, '-passin', f'file:{password_file}',
                    '-in', pem, '-out', pfx, '-passout', 'stdin',
                    '-name', 'Kilo Proxy Release Signing', input=(password + '\n').encode())
            der = openssl('x509', '-in', pem, '-outform', 'DER')
        backup.write_bytes(der)
    for path in (pfx, password_file, backup):
        path.chmod(0o600)
    if public_certificate.exists() and public_certificate.read_bytes() != der:
        raise ValueError('Repository certificate differs: refusing to rotate the published identity.')
    public_certificate.parent.mkdir(parents=True, exist_ok=True)
    if not public_certificate.exists():
        public_certificate.write_bytes(der)
    return pfx, password


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--private-directory', type=Path,
                        default=Path.home() / '.config/kilo-proxy/windows-signing')
    parser.add_argument('--public-certificate', type=Path, default=PUBLIC_CERTIFICATE)
    parser.add_argument('--upload-secrets', action='store_true')
    parser.add_argument('--repo', default='rosseca/kilo-proxy')
    args = parser.parse_args()
    # Restrict even the initial creation of private files, before chmod runs.
    os.umask(0o077)
    try:
        pfx, password = prepare_identity(args.private_directory, args.public_certificate)
        if args.upload_secrets:
            for name, value in [('KILO_WINDOWS_SIGNING_PFX', base64.b64encode(pfx.read_bytes()).decode()),
                                ('KILO_WINDOWS_SIGNING_PASSWORD', password)]:
                subprocess.run(['gh', 'secret', 'set', name, '--repo', args.repo],
                               input=value, text=True, check=True, capture_output=True)
            print('Configured GitHub Actions signing secrets.')
        print(f'Private backup (keep both files secure): {pfx.parent}')
        print(f'Public certificate: {args.public_certificate}')
        print(openssl('x509', '-inform', 'DER', '-in', args.public_certificate,
                      '-noout', '-subject', '-dates', '-fingerprint', '-sha1').decode().strip())
    except (ValueError, subprocess.CalledProcessError) as error:
        # Never print command arguments/output: a failed secret upload contains credentials.
        parser.exit(1, f'Signing setup failed: {str(error) if isinstance(error, ValueError) else "external command failed"}\n')


if __name__ == '__main__':
    main()
