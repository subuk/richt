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
import uuid
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
secret_markers=[]
def run(args,ok=True):
 p=subprocess.run([cli]+args,text=True,capture_output=True)
 if any(marker in p.stdout+p.stderr for marker in secret_markers):
  raise RuntimeError('secret marker escaped through CLI output')
 if (p.returncode==0)!=ok: raise RuntimeError((args,p.returncode,p.stdout,p.stderr))
 return p.stdout+p.stderr
def write_inv(configs, state='present'):
 with open(inv,'w') as f:
  f.write('tags:\n  vars:\n    order: 10\n    services:\n      example: {state: '+state+'}\n    vars:\n      greeting: example-one\nservices:\n  example:\n    order: 10\n    configs:\n')
  for name,path,extra in configs:
   f.write('      - name: '+name+'\n        path: '+path+'\n        template:\n          file: config.tmpl\n'+extra)
  f.write('hosts:\n  - name: local\n    tags: [vars]\n    transport:\n      type: local\n')
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
    # Synthetic canaries are generated at runtime, never included in reports.
    secret_markers.extend('RICHT_TEST_'+uuid.uuid4().hex for _ in range(3))
    default_value=secret_markers[0]+'\n'+secret_markers[1]+'\n'
    production_value=secret_markers[2]
    default_source=base+'/default.yaml';production_source=base+'/production.yaml'
    for filename,value in [(default_source,default_value),(production_source,production_value)]:
     with open(filename,'w') as f: json.dump({'database':{'password':value}},f)
     os.chmod(filename,0o600)
    first_secret=target+'/secret-first';second_secret=target+'/secret-second'
    secret_config=lambda path,template: dict(path=path,mode='0600',selinux='system_u:object_r:etc_t:s0',template=dict(inline=template))
    secret_inventory=dict(secret_sources={
      'default':dict(type='yaml',file='default.yaml'),
      'production':dict(type='yaml',file='production.yaml')},
      tags=dict(example=dict(services=dict(example={}))),
      services=dict(example=dict(order=10,configs=[dict(secret_config(first_secret,'{{secret "database.password"}}'), name='a'), dict(secret_config(second_secret,'{{secret "database.password" "production"}}'), name='b')])),
      hosts=[dict(name='local',tags=['example'],transport=dict(type='local'))])
    with open(inv,'w') as f: json.dump(secret_inventory,f)
    out=run(['diff','-i',inv,'-o',saved])
    tasks=json.load(open(saved))['hosts'][0]['tasks']
    assert all(task['file']['sensitive'] for task in tasks) and out.count('Content hidden')==2
    assert [task['file']['content'] for task in tasks]==[default_value,production_value]
    assert os.stat(saved).st_mode&0o777==0o600 and os.stat(saved+'.lock').st_mode&0o777==0o600
    checks.append('two independent YAML sources freeze multiline bytes and sensitivity in a private plan')
    os.mkdir(second_secret)
    run(['apply','-y',saved],False)
    assert [task['progress']['status'] for task in json.load(open(saved))['hosts'][0]['tasks']]==['success','error']
    assert open(first_secret).read()==default_value
    os.remove(default_source);os.remove(production_source);os.remove(inv)
    os.rmdir(second_secret)
    open(first_secret,'w').write('manual-after-secret-success\n')
    out=run(['apply','-y',saved])
    assert 'previously completed' in out and open(first_secret).read()=='manual-after-secret-success\n'
    assert open(second_secret).read()==production_value and os.stat(second_secret).st_mode&0o777==0o600
    assert 'system_u:object_r:etc_t:s0' in subprocess.check_output(['ls','-Z',second_secret],text=True)
    checks.append('secret plan resumes without sources, skips successes, and writes frozen bytes with mode and SELinux')
    secret_inventory['services']['example']['configs']=[dict(dict(path=second_secret,sensitive=True,template=dict(inline='public replacement\n')), name='main')]
    with open(inv,'w') as f: json.dump(secret_inventory,f)
    out=run(['diff','-i',inv,'-o',saved]);assert 'Content hidden' in out
    run(['apply','-y',saved]);assert open(second_secret).read()=='public replacement\n'
    checks.append('explicit sensitivity hides an old secret after removing the secret call')
    print(json.dumps({'checks':checks,'count':len(checks),'selinux':subprocess.check_output(['getenforce'],text=True).strip()},indent=2))
finally:
    if args.keep:
        print("Retained test directories:", base, target)
    else:
        shutil.rmtree(base)
        shutil.rmtree(target)
