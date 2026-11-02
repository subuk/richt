#!/usr/bin/env python3
"""Run only as root on a disposable DNF 4/systemd Linux host."""
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid

binary = str(Path(sys.argv[1] if len(sys.argv) > 1 else './richt').resolve())
work = Path(sys.argv[2] if len(sys.argv) > 2 else '/root/richt-service-test').resolve()
work.mkdir(parents=True, exist_ok=True)
checks = []
secret_markers = []

def run(*args, ok=True):
    p = subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if any(marker in p.stdout for marker in secret_markers):
        raise AssertionError('secret marker escaped through command output')
    if ok and p.returncode:
        raise AssertionError(f'{args}: exit {p.returncode}\n{p.stdout}')
    return p

def check(name, value):
    assert value, name
    checks.append(name)
    print(f'PASS {name}', flush=True)

def write(name, inventory):
    path = work / (name + '.json')
    path.write_text(json.dumps(inventory, indent=2))
    return path

def diff(name, inventory):
    source = write(name + '-inventory', inventory)
    target = work / (name + '-plan.json')
    p = run(binary, 'diff', '-i', str(source), '-o', str(target))
    (work / (name + '.diff')).write_text(p.stdout)
    return target, json.loads(target.read_text())['hosts'][0]['tasks']

def apply(name, path, ok=True):
    p = run(binary, 'apply', '-y', str(path), ok=ok)
    (work / (name + '.apply')).write_text(p.stdout)
    # The fixture handles HUP after its short sleep; systemd has already completed.
    time.sleep(0.2)
    return p

def events():
    p = Path('/run/richt-demo/events')
    return p.read_text().splitlines() if p.exists() else []

def active(unit='richt-demo.service'):
    return run('systemctl', 'show', '-p', 'ActiveState', '--value', unit).stdout.strip()

def installed():
    return run('rpm', '-q', '--qf', '%{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH}', 'richt-demo').stdout

def own_unit(name, content, state='running', **extra):
    return dict(type='systemd', unit=name, state=state, unit_file=dict(template=dict(inline=content)), **extra)

assert os.geteuid() == 0, 'run as root on a disposable host'
for unit in ['richt-demo.service', 'richt-first.service', 'richt-second.service', 'richt-timeout.service', 'richt-error.service']:
    run('systemctl', 'stop', unit, ok=False)
    run('systemctl', 'disable', unit, ok=False)
run('dnf', '-y', 'remove', 'richt-demo', ok=False)
for path in ['/etc/yum.repos.d/richt-demo.repo', '/etc/richt-demo.conf', '/etc/systemd/system/richt-demo.service.d/20-reload.conf', '/run/richt-demo/events', '/run/richt-order-first', '/run/richt-order-second']:
    Path(path).unlink(missing_ok=True)
for unit in ['richt-first.service', 'richt-second.service', 'richt-timeout.service', 'richt-error.service']:
    Path('/etc/systemd/system/' + unit).unlink(missing_ok=True)
run('systemctl', 'daemon-reload')
run('sh', str(Path(__file__).with_name('build-repository.sh')))
reload_content = '[Service]\nExecReload=\nExecReload=/bin/kill -HUP $MAINPID\n'
inventory = dict(tags=dict(example=dict(services=dict(repository={},demo={}))),services=dict(
    repository=dict(order=10,configs=dict(repo=dict(path='/etc/yum.repos.d/richt-demo.repo', mode='0644', template=dict(inline='[richt-demo]\nname=Richt demo\nbaseurl=file:///var/lib/richt-demo-fixture/repo\nenabled=1\ngpgcheck=0\n')))),
    demo=dict(order=20,binaries=dict(app=dict(type='rpm', manager='dnf', package='richt-demo', version='0:1.0-1.noarch')),
              configs=dict(main=dict(path='/etc/richt-demo.conf', mode='0644', template=dict(inline='message=one\n'))),
              processes=dict(main=dict(type='systemd', unit='richt-demo.service', enabled=True, state='running', timeout='5s', restart_on=['binary.app'], reload_on=['config.main'], overrides={'20-reload.conf': dict(template=dict(inline=reload_content))})))),
    hosts=[dict(name='isolated', tags=['example'], transport=dict(type='local'))])
