import base64
import unittest

from scripts.validate_release_keys import validate


class ReleaseKeyValidationTest(unittest.TestCase):
    def test_distinct_32_byte_keys_are_accepted(self) -> None:
        validate(base64.b64encode(b"u" * 32).decode(), base64.b64encode(b"e" * 32).decode())

    def test_identical_keys_are_rejected(self) -> None:
        key = base64.b64encode(b"x" * 32).decode()
        with self.assertRaisesRegex(ValueError, "must be different"):
            validate(key, key)

    def test_wrong_length_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "exactly 32 bytes"):
            validate(base64.b64encode(b"short").decode(), base64.b64encode(b"e" * 32).decode())


if __name__ == "__main__":
    unittest.main()
