"""Bounded, transient APT apply helper. Only fixed error categories leave it."""
import contextlib
import fcntl
import hashlib
import json
import os
import re
import shlex
import signal
import stat
import subprocess
import sys
import time
import uuid

STATE_DIRECTORY = "/var/lib/richt/apt"
OUTPUT_LIMIT = 32 * 1024 * 1024
INPUT_LIMIT = 64 * 1024 * 1024
ENTRY_LIMIT = 8192
STAMP_LIMIT = 2 * 1024 * 1024
QUERY_FORMAT = "${Package}\\t${Version}\\t${db:Status-Abbrev}\\t${Architecture}\\n"
ENV = dict(os.environ, LC_ALL="C", LANG="C", DEBIAN_FRONTEND="noninteractive", DEBIAN_PRIORITY="critical", APT_LISTCHANGES_FRONTEND="none", NEEDRESTART_MODE="l")
PATHS = {
    "ROOT": ("Dir/d", "/"),
    "ETC": ("Dir::Etc/d", "/etc/apt/"),
    "MAIN": ("Dir::Etc::main/f", "/etc/apt/apt.conf"),
    "PARTS": ("Dir::Etc::parts/d", "/etc/apt/apt.conf.d/"),
    "SOURCES": ("Dir::Etc::sourcelist/f", "/etc/apt/sources.list"),
    "SOURCEPARTS": ("Dir::Etc::sourceparts/d", "/etc/apt/sources.list.d/"),
    "TRUSTED": ("Dir::Etc::trusted/f", "/etc/apt/trusted.gpg"),
    "TRUSTEDPARTS": ("Dir::Etc::trustedparts/d", "/etc/apt/trusted.gpg.d/"),
    "STATE": ("Dir::State/d", "/var/lib/apt/"),
    "STATUS": ("Dir::State::status/f", "/var/lib/dpkg/status"),
    "LISTS": ("Dir::State::lists/d", "/var/lib/apt/lists/"),
    "CACHE": ("Dir::Cache/d", "/var/cache/apt/"),
    "ARCHIVES": ("Dir::Cache::archives/d", "/var/cache/apt/archives/"),
}


class Failure(Exception):
    pass


def fail(category):
    raise Failure(category)


class Commands:
    def __init__(self, seconds):
        self.deadline = time.monotonic() + seconds

    def run(self, argv, category, capture=True, accepted=(0,)):
        # A regular temporary file bounds captured machine metadata without a
        # potentially unbounded PIPE/communicate allocation. Human output is discarded.
        import tempfile
        if time.monotonic() >= self.deadline:
            fail("timeout")
        with tempfile.TemporaryFile() if capture else open(os.devnull, "wb") as output:
            try:
                proc = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.DEVNULL, env=ENV, start_new_session=True)
            except OSError:
                fail(category)
            try:
                while proc.poll() is None:
                    if time.monotonic() >= self.deadline:
                        fail("timeout")
                    if capture and os.fstat(output.fileno()).st_size > OUTPUT_LIMIT:
                        fail("metadata")
                    time.sleep(0.025)
                if time.monotonic() >= self.deadline:
                    fail("timeout")
                if proc.returncode not in accepted:
                    fail(category)
                if not capture:
                    return proc.returncode, ""
                output.seek(0)
                raw = output.read(OUTPUT_LIMIT + 1)
                if len(raw) > OUTPUT_LIMIT:
                    fail("metadata")
                return proc.returncode, raw.decode("utf-8", errors="strict")
            finally:
                if proc.poll() is None:
                    # Include maintainer-script children in timeout cancellation.
                    with contextlib.suppress(ProcessLookupError):
                        os.killpg(proc.pid, signal.SIGTERM)
                    try:
                        proc.wait(timeout=1)
                    except subprocess.TimeoutExpired:
                        with contextlib.suppress(ProcessLookupError):
                            os.killpg(proc.pid, signal.SIGKILL)
                        proc.wait()