path, tasks = diff('first', inventory)
check('diff does not install repository or package', not Path('/etc/yum.repos.d/richt-demo.repo').exists() and run('rpm', '-q', 'richt-demo', ok=False).returncode != 0)
check('repository task precedes package task', tasks[0]['service'] == 'repository' and next(i for i,t in enumerate(tasks) if t['operation']=='rpm') > 0)
apply('first', path)
check('first exact installation active and enabled', installed()=='0:1.0-1.noarch' and active()=='active' and run('systemctl','is-enabled','richt-demo.service').stdout.strip()=='enabled')
check('start has no extra restart or reload', events()==['start 1.0'])
_, tasks = diff('noop', inventory)
check('repeated diff is empty', tasks==[])
unversioned = copy.deepcopy(inventory)
del unversioned['services']['demo']['binaries']['app']['version']
_, tasks = diff('unversioned', unversioned)
check('unversioned installed package is not upgraded despite newer repo RPM', tasks==[] and installed()=='0:1.0-1.noarch')
inventory['services']['demo']['configs']['main']['template']['inline']='message=two\n'
path,tasks=diff('config',inventory)
check('config schedules exactly one reload', [t['process']['reaction'] for t in tasks if t['operation']=='systemd']==['reload'])
apply('config',path)
check('config executes one reload without restart', events()==['start 1.0','reload'])
for label,version in [('upgrade','2.0'),('downgrade','1.0')]:
    inventory['services']['demo']['binaries']['app']['version']=f'0:{version}-1.noarch'
    path,tasks=diff(label,inventory)
    apply(label,path)
    check(label+' ensures exact NEVRA', installed()==f'0:{version}-1.noarch' and events()[-1]==f'start {version}')
inventory['services']['demo']['binaries']['app']['version']='0:2.0-1.noarch'
inventory['services']['demo']['configs']['main']['template']['inline']='message=merged\n'
path,tasks=diff('merged',inventory)
check('restart wins over reload', [t['process']['reaction'] for t in tasks if t['operation']=='systemd']==['restart'])
before=events();apply('merged',path)
check('merged changes execute only one restart', events()==before+['start 2.0'])
run('systemctl','stop','richt-demo.service')
inventory['services']['demo']['configs']['main']['template']['inline']='message=start\n'
path,_=diff('stopped-start',inventory);before=events();apply('stopped-start',path)
check('stopped process with changed inputs only starts', events()==before+['start 2.0'])
inventory['services']['demo']['processes']['main']['enabled']=False
path,tasks=diff('disable-running',inventory);before=events();apply('disable-running',path)
check('enabled is independent of running', [t['operation'] for t in tasks]==['systemd-enable'] and active()=='active' and events()==before)
inventory['services']['demo']['processes']['main']['state']='stopped'
inventory['services']['demo']['processes']['main']['enabled']=True
path,_=diff('stopped',inventory);apply('stopped',path)
check('stopped target can remain enabled', active()=='inactive' and run('systemctl','is-enabled','richt-demo.service').stdout.strip()=='enabled')
inventory['services']['demo']['processes']['main']['state']='running'
path,_=diff('running-again',inventory);apply('running-again',path)
# Real secret rotation still schedules and executes a process reaction. Values
# are synthetic runtime canaries; neither the test report nor CLI output prints them.
secret_markers.extend('RICHT_TEST_'+uuid.uuid4().hex for _ in range(2))
secret_inventory=copy.deepcopy(inventory)
secret_source=work/'secret-source.yaml'
secret_inventory['secret_sources']={'default':dict(type='yaml',file=secret_source.name)}
secret_inventory['services']['demo']['configs']['main'].update(mode='0600',template=dict(inline='message={{secret "token"}}\n'))
for index,value in enumerate(secret_markers):
    with os.fdopen(os.open(secret_source,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600),'w') as stream:
        os.fchmod(stream.fileno(),0o600)
        json.dump(dict(token=value),stream)
    label='secret-initial' if index==0 else 'secret-rotation'
    path,tasks=diff(label,secret_inventory)
    check(label+' hides content and schedules one reload',
          [task['process']['reaction'] for task in tasks if task['operation']=='systemd']==['reload'] and
          all(task['file'].get('sensitive') for task in tasks if task['operation']=='file') and
          'Content hidden' in (work/(label+'.diff')).read_text())
    if index==1: secret_source.unlink()
    before=events();apply(label,path)
    check(label+' applies frozen bytes and executes the reload',
          Path('/etc/richt-demo.conf').read_text()=='message='+value+'\n' and events()==before+['reload'])
