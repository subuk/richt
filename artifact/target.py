"""Private target-side artifact implementation; request/response are JSON on stdin/stdout."""
import ctypes
import errno
import fcntl
import hashlib
import gzip
import json
import math
import os
import posixpath
import pwd
import grp
import signal
import stat
import sys
import tarfile
import tempfile
import urllib.request
import urllib.parse
import unicodedata
from collections import deque

DOWNLOAD_LIMIT = 1024 ** 3
EXPANDED_LIMIT = 4 * 1024 ** 3
MEMBER_LIMIT = 100000
PATH_LIMIT = 4096
DEPTH_LIMIT = 128
MANIFEST_LIMIT = 32 * 1024 * 1024
METADATA_LIMIT = 64 * 1024 * 1024


class Failure(Exception):
    pass


def require(test, message):
    if not test:
        raise Failure(message)


def identity(st):
    return {"device": st.st_dev, "inode": st.st_ino}


def same(st, expected):
    return st is not None and identity(st) == expected


def lstat(path):
    try:
        return os.lstat(path)
    except FileNotFoundError:
        return None


def digest_fd(fd, budget):
    h = hashlib.sha256()
    total = 0
    require(stat.S_ISREG(os.fstat(fd).st_mode), "tracked entry has incompatible type")
    while True:
        data = os.read(fd, 1024 * 1024)
        if not data:
            break
        total += len(data)
        require(total <= EXPANDED_LIMIT, "artifact exceeds data limit")
        budget[0] += len(data)
        require(budget[0] <= EXPANDED_LIMIT, "artifact exceeds total data limit")
        h.update(data)
    return h.hexdigest()


def mounts(path):
    # mountinfo records bind mounts too, unlike st_dev alone.
    with open("/proc/self/mountinfo", encoding="utf-8") as f:
        for line in f:
            fields = line.split()
            if len(fields) < 5:
                continue
            target = fields[4]
            for old, new in (("\\040", " "), ("\\011", "\t"), ("\\012", "\n"), ("\\134", "\\")):
                target = target.replace(old, new)
            require(target != path and not target.startswith(path + "/"), "artifact crosses an active mount")


def safe_parent(path, create):
    """Open each ancestor without following symlinks, and anchor all later operations."""
    parts = path.strip("/").split("/")[:-1]
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in parts:
            try:
                nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            except FileNotFoundError:
                if not create:
                    return None
                os.mkdir(part, 0o755, dir_fd=fd)
                nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            st = os.fstat(nxt)
            trusted = st.st_uid in (0, os.geteuid())
            sticky = st.st_uid == 0 and bool(st.st_mode & stat.S_ISVTX)
            require(trusted and (not st.st_mode & 0o022 or sticky), "artifact ancestor is writable by an untrusted identity")
            os.close(fd)
            fd = nxt
        st = os.fstat(fd)
        require(not st.st_mode & 0o022, "artifact parent must not be group or world writable")
        result = fd
        fd = None
        return result
    finally:
        if fd is not None:
            os.close(fd)


def accounts(p):
    try:
        owner, group = p.get("owner", ""), p.get("group", "")
        uid = (pwd.getpwuid(int(owner)) if owner.isdigit() else pwd.getpwnam(owner)).pw_uid if owner else os.geteuid()
        gid = (grp.getgrgid(int(group)) if group.isdigit() else grp.getgrnam(group)).gr_gid if group else os.getegid()
        return uid, gid
    except (KeyError, ValueError, OverflowError):
        raise Failure("requested artifact owner or group does not exist")


def metadata_name(path):
    return ".richt-artifact-" + hashlib.sha256(path.encode()).hexdigest()[:24]


def private_state(path):
    st = lstat(path)
    require(st is not None and stat.S_ISDIR(st.st_mode) and st.st_uid == os.geteuid() and stat.S_IMODE(st.st_mode) == 0o700, "artifact metadata directory is not protected")
    require(set(os.listdir(path)) <= {"lock", "manifest.json", "transaction.json", "payload", "archive", "manifest.new", "transaction.new"}, "artifact metadata contains unknown entries")


def read_json(path):
    st = lstat(path)
    if st is None:
        return None
    limit = MANIFEST_LIMIT if path.endswith("manifest.json") else METADATA_LIMIT
    require(stat.S_ISREG(st.st_mode) and st.st_nlink == 1 and st.st_uid == os.geteuid() and stat.S_IMODE(st.st_mode) == 0o600 and st.st_size <= limit, "artifact metadata file is not protected or exceeds size limit")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "r", encoding="utf-8") as f:
        try:
            return json.load(f)
        except (ValueError, TypeError):
            raise Failure("artifact metadata is invalid")