def capability(commands):
    if sys.platform != "linux" or sys.version_info < (3, 8):
        fail("unsupported")
    _, out = commands.run(["apt-get", "--version"], "unsupported")
    match = re.match(r"apt (\d+)\.(\d+)(?:\.|\s)", out)
    if not match or tuple(map(int, match.groups())) < (2, 2):
        fail("unsupported")
    commands.run(["dpkg-query", "--version"], "unsupported")
    # Probe the newest required protection flag without touching package state.
    commands.run(["dpkg", "--no-force-remove-protected", "--version"], "unsupported")


def native_architecture(commands):
    _, out = commands.run(["dpkg", "--print-architecture"], "database")
    native = out.strip()
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]*", native):
        fail("database")
    return native


def installed(commands, package, native):
    _, out = commands.run(["dpkg-query", "--show", "--showformat=" + QUERY_FORMAT], "database")
    result = None
    for line in out.splitlines():
        fields = line.split("\t")
        if len(fields) != 4:
            fail("database")
        name, version, status, architecture = fields
        if name != package or architecture not in (native, "all"):
            continue
        if result is not None or len(status) != 3 or status[0] not in "uihrp" or status[2] != " ":
            fail("database")
        if status[1] == "i" and valid_version(version):
            result = (True, status[0] == "h", version)
        elif status[1] in "nc":
            result = (False, status[0] == "h", "")
        else:
            fail("database")
    return result if result is not None else (False, False, "")


def version_equal(commands, first, second):
    if first == second:
        return True
    code, _ = commands.run(["dpkg", "--compare-versions", first, "eq", second], "database", accepted=(0, 1))
    return code == 0


def needs_change(commands, change, native):
    present, held, version = installed(commands, change["package"], native)
    changed = present if change["state"] == "absent" else not present or bool(change.get("version")) and not version_equal(commands, change["version"], version)
    if changed and held:
        fail("held")
    return changed


def valid_version(version):
    if not isinstance(version, str) or not version:
        return False
    rest = version
    if ":" in rest:
        epoch, rest = rest.split(":", 1)
        if not re.fullmatch(r"[0-9]+", epoch):
            return False
    if "-" in rest:
        rest, revision = rest.rsplit("-", 1)
        if not re.fullmatch(r"[A-Za-z0-9+.~]+", revision):
            return False
    return re.fullmatch(r"[0-9][A-Za-z0-9.+:~\-]*", rest) is not None


def protected(path, private=False):
    info = os.lstat(path)
    if info.st_uid != 0 or (not stat.S_ISLNK(info.st_mode) and info.st_mode & 0o022):
        fail("permissions")
    if private and (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600):
        fail("permissions")
    return info


def protected_parents(path):
    # Config symlinks are supported only through trusted root-controlled paths.
    current = os.path.abspath(path)
    while True:
        info = protected(current)
        if stat.S_ISLNK(info.st_mode):
            resolved = os.path.realpath(current)
            if resolved == current:
                fail("metadata")
            protected_parents(resolved)
        parent = os.path.dirname(current)
        if parent == current:
            break
        current = parent


def layout(commands):
    if os.environ.get("APT_CONFIG") or os.environ.get("DPKG_ROOT") or os.environ.get("DPKG_ADMINDIR"):
        fail("layout")
    argv = ["apt-config", "shell"]
    for key, (option, _) in PATHS.items():
        argv += [key, option]
    _, out = commands.run(argv, "layout")
    values = {}
    for line in out.splitlines():
        parsed = shlex.split(line)
        if len(parsed) != 1 or "=" not in parsed[0]:
            fail("layout")
        key, value = parsed[0].split("=", 1)
        values[key] = value
    for key, (_, expected) in PATHS.items():
        if os.path.normpath(values.get(key, "")) != os.path.normpath(expected):
            fail("layout")
    # Include the complete effective configuration in the fingerprint. Its
    # values are hashed privately and never returned or included in exceptions.
    _, dump = commands.run(["apt-config", "dump"], "metadata")
    return dump


