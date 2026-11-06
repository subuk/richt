---
title: "Binaries"
description: "Declare the installed software a service needs."
weight: 30
---

Binaries describe software installation requests in an ordered `binaries` list.
An optional `name` label allows [process reactions](../reactions.md) such as
`binary.package` to reference an item. A binary-only service is valid; binaries
do not require a config or process component.

Choose [RPM packages](rpm.md) (`type: rpm`) for DNF 4/YUM 4 package requests,
[DEB packages](deb.md) (`type: deb`) for APT package requests,
[downloaded files](download.md) (`type: download`) for a checksum-pinned HTTPS
file, or [archive trees](archive.md) (`type: archive`, `format: tar.gz`) for an
exclusively owned installation directory. Each reference defines preparation,
apply, ownership, limits and removal. Download and archive requests fetch their
frozen URL at apply; artifact bytes are not embedded in saved plans.

Binaries run in declaration order before configs and processes in a present
service, and in reverse declaration order after them during removal. See
[lifecycle and ordering](../lifecycle.md).
