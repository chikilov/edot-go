"""Source archive secret-file guards, using isolated synthetic Git repositories."""
import contextlib
import io
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import package_release


class PackageSecurityTests(unittest.TestCase):
    def repository(self, root, tracked):
        root.mkdir()
        subprocess.run(['git', 'init', '-q', str(root)], check=True)
        (root/'sdk.go').write_text('package edotgo\nconst Version = "0.5.1"\n')
        (root/'go.mod').write_text('module example.invalid/synthetic\ngo 1.26.10\n')
        for name in tracked:
            (root/name).write_text('synthetic-placeholder-only')
        subprocess.run(['git', '-C', str(root), 'add', '.'], check=True)
        subprocess.run(['git', '-C', str(root), '-c', 'user.name=Archive Test', '-c', 'user.email=archive@example.invalid', 'commit', '-qm', 'synthetic source'], check=True)

    def test_tracked_private_env_variants_rejected(self):
        for name in ('.env', '.env.backup', 'production.env'):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                root, output = Path(temp)/'repo', Path(temp)/'out'
                self.repository(root, [name])
                with patch.object(package_release, 'REPO', root), patch('sys.argv', ['package_release.py', '--output-dir', str(output)]), self.assertRaises(SystemExit):
                    package_release.main()
                self.assertFalse(output.exists())

    def test_examples_allowed_untracked_credentials_excluded(self):
        with tempfile.TemporaryDirectory() as temp:
            root, output = Path(temp)/'repo', Path(temp)/'out'
            self.repository(root, ['.env.example', '.env.apm.example'])
            (root/'.env.local').write_text('synthetic-private-content')
            with patch.object(package_release, 'REPO', root), patch('sys.argv', ['package_release.py', '--output-dir', str(output)]), contextlib.redirect_stdout(io.StringIO()):
                package_release.main()
            with zipfile.ZipFile(output/'edot-go-v0.5.1-source.zip') as archive:
                self.assertFalse(any(n.endswith('/.env.local') for n in archive.namelist()))
                self.assertTrue(any(n.endswith('/.env.example') for n in archive.namelist()))