def fingerprint(dump, roots=None):
    digest = hashlib.sha256(dump.encode())
    count = 0
    size = 0
    active = set()
    source_text = []

    def visit(path, depth=0, source_file=False):
        source_file = source_file or path.endswith((".list", ".sources")) or path == "/etc/apt/sources.list"
        nonlocal count, size
        count += 1
        if count > ENTRY_LIMIT or depth > 32:
            fail("metadata")
        digest.update(path.encode() + b"\0")
        try:
            info = protected(path)
        except FileNotFoundError:
            digest.update(b"missing\0")
            return
        protected_parents(path)
        key = (info.st_dev, info.st_ino)
        if key in active:
            fail("metadata")
        digest.update(str((info.st_mode, info.st_uid, info.st_gid)).encode() + b"\0")
        if stat.S_ISLNK(info.st_mode):
            active.add(key)
            digest.update(os.readlink(path).encode() + b"\0")
            visit(os.path.realpath(path), depth + 1, source_file)
            active.remove(key)
        elif stat.S_ISDIR(info.st_mode):
            active.add(key)
            for name in sorted(os.listdir(path)):
                visit(os.path.join(path, name), depth + 1)
            active.remove(key)
        elif stat.S_ISREG(info.st_mode):
            if info.st_size > INPUT_LIMIT - size:
                fail("metadata")
            with open(path, "rb") as source:
                content = source.read(INPUT_LIMIT - size + 1)
            size += len(content)
            if size > INPUT_LIMIT:
                fail("metadata")
            digest.update(content + b"\0")
            if source_file:
                source_text.append(content.decode("utf-8", errors="strict"))
        else:
            fail("metadata")

    for root in roots if roots is not None else ("/etc/apt", "/usr/share/keyrings"):
        visit(root)
    # Signed-By may point outside the normal keyring directories. Follow those
    # exact files as well; embedded Deb822 keys are already in the source hash.
    for text in list(source_text):
        values = re.findall(r"(?im)\bsigned-by\s*=\s*([^\]\s]+)", text)
        values += re.findall(r"(?im)^signed-by\s*:\s*([^\n]*(?:\n[ \t]+[^\n]*)*)", text)
        for value in values:
            for token in re.split(r"[,\s]+", value):
                if token.startswith("/"):
                    visit(token)
    return digest.hexdigest()


def index_state(path="/var/lib/apt/lists"):
    if not os.path.lexists(path):
        protected_parents(os.path.dirname(path))
        return [], False
    protected_parents(path)
    result = []
    usable = False
    for name in sorted(os.listdir(path)):
        if name in ("lock", "partial", "auxfiles"):
            continue
        info = protected(os.path.join(path, name))
        if not stat.S_ISREG(info.st_mode):
            fail("metadata")
        result.append([name, info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns])
        usable |= info.st_size > 0 and re.search(r"_Packages(?:\.(?:lz4|gz|xz|bz2|zst))?$", name) is not None
        if len(result) > ENTRY_LIMIT:
            fail("metadata")
    return result, usable


def open_state_directory():
    if os.geteuid() != 0:
        fail("permissions")
    # Traverse with directory FDs so neither state ancestors nor the final
    # private directory can redirect stamp/lock writes through symlinks.
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    try:
        parts = STATE_DIRECTORY.strip("/").split("/")
        for position, name in enumerate(parts):
            try:
                nextfd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
            except FileNotFoundError:
                if position < len(parts) - 2:
                    fail("permissions")
                with contextlib.suppress(FileExistsError):
                    os.mkdir(name, 0o700, dir_fd=fd)
                nextfd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
            os.close(fd)
            fd = nextfd
            info = os.fstat(fd)
            if info.st_uid != 0 or info.st_mode & 0o022 or (position == len(parts)-1 and stat.S_IMODE(info.st_mode) != 0o700):
                fail("permissions")
        return fd
    except BaseException:
        os.close(fd)
        raise


def private_fd(directory, name, flags):
    fd = os.open(name, flags | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=directory)
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600:
        os.close(fd)
        fail("permissions")
    return fd


def acquire(fd, deadline, native=False):
    while True:
        try:
            if native:
                fcntl.lockf(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            else:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return
        except BlockingIOError:
            if time.monotonic() >= deadline:
                fail("lock")
            time.sleep(min(0.05, max(0, deadline-time.monotonic())))


def wait_native(commands, lock_seconds, removal=False):
    deadline = min(commands.deadline, time.monotonic() + lock_seconds)
    paths = ["/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock", "/var/cache/apt/archives/lock"]
    if not removal:
        paths.append("/var/lib/apt/lists/lock")
    for path in paths:
        if not os.path.lexists(os.path.dirname(path)):
            protected_parents(os.path.dirname(os.path.dirname(path)))
            continue
        protected_parents(os.path.dirname(path))
        fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o640)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or info.st_mode & 0o022:
                fail("permissions")
            acquire(fd, deadline, native=True)
            # APT/dpkg must acquire their own native locks, so release this
            # preflight wait immediately. A racing external writer can still
            # make APT fail safely; no package transaction is retried.
        finally:
            os.close(fd)


