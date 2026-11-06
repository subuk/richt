# systemd socket example

Run on an authorized disposable Linux host with systemd and root privileges.
This example owns `richt-socket-example.socket`, its access drop-in, and the
`richt-socket-example@.service` activation template. Check that these names and
`/run/richt-socket-example.sock` are unused before applying it.

From the repository root, using a Linux build of Richt:

```sh
bin/richt inventory validate -i examples/systemd-socket/inventory.yaml
bin/richt diff -i examples/systemd-socket/inventory.yaml -o /tmp/richt-socket.json
bin/richt apply -y /tmp/richt-socket.json
systemctl show -p ActiveState -p SubState -p UnitFileState richt-socket-example.socket
bin/richt diff -i examples/systemd-socket/inventory.yaml
```

The socket should be active/listening and enabled, and the fresh diff empty.
`state: running` describes the socket's active state, not an activated service
or application readiness. The service template is an ordinary managed config;
systemd starts a separate instance for each connection because `Accept=yes`.
Richt does not create process tasks for those instances.

As root, exercise the local echo socket:

```sh
python3 - <<'PY'
import socket
with socket.socket(socket.AF_UNIX) as client:
    client.settimeout(5)
    client.connect('/run/richt-socket-example.sock')
    client.sendall(b'hello\n')
    data = b''
    while not data.endswith(b'\n'):
        chunk = client.recv(1024)
        if not chunk:
            raise RuntimeError('socket closed before reply')
        data += chunk
    assert data == b'hello\n', data
PY
```

Changing the socket definition or drop-in schedules daemon reload and one
socket restart (or just start if stopped). Socket units cannot use `reload_on`.
Stopping/masking a socket does not guarantee that activated services stop;
manage those explicitly when needed. With `Accept=no`, starting a socket while
its service is active can fail; explicitly stop the service first when required.

For cleanup, change the `listener` tag assignment to `{state: absent}`, prepare
and apply a new plan. This stops/disables the socket and removes its owned
definition, drop-in and activation template. Close all test clients first; any
remaining activated instances need explicit stopping. Remove the saved plan
and its `.lock` after reviewing the result.

The existing [systemd acceptance harness](../systemd-mask/README.md) exercises
socket lifecycle, masks, configuration reactions and continuation with unique
names over local execution or SSH/sudo. This isolated echo example is not a
libvirt installer; validate the selected libvirt daemon layout on its target
distribution separately.
