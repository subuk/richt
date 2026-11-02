---
title: "Binaries"
description: "Declare the installed software a service needs."
weight: 30
---

Binaries describe software installation requests. Each component has a stable
name under `binaries`, used by [process reactions](../reactions.md) such as
`binary.package`. A binary-only service is valid; binaries do not require a
config or process component.

The current implementation is [RPM packages](rpm.md), selected with `type: rpm`
and managed through DNF 4 or its YUM 4 compatibility command. Its reference owns
package fields, exact versions, preparation, apply, and removal behavior.

Binaries run before configs and processes in a present service and after them
during removal. See [lifecycle and ordering](../lifecycle.md).
