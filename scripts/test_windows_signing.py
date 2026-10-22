"""Exercise persistent key setup and real Authenticode signing on Windows runners."""
import base64
import os
from pathlib import Path
import shutil
import struct
import subprocess
import tempfile
import unittest

import setup_windows_signing

ROOT = Path(__file__).resolve().parents[1]


@unittest.skipUnless(shutil.which('openssl'), 'OpenSSL is required for identity setup')
class SigningIdentityTests(unittest.TestCase):
    def test_setup_reuses_identical_identity_and_recovers_public_copy(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            private, public = root / 'private', root / 'public/signing.cer'
            pfx, password = setup_windows_signing.prepare_identity(private, public)
            identity = pfx.read_bytes(), password, public.read_bytes()
            self.assertEqual(setup_windows_signing.prepare_identity(private, public), (pfx, password))
            self.assertEqual((pfx.read_bytes(), password, public.read_bytes()), identity)
            public.unlink()
            setup_windows_signing.prepare_identity(private, public)
            self.assertEqual(public.read_bytes(), identity[2])
            if os.name != 'nt':
                self.assertEqual(private.stat().st_mode & 0o777, 0o700)
                for path in private.iterdir():
                    self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            public.write_bytes(b'another identity')
            with self.assertRaisesRegex(ValueError, 'refusing to rotate'):
                setup_windows_signing.prepare_identity(private, public)
            self.assertEqual(pfx.read_bytes(), identity[0])

    def test_missing_private_backup_never_replaces_published_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            public = root / 'signing.cer'
            public.write_bytes(b'existing published certificate')
            with self.assertRaisesRegex(ValueError, 'restore its original PFX'):
                setup_windows_signing.prepare_identity(root / 'private', public)
            self.assertEqual(public.read_bytes(), b'existing published certificate')
            (root / 'private/password.txt').write_text('partial setup')
            with self.assertRaisesRegex(ValueError, 'Incomplete private identity'):
                setup_windows_signing.prepare_identity(root / 'private', public)


@unittest.skipUnless(os.name == 'nt' and os.environ.get('KILO_TEST_WINDOWS_SIGNING') == '1',
                     'Real SignTool verification runs explicitly in the native Windows jobs')
class WindowsAuthenticodeTests(unittest.TestCase):
    def run_signing(self, command, env):
        try:
            return subprocess.run(command, env=env, capture_output=True, text=True, timeout=90)
        except subprocess.TimeoutExpired as error:
            output = (error.stdout or b'') + (error.stderr or b'')
            self.fail('Signing command timed out. Last output: ' + output.decode(errors='replace'))

    def test_sign_both_architectures_verify_archive_and_reject_tampering(self):
        # CI uses a disposable identity here, never the production release secrets.
        with tempfile.TemporaryDirectory(prefix='kilo signing test ') as temporary:
            root = Path(temporary)
            fixture = r'''
                $ErrorActionPreference = 'Stop'
                $cert = New-SelfSignedCertificate -Type CodeSigningCert -Subject 'CN=Kilo CI Fixture' -CertStoreLocation Cert:\CurrentUser\My
                try {
                    $password = ConvertTo-SecureString 'disposable-fixture-password' -AsPlainText -Force
                    $null = Export-PfxCertificate -Cert $cert -FilePath (Join-Path $env:KILO_FIXTURE_DIR 'fixture.pfx') -Password $password
                    $null = Export-Certificate -Cert $cert -FilePath (Join-Path $env:KILO_FIXTURE_DIR 'fixture.cer')
                } finally {
                    Remove-Item "Cert:\CurrentUser\My\$($cert.Thumbprint)" -DeleteKey -Force
                }
            '''
            env = dict(os.environ, KILO_FIXTURE_DIR=str(root))
            if shutil.which('openssl'):
                # Exercise the same OpenSSL PFX format and leaf extensions as production.
                pfx, password = setup_windows_signing.prepare_identity(root / 'private', root / 'fixture.cer')
            else:
                subprocess.run(['pwsh', '-NoProfile', '-NonInteractive', '-Command', fixture], env=env, check=True,
                               capture_output=True, text=True)
                pfx, password = root / 'fixture.pfx', 'disposable-fixture-password'
            env.update(KILO_WINDOWS_SIGNING_PFX=base64.b64encode(pfx.read_bytes()).decode(),
                       KILO_WINDOWS_SIGNING_PASSWORD=password)
            source = root / 'main.go'
            source.write_text('package main\nfunc main() {}\n')
            script_command = ['pwsh', '-NoProfile', '-NonInteractive', '-File', str(ROOT / 'scripts/windows_sign.ps1')]
            command = [*script_command, '-Certificate', str(root / 'fixture.cer')]
            for arch in ('amd64', 'arm64'):
                binary = root / f'fixture-{arch}.exe'
                subprocess.run(['go', 'build', '-o', str(binary), str(source)],
                               env=dict(env, GOOS='windows', GOARCH=arch, CGO_ENABLED='0'), check=True,
                               capture_output=True, text=True)
                missing = dict(env, KILO_WINDOWS_SIGNING_PFX='', KILO_WINDOWS_SIGNING_PASSWORD='')
                result = self.run_signing([*command, '-Binary', str(binary)], missing)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('Release signing requires', result.stdout + result.stderr)
                wrong_identity = [*script_command, '-Certificate', str(setup_windows_signing.PUBLIC_CERTIFICATE)]
                result = self.run_signing([*wrong_identity, '-Binary', str(binary)], env)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('PFX does not match', result.stdout + result.stderr)
                for arguments in ([], ['-VerifyOnly']):
                    result = self.run_signing([*command, '-Binary', str(binary), *arguments], env)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                # Verify the copy that a ZIP consumer actually extracts.
                import zipfile
                archive = root / f'{arch}.zip'
                with zipfile.ZipFile(archive, 'w') as output:
                    output.write(binary, 'Kilo Proxy.exe')
                with zipfile.ZipFile(archive) as output:
                    output.extractall(root / arch)
                extracted = root / arch / 'Kilo Proxy.exe'
                self.assertEqual(extracted.read_bytes(), binary.read_bytes())
                result = self.run_signing([*command, '-VerifyOnly', '-Binary', str(extracted)], missing)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                data = bytearray(extracted.read_bytes())
                pe = struct.unpack_from('<I', data, 60)[0]
                optional_size = struct.unpack_from('<H', data, pe + 20)[0]
                section = pe + 24 + optional_size
                text_offset = struct.unpack_from('<I', data, section + 20)[0]
                data[text_offset + 32] ^= 1
                extracted.write_bytes(data)
                result = self.run_signing([*command, '-VerifyOnly', '-Binary', str(extracted)], missing)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('verification failed', result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main()
