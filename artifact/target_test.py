"""Target helper boundary tests; invoked by Go on every supported development host."""
import contextlib
import copy
import hashlib
import gzip
import importlib.util
import io
import json
import os
import stat
import tarfile
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("artifact_target", os.path.join(os.path.dirname(__file__), "target.py"))
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)


class HelperTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = self.temp.name

    def tar(self, members):
        path = os.path.join(self.root, "fixture.tar.gz")
        with tarfile.open(path, "w:gz") as tar:
            for name, kind, value, mode in members:
                info = tarfile.TarInfo(name)
                info.mode = mode
                if kind == "file":
                    info.size = len(value)
                    tar.addfile(info, io.BytesIO(value))
                else:
                    info.type = {"directory": tarfile.DIRTYPE, "symlink": tarfile.SYMTYPE, "hardlink": tarfile.LNKTYPE, "fifo": tarfile.FIFOTYPE}[kind]
                    info.linkname = value
                    tar.addfile(info)
        return path

    def extract(self, members, strip=0):
        target = os.path.join(self.root, "output")
        h.extract(self.tar(members), target, strip, os.getuid(), os.getgid())
        return target

    def test_archive_safe_modes_and_relative_symlinks(self):
        out = self.extract([
            ("release/bin/tool", "file", b"binary", 0o6755),
            ("release/data", "file", b"data", 0o666),
            ("release/current", "symlink", "bin/tool", 0o777),
        ], 1)
        self.assertEqual(stat.S_IMODE(os.stat(os.path.join(out, "bin/tool")).st_mode), 0o755)
        self.assertEqual(stat.S_IMODE(os.stat(os.path.join(out, "data")).st_mode), 0o644)
        self.assertEqual(os.readlink(os.path.join(out, "current")), "bin/tool")
        self.assertEqual(stat.S_IMODE(os.stat(out).st_mode), 0o755)

    def test_archive_rejects_unsafe_members(self):
        cases = [
            [("../escape", "file", b"x", 0o644)],
            [("/escape", "file", b"x", 0o644)],
            [("release/../escape", "file", b"x", 0o644)],
            [("link", "hardlink", "file", 0o644)],
            [("fifo", "fifo", "", 0o644)],
            [("a", "file", b"a", 0o644), ("a", "file", b"b", 0o644)],
            [("a", "symlink", "../../escape", 0o777)],
            [("a", "symlink", "/escape", 0o777)],
            [("a", "symlink", "b", 0o777), ("b", "symlink", "../escape", 0o777)],
            [("a", "symlink", "b", 0o777), ("b", "symlink", "a", 0o777)],
            [("a", "symlink", "file", 0o777), ("a/x", "file", b"x", 0o644)],
        ]
        for members in cases:
            with self.subTest(members=members):
                with tempfile.TemporaryDirectory(dir=self.root) as dest:
                    with self.assertRaises((h.Failure, FileExistsError)):
                        h.extract(self.tar(members), os.path.join(dest,"output"), 0, os.getuid(), os.getgid())
        self.assertFalse(os.path.exists(os.path.join(self.root,"escape")))

    def test_member_limits(self):
        with mock.patch.object(h,"MEMBER_LIMIT",1):
            with self.assertRaisesRegex(h.Failure,"member limit"):
                self.extract([("a","file",b"a",0o644),("b","file",b"b",0o644)])
        with mock.patch.object(h,"EXPANDED_LIMIT",1):
            with self.assertRaisesRegex(h.Failure,"data limit"):
                h.extract(self.tar([("a","file",b"ab",0o644)]),os.path.join(self.root,"other"),0,os.getuid(),os.getgid())

    def test_extension_chains_and_expanded_stream_are_bounded(self):
        with self.assertRaisesRegex(h.Failure,"depth limit"):
            h.member_name("/".join(["a"]*129),0)
        header=tarfile.TarInfo("extension")
        header.type=tarfile.XHDTYPE
        path=os.path.join(self.root,"extensions.tar.gz")
        with gzip.open(path,"wb") as f:
            f.write(header.tobuf()*40 + bytes(1024))
        with self.assertRaisesRegex(h.Failure,"aggregate limit"):
            h.extract(path,os.path.join(self.root,"extensions"),0,os.getuid(),os.getgid())
        archive=self.tar([("directory","directory","",0o755)])
        with mock.patch.object(h,"EXPANDED_LIMIT",100):
            with self.assertRaisesRegex(h.Failure,"expanded data limit"):
                h.extract(archive,os.path.join(self.root,"stream"),0,os.getuid(),os.getgid())
        with mock.patch.object(h,"MEMBER_LIMIT",2):
            with self.assertRaisesRegex(h.Failure,"output entry limit"):
                h.extract(self.tar([("a/b/c/file","file",b"x",0o644)]),os.path.join(self.root,"deep"),0,os.getuid(),os.getgid())
        sparse=tarfile.TarInfo("sparse")
        sparse.type=tarfile.GNUTYPE_SPARSE
        path=os.path.join(self.root,"sparse.tar.gz")
        with gzip.open(path,"wb") as f:f.write(sparse.tobuf() + bytes(1024))
        with self.assertRaisesRegex(h.Failure,"sparse files are unsupported"):
            h.extract(path,os.path.join(self.root,"sparse"),0,os.getuid(),os.getgid())

    def test_oversized_metadata_never_replaces_readable_journal(self):
        state=os.path.join(self.root,"state");os.mkdir(state,0o700)
        old={"old":"retained"}
        h.write_json(state,"transaction.json",old)
        with mock.patch.object(h,"METADATA_LIMIT",32):
            with self.assertRaisesRegex(h.Failure,"size limit"):
                h.write_json(state,"transaction.json",{"new":"x"*100})
        self.assertEqual(h.read_json(os.path.join(state,"transaction.json")),old)
        stage=os.path.join(state,"payload")
        h.extract(self.tar([("file","file",b"data",0o644)]),stage,0,os.getuid(),os.getgid())
        p={"type":"archive","path":"/opt/test","token":"a"*32}
        with mock.patch.object(h,"MANIFEST_LIMIT",64):
            with self.assertRaisesRegex(h.Failure,"manifest exceeds"):
                h.manifest_for(p,stage)
            h.clean_partial(p,state)
        self.assertFalse(os.path.exists(stage))

    def test_manifest_repairable_drift_and_extra_entries(self):
        target=self.extract([("file","file",b"old",0o644)])
        p={"type":"archive","path":target,"url":"https://example.test/a","sha256":"a"*64,"format":"tar.gz","token":"a"*32}
        m=h.manifest_for(p,target)
        with open(os.path.join(target,"file"),"wb") as f:f.write(b"edited")
        with mock.patch.object(h,"mounts"):
            current=h.owned(p,target,m)
            self.assertNotEqual(current,m["entries"])
            self.assertFalse(h.desired(p,m,current,os.getuid(),os.getgid()))
            os.unlink(os.path.join(target,"file"))
            self.assertEqual(set(h.owned(p,target,m)),{""})
            with open(os.path.join(target,"extra"),"w") as f:f.write("undeclared")
            with self.assertRaisesRegex(h.Failure,"extra entries"):h.owned(p,target,m)

    def test_mirror_url_alone_is_not_an_installed_change(self):
        target=self.extract([("file","file",b"data",0o644)])
        p={"type":"archive","path":target,"url":"https://example.test/first","sha256":"a"*64,"format":"tar.gz","token":"a"*32}
        m=h.manifest_for(p,target)
        p["url"]="https://mirror.test/same-artifact"
        self.assertTrue(h.desired(p,m,h.scan(target),os.getuid(),os.getgid()))

    @unittest.skipUnless(h.sys.platform.startswith("linux") and os.geteuid()==0,"root Linux permission boundary")
    def test_retiring_tree_freezes_retained_application_descriptors(self):
        app=h.pwd.getpwnam("nobody")
        os.chmod(self.root,0o755)
        target=os.path.join(self.root,"retiring")
        h.extract(self.tar([("child/file","file",b"owned",0o644)]),target,0,app.pw_uid,app.pw_gid)
        p={"type":"archive","path":target,"token":"a"*32}
        expected=h.manifest_for(p,target)
        retained=os.open(target,os.O_RDONLY|os.O_DIRECTORY)
        outside=os.path.join(self.root,"outside");os.mkdir(outside,0o777);os.chmod(outside,0o777)
        moved=os.path.join(outside,"moved")
        original=h.os.listdir
        attacked=[False]
        root_inode=os.fstat(retained).st_ino
        def attempt(fd):
            if isinstance(fd,int) and os.fstat(fd).st_ino==root_inode and stat.S_IMODE(os.fstat(fd).st_mode)==0o700:
                attacked[0]=True
                child=os.fork()
                if child==0:
                    try:
                        os.setgroups([]);os.setgid(app.pw_gid);os.setuid(app.pw_uid)
                        try:os.rename("child",moved,src_dir_fd=retained)
                        except PermissionError:os._exit(0)
                        os._exit(1)
                    except BaseException:os._exit(2)
                _,status=os.waitpid(child,0)
                self.assertEqual(status,0,"application moved a retiring child through its retained FD")
            return original(fd)
        try:
            with mock.patch.object(h.os,"listdir",side_effect=attempt):h.remove_tree(target,expected)
        finally:os.close(retained)
        self.assertTrue(attacked[0]);self.assertFalse(os.path.exists(moved));self.assertFalse(os.path.exists(target))

    def test_marker_without_protected_manifest_cannot_own(self):
        target=self.extract([])
        p={"type":"archive","path":target}
        with self.assertRaisesRegex(h.Failure,"unowned"):h.owned(p,target,None)
        state=os.path.join(self.root,"state");os.mkdir(state,0o755)
        with self.assertRaisesRegex(h.Failure,"not protected"):h.private_state(state)
        os.chmod(state,0o700)
        targetfile=os.path.join(self.root,"outside")
        with open(targetfile,"w") as f:f.write("{}")
        os.symlink(targetfile,os.path.join(state,"manifest.json"))
        with self.assertRaisesRegex(h.Failure,"not protected"):h.read_json(os.path.join(state,"manifest.json"))

    def test_redirects_never_downgrade_or_accept_credentials(self):
        for url in ("http://example.test/x","https://u:p@example.test/x","https://@example.test/x","https://example.test/x#fragment"):
            with self.assertRaises(h.Failure):h.Redirects().redirect_request(None,None,302,"",{},url)
        request=h.urllib.request.Request("https://github.com/example/tool/releases/download/v1/tool")
        redirect=h.Redirects().redirect_request(request,None,302,"",{},"https://release-assets.githubusercontent.com/tool?signature=ephemeral")
        self.assertEqual(redirect.full_url,"https://release-assets.githubusercontent.com/tool?signature=ephemeral")

    def test_download_hash_and_size_gate_publication(self):
        class Response(io.BytesIO):
            headers={"Content-Length":"3"}
        opener=mock.Mock()
        p={"url":"https://example.test/x","sha256":hashlib.sha256(b"abc").hexdigest()}
        with mock.patch.object(h.urllib.request,"build_opener",return_value=opener):
            opener.open.return_value=Response(b"abc")
            out=os.path.join(self.root,"download")
            h.download(p,out,5)
            with open(out,"rb") as f:self.assertEqual(f.read(),b"abc")
            opener.open.return_value=Response(b"bad")
            with self.assertRaisesRegex(h.Failure,"checksum mismatch"):h.download(p,out+"2",5)
            opener.open.return_value=Response(b"abc")
            with mock.patch.object(h,"DOWNLOAD_LIMIT",2):
                with self.assertRaisesRegex(h.Failure,"download limit"):h.download(p,out+"3",5)
            response=Response(b"abc");response.headers={"Content-Length":"4"}
            opener.open.return_value=response
            with self.assertRaisesRegex(h.Failure,"truncated"):h.download(p,out+"4",5)

    @unittest.skipUnless(h.sys.platform.startswith("linux"),"Linux renameat2 and /proc integration")
    def test_resource_lock_survives_removal_and_reinstallation(self):
        p={"type":"download","state":"present","path":os.path.join(self.root,"install"),"url":"https://example.test/x","sha256":hashlib.sha256(b"binary").hexdigest(),"mode":0o755,"token":"a"*32}
        def download(_p,path,_seconds):
            with open(path,"xb") as dest:dest.write(b"binary")
        def run(op):return h.run({"op":op,"change":p,"seconds":30})
        with mock.patch.object(h,"download",side_effect=download) as fetch:
            run("execute")
            state=os.path.join(self.root,h.metadata_name(p["path"]))
            lockpath=os.path.join(state,"lock")
            lock_identity=h.identity(os.stat(lockpath))
            def snapshot():
                result={}
                for name in os.listdir(state):
                    with open(os.path.join(state,name),"rb") as f:result[name]=f.read()
                return result
            before=snapshot()
            target_before=h.scan(p["path"])
            lock=os.open(lockpath,os.O_RDWR)
            try:
                h.fcntl.flock(lock,h.fcntl.LOCK_EX|h.fcntl.LOCK_NB)
                for op in ("execute","observe"):
                    with self.assertRaisesRegex(h.Failure,"resource lock"):run(op)
                self.assertEqual(snapshot(),before)
                self.assertEqual(h.scan(p["path"]),target_before)
                self.assertEqual(fetch.call_count,1)
            finally:os.close(lock)
            observed=run("observe")
            p.update(state="absent",identity=observed["identity"],expected_token=observed["token"],token="b"*32)
            run("execute")
            self.assertEqual(h.identity(os.stat(lockpath)),lock_identity)
            self.assertEqual(set(os.listdir(state)),{"lock"})
            p.update(state="present",token="c"*32)
            del p["identity"];del p["expected_token"]
            run("execute")
            self.assertEqual(h.identity(os.stat(lockpath)),lock_identity)
            self.assertEqual(fetch.call_count,2)
            # A saved task must not adopt a replacement that appeared afterward.
            observed=run("observe")
            p.update(identity=observed["identity"],expected_token=observed["token"],token="d"*32)
            alien=os.path.join(self.root,"alien")
            with open(alien,"wb") as f:f.write(b"foreign")
            os.replace(alien,p["path"])
            alien_identity=h.identity(os.stat(p["path"]))
            for op in ("execute","observe"):
                with self.assertRaisesRegex(h.Failure,"unowned or has been replaced"):run(op)
            self.assertEqual(h.identity(os.stat(p["path"])),alien_identity)
            with open(p["path"],"rb") as f:self.assertEqual(f.read(),b"foreign")
            self.assertEqual(fetch.call_count,2)

    @unittest.skipUnless(h.sys.platform.startswith("linux"),"Linux renameat2 and /proc integration")
    def test_lifecycle_and_interruption_recovery(self):
        p={"type":"archive","state":"present","path":os.path.join(self.root,"install"),"url":"https://example.test/x","sha256":"a"*64,"format":"tar.gz","token":"a"*32}
        fixture=self.tar([("file","file",b"original",0o644)])
        def download(_p,path,_seconds):
            with open(fixture,"rb") as source,open(path,"xb") as dest:dest.write(source.read())
        def run(op):return h.run({"op":op,"change":p,"seconds":30})
        with mock.patch.object(h,"download",side_effect=download):
            self.assertTrue(run("observe")["changed"])
            self.assertFalse(os.path.exists(os.path.join(self.root,h.metadata_name(p["path"]))))
            run("execute")
            self.assertFalse(run("observe")["changed"])
            # Same frozen token recognizes an already committed outcome.
            run("execute")
            p["identity"]=run("observe")["identity"];p["expected_token"]=p["token"];p["token"]="b"*32
            with open(os.path.join(p["path"],"file"),"w") as f:f.write("edited")
            self.assertTrue(run("observe")["changed"])
            # Interrupt immediately after the atomic exchange, before manifest commit.
            original=h.recover
            count=[0]
            def interrupt(state,dest):
                count[0]+=1
                if count[0]==2:raise h.Failure("interrupted")
                return original(state,dest)
            with mock.patch.object(h,"recover",side_effect=interrupt):
                with self.assertRaisesRegex(h.Failure,"interrupted"):run("execute")
            run("execute")
            with open(os.path.join(p["path"],"file"),"rb") as f:self.assertEqual(f.read(),b"original")
            self.assertFalse(run("observe")["changed"])
            p["state"]="absent"
            obs=run("observe");p["identity"]=obs["identity"];p["expected_token"]=obs["token"]
            run("execute");run("execute")
            self.assertFalse(os.path.exists(p["path"]))


if __name__=="__main__":unittest.main()
