# Contributing

Thanks for helping. Bug reports, testing on real Macs and pull requests are all welcome.

## Setup

You need:

- Go (the version in `go.mod` or newer)
- [Bun](https://bun.sh), for the UI in `web/`
- [just](https://github.com/casey/just) (`brew install just`, or see its README for other platforms)
- To build the desktop app: Xcode Command Line Tools on macOS (`xcode-select --install`), or `libgtk-3-dev` and `libwebkit2gtk-4.1-dev` on Linux

Run `just` to list the tasks:

```sh
just build              # CLI + daemon for this machine → dist/modalfs
just cli                # cross-compile the CLI for macOS and Linux
just app                # dist/modalfs.app (on a Mac)
just linux-gui          # Linux desktop app
just fmt                # gofmt
just lint               # gofmt check, go vet, the UI's typecheck and translation check
just test               # unit tests (offline: no Modal account needed)
```

Every build recipe builds the UI first (`just ui`), because the Go binaries embed it.

## Working on the UI

The UI in `web/` is React, TypeScript, Tailwind CSS v4 and shadcn/ui on Base UI. It's served by the daemon and shown by the desktop app; it talks to the daemon only through the JSON API in `internal/daemon/api.go`.

```sh
just dev-mock           # hot reload on sample data (web/src/api/mock.ts): no daemon, no Modal account
just dev                # hot reload against your running daemon (start one with `modalfs daemon`)
```

- **Strings** are in `web/src/i18n/{en,zh-Hant,zh-Hans}.json`. Add a new string to all three; `just lint` fails when a language is missing a key. If you can't write the Chinese, copy the English into both and say so in the PR.
- **New API fields** go in `web/src/api/types.ts`, and in the mock if a page shows them.
- **The README's pictures** come from `cd web && bun run screenshots`. Run it if you changed how a pictured page looks, and commit the JPEGs it writes.

## End-to-end tests

`just test-e2e` (macOS) mounts modalfs for real, over NFS, against a fake Modal (`internal/modalfake`) with a slow network, and checks from outside that the mount stays responsive and that what reaches "Modal" is exactly what was written. It needs no account. If you change how files are read, written or committed, add a test there. `just fake` runs the fake on its own, to try modalfs by hand.

## Integration tests

The integration tests talk to Modal and **create and delete files** in the volume you name. Use a scratch volume you don't care about:

```sh
modalfs volume create modalfs-scratch
MODALFS_TEST_VOLUME=modalfs-scratch just test-integration
```

They need a Modal token. They read it the way the Modal client does (`~/.modal.toml` or `MODAL_TOKEN_ID` / `MODAL_TOKEN_SECRET`).

## Pull requests

- Keep each PR to one change, and explain why it's needed.
- Run `just lint` and `just test`. If you touched `internal/vfs`, `internal/nfsfront`, `internal/fusefront` or `internal/modalapi`, please also run the integration tests against a scratch volume.
- If you changed behaviour on macOS, say which macOS version you tested on.
- UI text lives in `internal/webui/static/i18n.js`. Add new strings in both English and Traditional Chinese, or leave the Chinese for a maintainer.

## Reporting bugs

Please include your OS and version, the modalfs version (`modalfs version`), whether the mount uses NFS or FUSE, and the relevant part of the daemon log (`daemon.log` in the config folder listed in the README). Run with `MODALFS_DEBUG=1` for more detail. Check that the log contains no token secrets before you paste it.
