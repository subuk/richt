"""Fault injection at publication boundaries; called by the Go test suite."""
import copy
import importlib.util
import json
import os
import stat
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("prepared_file_helper", "file_helper.py")
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


class Transactions(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.TemporaryDirectory()
        self.addCleanup(self.root.cleanup)
        self.path = os.path.join(self.root.name, "config")
        with open(self.path, "wb") as output:
            output.write(b"old\x00bytes")
        os.chmod(self.path, 0o640)
        self.expected, _ = helper.snapshot(self.path)
        self.f = {"path": self.path, "state": "present", "content": "new bytes",
                  "atomic_writes": True, "backup": True,
                  "operation_id": "a" * 32, "expected": self.expected,
                  "desired": {"kind": "regular", "sha256": helper.hashlib.sha256(b"new bytes").hexdigest()},
                  "backup_path": self.path + ".20261106T200000Z." + "a" * 32 + "~"}

    def request(self, action="overwrite-file", resume=False):
        return {"file": self.f, "action": action, "resume": resume, "uid": -1, "gid": -1}

    def read(self, path=None):
        with open(path or self.path, "rb") as source:
            return source.read()

    def test_backup_is_independent_and_retry_keeps_identity(self):
        alias = self.path + ".alias"
        os.link(self.path, alias)
        helper.main(self.request())
        self.assertEqual(self.read(), b"new bytes")
        self.assertEqual(self.read(self.f["backup_path"]), b"old\x00bytes")
        with open(alias, "wb") as output:
            output.write(b"external")
        helper.main(self.request(resume=True))
        self.assertEqual(self.read(self.f["backup_path"]), b"old\x00bytes")
        os.unlink(self.f["backup_path"])
        with self.assertRaises(helper.Failure):
            helper.main(self.request(resume=True))

    def test_pending_does_not_adopt_desired_bytes(self):
        with open(self.path, "wb") as output:
            output.write(b"new bytes")
        with self.assertRaisesRegex(helper.Failure, "conflict"):
            helper.main(self.request())

    def test_failed_publication_keeps_old_name_and_backup(self):
        with mock.patch.object(helper.os, "replace", side_effect=OSError("injected")):
            with self.assertRaises(OSError):
                helper.main(self.request())
        self.assertEqual(self.read(), b"old\x00bytes")
        self.assertEqual(self.read(self.f["backup_path"]), b"old\x00bytes")
        helper.main(self.request(resume=True))
        self.assertEqual(self.read(), b"new bytes")

    def test_original_entry_stays_present_until_single_publication(self):
        real_replace = helper.os.replace
        calls = []
        def replace(source, destination):
            self.assertEqual(destination, self.path)
            self.assertEqual(self.read(), b"old\x00bytes")
            self.assertEqual(self.read(self.f["backup_path"]), b"old\x00bytes")
            calls.append(destination)
            real_replace(source, destination)
        with mock.patch.object(helper.os, "replace", side_effect=replace):
            helper.main(self.request())
        self.assertEqual(calls, [self.path])

    def test_backup_failure_precedes_publication(self):
        with mock.patch.object(helper.os, "link", side_effect=OSError("injected")):
            with self.assertRaises(OSError):
                helper.main(self.request())
        self.assertEqual(self.read(), b"old\x00bytes")
        self.assertFalse(os.path.lexists(self.f["backup_path"]))

    def test_backup_without_receipt_cannot_be_adopted(self):
        helper.main(self.request())
        os.unlink(self.f["backup_path"] + ".richt.json")
        with self.assertRaises(helper.Failure):
            helper.main(self.request(resume=True))
        self.assertEqual(self.read(self.f["backup_path"]), b"old\x00bytes")

    def test_receipt_only_crash_recovers_only_matching_old_metadata(self):
        real_link = helper.os.link
        def link(source, dest, **kwargs):
            if dest == self.f["backup_path"]:
                raise OSError("interrupted before backup publication")
            return real_link(source, dest, **kwargs)
        with mock.patch.object(helper.os, "link", side_effect=link):
            with self.assertRaises(OSError):
                helper.main(self.request())
        self.assertEqual(self.read(), b"old\x00bytes")
        self.assertTrue(os.path.exists(self.f["backup_path"] + ".richt.json"))
        helper.main(self.request(resume=True))
        self.assertEqual(self.read(), b"new bytes")

    def test_metadata_drift_does_not_content_conflict(self):
        os.chmod(self.path, 0o600)
        helper.main(self.request())
        self.assertEqual(stat.S_IMODE(os.stat(self.path).st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(os.stat(self.f["backup_path"]).st_mode), 0o600)
        helper.main(self.request(resume=True))
        os.chmod(self.f["backup_path"], 0o644)
        with self.assertRaisesRegex(helper.Failure, "backup"):
            helper.main(self.request(resume=True))

    def test_backup_collision_preserves_destination(self):
        with open(self.f["backup_path"], "wb") as output:
            output.write(b"collision")
        with self.assertRaises(helper.Failure):
            helper.main(self.request())
        self.assertEqual(self.read(), b"old\x00bytes")
        self.assertEqual(self.read(self.f["backup_path"]), b"collision")

    def test_directory_sync_failure_after_publish_reports_installed_state(self):
        self.f["backup_path"] = ""
        request = self.request()
        with mock.patch.object(helper, "sync_dir", side_effect=OSError("injected")):
            with self.assertRaises(OSError):
                helper.main(request)
        self.assertTrue(request["published"])
        self.assertEqual(self.read(), b"new bytes")
        helper.main(self.request(resume=True))

    def test_interrupted_removal_requires_backup(self):
        self.f["state"] = "absent"
        self.f["desired"] = {"kind": "absent"}
        request = self.request("remove-file")
        real_sync = helper.sync_dir
        def sync(path):
            if not os.path.lexists(self.path):
                raise OSError("injected post-removal")
            return real_sync(path)
        with mock.patch.object(helper, "sync_dir", side_effect=sync):
            with self.assertRaises(OSError):
                helper.main(request)
        self.assertFalse(os.path.lexists(self.path))
        helper.main(self.request("remove-file", resume=True))
        os.unlink(self.f["backup_path"])
        with self.assertRaises(helper.Failure):
            helper.main(self.request("remove-file", resume=True))

    def test_symlink_backup_does_not_read_or_modify_referent(self):
        os.unlink(self.path)
        os.symlink("missing-target", self.path)
        self.f["expected"], _ = helper.snapshot(self.path)
        helper.main(self.request())
        self.assertFalse(os.path.islink(self.path))
        self.assertEqual(os.readlink(self.f["backup_path"]), "missing-target")
        self.assertFalse(os.path.exists(os.path.join(self.root.name, "missing-target")))

    def test_zero_mode_can_sync_without_reopening_installed_file(self):
        self.f["backup_path"] = ""
        self.f["mode"] = 0
        helper.main(self.request())
        self.assertEqual(stat.S_IMODE(os.stat(self.path).st_mode), 0)
        os.chmod(self.path, 0o600)


if __name__ == "__main__":
    unittest.main()
