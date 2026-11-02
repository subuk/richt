---
title: "Binaries"
description: "Declare the installed software a service needs."
weight: 30
---

Binaries describe software installation requests in an ordered `binaries` list.
An optional `name` label allows [process reactions](../reactions.md) such as
`binary.package` to reference an item. A binary-only service is valid; binaries
do not require a config or process component.

The current implementation is [RPM packages](rpm.md), selected with `type: rpm`
and managed through DNF 4 or its YUM 4 compatibility command. Its reference owns
package fields, exact versions, preparation, apply, and removal behavior.

Binaries run in declaration order before configs and processes in a present
service, and in reverse declaration order after them during removal. See
[lifecycle and ordering](../lifecycle.md).
