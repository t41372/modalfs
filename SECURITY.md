# Security policy

## Reporting a vulnerability

Please **don't** open a public issue for security problems. Report them privately through GitHub's [private vulnerability reporting](https://github.com/t41372/modalfs/security/advisories/new) instead.

Include what you found, how to reproduce it, and which version or commit you tested. You should get a reply within a week.

## Scope

modalfs handles Modal API tokens and serves files on your machine, so these are in scope:

- token secrets leaking (logs, files with loose permissions, the browser UI)
- the daemon's Unix socket or loopback HTTP port being reachable by other users, other local web pages, or DNS rebinding
- a mount reading or writing a different volume or account than the one it was pinned to
- data loss or corruption caused by modalfs

Bugs in Modal itself should go to Modal.

## Supported versions

Only the latest release gets fixes.
