"""Prepared file entry transactions, shared by local and SSH execution.

Only controlled errors leave this helper. File contents and OS exception text
must never become command diagnostics. Parent symlinks follow normal OS rules.
"""
import base64
import errno
import hashlib
import json
import os
import stat
import sys
import tempfile

# Apple's system Python omits os.*xattr even though Darwin implements them.
# Keep the shared helper usable by local development tests without omitting
# metadata. Linux uses Python's native descriptor-aware functions.
if not hasattr(os, "listxattr"):
    import ctypes
    _libc = ctypes.CDLL(None, use_errno=True)

    def _xattr_call(operation, obj, name=None, value=None, follow_symlinks=True):
        fd = isinstance(obj, int)
        func = getattr(_libc, ("f" if fd else "") + operation + "xattr")
        args = [obj if fd else os.fsencode(obj)]
        if name is not None:
            args.append(os.fsencode(name))
        options = 0 if follow_symlinks or fd else 1
        if operation == "remove":
            if not fd:
                args.append(options)
            result = func(*args)
        elif operation == "set":
            args += [value, len(value), 0, options]
            result = func(*args)
        else:
            tail = [0, options] if operation == "get" else [options]
            size = func(*(args + [None, 0] + tail))
            if size < 0:
                raise OSError(ctypes.get_errno(), "extended attribute read failed")
            buffer = ctypes.create_string_buffer(size)
            result = func(*(args + [buffer, size] + tail))
            if result >= 0:
                return buffer.raw[:result]
        if result < 0:
            raise OSError(ctypes.get_errno(), "extended attribute operation failed")

    def _listxattr(obj, **kwargs):
        return [os.fsdecode(x) for x in _xattr_call("list", obj, **kwargs).split(b"\0") if x]

    os.listxattr = _listxattr
    os.getxattr = lambda obj, name, **kwargs: _xattr_call("get", obj, name, **kwargs)
    os.setxattr = lambda obj, name, value, **kwargs: _xattr_call("set", obj, name, value, **kwargs)
    os.removexattr = lambda obj, name, **kwargs: _xattr_call("remove", obj, name, **kwargs)


class Failure(Exception):
    pass


def fail(message):
    raise Failure(message)


def metadata(path, st, fd=None):
    obj = fd if fd is not None else path
    kwargs = {} if fd is not None else {"follow_symlinks": False}
    attrs = {}
    for name in os.listxattr(obj, **kwargs):
        attrs[name] = base64.b64encode(os.getxattr(obj, name, **kwargs)).decode("ascii")
    return {"uid": st.st_uid, "gid": st.st_gid,
            "mode": stat.S_IMODE(st.st_mode), "xattrs": attrs}


def snapshot(path):
    try:
        st = os.lstat(path)
    except FileNotFoundError:
        return {"kind": "absent"}, b""
    if stat.S_ISLNK(st.st_mode):
        return {"kind": "symlink", "link": os.readlink(path),
                "metadata": metadata(path, st)}, b""
    if not stat.S_ISREG(st.st_mode):
        fail("target is not a regular file or symlink")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as source:
        st = os.fstat(source.fileno())
        if not stat.S_ISREG(st.st_mode):
            fail("target is not a regular file")
        data = source.read()
        state = {"kind": "regular", "sha256": hashlib.sha256(data).hexdigest(),
                 "metadata": metadata(path, st, source.fileno())}
    return state, data


def matches(left, right):
    return left["kind"] == right["kind"] and all(
        left.get(key) == right.get(key) for key in ("sha256", "link"))


def sync_dir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def apply_metadata(path, meta, symlink=False):
    # Ownership can clear setid bits/capabilities; restore mode and all xattrs
    # afterwards. ACLs live in xattrs and must precede the final explicit mode.
    os.chown(path, meta["uid"], meta["gid"], follow_symlinks=False)
    if not symlink:
        # Metadata APIs on some supported development systems require owner
        # write permission. Staging is private; in-place contents are complete
        # before this call. Restore final permissions after restoring xattrs.
        os.chmod(path, meta["mode"] | stat.S_IWUSR)
    existing = set(os.listxattr(path, follow_symlinks=False))
    for name in existing - set(meta["xattrs"]):
        os.removexattr(path, name, follow_symlinks=False)
    for name, value in meta["xattrs"].items():
        os.setxattr(path, name, base64.b64decode(value), follow_symlinks=False)
    if not symlink:
        os.chmod(path, meta["mode"])
    # Linux ignores symlink permissions; all other link metadata is applicable.
    if not symlink and stat.S_IMODE(os.lstat(path).st_mode) != meta["mode"]:
        fail("file metadata preservation failed")
    if metadata(path, os.lstat(path)) != meta:
        fail("file metadata preservation failed")


