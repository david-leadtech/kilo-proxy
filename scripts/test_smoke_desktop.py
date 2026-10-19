import errno
from pathlib import Path
import tempfile
import unittest
from unittest.mock import call, patch
import zipfile

import smoke_desktop


def windows_error(number, path):
    error = PermissionError(errno.EACCES, 'synthetic Windows file error', str(path))
    error.winerror = number
    return error


class SmokeTemporaryDirectoryTests(unittest.TestCase):
    def test_windows_sharing_violation_releases_and_cleans_same_owned_root(self):
        with tempfile.TemporaryDirectory() as parent:
            temporary = smoke_desktop.SmokeTemporaryDirectory(dir=parent)
            root = Path(temporary.name)
            binary = root / 'Kilo Proxy.exe'
            binary.write_bytes(b'owned test executable')
            sibling = Path(parent) / 'unrelated'
            sibling.write_text('preserve')
            original_cleanup = tempfile.TemporaryDirectory.cleanup
            attempts = []

            def cleanup(directory):
                self.assertIs(directory, temporary)
                attempts.append(directory.name)
                if len(attempts) == 1:
                    raise windows_error(32, binary)
                original_cleanup(directory)

            with patch.object(smoke_desktop.platform, 'system', return_value='Windows'), \
                    patch.object(tempfile.TemporaryDirectory, 'cleanup', autospec=True,
                                 side_effect=cleanup) as clean, \
                    patch.object(smoke_desktop.time, 'sleep') as sleep:
                temporary.cleanup()

            self.assertEqual(attempts, [str(root), str(root)])
            self.assertEqual(clean.call_count, 2)
            sleep.assert_called_once_with(0.1)
            self.assertFalse(root.exists())
            self.assertEqual(sibling.read_text(), 'preserve')

    def test_persistent_windows_sharing_violation_fails_after_two_second_budget(self):
        temporary = smoke_desktop.SmokeTemporaryDirectory()
        root = Path(temporary.name)
        binary = root / 'Kilo Proxy.exe'
        binary.write_bytes(b'owned test executable')
        error = windows_error(32, binary)
        try:
            with patch.object(smoke_desktop.platform, 'system', return_value='Windows'), \
                    patch.object(tempfile.TemporaryDirectory, 'cleanup', autospec=True,
                                 side_effect=error) as clean, \
                    patch.object(smoke_desktop.time, 'sleep') as sleep, \
                    self.assertRaises(PermissionError) as raised:
                temporary.cleanup()
            self.assertIs(raised.exception, error)
            self.assertEqual(clean.call_args_list, [call(temporary)] * 21)
            self.assertEqual(sleep.call_args_list, [call(0.1)] * 20)
            self.assertTrue(binary.exists(), 'persistent cleanup failure must not be ignored')
        finally:
            temporary.cleanup()

    def test_noneligible_errors_fail_immediately_without_sleep(self):
        cases = [
            ('Windows', 33),
            ('Windows', 5),
            ('Windows', None),
            ('Linux', 32),
            ('Darwin', 32),
        ]
        for system, winerror in cases:
            with self.subTest(system=system, winerror=winerror):
                temporary = smoke_desktop.SmokeTemporaryDirectory()
                path = Path(temporary.name) / 'owned'
                error = (windows_error(winerror, path) if winerror is not None
                         else PermissionError(errno.EACCES, 'not a Windows sharing violation', str(path)))
                try:
                    with patch.object(smoke_desktop.platform, 'system', return_value=system), \
                            patch.object(tempfile.TemporaryDirectory, 'cleanup', autospec=True,
                                         side_effect=error) as clean, \
                            patch.object(smoke_desktop.time, 'sleep') as sleep, \
                            self.assertRaises(PermissionError) as raised:
                        temporary.cleanup()
                    self.assertIs(raised.exception, error)
                    clean.assert_called_once_with(temporary)
                    sleep.assert_not_called()
                finally:
                    temporary.cleanup()

    def test_smoke_failure_remains_authoritative_if_cleanup_also_fails(self):
        temporary = smoke_desktop.SmokeTemporaryDirectory()
        failure = RuntimeError('native smoke report rejected')
        cleanup_error = windows_error(5, Path(temporary.name) / 'owned')
        try:
            with patch.object(smoke_desktop.platform, 'system', return_value='Windows'), \
                    patch.object(tempfile.TemporaryDirectory, 'cleanup', autospec=True,
                                 side_effect=cleanup_error) as clean, \
                    patch.object(smoke_desktop.time, 'sleep') as sleep, \
                    self.assertRaises(RuntimeError) as raised:
                with temporary:
                    raise failure
            self.assertIs(raised.exception, failure)
            self.assertIs(raised.exception.__cause__, cleanup_error)
            clean.assert_called_once_with(temporary)
            sleep.assert_not_called()
        finally:
            temporary.cleanup()

    def test_successful_smoke_still_fails_if_cleanup_cannot_complete(self):
        temporary = smoke_desktop.SmokeTemporaryDirectory()
        error = windows_error(32, Path(temporary.name) / 'Kilo Proxy.exe')
        try:
            with patch.object(smoke_desktop.platform, 'system', return_value='Windows'), \
                    patch.object(tempfile.TemporaryDirectory, 'cleanup', autospec=True,
                                 side_effect=error) as clean, \
                    patch.object(smoke_desktop.time, 'sleep') as sleep, \
                    self.assertRaises(PermissionError) as raised:
                with temporary:
                    pass
            self.assertIs(raised.exception, error)
            self.assertEqual(clean.call_count, 21)
            self.assertEqual(sleep.call_count, 20)
        finally:
            temporary.cleanup()

    def test_main_does_not_repeat_smoke_or_extract_when_cleanup_retries(self):
        for source in ('archive', 'binary'):
            with self.subTest(source=source), tempfile.TemporaryDirectory() as parent:
                parent = Path(parent)
                binary = parent / 'external.exe'
                binary.write_bytes(b'preserve external binary')
                archive = parent / 'desktop.zip'
                with zipfile.ZipFile(archive, 'w') as zipped:
                    zipped.writestr('package/Kilo Proxy.exe', b'owned extracted executable')
                source_path = archive if source == 'archive' else binary
                original_cleanup = tempfile.TemporaryDirectory.cleanup
                original_extract = smoke_desktop.extract
                roots = []
                attempts = []

                def run_smoke(executable, root, version):
                    roots.append(root)
                    self.assertTrue(root.is_dir())
                    self.assertEqual(version, '0.67.2')
                    self.assertEqual(executable.read_bytes(),
                                     b'owned extracted executable' if source == 'archive'
                                     else b'preserve external binary')
                    (root / 'desktop-report.json').write_text('synthetic passed report')

                def cleanup(directory):
                    self.assertEqual(Path(directory.name).resolve(), roots[0])
                    attempts.append(directory)
                    if len(attempts) == 1:
                        raise windows_error(32, roots[0] / 'Kilo Proxy.exe')
                    original_cleanup(directory)

                with patch('sys.argv', ['smoke_desktop.py', '--' + source, str(source_path),
                                        '--version', '0.67.2']), \
                        patch.object(smoke_desktop.platform, 'system', return_value='Windows'), \
                        patch.object(smoke_desktop, 'smoke', side_effect=run_smoke) as run, \
                        patch.object(smoke_desktop, 'extract', wraps=original_extract) as extract, \
                        patch.object(tempfile.TemporaryDirectory, 'cleanup', autospec=True,
                                     side_effect=cleanup) as clean, \
                        patch.object(smoke_desktop.time, 'sleep') as sleep:
                    smoke_desktop.main()

                run.assert_called_once()
                self.assertEqual(extract.call_count, 1 if source == 'archive' else 0)
                self.assertEqual(clean.call_count, 2)
                self.assertIs(attempts[0], attempts[1])
                sleep.assert_called_once_with(0.1)
                self.assertFalse(roots[0].exists())
                self.assertEqual(binary.read_bytes(), b'preserve external binary')
                self.assertTrue(archive.exists())

    def test_main_does_not_repeat_failed_smoke_after_cleanup_releases(self):
        failure = RuntimeError('native process failed its original smoke check')
        original_cleanup = tempfile.TemporaryDirectory.cleanup
        roots = []
        attempts = []

        def run_smoke(binary, root, version):
            roots.append(root)
            raise failure

        def cleanup(directory):
            self.assertEqual(Path(directory.name).resolve(), roots[0])
            attempts.append(directory)
            if len(attempts) == 1:
                raise windows_error(32, roots[0] / 'Kilo Proxy.exe')
            original_cleanup(directory)

        with patch('sys.argv', ['smoke_desktop.py', '--binary', 'not-executed']), \
                patch.object(smoke_desktop.platform, 'system', return_value='Windows'), \
                patch.object(smoke_desktop, 'smoke', side_effect=run_smoke) as run, \
                patch.object(tempfile.TemporaryDirectory, 'cleanup', autospec=True,
                             side_effect=cleanup) as clean, \
                patch.object(smoke_desktop.time, 'sleep') as sleep, \
                self.assertRaises(RuntimeError) as raised:
            smoke_desktop.main()
        self.assertIs(raised.exception, failure)
        run.assert_called_once()
        self.assertEqual(clean.call_count, 2)
        sleep.assert_called_once_with(0.1)
        self.assertFalse(roots[0].exists())


if __name__ == '__main__':
    unittest.main()