def read_stamp(directory):
    try:
        fd = private_fd(directory, "success.json", os.O_RDONLY)
    except FileNotFoundError:
        return None
    with os.fdopen(fd, "rb") as source:
        raw = source.read(STAMP_LIMIT + 1)
    if len(raw) > STAMP_LIMIT:
        fail("metadata")
    try:
        return json.loads(raw)
    except (ValueError, UnicodeDecodeError):
        return None


def fresh(stamp, config, indexes, usable, max_age, now):
    if not usable or not isinstance(stamp, dict):
        return False
    when = stamp.get("time")
    return type(when) in (int, float) and 0 <= now - when < max_age and stamp.get("schema") == 1 and stamp.get("config") == config and stamp.get("indexes") == indexes


def write_stamp(directory, config, indexes):
    value = json.dumps({"schema":1, "time":time.time(), "config":config, "indexes":indexes}, separators=(",", ":")).encode()
    if len(value) > STAMP_LIMIT:
        fail("metadata")
    name = "success-" + uuid.uuid4().hex + ".tmp"
    try:
        fd = private_fd(directory, name, os.O_WRONLY | os.O_CREAT | os.O_EXCL)
        with os.fdopen(fd, "wb") as destination:
            destination.write(value)
            destination.flush()
            os.fsync(destination.fileno())
        os.replace(name, "success.json", src_dir_fd=directory, dst_dir_fd=directory)
        os.fsync(directory)
    finally:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(name, dir_fd=directory)


def apt_options(lock_seconds):
    return ["-y", "-o", "DPkg::Lock::Timeout=" + str(max(1, int(lock_seconds))),
            "-o", "APT::Get::force-yes=false", "-o", "APT::Get::allow-change-held-packages=false",
            "-o", "APT::Get::allow-remove-essential=false", "-o", "APT::Get::allow-unauthenticated=false",
            "-o", "Acquire::AllowInsecureRepositories=false", "-o", "Acquire::AllowDowngradeToInsecureRepositories=false",
            "-o", "Acquire::Retries=0", "-o", "APT::Get::AutomaticRemove=false", "-o", "APT::Get::Purge=false",
            "-o", "APT::Get::Fix-Broken=false", "-o", "APT::Get::Fix-Missing=false",
            # Named disables also override inherited force-all/confnew settings.
            # confold keeps local modifications; missing local conffiles stay missing.
            "-o", "Dpkg::Options::=--no-force-confnew", "-o", "Dpkg::Options::=--no-force-confdef",
            "-o", "Dpkg::Options::=--no-force-confask", "-o", "Dpkg::Options::=--no-force-confmiss",
            "-o", "Dpkg::Options::=--no-force-hold", "-o", "Dpkg::Options::=--no-force-remove-essential",
            "-o", "Dpkg::Options::=--no-force-remove-protected", "-o", "Dpkg::Options::=--force-confold"]


def refresh(commands, directory, lock_seconds, max_age):
    config = fingerprint(layout(commands))
    indexes, usable = index_state()
    if fresh(read_stamp(directory), config, indexes, usable, max_age, time.time()):
        return
    with contextlib.suppress(FileNotFoundError):
        os.unlink("success.json", dir_fd=directory)
    wait_native(commands, lock_seconds)
    commands.run(["apt-get"] + apt_options(lock_seconds) + ["-o", "APT::Update::Error-Mode=any", "update"], "update", capture=False)
    after = fingerprint(layout(commands))
    indexes, usable = index_state()
    if after != config or not usable:
        fail("update")
    try:
        write_stamp(directory, after, indexes)
    except (OSError, Failure):
        fail("stamp")


