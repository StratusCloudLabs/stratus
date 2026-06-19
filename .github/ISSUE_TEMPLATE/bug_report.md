---
name: Bug report
about: Report a problem with stratus-runtime so we can fix it
title: "[Bug]: "
labels: [bug]
assignees: []
---

<!--
Thanks for reporting a bug! Please do NOT use this template for security
vulnerabilities — follow SECURITY.md and report privately instead.
-->

## Description

A clear and concise description of what the bug is.

## Affected component

Which part of stratus-runtime is involved?

- [ ] JIT runner controller
- [ ] Kata sandbox controller
- [ ] HMAC webhook proxy
- [ ] Job reaper / metrics
- [ ] HTTP API
- [ ] Build / configuration
- [ ] Other / not sure

## Steps to reproduce

1. ...
2. ...
3. ...

## Expected behavior

What you expected to happen.

## Actual behavior

What actually happened. Include relevant logs, metrics, or error output
(redact any secrets).

## Environment

- stratus-runtime version / commit:
- Kubernetes version / distribution:
- Storage backend (e.g. Firestore):
- Go version (if building from source):
- OS / arch:

## Additional context

Add any other context, configuration (`.env` with secrets redacted), or
screenshots about the problem here.
