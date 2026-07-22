from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from scripts.lanready_compose_paths import bind_source, cache_mode


class ComposePathTest(unittest.TestCase):
    def config(self, data: Path, cache: Path) -> dict:
        return {
            "services": {
                "server": {
                    "volumes": [
                        {"type": "bind", "source": str(data), "target": "/data"},
                        {"type": "bind", "source": str(cache), "target": "/cache"},
                    ]
                }
            }
        }

    def test_resolves_non_default_external_cache(self) -> None:
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            data = root / "state"
            cache = root / "custom" / "nas-cache"
            data.mkdir()
            cache.mkdir(parents=True)
            config = self.config(data, cache)
            self.assertEqual(bind_source(config, "/cache"), cache.resolve())
            self.assertEqual(cache_mode(config), "external")

    def test_detects_cache_inside_data(self) -> None:
        with TemporaryDirectory() as temporary:
            data = Path(temporary) / "state"
            cache = data / "cache"
            cache.mkdir(parents=True)
            self.assertEqual(cache_mode(self.config(data, cache)), "inside-data")


if __name__ == "__main__":
    unittest.main()
