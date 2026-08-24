---
title: CLI reference
linkTitle: CLI
weight: 20
description: Every leoflow command and flag, generated from Cobra.
cascade: { type: docs }
---

Every `leoflow` command and flag. This reference is **generated from Cobra** on
every push, so it never drifts from the binary.

{{% alert title="Generated content — wiring pending" color="info" %}}
The per-command pages (`leoflow`, `leoflow lite`, `leoflow init`, …) are produced by
the docs generator and dropped into this directory at build time. The generation
step is being ported from the MkDocs pipeline (`cobra doc` → `content/reference/cli/`)
as part of the Hugo migration; until it is wired into CI these pages are not yet
present here.
{{% /alert %}}

Until then, run `leoflow --help` (and `leoflow <command> --help`) for the
authoritative command surface.
