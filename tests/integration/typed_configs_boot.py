#!/usr/bin/env python3
"""Verify typed storage boot persistence on an authorized disposable SSH target.

This test reboots the target. It adds a temporary systemd service
which reattaches its two private loop backing files and emits device-add
events (loop attachment normally emits change, which MD udev rules skip).
Normal Linux MD/LVM discovery and the fstab entry written by Richt must restore
the stack.
The test fstab entry uses nofail so a fixture failure does not block the host
boot; the assertion still requires the real mount and its persisted data.
The controller must have SSH host trust and a non-root login with sudo -n.
"""
import argparse
import json
from pathlib import Path
import shlex
import subprocess
import time
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--host', required=True)
    parser.add_argument('--work-root', required=True, type=Path)
    args = parser.parse_args()
    work = args.work_root.resolve()
    work.mkdir(parents=True, exist_ok=True, mode=0o700)
    suffix = uuid.uuid4().hex[:10]
    root = '/var/tmp/richt-boot-' + suffix
    unit = 'richt-loop-' + suffix + '.service'
    array = '/dev/md/richtboot' + suffix
    vg = 'richtboot' + suffix
    source = '/dev/' + vg + '/data'
    target = root + '/mounted'
    aliases = ['/dev/richt-loop-' + suffix + '-' + str(i) for i in range(2)]
    checks = []
    seq = 0
    created = False
    destroyed = False
    failure = None
    cleanup_errors = []
    def remote(script, check=True, timeout=120):
        p = subprocess.run(['ssh','-T','-o','BatchMode=yes','-o','ConnectTimeout=5','--',args.host,
                            shlex.join(['sudo','-n','sh','-c',script])],capture_output=True,text=True,timeout=timeout)
        if check and p.returncode: raise RuntimeError(p.stderr or p.stdout)
        return p
    def shell(script): return remote(script).stdout.strip()
    def cli(*argv):
        nonlocal seq
        seq += 1
        p = subprocess.run([str(args.binary.resolve()), *map(str,argv)], capture_output=True,text=True,timeout=240)
        (work / ('cli-%03d.log'%seq)).write_text(p.stdout+p.stderr)
        if p.returncode: raise RuntimeError(p.stdout+p.stderr)
    def check(name, condition):
        if not condition: raise AssertionError(name)
        checks.append(name)
        print('PASS '+name,flush=True)
    def write(path, content):
        remote('umask 077; printf %s '+shlex.quote(content)+' > '+shlex.quote(path))
    transport={'type':'ssh','host':args.host,'sudo':True}
    if '@' in args.host: transport['user'],transport['host']=args.host.rsplit('@',1)
    configs=[{'type':'mdadm','array':array,'level':'raid1','metadata':'1.2','members':aliases},
             {'type':'lvm-vg','vg':vg,'devices':[array]},
             {'type':'lvm-lv','vg':vg,'lv':'data','size':'512MiB','filesystem':'xfs'},
             {'type':'mount','source':source,'target':target,'filesystem':'xfs','options':['defaults','noatime','nofail','x-systemd.device-timeout=90s']},
             {'path':target+'/marker','template':{'inline':'persistent-'+suffix+'\n'}}]
    def inventory(state):
        return {'tags':{'test':{'services':{'test':{'state':state}}}},
                'services':{'test':{'order':10,'configs':configs}},
                'hosts':[{'name':'test','tags':['test'],'transport':transport}]}
    def prepare(name, state):
        inv=work/(name+'.yaml'); plan=work/(name+'.json')
        inv.write_text(json.dumps(inventory(state),indent=2))
        cli('diff','-i',inv,'-o',plan)
        return plan
    try:
        facts=shell('cat /etc/os-release; uname -m; getenforce; systemctl --version; mdadm --version 2>&1; lvm version')
        (work/'platform.log').write_text(facts+'\n')
        oldboot=shell('cat /proc/sys/kernel/random/boot_id')
        (work/'boot-id-before.txt').write_text(oldboot+'\n')
        remote('test ! -e '+shlex.quote(root)+' && mkdir -m 0700 '+shlex.quote(root))
        created=True
        for i in range(2): remote('truncate -s 2G -- '+shlex.quote(root+'/disk-'+str(i)))
        # The backing files reside on the already available root filesystem.
        loop_script='#!/bin/sh\nset -eu\n'
        for i,alias in enumerate(aliases):
            loop_script+='device=$(losetup --find --show --nooverlap -- '+shlex.quote(root+'/disk-'+str(i))+')\n'
            loop_script+='ln -sfn -- "$device" '+shlex.quote(alias)+'\n'
        # MD assembly ignores change events for bare devices. Reannounce only
        # our attached loops as added devices; distro rules own MD/LVM assembly.
        for alias in aliases:
            loop_script+='device=$(readlink -f -- '+shlex.quote(alias)+')\n'
            loop_script+='udevadm trigger --action=add "/sys/class/block/${device##*/}"\n'
        loop_script+='udevadm settle --timeout=30\n'
        write(root+'/attach',loop_script)
        write('/etc/systemd/system/'+unit,
              '[Unit]\nDescription=Richt disposable loop boot fixture\n'
              'After=local-fs.target systemd-udevd.service\n'
              '[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/sh '+root+'/attach\n'
              '[Install]\nWantedBy=multi-user.target\n')
        remote('systemctl daemon-reload && systemctl enable --now '+shlex.quote(unit))
        loops=[shell('readlink -f -- '+shlex.quote(a)) for a in aliases]
        check('loop fixtures use distinct backing devices',len(set(loops))==2 and all(x.startswith('/dev/loop') for x in loops))
        initial=prepare('present','present'); cli('apply','-y',initial)
        fsuuid=shell('blkid -s UUID -o value -- '+shlex.quote(source))
        mduuid=shell('mdadm --detail --export '+shlex.quote(array))
        (work/'md-before.log').write_text(mduuid+'\n')
        check('initial filesystem is mounted with persistent marker',shell('cat -- '+shlex.quote(target+'/marker'))=='persistent-'+suffix)
        check('fstab uses prepared filesystem UUID',('UUID='+fsuuid) in shell('cat /etc/fstab'))
        remote('systemctl reboot',check=False)
        deadline=time.monotonic()+180
        newboot=''
        while time.monotonic()<deadline:
            time.sleep(3)
            try:
                p=remote('cat /proc/sys/kernel/random/boot_id',check=False,timeout=12)
            except subprocess.TimeoutExpired:
                continue
            if p.returncode==0 and p.stdout.strip()!=oldboot:
                newboot=p.stdout.strip(); break
        check('target completed a real reboot',bool(newboot))
        (work/'boot-id-after.txt').write_text(newboot+'\n')
        ready=False
        deadline=time.monotonic()+90
        while time.monotonic()<deadline:
            p=remote('mountpoint -q -- '+shlex.quote(target),check=False)
            if p.returncode==0: ready=True; break
            time.sleep(2)
        (work/'boot-state.log').write_text(shell('systemctl status '+shlex.quote(unit)+' --no-pager; cat /proc/mdstat; lsblk -f; findmnt --fstab; journalctl -b -u '+shlex.quote(unit)+' --no-pager'))
        check('normal MD/LVM discovery and fstab restore mount',ready)
        check('reboot preserves filesystem identity and data',shell('blkid -s UUID -o value -- '+shlex.quote(source))==fsuuid and shell('cat -- '+shlex.quote(target+'/marker'))=='persistent-'+suffix)
        noop=prepare('after-reboot','present')
        check('post-reboot stack has empty diff',json.loads(noop.read_text())['hosts'][0]['tasks']==[])
        remove=prepare('absent','absent'); cli('apply','-y',remove)
        check('post-reboot explicit absent destroys stack',remote('test ! -b '+shlex.quote(source)+' && ! mountpoint -q -- '+shlex.quote(target),check=False).returncode==0)
        check('VG, MD and fstab entry are absent',vg not in shell('vgs --noheadings --options vg_name').split()
              and remote('test ! -b '+shlex.quote(array),check=False).returncode==0
              and target not in shell('cat /etc/fstab'))
        check('member signatures are removed',all(not shell('wipefs --noheadings --output TYPE -- '+shlex.quote(alias)) for alias in aliases))
        destroyed=True
    except Exception as exc:
        failure=str(exc)
        raise
    finally:
        # Preserve failure evidence. If the stack is still mounted/in use, retain
        # its backing files for diagnosis rather than attempting forced cleanup.
        try:
            if destroyed:
                remote('systemctl disable --now '+shlex.quote(unit))
                remote('rm -f -- '+shlex.quote('/etc/systemd/system/'+unit)+'; systemctl daemon-reload')
                for alias in aliases:
                    remote('if test -L '+shlex.quote(alias)+'; then losetup -d "$(readlink -f -- '+shlex.quote(alias)+')" && rm -f -- '+shlex.quote(alias)+'; fi')
                # Never unlink a backing file while a loop still holds it.
                for i in range(2):
                    remote('attached=$(losetup -j '+shlex.quote(root+'/disk-'+str(i))+') && test -z "$attached"')
                remote('rm -rf -- '+shlex.quote(root))
        except Exception as exc:
            cleanup_errors.append(str(exc))
        finally:
            (work/'report.json').write_text(json.dumps({'checks':checks,'passed':len(checks),'fixture':root,'unit':unit,'retained':created and (not destroyed or bool(cleanup_errors)), 'failure':failure,'cleanup_errors':cleanup_errors},indent=2)+'\n')
        if cleanup_errors and failure is None:
            raise RuntimeError('fixture cleanup failed: '+'; '.join(cleanup_errors))
    print('Passed '+str(len(checks))+' reboot checks; evidence: '+str(work))

if __name__=='__main__': main()