def available_version(commands, package, native, requested):
    # APT operands otherwise interpret trailing +/- and can fall back to regex
    # matching when a name is unavailable. Resolve an exact native record first
    # and always pass name:architecture=version to install.
    operand = package + ":" + native
    if not requested:
        _, policy = commands.run(["apt-cache", "policy", operand], "unavailable")
        candidates = re.findall(r"^\s+Candidate:\s+(\S+)\s*$", policy, re.M)
        headers = [line[:-1] for line in policy.splitlines() if line and not line[0].isspace() and line.endswith(":")]
        if len(candidates) != 1 or headers not in ([package], [operand]) or not valid_version(candidates[0]):
            fail("unavailable")
        requested = candidates[0]
    _, metadata = commands.run(["apt-cache", "show", operand], "unavailable")
    for record in re.split(r"\n\s*\n", metadata):
        fields = {}
        for line in record.splitlines():
            if line and not line[0].isspace() and ":" in line:
                key, value = line.split(":", 1)
                if key in ("Package", "Architecture", "Version"):
                    if key in fields:
                        fail("metadata")
                    fields[key] = value.strip()
        if fields.get("Package") != package or fields.get("Architecture") not in (native, "all"):
            continue
        version = fields.get("Version", "")
        if valid_version(version) and version_equal(commands, requested, version):
            return version
    fail("unavailable")


def apply(commands, request):
    change = request["change"]
    # Layout is checked for removal too: no APT operation may silently select a
    # different package database or repositories from dpkg observation.
    layout(commands)
    directory = open_state_directory()
    lock = None
    try:
        lock = private_fd(directory, "lock", os.O_RDWR | os.O_CREAT)
        acquire(lock, min(commands.deadline, time.monotonic()+request["lock_seconds"]))
        native = native_architecture(commands)
        if not needs_change(commands, change, native):
            return
        _, audit = commands.run(["dpkg", "--audit"], "database")
        if audit.strip():
            fail("database")
        options = apt_options(request["lock_seconds"])
        if change["state"] == "present":
            refresh(commands, directory, request["lock_seconds"], request["cache_seconds"])
            version = available_version(commands, change["package"], native, change.get("version", ""))
            argv = ["apt-get"] + options + ["--allow-downgrades", "install", "--", change["package"]+":"+native+"="+version]
        else:
            # An architecture suffix disarms APT's trailing +/- action syntax.
            # This package was proved installed by exact database identity above.
            argv = ["apt-get"] + options + ["remove", "--", change["package"]+":"+native]
        wait_native(commands, request["lock_seconds"], removal=change["state"] == "absent")
        commands.run(argv, "transaction", capture=False)
        if needs_change(commands, change, native):
            fail("verify")
    finally:
        if lock is not None:
            os.close(lock)
        os.close(directory)


def validate_request(request):
    if not isinstance(request, dict) or request.get("op") not in ("check", "apply"):
        fail("request")
    if request["op"] == "check":
        return
    change = request.get("change")
    if not isinstance(change, dict) or change.get("manager") != "apt" or change.get("state") not in ("present", "absent") or change.get("cache_max_age") != "48h" or change.get("conffile_policy") != "keep":
        fail("request")
    if not isinstance(change.get("package"), str) or not re.fullmatch(r"[a-z0-9][a-z0-9+.-]+", change["package"]):
        fail("request")
    if change.get("version", "") and not valid_version(change["version"]):
        fail("request")
    for key in ("seconds", "lock_seconds", "cache_seconds"):
        value = request.get(key)
        if type(value) not in (int, float) or not 0 < value <= 172800:
            fail("request")
    if request["cache_seconds"] != 172800 or request["seconds"] > 86400 or request["lock_seconds"] > request["seconds"]:
        fail("request")


def main():
    try:
        if len(sys.argv) != 2 or len(sys.argv[1]) > 8192:
            fail("request")
        request = json.loads(sys.argv[1])
        validate_request(request)
        commands = Commands(request.get("seconds", 30))
        capability(commands)
        if request["op"] == "apply":
            apply(commands, request)
        result = {}
    except Failure as error:
        result = {"error":str(error)}
    except Exception:
        result = {"error":"metadata"}
    print(json.dumps(result, separators=(",", ":")))


if __name__ == "__main__":
    main()
