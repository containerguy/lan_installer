import os
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from scripts.lanready_secrets import SECRET_NAMES, backup, restore


class SecretBackupRestoreTest(unittest.TestCase):
    def test_round_trip_with_all_sources_outside_repo_secret_directory(self) -> None:
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            sources = root / "external" / "configured"
            sources.mkdir(parents=True)
            config = {"secrets": {}}
            originals: dict[str, bytes] = {}
            for index, name in enumerate(SECRET_NAMES):
                path = sources / f"custom-{index}.key"
                content = f"secret-{name}".encode()
                path.write_bytes(content)
                os.chmod(path, 0o600)
                originals[name] = content
                config["secrets"][name] = {"file": str(path)}

            backup_directory = root / "backup" / "resolved-secrets"
            backup(config, backup_directory)
            for name in SECRET_NAMES:
                Path(config["secrets"][name]["file"]).write_bytes(b"changed")
            restore(config, backup_directory)

            for name, content in originals.items():
                path = Path(config["secrets"][name]["file"])
                self.assertEqual(path.read_bytes(), content)
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_restore_rejects_changed_configured_path(self) -> None:
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            config = {"secrets": {}}
            for name in SECRET_NAMES:
                path = root / f"{name}.key"
                path.write_text(name, encoding="utf-8")
                config["secrets"][name] = {"file": str(path)}
            backup_directory = root / "backup"
            backup(config, backup_directory)
            config["secrets"][SECRET_NAMES[0]]["file"] = str(root / "different.key")
            with self.assertRaisesRegex(ValueError, "configured restore path"):
                restore(config, backup_directory)

    def test_missing_backup_secret_changes_no_current_secret(self) -> None:
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            config = {"secrets": {}}
            old_values: dict[str, bytes] = {}
            for name in SECRET_NAMES:
                path = root / f"{name}.key"
                value = f"old-{name}".encode()
                path.write_bytes(value)
                old_values[name] = value
                config["secrets"][name] = {"file": str(path)}
            backup_directory = root / "backup"
            backup(config, backup_directory)
            (backup_directory / f"{SECRET_NAMES[-1]}.secret").unlink()

            with self.assertRaises(FileNotFoundError):
                restore(config, backup_directory)
            for name, value in old_values.items():
                self.assertEqual(Path(config["secrets"][name]["file"]).read_bytes(), value)


if __name__ == "__main__":
    unittest.main()
