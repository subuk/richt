#!/usr/bin/env python3
"""Disposable Linux acceptance checks; no package or service operations."""
import argparse
import json
import os
import pty
import select
import shutil
import subprocess
import tempfile
import time
parser = argparse.ArgumentParser(description="Run disposable Linux file, SELinux, and terminal acceptance checks.")
parser.add_argument("--binary", required=True, help="Path to the built richt binary")
parser.add_argument("--work-root", default="/tmp", help="Existing directory for a unique disposable test directory")
parser.add_argument("--target-root", default="/var/tmp", help="Existing directory for a unique disposable managed-file directory")
parser.add_argument("--keep", action="store_true", help="Retain test directories for inspection")
args = parser.parse_args()
if os.geteuid() != 0:
    parser.error("run as root on a disposable SELinux-enabled Linux test machine")
if subprocess.check_output(["getenforce"], text=True).strip() != "Enforcing":
    parser.error("this acceptance suite requires SELinux Enforcing")
base = tempfile.mkdtemp(prefix="richt-files-", dir=os.path.realpath(args.work_root))
target = tempfile.mkdtemp(prefix="richt-files-", dir=os.path.realpath(args.target_root))
cli = os.path.realpath(args.binary)
inv = base + "/inventory.yaml"
saved = base + "/plan.json"
source = base + "/config.tmpl"
checks=[]
def run(args,ok=True):
 p=subprocess.run([cli]+args,text=True,capture_output=True)
 if (p.returncode==0)!=ok: raise RuntimeError((args,p.returncode,p.stdout,p.stderr))
 return p.stdout+p.stderr
def write_inv(configs, state='present'):
 with open(inv,'w') as f:
  f.write('tags:\n  vars:\n    weight: 10\n    vars:\n      greeting: example-one\nservices:\n  example:\n    configs:\n')
  for name,path,extra in configs:
   f.write('      '+name+':\n        path: '+path+'\n        template:\n          file: config.tmpl\n'+extra)
  f.write('hosts:\n  - name: local\n    tags: [vars]\n    transport:\n      type: local\n    services:\n      - name: example\n        state: '+state+'\n')
def tty_apply(answer,expect_ok,hold=False):
 master,slave=pty.openpty();p=subprocess.Popen([cli,'apply',saved],stdin=slave,stdout=slave,stderr=slave,close_fds=True);os.close(slave);out=b'';deadline=time.monotonic()+10
 while b'Apply these changes?' not in out:
  if time.monotonic()>deadline: raise RuntimeError('no prompt '+repr(out))
  ready,_,_=select.select([master],[],[],1)
  if ready: out+=os.read(master,65536)
 if hold:
  run(['apply','-y',saved],False);checks.append('same-plan concurrent apply rejected while confirmation waits')
 os.write(master,answer.encode()+b'\n')
 while p.poll() is None:
  ready,_,_=select.select([master],[],[],1)
  if ready:
   try:out+=os.read(master,65536)
   except OSError:break
 p.wait(timeout=10);os.close(master)
 if (p.returncode==0)!=expect_ok:raise RuntimeError(('tty',p.returncode,out))
 return out
try:
    # create+label, no write during diff, saved sources vanish, stale content ignored
    open(source,'w').write('message={{.greeting}}\n')
    path=target+'/nested/config';write_inv([('main',path,'        owner: root\n        group: root\n        mode: "0640"\n        selinux: system_u:object_r:etc_t:s0\n')])
    run(['diff','-i',inv,'-o',saved]);assert not os.path.exists(path)
    run(['apply',saved],False);assert not os.path.exists(path);checks.append('no TTY without -y refuses without mutation')
    tty_apply('n',False);assert not os.path.exists(path);checks.append('TTY negative answer cancels')
    tty_apply('',False);assert not os.path.exists(path);checks.append('TTY empty answer cancels')
    tty_apply('yes',True,hold=True);assert open(path).read()=='message=example-one\n';assert os.stat(path).st_mode&0o777==0o640
    label=subprocess.check_output(['ls','-Z',path],text=True);assert 'system_u:object_r:etc_t:s0' in label;checks.append('TTY yes creates parent directories and file with root:root mode0640 and SELinux etc_t on Enforcing host')
    assert 'No changes' in run(['diff','-i',inv]);checks.append('no changes after apply')
    text=open(inv).read().replace('example-one','example-two');open(inv,'w').write(text);run(['diff','-i',inv,'-o',saved]);inode=os.stat(path).st_ino
    open(path,'w').write('manual-intervening-change\n');os.remove(source);os.remove(inv);run(['apply','-y',saved]);assert open(path).read()=='message=example-two\n' and os.stat(path).st_ino==inode;checks.append('saved apply ignores deleted inventory/template and intervening target content, preserves inode')
    assert 'previously completed' in run(['apply','-y',saved]);checks.append('repeat saved apply skips successful tasks')
    open(source,'w').write('message={{.greeting}}\n');write_inv([('main',path,'')],state='absent');run(['diff','-i',inv,'-o',saved]);run(['apply','-y',saved]);assert not os.path.exists(path);checks.append('explicit absent removes file')
    # Ordinary writes follow target and parent symlinks.
    link_target=target+'/link-target';open(link_target,'w').write('old\n');os.symlink(link_target,path);write_inv([('main',path,'')]);run(['diff','-i',inv,'-o',saved]);run(['apply','-y',saved]);assert os.path.islink(path) and open(link_target).read()=='message=example-one\n';os.unlink(path);checks.append('symlink target is written through')
    parent_link=target+'/parent-link';os.symlink(target+'/nested',parent_link);write_inv([('main',parent_link+'/via-link','')]);run(['diff','-i',inv,'-o',saved]);run(['apply','-y',saved]);assert open(target+'/nested/via-link').read()=='message=example-one\n';checks.append('symlink parent is followed')
    for extra in ['        owner: richt-user-does-not-exist\n','        group: richt-group-does-not-exist\n']:
     write_inv([('main',path,extra)]);run(['diff','-i',inv],False)
    checks.append('missing owner and group rejected')
    # partial failure+resume
    first=target+'/resume-first';parent=target+'/resume-parent';second=parent+'/resume-second';write_inv([('a',first,''),('b',second,'')]);run(['diff','-i',inv,'-o',saved]);os.mkdir(parent);os.mkdir(second)
    run(['apply','-y',saved],False);p=json.load(open(saved));assert p['hosts'][0]['tasks'][0]['progress']['status']=='success';assert p['hosts'][0]['tasks'][1]['progress']['status']=='error';os.rmdir(second);open(first,'w').write('manual-after-first-success');run(['apply','-y',saved]);assert open(first).read()=='manual-after-first-success';assert open(second).read()=='message=example-one\n';checks.append('second task failure persists first success and explicit resume skips first task')
    # sensitive
    open(second,'w').write('OLD_EXAMPLE_PRIVATE\n');open(source,'w').write('NEW_EXAMPLE_PRIVATE\n');write_inv([('main',second,'        sensitive: true\n')]);out=run(['diff','-i',inv,'-o',saved]);assert 'EXAMPLE_PRIVATE' not in out and 'Content hidden' in out;run(['apply','-y',saved]);checks.append('sensitive output hides both patch sides')
    print(json.dumps({'checks':checks,'count':len(checks),'selinux':subprocess.check_output(['getenforce'],text=True).strip()},indent=2))
finally:
    if args.keep:
        print("Retained test directories:", base, target)
    else:
        shutil.rmtree(base)
        shutil.rmtree(target)