def write_json(state, name, value):
    temp = os.path.join(state, name.split(".")[0] + ".new")
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        total = 0
        limit = MANIFEST_LIMIT if name == "manifest.json" else METADATA_LIMIT
        for chunk in json.JSONEncoder(separators=(",", ":"), sort_keys=True).iterencode(value):
            total += len(chunk.encode())
            require(total <= limit, "artifact metadata exceeds size limit")
            f.write(chunk)
        f.flush()
        os.fsync(f.fileno())
    os.replace(temp, os.path.join(state, name))
    syncdir(state)


def syncdir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def origin(p):
    return {key: p.get(key) for key in ("type", "path", "url", "sha256", "format", "strip_components")}


def scan(path, expected=None):
    """Descriptor-relative traversal never follows a swapped child directory."""
    root = os.lstat(path)
    budget = [0]
    result = {}

    def visit(parent, name, rel):
        st = os.stat(name, dir_fd=parent, follow_symlinks=False)
        require(st.st_dev == root.st_dev, "artifact crosses a device boundary")
        require(len(result) < MEMBER_LIMIT + 1 and len(rel.encode()) <= PATH_LIMIT, "artifact exceeds entry or path limit")
        if expected is not None:
            require(rel in expected, "artifact contains unexpected extra entries")
        e = {"mode": stat.S_IMODE(st.st_mode), "uid": st.st_uid, "gid": st.st_gid}
        kind = "directory" if stat.S_ISDIR(st.st_mode) else "file" if stat.S_ISREG(st.st_mode) else "symlink" if stat.S_ISLNK(st.st_mode) else "special"
        e["kind"] = kind
        require(kind != "special", "artifact contains an unsupported entry")
        if expected is not None:
            require(kind == expected[rel]["kind"], "tracked entry has incompatible type")
        result[rel] = e
        if kind == "symlink":
            e["link"] = os.readlink(name, dir_fd=parent)
        else:
            flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
            if kind == "directory":
                flags |= os.O_DIRECTORY
            fd = os.open(name, flags, dir_fd=parent)
            try:
                require(same(os.fstat(fd), identity(st)), "artifact changed during observation")
                if kind == "file":
                    require(st.st_nlink == 1, "artifact contains hardlinked files")
                    require(st.st_size <= EXPANDED_LIMIT - budget[0], "artifact exceeds total data limit")
                    e["sha256"] = digest_fd(fd, budget)
                else:
                    for child in os.listdir(fd):
                        visit(fd, child, child if not rel else rel + "/" + child)
            finally:
                os.close(fd)
        require(same(os.stat(name, dir_fd=parent, follow_symlinks=False), identity(st)), "artifact changed during observation")

    # The path's parent may be an intentional /proc/self/fd anchor. Only its
    # final basename is opened without following links by visit().
    parent = os.open(os.path.dirname(path), os.O_RDONLY | os.O_DIRECTORY)
    try:
        visit(parent, os.path.basename(path), "")
    finally:
        os.close(parent)
    require(same(os.lstat(path), identity(root)), "artifact root changed during observation")
    return result


def manifest_for(p, path, enforce_budget=True):
    m = {"version": 1, "token": p["token"], "origin": origin(p), "identity": identity(os.lstat(path)), "entries": scan(path)}
    total = 0
    for chunk in json.JSONEncoder(separators=(",", ":"), sort_keys=True).iterencode(m):
        total += len(chunk.encode())
        require(not enforce_budget or total <= MANIFEST_LIMIT, "artifact manifest exceeds size limit")
    return m


def owned(p, dest, m):
    st = lstat(dest)
    if st is None:
        return None
    require(m is not None and m.get("version") == 1 and m.get("origin", {}).get("path") == p["path"] and m.get("origin", {}).get("type") == p["type"] and same(st, m.get("identity")), "target is unowned or has been replaced")
    require(stat.S_ISREG(st.st_mode) if p["type"] == "download" else stat.S_ISDIR(st.st_mode), "artifact target has incompatible type")
    mounts(p["path"])
    return scan(dest, m["entries"])


def desired(p, m, current, uid, gid):
    installed = lambda value: {k: v for k, v in value.items() if k != "url"}
    if m is None or current is None or installed(m["origin"]) != installed(origin(p)) or current != m["entries"]:
        return False
    for name, e in current.items():
        if e["uid"] != uid or e["gid"] != gid:
            return False
        if p["type"] == "download" and e["mode"] != p.get("mode", 0o755):
            return False
    return True