def sync_file(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def verify_backup(f):
    backup = f["backup_path"]
    receipt = backup + ".richt.json"
    try:
        receipt_state, raw = snapshot(receipt)
        if receipt_state["kind"] != "regular" or receipt_state["metadata"]["mode"] != 0o600:
            fail("backup receipt is missing or incompatible")
        record = json.loads(raw)
        old, _ = snapshot(backup)
        if record["operation_id"] != f["operation_id"] or old != record["state"] or not matches(old, f["expected"]):
            fail("backup conflicts with prepared operation")
    except (OSError, ValueError, KeyError, TypeError):
        fail("backup receipt is missing or incompatible")
    if old["kind"] == "regular":
        sync_file(backup)
    sync_file(receipt)
    sync_dir(os.path.dirname(backup))


def backup_old(f, current, data, staging):
    if not f.get("backup_path"):
        return
    backup = f["backup_path"]
    receipt = backup + ".richt.json"
    has_backup, has_receipt = os.path.lexists(backup), os.path.lexists(receipt)
    if has_backup:
        verify_backup(f)
        return
    if has_receipt:
        # A crash after durable receipt publication but before publishing the
        # old copy can be repaired only from the exact still-current old object.
        state, raw = snapshot(receipt)
        try:
            record = json.loads(raw)
            if state["kind"] != "regular" or state["metadata"]["mode"] != 0o600 or record["operation_id"] != f["operation_id"] or record["state"] != current or not matches(current, f["expected"]):
                fail("backup receipt conflicts with prepared operation")
        except (ValueError, KeyError, TypeError):
            fail("backup receipt conflicts with prepared operation")
    candidate = os.path.join(staging, "backup")
    if current["kind"] == "symlink":
        os.symlink(current["link"], candidate)
        apply_metadata(candidate, current["metadata"], True)
    else:
        with open(candidate, "xb") as output:
            output.write(data)
            output.flush()
            apply_metadata(candidate, current["metadata"])
            os.fsync(output.fileno())
    if metadata(candidate, os.lstat(candidate)) != current["metadata"]:
        fail("backup verification failed")
    receipt_tmp = os.path.join(staging, "receipt")
    fd = os.open(receipt_tmp, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "w") as output:
        json.dump({"operation_id": f["operation_id"], "state": current}, output)
        output.flush()
        os.fsync(output.fileno())
    # link() publishes without clobbering an existing entry. The regular backup
    # is an independent copy, never a hardlink to the managed source inode.
    # Publish the receipt first so recovery can prove where its old copy came
    # from even if interrupted before publishing that copy.
    if not has_receipt:
        os.link(receipt_tmp, receipt, follow_symlinks=False)
        sync_dir(os.path.dirname(backup))
    os.link(candidate, backup, follow_symlinks=False)
    sync_dir(os.path.dirname(backup))
    verify_backup(f)


def intended_metadata(f, current, uid, gid, candidate):
    if current["kind"] == "regular":
        meta = dict(current["metadata"])
        meta["xattrs"] = dict(meta["xattrs"])
    else:
        st = os.lstat(candidate)
        meta = metadata(candidate, st)
        meta["mode"] = 0o644
        # A staged path's default context can differ from the declared path.
        # Restore the final path's default policy label for a new regular file.
        if sys.platform.startswith("linux") and os.path.exists("/sys/fs/selinux/enforce"):
            import ctypes
            lib = ctypes.CDLL("libselinux.so.1", use_errno=True)
            value = ctypes.c_char_p()
            if lib.matchpathcon(os.fsencode(f["path"]), stat.S_IFREG | meta["mode"], ctypes.byref(value)) != 0:
                # A policy <<none>> entry (notably /var/tmp on the acceptance
                # platform) requests ordinary kernel inheritance, already on
                # the adjacent candidate. Other lookup errors fail closed.
                if ctypes.get_errno() != errno.ENOENT:
                    fail("default SELinux context lookup failed")
            else:
                try:
                    meta["xattrs"]["security.selinux"] = base64.b64encode(value.value + b"\x00").decode("ascii")
                finally:
                    lib.freecon(value)
    if uid != -1:
        meta["uid"] = uid
    if gid != -1:
        meta["gid"] = gid
    if f.get("selinux"):
        meta["xattrs"]["security.selinux"] = base64.b64encode(f["selinux"].encode() + b"\x00").decode("ascii")
    return meta


def mutate(req):
    f = req["file"]
    path = f["path"]
    try:
        current, old = snapshot(path)
    except Failure:
        fail("prepared file conflict: incompatible destination type")
    expected = f["expected"]
    desired = f["desired"]
    prior = matches(current, expected)
    resumed = bool(req.get("resume")) and matches(current, desired)
    if not prior and not resumed:
        fail("prepared file conflict: destination content, presence, type or link changed")
    required = bool(f.get("backup_path"))
    if resumed and not prior:
        if required:
            verify_backup(f)
        if desired["kind"] == "absent":
            sync_dir(os.path.dirname(path))
            return
    if req["action"] == "remove-file":
        if current["kind"] == "absent":
            return
        parent = os.path.dirname(path)
        with tempfile.TemporaryDirectory(prefix=".richt-" + f["operation_id"] + "-", dir=parent) as staging:
            backup_old(f, current, old, staging)
            check, _ = snapshot(path)
            if not matches(check, current):
                fail("prepared file conflict before removal")
            os.unlink(path)
            req["published"] = True
            sync_dir(parent)
        sync_dir(parent)
        return
    # Named ownership was resolved on the target by the caller immediately
    # before mutation. Preparation can project not-yet-created accounts.
    uid, gid = req.get("uid", -1), req.get("gid", -1)
    if req["action"] == "set-file-attributes":
        if current["kind"] != "regular":
            fail("prepared file conflict: attribute destination is not regular")
        meta = intended_metadata(f, current, uid, gid, path)
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            apply_metadata(path, meta)
            if f.get("mode") is not None:
                os.chmod(path, f["mode"])
            os.fsync(fd)
        finally:
            os.close(fd)
        sync_dir(os.path.dirname(path))
        return
    os.makedirs(os.path.dirname(path), mode=0o755, exist_ok=True)
    parent = os.path.dirname(path)
    with tempfile.TemporaryDirectory(prefix=".richt-" + f["operation_id"] + "-", dir=parent) as staging:
        candidate = os.path.join(staging, "new")
        with open(candidate, "xb") as output:
            os.chmod(candidate, 0o600)
            output.write(f.get("content", "").encode("utf-8"))
            output.flush()
            meta = intended_metadata(f, current, uid, gid, candidate)
            apply_metadata(candidate, meta)
            if f.get("mode") is not None:
                os.chmod(candidate, f["mode"])
            os.fsync(output.fileno())
        if prior:
            backup_old(f, current, old, staging)
        elif required:
            verify_backup(f)
        check, _ = snapshot(path)
        if not matches(check, current):
            fail("prepared file conflict before publication")
        if f["atomic_writes"] or current["kind"] != "regular":
            os.replace(candidate, path)
            req["published"] = True
        else:
            # Explicit weaker mode preserves the regular inode, including its
            # hardlink effects. Final symlinks always use entry replacement.
            fd = os.open(path, os.O_WRONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(fd, "wb") as output:
                os.fchmod(output.fileno(), 0o600)
                output.truncate(0)
                req["published"] = True
                output.write(f.get("content", "").encode("utf-8"))
                output.flush()
                apply_metadata(path, meta)
                if f.get("mode") is not None:
                    os.chmod(path, f["mode"])
                os.fsync(output.fileno())
        sync_dir(parent)
    sync_dir(parent)


def main(req):
    if req["action"] == "observe":
        state, data = snapshot(req["file"]["path"])
        return {"state": state, "content": base64.b64encode(data).decode("ascii")}
    mutate(req)
    return {}


if __name__ == "__main__":
    request = {}
    try:
        request = json.load(sys.stdin)
        result = main(request)
    except Failure as error:
        result = {"error": str(error)}
    except Exception:
        result = {"error": "prepared file operation failed"}
    if result.get("error") and request.get("published"):
        result["error"] = "file state may already be installed; durability or completion failed"
    json.dump(result, sys.stdout)
