import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("release_tag", Path(__file__).with_name("release-tag.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ReleaseTagTests(unittest.TestCase):
    def test_release_and_candidate_channels(self):
        for tag in ("v0.1.0", "v1.0.0", "v10.20.30"):
            self.assertEqual(module.classify(tag), "stable")
        for tag in ("v0.1.0-rc.1", "v0.1.0-d1.4"):
            self.assertEqual(module.classify(tag), "prerelease")

    def test_ambiguous_or_unreviewed_tags_rejected(self):
        for tag in ("v01.1.0", "v1.0", "v1.0.0\n", "v1.0.0-dev", "v1.0.0-rc.0", "v1.0.0-rc.01", "v1.0.0+build", "v1.0.0;id", "main"):
            with self.assertRaises(ValueError):
                module.classify(tag)