class Redirects(urllib.request.HTTPRedirectHandler):
    def __init__(self):
        self.count = 0

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        self.count += 1
        require(self.count <= 5, "artifact redirect limit exceeded")
        u = urllib.parse.urlsplit(newurl)
        # A public release URL may supply an ephemeral signed HTTPS redirect.
        # Redirect URLs stay in memory and never enter metadata or diagnostics.
        require(u.scheme == "https" and u.hostname and u.username is None and u.password is None and "#" not in newurl and not any(c.isspace() or unicodedata.category(c).startswith("C") for c in newurl), "artifact redirect must use HTTPS without userinfo or fragment")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def download(p, target, seconds):
    h = hashlib.sha256()
    total = 0
    opener = urllib.request.build_opener(Redirects())
    try:
        # Normal target CA/proxy policy applies. No TLS-verification bypass exists.
        with opener.open(p["url"], timeout=min(seconds, 30)) as response, open(target, "xb") as out:
            os.chmod(target, 0o600)
            length = response.headers.get("Content-Length")
            if length:
                require(length.isdigit() and int(length) <= DOWNLOAD_LIMIT, "artifact exceeds download limit")
            while True:
                data = response.read(1024 * 1024)
                if not data:
                    break
                total += len(data)
                require(total <= DOWNLOAD_LIMIT, "artifact exceeds download limit")
                h.update(data)
                out.write(data)
            out.flush()
            os.fsync(out.fileno())
            require(not length or total == int(length), "HTTPS artifact response was truncated")
    except Failure:
        raise
    except Exception:
        raise Failure("HTTPS artifact download failed")
    require(h.hexdigest() == p["sha256"], "artifact SHA256 checksum mismatch")


def member_name(name, strip):
    require(len(name.encode()) <= PATH_LIMIT and "\x00" not in name and "\\" not in name and not name.startswith("/"), "archive member path is unsafe")
    parts = name.split("/")
    require(".." not in parts, "archive member path is unsafe")
    parts = [x for x in parts if x not in ("", ".")]
    require(len(parts[strip:]) <= DEPTH_LIMIT, "archive output path exceeds depth limit")
    return "/".join(parts[strip:])