# The following template removes secret() but must still hide the previous value.
inventory['services']['demo']['configs']['main']['sensitive']=True
# Capability exists, but this reload command deliberately fails. Match desired and
# current drop-in before diff so only config and reload tasks are prepared.
failing='[Service]\nExecReload=\nExecReload=/bin/false\n'
Path('/etc/systemd/system/richt-demo.service.d/20-reload.conf').write_text(failing)
run('systemctl','daemon-reload')
inventory['services']['demo']['processes']['main']['overrides']['20-reload.conf']['template']['inline']=failing
inventory['services']['demo']['configs']['main']['template']['inline']='message=resume\n'
path,tasks=diff('resume',inventory)
p=apply('resume-failure',path,ok=False)
progress=json.loads(path.read_text())['hosts'][0]['tasks']
check('failed command persists successful config and failed process', p.returncode!=0 and [t['progress']['status'] for t in progress]==['success','error'])
Path('/etc/richt-demo.conf').write_text('changed after failed apply\n')
Path('/etc/systemd/system/richt-demo.service.d/20-reload.conf').write_text(reload_content)
run('systemctl','daemon-reload');before=events();apply('resume-success',path)
check('resume skips config and preserves pending reload', Path('/etc/richt-demo.conf').read_text()=='changed after failed apply\n' and events()==before+['reload'])
inventory['services']['demo']['processes']['main']['overrides']['20-reload.conf']['template']['inline']=reload_content
# Multiple processes use after; the second start fails if the first has not finished.
unit1='[Service]\nType=simple\nExecStart=/usr/bin/sleep infinity\nExecStartPost=/usr/bin/touch /run/richt-order-first\n'
unit2='[Service]\nType=simple\nExecStartPre=/usr/bin/test -e /run/richt-order-first\nExecStart=/usr/bin/sleep infinity\nExecStartPost=/usr/bin/touch /run/richt-order-second\n'
multiple=dict(tags=dict(example=dict(services=dict(order={}))),services=dict(order=dict(order=10,processes={'z-first':own_unit('richt-first.service',unit1),'a-second':own_unit('richt-second.service',unit2,after=['z-first'])})),hosts=[dict(name='isolated',tags=['example'],transport=dict(type='local'))])
path,tasks=diff('after',multiple)
check('after is reflected in process task order',[t['component'] for t in tasks if t['operation']=='systemd']==['process/z-first','process/a-second'])
apply('after',path)
check('multiple processes start in dependency order',active('richt-first.service')=='active' and active('richt-second.service')=='active' and Path('/run/richt-order-second').exists())
for label,command,timeout in [('timeout','/usr/bin/sleep 2','100ms'),('error','/bin/false','5s')]:
    unit=f'richt-{label}.service'
    bad=dict(tags=dict(example=dict(services=dict(bad={}))),services=dict(bad=dict(order=10,processes=dict(main=own_unit(unit,f'[Service]\nType=oneshot\nExecStart={command}\nRemainAfterExit=yes\n',timeout=timeout)))),hosts=[dict(name='isolated',tags=['example'],transport=dict(type='local'))])
    path,_=diff(label,bad);start=time.monotonic();p=apply(label,path,ok=False);elapsed=time.monotonic()-start
    check(label+' stops host and records error',p.returncode!=0 and json.loads(path.read_text())['hosts'][0]['tasks'][-1]['progress']['status']=='error')
    if label=='timeout':check('timeout bounds command and waiting together',elapsed<1.5)
    run('systemctl','stop',unit,ok=False)
# Unsupported reload fails preparation even when the triggering file is unchanged.
unsupported=copy.deepcopy(multiple)
unsupported['services']['order']['configs']=dict(main=dict(path='/etc/richt-demo.conf',template=dict(inline='x')))
unsupported['services']['order']['processes']['z-first']['reload_on']=['config.main']
p=run(binary,'diff','-i',str(write('unsupported',unsupported)),ok=False)
check('unsupported reload is rejected in preparation',p.returncode!=0 and 'reload is unsupported' in p.stdout)
# Full removal stops and disables before removing owned files and package.
inventory['tags']['example']['services']['demo']['state']='absent'
path,tasks=diff('absent',inventory)
ops=[t['operation'] for t in tasks]
check('absent orders process before files and package',ops.index('systemd')<ops.index('file')<ops.index('rpm') and ops.index('systemd-enable')<ops.index('file'))
apply('absent',path)
check('absent removes declared package config and drop-in',run('rpm','-q','richt-demo',ok=False).returncode!=0 and not Path('/etc/richt-demo.conf').exists() and not Path('/etc/systemd/system/richt-demo.service.d/20-reload.conf').exists())
check('application state is retained',Path('/run/richt-demo/events').exists())
# YUM 4 is DNF's compatibility entry point, exercised with the same exact request.
inventory['tags']['example']['services']['demo']['state']='present'
inventory['services']['demo']['binaries']['app']['manager']='yum'
inventory['services']['demo']['binaries']['app']['version']='0:1.0-1.noarch'
path,_=diff('yum-install',inventory);apply('yum-install',path)
check('YUM 4 exact package installation works',installed()=='0:1.0-1.noarch' and active()=='active')
inventory['tags']['example']['services']['demo']['state']='absent'
path,_=diff('yum-remove',inventory);apply('yum-remove',path)
check('YUM 4 package removal works',run('rpm','-q','richt-demo',ok=False).returncode!=0)
# Clean owned extra units through the same absent mechanism.
multiple['tags']['example']['services']['order']['state']='absent'
path,_=diff('after-absent',multiple);apply('after-absent',path)
check('owned full units are removed',not Path('/etc/systemd/system/richt-first.service').exists() and not Path('/etc/systemd/system/richt-second.service').exists())
(work/'acceptance.json').write_text(json.dumps(dict(checks=checks,passed=len(checks)),indent=2)+'\n')
print(f'{len(checks)} acceptance checks passed')
