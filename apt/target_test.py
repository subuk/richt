"""Portable APT helper tests; package transactions are fakes and never run."""
import contextlib
import importlib.util
import json
import os
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("apt_target", os.path.join(os.path.dirname(__file__), "target.py"))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)


class FakeCommands:
    def __init__(self, responder=None):
        self.calls = []
        self.deadline = time.monotonic() + 60
        self.responder = responder

    def run(self, argv, category, capture=True, accepted=(0,)):
        self.calls.append((argv, category, capture, accepted))
        if self.responder:
            return self.responder(argv, category)
        return 0, ""


class HelperTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = self.temp.name
        self.directory = os.open(self.root, os.O_RDONLY | os.O_DIRECTORY)
        self.addCleanup(os.close, self.directory)
        # File-owner checks are exercised separately. Portable fixture files
        # belong to the developer; production always requires UID 0.
        actual_private_fd = h.private_fd
        def local_private_fd(directory, name, flags):
            fd = os.open(name, flags | os.O_NOFOLLOW, 0o600, dir_fd=directory)
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600:
                os.close(fd)
                h.fail("permissions")
            return fd
        self.private_patch = mock.patch.object(h, "private_fd", side_effect=local_private_fd)
        self.private_patch.start()
        self.addCleanup(self.private_patch.stop)
        self.production_private_fd = actual_private_fd

    def change(self, state="present", version=""):
        return {"manager":"apt", "package":"example+", "state":state, "version":version, "cache_max_age":"48h", "lock_timeout":"60s", "timeout":"15m", "conffile_policy":"keep"}

    def request(self, state="present", version=""):
        return {"op":"apply", "change":self.change(state, version), "seconds":900, "lock_seconds":60, "cache_seconds":172800}

    @contextlib.contextmanager
    def cache(self, config="fingerprint", indexes=None, usable=True):
        if indexes is None:
            indexes = [["fixture_Packages", 1, 2, 3, 4, 5]]
        with mock.patch.object(h, "layout", return_value="dump"), mock.patch.object(h, "fingerprint", return_value=config), mock.patch.object(h, "index_state", return_value=(indexes, usable)), mock.patch.object(h, "wait_native"):
            yield indexes

    def test_stamp_roundtrip_atomic_private_and_future_or_expired_rejected(self):
        with self.cache() as indexes:
            h.write_stamp(self.directory, "fingerprint", indexes)
            stamp = h.read_stamp(self.directory)
            self.assertTrue(h.fresh(stamp, "fingerprint", indexes, True, 172800, time.time()))
            self.assertFalse(h.fresh(stamp, "fingerprint", indexes, True, 172800, stamp["time"]-1))
            self.assertFalse(h.fresh(stamp, "fingerprint", indexes, True, 172800, stamp["time"]+172800))
            self.assertFalse(h.fresh(stamp, "changed", indexes, True, 172800, time.time()))
            self.assertFalse(h.fresh(stamp, "fingerprint", [], False, 172800, time.time()))
            self.assertFalse(h.fresh({"time": "SECRET"}, "fingerprint", indexes, True, 172800, time.time()))
        info = os.stat(os.path.join(self.root, "success.json"))
        self.assertEqual(stat.S_IMODE(info.st_mode), 0o600)
        self.assertEqual(os.listdir(self.root), ["success.json"])

    def test_cache_reuses_only_own_successful_refresh(self):
        commands = FakeCommands()
        with self.cache():
            h.refresh(commands, self.directory, 60, 172800)
            h.refresh(commands, self.directory, 60, 172800)
        self.assertEqual(len(commands.calls), 1)
        argv, category, capture, _ = commands.calls[0]
        self.assertEqual(category, "update")
        self.assertIn("APT::Update::Error-Mode=any", argv)
        self.assertIn("Acquire::Retries=0", argv)
        self.assertFalse(capture)
        self.assertEqual(argv[-1], "update")

    def test_cache_invalidated_by_expiry_config_keys_and_indexes(self):
        for invalidation in ("expiry", "config", "index changed", "index removed", "stamp missing", "invalid stamp"):
            with self.subTest(invalidation=invalidation):
                with self.cache() as indexes:
                    h.write_stamp(self.directory, "fingerprint", indexes)
                if invalidation == "expiry":
                    stamp = h.read_stamp(self.directory)
                    stamp["time"] = 0
                    with open(os.path.join(self.root, "success.json"), "w") as file:
                        json.dump(stamp, file)
                elif invalidation == "stamp missing":
                    os.unlink("success.json", dir_fd=self.directory)
                elif invalidation == "invalid stamp":
                    with open(os.path.join(self.root, "success.json"), "w") as file:
                        file.write("SECRET")
                commands = FakeCommands()
                config = "new keys/config" if invalidation == "config" else "fingerprint"
                observed = [["fixture_Packages", 1, 2, 3, 40, 5]] if invalidation == "index changed" else indexes
                with self.cache(config, observed), mock.patch.object(h, "index_state", side_effect=[([], False), (indexes, True)] if invalidation == "index removed" else [(observed, True), (observed, True)]):
                    h.refresh(commands, self.directory, 60, 172800)
                self.assertEqual(len(commands.calls), 1)

    def test_failed_update_never_advances_stamp_or_attempts_transaction(self):
        def reject(argv, category):
            self.assertEqual(argv[-1], "update")
            h.fail(category)
        commands = FakeCommands(reject)
        with self.cache():
            h.write_stamp(self.directory, "old config", [])
            with self.assertRaisesRegex(h.Failure, "^update$"):
                h.refresh(commands, self.directory, 60, 172800)
        self.assertFalse(os.path.exists(os.path.join(self.root, "success.json")))
        self.assertEqual(len(commands.calls), 1)

    def test_refresh_configuration_race_and_empty_indexes_do_not_stamp(self):
        for changed, usable in ((True, True), (False, False)):
            with self.cache(usable=usable), mock.patch.object(h, "fingerprint", side_effect=["before", "after" if changed else "before"]):
                with self.assertRaisesRegex(h.Failure, "^update$"):
                    h.refresh(FakeCommands(), self.directory, 60, 172800)
                self.assertIsNone(h.read_stamp(self.directory))

    def test_stamp_symlinks_hardlinks_and_unsafe_modes_are_rejected(self):
        target = os.path.join(self.root, "target")
        with open(target, "wb") as file:
            file.write(b"SECRET")
        os.chmod(target, 0o600)
        link = os.path.join(self.root, "success.json")
        os.symlink(target, link)
        with self.assertRaises(OSError):
            h.read_stamp(self.directory)
        os.unlink(link)
        os.link(target, link)
        with self.assertRaisesRegex(h.Failure, "permissions"):
            h.read_stamp(self.directory)
        os.unlink(link)
        os.chmod(target, 0o644)
        os.rename(target, link)
        with self.assertRaisesRegex(h.Failure, "permissions"):
            h.read_stamp(self.directory)
        with open(link) as source:
            self.assertEqual(source.read(), "SECRET")

    def test_private_file_owner_is_required_in_production(self):
        path = os.path.join(self.root, "stamp")
        with open(path, "wb") as file:
            file.write(b"{}")
        os.chmod(path, 0o600)
        if os.getuid() != 0:
            with self.assertRaisesRegex(h.Failure, "permissions"):
                self.production_private_fd(self.directory, "stamp", os.O_RDONLY)
        else:
            fd = self.production_private_fd(self.directory, "stamp", os.O_RDONLY)
            os.close(fd)

    def test_installed_database_states_and_native_filter(self):
        cases = [
            ("", (False, False, "")),
            ("example+\t1.0-1\tii \tamd64\n", (True, False, "1.0-1")),
            ("example+\t1.0-1\thi \tall\n", (True, True, "1.0-1")),
            ("example+\t1.0-1\trc \tamd64\n", (False, False, "")),
            ("example+\t1.0-1\tii \tarm64\n", (False, False, "")),
        ]
        for output, expected in cases:
            commands = FakeCommands(lambda *_:(0, output))
            self.assertEqual(h.installed(commands, "example+", "amd64"), expected)
            self.assertEqual(commands.calls[0][0][0], "dpkg-query")
        for status in ("iF ", "iU ", "iiR", "bad", "iW "):
            with self.assertRaisesRegex(h.Failure, "database"):
                h.installed(FakeCommands(lambda *_:(0, "example+\t1.0\t"+status+"\tamd64\n")), "example+", "amd64")

    def test_installed_omitted_version_is_preserved_and_held_changes_fail(self):
        commands = FakeCommands()
        with mock.patch.object(h, "installed", return_value=(True, True, "1.0")):
            self.assertFalse(h.needs_change(commands, self.change(), "amd64"))
            with self.assertRaisesRegex(h.Failure, "held"):
                h.needs_change(commands, self.change("absent"), "amd64")
        self.assertEqual(commands.calls, [])

    def test_debian_equivalent_versions_use_dpkg_comparison(self):
        commands = FakeCommands()
        self.assertTrue(h.version_equal(commands, "0:1.0", "1.0-0"))
        self.assertEqual(commands.calls[0][0], ["dpkg", "--compare-versions", "0:1.0", "eq", "1.0-0"])
        self.assertEqual(commands.calls[0][3], (0, 1))

    def test_candidate_resolution_requires_exact_native_record(self):
        def respond(argv, category):
            if argv[1] == "policy":
                return 0, "example+:amd64:\n  Installed: (none)\n  Candidate: 2.0-1\n  Version table:\n"
            return 0, "Package: unrelated\nVersion: 2.0-1\nArchitecture: amd64\n\nPackage: example+\nVersion: 2.0-1\nArchitecture: all\n"
        commands = FakeCommands(respond)
        self.assertEqual(h.available_version(commands, "example+", "amd64", ""), "2.0-1")
        self.assertEqual(commands.calls[0][0], ["apt-cache", "policy", "example+:amd64"])
        for output in ("Package: unrelated\nVersion: 2.0-1\nArchitecture: amd64\n", "Package: example+\nVersion: 2.0-1\nArchitecture: arm64\n"):
            with self.assertRaisesRegex(h.Failure, "unavailable"):
                h.available_version(FakeCommands(lambda *_:(0, output)), "example+", "amd64", "2.0-1")
        with self.assertRaisesRegex(h.Failure, "unavailable"):
            h.available_version(FakeCommands(lambda *_:(0, "unexpected:\n  Candidate: 2.0\n")), "example+", "amd64", "")

    def apply_fakes(self, state="present", version="", changes=None):
        stack = contextlib.ExitStack()
        self.addCleanup(stack.close)
        stack.enter_context(mock.patch.object(h, "layout", return_value="dump"))
        stack.enter_context(mock.patch.object(h, "open_state_directory", side_effect=lambda:os.dup(self.directory)))
        stack.enter_context(mock.patch.object(h, "native_architecture", return_value="amd64"))
        needed = stack.enter_context(mock.patch.object(h, "needs_change", side_effect=changes if changes is not None else [True, False]))
        refresh = stack.enter_context(mock.patch.object(h, "refresh"))
        stack.enter_context(mock.patch.object(h, "available_version", return_value="2.0-1"))
        stack.enter_context(mock.patch.object(h, "wait_native"))
        return self.request(state, version), refresh, needed

    def test_apply_install_pins_exact_name_arch_and_version_and_protects_policies(self):
        request, refresh, needed = self.apply_fakes(version="2.0-1")
        commands = FakeCommands()
        h.apply(commands, request)
        refresh.assert_called_once()
        self.assertEqual(needed.call_count, 2)
        argv = commands.calls[-1][0]
        self.assertEqual(argv[-3:], ["install", "--", "example+:amd64=2.0-1"])
        for policy in ("--allow-downgrades", "Dpkg::Options::=--force-confold", "APT::Get::Purge=false", "APT::Get::AutomaticRemove=false", "APT::Get::allow-change-held-packages=false", "APT::Get::allow-remove-essential=false", "APT::Get::allow-unauthenticated=false"):
            self.assertIn(policy, argv)
        self.assertFalse(commands.calls[-1][2])

    def test_frozen_policies_override_inherited_repair_and_conffile_force_flags(self):
        # dpkg's force settings are independent flags, so confold alone does not
        # reliably cancel inherited confnew/confdef/force-all options.
        options = h.apt_options(60)
        self.assertIn("APT::Get::Fix-Broken=false", options)
        self.assertIn("APT::Get::Fix-Missing=false", options)
        self.assertNotIn("Dpkg::Options::=--force-confdef", options)
        for thing in ("confnew", "confdef", "confask", "confmiss", "hold", "remove-essential", "remove-protected"):
            self.assertIn("Dpkg::Options::=--no-force-"+thing, options)
        self.assertEqual(options[-1], "Dpkg::Options::=--force-confold")

    def test_removal_never_refreshes_or_purges(self):
        request, refresh, _ = self.apply_fakes("absent", "1.0")
        commands = FakeCommands()
        h.apply(commands, request)
        refresh.assert_not_called()
        self.assertEqual(commands.calls[-1][0][-3:], ["remove", "--", "example+:amd64"])
        self.assertNotIn("--allow-downgrades", commands.calls[-1][0])

    def test_apply_already_satisfied_never_updates_or_transacts(self):
        request, refresh, _ = self.apply_fakes(changes=[False])
        commands = FakeCommands()
        h.apply(commands, request)
        refresh.assert_not_called()
        self.assertEqual(commands.calls, [])

    def test_failed_transaction_has_no_retry_and_postcondition_is_verified(self):
        request, _, needed = self.apply_fakes(changes=[True, True])
        commands = FakeCommands()
        with self.assertRaisesRegex(h.Failure, "verify"):
            h.apply(commands, request)
        self.assertEqual(len(commands.calls), 2)
        needed.side_effect = [True]
        def reject(argv, category):
            if argv == ["dpkg", "--audit"]:
                return 0, ""
            h.fail("transaction")
        with self.assertRaisesRegex(h.Failure, "transaction"):
            h.apply(FakeCommands(reject), request)

    def test_lock_wait_is_bounded_and_shared_by_helpers(self):
        path = os.path.join(self.root, "lock")
        first = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
        second = os.open(path, os.O_RDWR)
        self.addCleanup(os.close, first)
        self.addCleanup(os.close, second)
        h.acquire(first, time.monotonic()+1)
        start = time.monotonic()
        with self.assertRaisesRegex(h.Failure, "lock"):
            h.acquire(second, time.monotonic()+0.06)
        self.assertLess(time.monotonic()-start, 0.3)
        h.fcntl.flock(first, h.fcntl.LOCK_UN)
        h.acquire(second, time.monotonic()+1)

    def test_effective_layout_rejects_overrides_and_environment(self):
        def outputs(argv, category):
            if argv[1] == "shell":
                return 0, "\n".join(name+"="+repr(value) for name, (_, value) in h.PATHS.items())
            return 0, 'APT::Architecture "amd64";\n'
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertIn("Architecture", h.layout(FakeCommands(outputs)))
        with mock.patch.dict(os.environ, {"APT_CONFIG":"SECRET"}):
            with self.assertRaisesRegex(h.Failure, "layout"):
                h.layout(FakeCommands(outputs))
        with mock.patch.dict(os.environ, {}, clear=True):
            for key in ("LISTS", "STATUS", "SOURCES", "ROOT"):
                def changed(argv, category):
                    _, out = outputs(argv, category)
                    return 0, out.replace(key+"="+repr(h.PATHS[key][1]), key+"='/unexpected'")
                with self.assertRaisesRegex(h.Failure, "layout"):
                    h.layout(FakeCommands(changed))

    def test_request_validation_rejects_unbounded_or_expression_inputs(self):
        h.validate_request(self.request())
        for key, value in (("seconds", 0), ("seconds", 86401), ("lock_seconds", 1000), ("cache_seconds", 1)):
            request = self.request()
            request[key] = value
            with self.assertRaisesRegex(h.Failure, "request"):
                h.validate_request(request)

    def test_capability_checks_required_dpkg_protection_without_mutation(self):
        def output(argv, category):
            if argv == ["apt-get", "--version"]:
                return 0, "apt 2.8.4 (amd64)\n"
            return 0, "version\n"
        commands = FakeCommands(output)
        with mock.patch.object(sys, "platform", "linux"):
            h.capability(commands)
        self.assertEqual([call[0] for call in commands.calls], [
            ["apt-get", "--version"], ["dpkg-query", "--version"],
            ["dpkg", "--no-force-remove-protected", "--version"]])
        for value in ("name*", "name:arm64", "name/latest", "-option", "x"):
            request = self.request()
            request["change"]["package"] = value
            with self.assertRaisesRegex(h.Failure, "request"):
                h.validate_request(request)

    def test_machine_output_and_process_timeout_are_bounded(self):
        commands = h.Commands(0.08)
        started = time.monotonic()
        with self.assertRaisesRegex(h.Failure, "timeout"):
            commands.run([sys.executable, "-c", "import time; time.sleep(10)"], "transaction")
        self.assertLess(time.monotonic()-started, 2)
        with mock.patch.object(h, "OUTPUT_LIMIT", 8):
            with self.assertRaisesRegex(h.Failure, "metadata"):
                h.Commands(2).run([sys.executable, "-c", "print('SECRET'*10)"], "database")


    def test_pending_unrelated_dpkg_work_blocks_automatic_repair(self):
        request, refresh, _ = self.apply_fakes(changes=[True])
        commands = FakeCommands(lambda *_:(0, "SECRET unrelated unpacked package"))
        with self.assertRaisesRegex(h.Failure, "^database$"):
            h.apply(commands, request)
        refresh.assert_not_called()
        self.assertEqual([call[0] for call in commands.calls], [["dpkg", "--audit"]])

    def test_fingerprint_tracks_source_symlinks_external_signed_by_and_keys(self):
        config = os.path.join(self.root, "config")
        os.mkdir(config)
        source = os.path.join(self.root, "actual.conf")
        key = os.path.join(self.root, "signing.key")
        with open(key, "w") as file:
            file.write("first key")
        with open(source, "w") as file:
            file.write("deb [signed-by="+key+"] https://example.invalid/repo stable main\n")
        os.symlink(source, os.path.join(config, "repository.list"))
        def local_protected(path, private=False):
            info = os.lstat(path)
            if not stat.S_ISLNK(info.st_mode) and info.st_mode & 0o022:
                h.fail("permissions")
            return info
        with mock.patch.object(h, "protected", side_effect=local_protected), mock.patch.object(h, "protected_parents"):
            first = h.fingerprint("dump", [config])
            self.assertEqual(first, h.fingerprint("dump", [config]))
            with open(key, "w") as file:
                file.write("second key")
            second = h.fingerprint("dump", [config])
            self.assertNotEqual(first, second)
            self.assertNotEqual(second, h.fingerprint("different effective config", [config]))
            with open(source, "w") as file:
                file.write("Types: deb\nURIs: https://example.invalid/repo\nSigned-By: "+key+"\n")
            os.unlink(os.path.join(config, "repository.list"))
            os.symlink(source, os.path.join(config, "repository.sources"))
            third = h.fingerprint("dump", [config])
            with open(key, "w") as file:
                file.write("third key")
            self.assertNotEqual(third, h.fingerprint("dump", [config]))
            os.chmod(key, 0o666)
            with self.assertRaisesRegex(h.Failure, "permissions"):
                h.fingerprint("dump", [config])

    def test_fingerprint_limits_and_unsafe_entries_are_rejected(self):
        path = os.path.join(self.root, "apt.conf")
        with open(path, "w") as file:
            file.write("configuration")
        with mock.patch.object(h, "protected", side_effect=os.lstat), mock.patch.object(h, "protected_parents"):
            with mock.patch.object(h, "INPUT_LIMIT", 1):
                with self.assertRaisesRegex(h.Failure, "metadata"):
                    h.fingerprint("dump", [path])
            with mock.patch.object(h, "ENTRY_LIMIT", 0):
                with self.assertRaisesRegex(h.Failure, "metadata"):
                    h.fingerprint("dump", [path])
            os.unlink(path)
            os.mkfifo(path)
            with self.assertRaisesRegex(h.Failure, "metadata"):
                h.fingerprint("dump", [path])

    def test_index_state_tracks_mutation_and_whole_directory_deletion(self):
        lists = os.path.join(self.root, "lists")
        os.mkdir(lists)
        os.mkdir(os.path.join(lists, "partial"))
        with open(os.path.join(lists, "lock"), "w"):
            pass
        index = os.path.join(lists, "fixture_Packages.lz4")
        with open(index, "w") as file:
            file.write("first")
        with mock.patch.object(h, "protected", side_effect=os.lstat), mock.patch.object(h, "protected_parents"):
            first, usable = h.index_state(lists)
            self.assertTrue(usable)
            self.assertEqual(len(first), 1)
            with open(index, "w") as file:
                file.write("modified index")
            second, usable = h.index_state(lists)
            self.assertTrue(usable)
            self.assertNotEqual(first, second)
            os.unlink(index)
            self.assertEqual(h.index_state(lists), ([], False))
            os.unlink(os.path.join(lists, "lock"))
            os.rmdir(os.path.join(lists, "partial"))
            os.rmdir(lists)
            self.assertEqual(h.index_state(lists), ([], False))

    def test_native_lock_preflight_waits_and_skips_missing_parent_safely(self):
        fd = os.open(os.path.join(self.root, "native-lock"), os.O_CREAT | os.O_RDWR, 0o600)
        self.addCleanup(os.close, fd)
        fake_stat = mock.Mock(st_mode=stat.S_IFREG | 0o600, st_uid=0, st_nlink=1)
        with mock.patch.object(h, "protected_parents"), mock.patch.object(h.os, "open", side_effect=lambda *_args, **_kwargs:os.dup(fd)), mock.patch.object(h.os, "fstat", return_value=fake_stat), mock.patch.object(h.os.path, "lexists", return_value=True), mock.patch.object(h, "acquire") as acquired:
            h.wait_native(FakeCommands(), 0.5)
            self.assertEqual(acquired.call_count, 4)
            self.assertTrue(all(call.kwargs == {"native":True} for call in acquired.call_args_list))
        with mock.patch.object(h, "protected_parents"), mock.patch.object(h.os.path, "lexists", return_value=False), mock.patch.object(h, "acquire") as acquired:
            h.wait_native(FakeCommands(), 0.5)
            acquired.assert_not_called()


if __name__ == "__main__":
    unittest.main()