def extract(archive, target, strip, uid, gid):
    os.mkdir(target, 0o700)
    seen = set()
    produced = {""}
    path_bytes = 0
    links = []
    total = 0
    count = 0
    class ExpandedReader:
        def __init__(self, source):
            self.source = source
            self.total = 0

        def read(self, size):
            # Bound the complete expanded tar stream, including ignored padding,
            # directory payloads and extension headers, before tarfile allocates it.
            require(size >= 0, "archive requested an unbounded read")
            data = self.source.read(min(size, EXPANDED_LIMIT - self.total + 1))
            self.total += len(data)
            require(self.total <= EXPANDED_LIMIT, "archive exceeds expanded data limit")
            return data

    class BoundedInfo(tarfile.TarInfo):
        def _proc_sparse(self, *args):
            raise Failure("archive sparse files are unsupported")

        def _proc_gnusparse_00(self, *args):
            raise Failure("archive sparse files are unsupported")

        def _proc_gnusparse_01(self, *args):
            raise Failure("archive sparse files are unsupported")

        def _proc_gnusparse_10(self, *args):
            raise Failure("archive sparse files are unsupported")

        def extended(self, tar, handler):
            depth = getattr(tar, "_artifact_depth", 0) + 1
            total = getattr(tar, "_artifact_headers", 0) + self.size
            require(depth <= 32 and total <= 16 * 1024 * 1024, "archive extended headers exceed aggregate limit")
            tar._artifact_depth, tar._artifact_headers = depth, total
            try:
                return handler(tar)
            finally:
                tar._artifact_depth -= 1

        def _proc_pax(self, tar):
            require(self.size <= 1024 * 1024, "archive extended header exceeds limit")
            return self.extended(tar, super()._proc_pax)

        def _proc_gnulong(self, tar):
            require(self.size <= PATH_LIMIT, "archive extended name exceeds limit")
            return self.extended(tar, super()._proc_gnulong)

    with gzip.open(archive, "rb") as compressed, tarfile.open(fileobj=ExpandedReader(compressed), mode="r|", tarinfo=BoundedInfo) as tar:
        for member in tar:
            count += 1
            require(count <= MEMBER_LIMIT, "archive exceeds member limit")
            require((member.isdir() or member.isfile() or member.issym()) and not member.issparse(), "archive contains unsupported member type")
            require(member.isfile() or member.size == 0, "archive non-file member has a payload")
            name = member_name(member.name, strip)
            if not name:
                require(member.isdir(), "archive strips an entire non-directory member")
                continue
            require(name not in seen, "archive contains duplicate output paths")
            seen.add(name)
            parts = name.split("/")
            for i in range(1, len(parts) + 1):
                prefix = "/".join(parts[:i])
                if prefix not in produced:
                    path_bytes += len(json.dumps(prefix).encode())
                    require(path_bytes <= MANIFEST_LIMIT // 2, "archive output paths exceed metadata limit")
                produced.add(prefix)
                require(len(produced) <= MEMBER_LIMIT + 1, "archive exceeds output entry limit")
            out = os.path.join(target, name)
            os.makedirs(os.path.dirname(out), mode=0o700, exist_ok=True)
            if member.isdir():
                os.makedirs(out, mode=0o700, exist_ok=True)
            elif member.issym():
                link = member.linkname
                require(link and not link.startswith("/") and "\\" not in link and "\x00" not in link and len(link.encode()) <= PATH_LIMIT, "archive symlink is unsafe")
                require(len(link.split("/")) <= DEPTH_LIMIT, "archive symlink target exceeds depth limit")
                normalized = posixpath.normpath(posixpath.join(posixpath.dirname(name), link))
                require(normalized != ".." and not normalized.startswith("../"), "archive symlink escapes destination")
                links.append((out, link))
            else:
                total += member.size
                require(member.size >= 0 and total <= EXPANDED_LIMIT, "archive exceeds expanded data limit")
                with tar.extractfile(member) as source, open(out, "xb") as dest:
                    remaining = member.size
                    while remaining:
                        data = source.read(min(1024 * 1024, remaining))
                        require(bool(data), "archive member is truncated")
                        dest.write(data)
                        remaining -= len(data)
                    dest.flush()
                    os.fsync(dest.fileno())
                os.chmod(out, 0o755 if member.mode & 0o111 else 0o644)
    for out, link in links:
        require(not os.path.lexists(out), "archive symlink conflicts with another member")
        os.symlink(link, out)
    # Resolve chains iteratively; never recurse through attacker-chosen links.
    linkmap = {os.path.relpath(out, target): link for out, link in links}
    for name in linkmap:
        pending = deque(name.split("/"))
        resolved = []
        steps = 0
        while pending:
            part = pending.popleft()
            if part in ("", "."):
                continue
            if part == "..":
                require(bool(resolved), "archive symlink escapes destination")
                resolved.pop()
                continue
            resolved.append(part)
            require(len(resolved) <= DEPTH_LIMIT, "archive symlink target exceeds depth limit")
            candidate = "/".join(resolved)
            if candidate in linkmap:
                steps += 1
                require(steps <= DEPTH_LIMIT, "archive symlink chain is cyclic or exceeds limit")
                resolved.pop()
                pending.extendleft(reversed(linkmap[candidate].split("/")))
    for current, dirs, files in os.walk(target, followlinks=False):
        os.chown(current, uid, gid, follow_symlinks=False)
        os.chmod(current, 0o755)
        for name in dirs + files:
            os.chown(os.path.join(current, name), uid, gid, follow_symlinks=False)
        syncdir(current)


def exchange(a, b):
    libc = ctypes.CDLL(None, use_errno=True)
    rename = getattr(libc, "renameat2", None)
    require(rename is not None, "atomic directory exchange requires Linux renameat2")
    rename.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
    rename.restype = ctypes.c_int
    if rename(-100, os.fsencode(a), -100, os.fsencode(b), 2) != 0:
        raise Failure("atomic directory exchange is unavailable or failed")


def remove_tree(path, expected):
    st = lstat(path)
    if st is None:
        return
    require(same(st, expected["identity"]), "artifact identity changed before cleanup")
    scan(path, expected["entries"])
    # Freeze each retiring directory before opening its children. An application
    # may retain directory descriptors from before exchange; changing ownership
    # and permissions blocks rename through those descriptors as well.
    def remove_at(parent, name, device, rel):
        st = os.stat(name, dir_fd=parent, follow_symlinks=False)
        require(st.st_dev == device, "artifact cleanup crosses a device boundary")
        require(rel in expected["entries"], "artifact contains unexpected extra entries")
        expected_kind = expected["entries"][rel]["kind"]
        actual_kind = "directory" if stat.S_ISDIR(st.st_mode) else "file" if stat.S_ISREG(st.st_mode) else "symlink" if stat.S_ISLNK(st.st_mode) else "special"
        require(actual_kind == expected_kind, "tracked entry has incompatible type")
        if stat.S_ISDIR(st.st_mode):
            fd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
            try:
                require(same(os.fstat(fd), identity(st)), "artifact changed during cleanup")
                os.fchown(fd, os.geteuid(), os.getegid())
                os.fchmod(fd, 0o700)
                for child in os.listdir(fd):
                    remove_at(fd, child, device, child if not rel else rel + "/" + child)
                require(same(os.stat(name, dir_fd=parent, follow_symlinks=False), identity(st)), "artifact changed during cleanup")
                os.rmdir(name, dir_fd=parent)
            finally:
                os.close(fd)
        else:
            require(same(os.stat(name, dir_fd=parent, follow_symlinks=False), identity(st)), "artifact changed during cleanup")
            os.unlink(name, dir_fd=parent)
    parent = os.open(os.path.dirname(path), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        remove_at(parent, os.path.basename(path), st.st_dev, "")
    finally:
        os.close(parent)


def recover(state, dest):
    tx = read_json(os.path.join(state, "transaction.json"))
    if tx is None:
        return
    stage = os.path.join(state, "payload")
    current = lstat(dest)
    new, old = tx["new"], tx["old"]
    if new is not None and same(current, new["identity"]):
        write_json(state, "manifest.json", new)
        if lstat(stage) is not None:
            require(old is not None, "unexpected transaction staging entry")
            remove_tree(stage, old)
    elif new is None and current is None:
        if lstat(stage) is not None:
            remove_tree(stage, old)
        if lstat(os.path.join(state, "manifest.json")) is not None:
            os.unlink(os.path.join(state, "manifest.json"))
    else:
        require((old is None and current is None) or old is not None and same(current, old["identity"]), "artifact changed during interrupted publication")
        if new is not None and lstat(stage) is not None:
            remove_tree(stage, new)
    os.unlink(os.path.join(state, "transaction.json"))
    syncdir(state)


def clean_partial(p, state):
    """Unpublished files are private to this resource and may be safely discarded."""
    stage = os.path.join(state, "payload")
    if lstat(stage) is not None:
        remove_tree(stage, manifest_for(p, stage, enforce_budget=False))
    archive = os.path.join(state, "archive")
    if lstat(archive) is not None:
        ast = os.lstat(archive)
        require(stat.S_ISREG(ast.st_mode) and ast.st_nlink == 1 and ast.st_uid == os.geteuid(), "artifact staging file is unsafe")
        os.unlink(archive)


def run(request):
    require(sys.platform.startswith("linux") and sys.version_info >= (3, 8), "artifacts require Linux and Python 3.8 or newer")
    require(getattr(ctypes.CDLL(None), "renameat2", None) is not None, "artifacts require Linux renameat2 support")
    if request["op"] == "check":
        return {}
    p = request["change"]
    uid, gid = accounts(p)
    execute = request["op"] == "execute"
    signal.signal(signal.SIGALRM, lambda *_: (_ for _ in ()).throw(Failure("artifact operation timed out")))
    signal.alarm(max(1, math.ceil(request["seconds"])))
    parent = safe_parent(p["path"], execute and p["state"] == "present")
    if parent is None:
        return {"changed": p["state"] == "present"}
    try:
        # /proc/self/fd anchors operations even when an ancestor is concurrently renamed.
        anchored = "/proc/self/fd/" + str(parent)
        dest = os.path.join(anchored, os.path.basename(p["path"]))
        state = os.path.join(anchored, metadata_name(p["path"]))
        if lstat(state) is None:
            require(lstat(dest) is None, "target is unowned or has been replaced")
            if not execute or p["state"] == "absent":
                return {"changed": p["state"] == "present"}
            os.mkdir(state, 0o700)
            os.fsync(parent)
        private_state(state)
        mounts(os.path.join(os.path.dirname(p["path"]), metadata_name(p["path"])))
        lockpath = os.path.join(state, "lock")
        if not execute and lstat(lockpath) is None:
            require(not os.listdir(state), "artifact metadata is incomplete")
            return {"changed": p["state"] == "present"}
        flags = os.O_RDWR | os.O_NOFOLLOW | (os.O_CREAT if execute else 0)
        lock = os.open(lockpath, flags, 0o600)
        try:
            st = os.fstat(lock)
            require(stat.S_ISREG(st.st_mode) and st.st_nlink == 1 and st.st_uid == os.geteuid() and stat.S_IMODE(st.st_mode) == 0o600, "artifact lock is not protected")
            try:
                fcntl.flock(lock, (fcntl.LOCK_EX if execute else fcntl.LOCK_SH) | fcntl.LOCK_NB)
            except BlockingIOError:
                raise Failure("another artifact operation holds the resource lock")
            if execute:
                recover(state, dest)
                clean_partial(p, state)
            else:
                require(read_json(os.path.join(state, "transaction.json")) is None, "artifact has interrupted publication; continue its saved plan first")
            m = read_json(os.path.join(state, "manifest.json"))
            current = owned(p, dest, m)
            if not execute:
                changed = current is not None if p["state"] == "absent" else not desired(p, m, current, uid, gid)
                return {"changed": changed, "identity": m["identity"] if current is not None else None, "token": m["token"] if current is not None else ""}
            if current is not None:
                own_result = m["token"] == p["token"] and m["origin"] == origin(p)
                require(own_result or p.get("identity") == m["identity"] and p.get("expected_token") == m["token"], "artifact identity or generation changed since preparation")
                if p["state"] == "present" and desired(p, m, current, uid, gid) and own_result:
                    return {}
            elif p.get("identity") is not None:
                require(p["state"] == "absent", "artifact disappeared since preparation")
            stage = os.path.join(state, "payload")
            require(lstat(stage) is None, "artifact has incomplete staging data; inspect protected metadata before continuing")
            archive = os.path.join(state, "archive")
            if lstat(archive) is not None:
                ast = os.lstat(archive)
                require(stat.S_ISREG(ast.st_mode) and ast.st_nlink == 1 and ast.st_uid == os.geteuid(), "artifact staging file is unsafe")
                os.unlink(archive)
            if p["state"] == "absent":
                if current is None:
                    return {}
                write_json(state, "transaction.json", {"new": None, "old": m})
                os.rename(dest, stage)
                os.fsync(parent)
                syncdir(state)
                recover(state, dest)
                return {}
            try:
                download(p, archive, request["seconds"])
                if p["type"] == "archive":
                    extract(archive, stage, p.get("strip_components", 0), uid, gid)
                    os.unlink(archive)
                else:
                    os.rename(archive, stage)
                    os.chown(stage, uid, gid)
                    os.chmod(stage, p.get("mode", 0o755))
                new = manifest_for(p, stage)
                # Recheck resource identity, complete entry set, mounts and parent before publication.
                owned(p, dest, m)
                check_parent = safe_parent(p["path"], False)
                require(check_parent is not None and identity(os.fstat(check_parent)) == identity(os.fstat(parent)), "artifact parent changed before publication")
                os.close(check_parent)
                write_json(state, "transaction.json", {"new": new, "old": m if current is not None else None})
                if current is None:
                    # RENAME_NOREPLACE prevents adoption of an entry that appeared during fetching.
                    rename_noreplace(stage, dest)
                elif p["type"] == "archive":
                    exchange(stage, dest)
                else:
                    # Exchange retains the old file until journal recovery commits metadata.
                    exchange(stage, dest)
                os.fsync(parent)
                syncdir(state)
                recover(state, dest)
                require(desired(p, read_json(os.path.join(state, "manifest.json")), owned(p, dest, new), uid, gid), "published artifact verification failed")
                return {}
            finally:
                # Journaled staging is deliberately retained for explicit continuation.
                if read_json(os.path.join(state, "transaction.json")) is None:
                    clean_partial(p, state)
        finally:
            os.close(lock)
    finally:
        os.close(parent)


def rename_noreplace(a, b):
    libc = ctypes.CDLL(None, use_errno=True)
    rename = getattr(libc, "renameat2", None)
    require(rename is not None, "atomic artifact publication requires Linux renameat2")
    rename.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
    rename.restype = ctypes.c_int
    require(rename(-100, os.fsencode(a), -100, os.fsencode(b), 1) == 0, "artifact appeared during publication or atomic rename is unavailable")


if __name__ == "__main__":
    try:
        result = run(json.load(sys.stdin))
    except Failure as exc:
        result = {"error": str(exc)}
    except Exception:
        # Never expose URLs, network diagnostics, response bodies or archive names.
        result = {"error": "target artifact operation failed safely"}
    print(json.dumps(result, separators=(",", ":")))
