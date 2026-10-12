# modalfs

modalfs mounts [Modal Volumes](https://modal.com/docs/guide/volumes) as local folders on macOS (over the built-in NFS client) and Linux (FUSE), without starting any Modal compute. One background daemon owns the mounts; the CLI, the desktop app and the browser UI are all clients of its JSON API. `README.md` explains how it works.

This file is for you, the agent working on modalfs. It's a small project: keep it that way.

## Principles

1. **Never lose the user's writes.** A volume holds data people care about. Every write is journaled until Modal has it, survives a crash, and is never silently dropped or overwritten: a conflict keeps both versions unless the user chose otherwise. When modalfs can't do something safely, it fails loudly and says why.
2. **Compute only when asked.** Mounting, browsing and syncing only call Modal's control-plane API and signed block URLs, and never start a Sandbox, Function or container. The one exception is remote downloads: off until the user turns them on, and then a Function (or the short-lived Sandbox that deploys it) starts only for a download the user adds or resumes. It stops when the download ends, pauses or stalls, and is listed where the user can see and stop it.
3. **A mount never silently changes what it points at.** Each mount is pinned to an account, environment and volume ID. Switching Modal CLI profiles, renaming or recreating a volume must never move a mount somewhere else.
4. **Explicit over implicit.** No swallowed errors, no fake success, no silent fallback. Show what's happening (pending uploads, conflicts, errors) where the user will see it. A message says what went wrong and what to do about it.
5. **The app and the CLI are both complete.** Everything goes through the daemon's API, so anything one can do the other can too. The app's window is only a remote control: closing it never unmounts anything.
6. **Easy beats powerful.** Good defaults first. The user shouldn't need to know NFS, FUSE or block sizes to mount a folder.
7. **Small and maintainable.** Prefer the standard library and the dependencies we already have. A bigger design has to justify itself against the smaller one that would also work. Write code and text for the person who maintains this for years: plain words, no AI slop.

## Trust model

modalfs runs on one person's machine for that person. The owner, their files and their Modal tokens are trusted. Defend against real risks only:

- **Token secrets** stay in the keychain (or a `0600` file) and never reach logs, errors or the UI.
- **Other local processes and web pages** can reach the daemon's localhost port, so every request there needs the session token and a loopback `Host` header.
- **Data loss**, from crashes, races or Modal's lack of compare-and-swap (principle 1).

Don't add defenses that only protect the owner from themselves.

## How we work

- **Reason from first principles.** Start from what the user needs and what Modal's API actually guarantees, not from how other sync tools do it. Precedent is evidence, not an argument.
- **Ask or act.** Make routine calls yourself and state the assumption. Ask when different readings lead to materially different work.
- **Precedence.** The user's request in the conversation overrides this file. If it conflicts with a principle, say so, then follow the user.
- **The design follows Jezo.** The UI in `web/` uses the tokens, layout and components of [Jezo](https://github.com/t41372/Jezo). Match them rather than inventing new styles.

## Testing

- **Test the real thing.** `just test-e2e` mounts modalfs for real (a separate process, race detector on) against the fake Modal in `internal/modalfake` with a slow network, and checks the mount from outside; add a test there for any change to how files are read, written or committed. The fake is no proof of Modal's own behavior: `just test-e2e-live <profile>` runs the same tests against real Modal, each in a scratch volume it creates and deletes, and the integration tests run against a scratch volume you name. The UI's mock API (`just dev-mock`) is for building the UI and for the README's pictures.
- **Never do file I/O on a mount from the process that serves it.** The kernel and the Go runtime can wait on each other forever; tests run modalfs as its own process. Sync files with `fsync.File`, never `os.File.Sync`: on macOS a slow sync with the latter stops every goroutine in the process at the next GC, which deadlocks a test whose fake Modal serves the mount it's syncing, and stalls the whole mount in modalfs.
- **Look at UI changes.** Run the UI, click through the change in both themes and at a narrow width, and check the console for errors.
- **Never touch the user's real setup while testing.** Run a daemon with `MODALFS_HOME=$(mktemp -d)` and `MODALFS_NO_KEYCHAIN=1`. Keep that path short: macOS limits Unix socket paths to 104 characters. Don't mount anything you didn't create.
- Before calling a change done, run `just lint` and `just test`. If you touched the app, also run `just app`.

## Conventions

- Tasks live in the `justfile`. There's no Makefile.
- The version is `Version` in `internal/daemon/daemon.go`. A release is a `v<Version>` tag; `.github/workflows/release.yml` refuses a tag that doesn't match.
- English for code, comments, logs and commit messages. Commit messages are imperative, one logical change per commit. Don't hard-wrap Markdown.
- UI strings live in `web/src/i18n/` in English, Traditional Chinese and Simplified Chinese; add every new string to all three. `README.zh-CN.md` mirrors `README.md`: change both together, and rerun `cd web && bun run screenshots` when a pictured page changes.
