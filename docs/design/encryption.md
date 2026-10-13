# End-to-end encrypted volumes

Status: design, decided with the owner on 2026-10-12. Not built yet.

## What it is

A volume can be created encrypted. Its files and their names are encrypted on this computer before they leave it and decrypted after they arrive, so Modal, and anyone who can read the workspace, sees only ciphertext. On the computer the mount is an ordinary folder of plain files, exactly as today.

The computer and its owner are trusted (see AGENTS.md, Trust model). Modal and everything on Modal's side are not, for the contents and names of files in an encrypted volume.

## The rule

**Key + encrypted data = data. Always.** Anyone who has the volume's key and its encrypted files can decrypt every file and name, with nothing else: no passphrase, no file modalfs keeps on the volume or on a computer, no modalfs account, not even modalfs itself (the format is fully specified here, so another program can decrypt it).

The converse is just as absolute: **without the key, the files are gone.** modalfs never sends the key anywhere and keeps no copy outside the places the user chooses. There is no "forgot my key", for anyone, including Modal and modalfs's authors.

## Decisions

1. **One key, shown to the user as it is.** Each encrypted volume has a random 256-bit key. The user sees that key (as text) when the volume is created and saves it, for example in a password manager. There's no passphrase and no recovery key; the key is both.
2. **Remember on this computer**, optionally, in the OS's secret store (macOS Keychain; Linux Secret Service; otherwise `secrets.json`, mode 0600, like token secrets in `internal/config/secrets.go`). Otherwise the key lives only in the daemon's memory until the volume is locked or the daemon stops.
3. **File and folder names are encrypted too**, not only contents.
4. **Remote downloads can't write into an encrypted volume.** The container would need the key. The daemon, CLI and UI refuse, and say why. (Write-only downloads with a public key are possible later; see "Later".)
5. **Only new, empty volumes can be encrypted.** Converting a volume means uploading everything again; not in the first version.
6. **Modal Functions can't read an encrypted volume's files.** This is the point, and the UI says so.
7. **Labels** (`.modalfs/volume.json`) are encrypted like any file. A computer without the key shows the Modal name and a lock.

## What Modal still sees

- The number of files and folders, and the shape of the tree (which encrypted names are in which folder).
- Each file's size, to within the per-block overhead, and its modification time.
- Which 8 MiB blocks have identical contents and which names are equal (both are encrypted deterministically; this keeps deduplication, cheap copies, renames that move no data, and block-level uploads).
- When files change, and how much.

It does not see contents, names, the label, or the key.

## The key as text

`mfs1-` followed by the 32 key bytes and a short checksum in Crockford base32, grouped for reading (for example `mfs1-7K3Q-…`). The prefix names the format version; the checksum catches a mistyped or truncated key before anything is tried with it. Pasting tolerates spaces, dashes and case.

## Format (version 1)

Everything below is determined by the key and the data alone. Constants (block size, cipher, KDF labels) are fixed by the format version in the key's prefix.

- **Subkeys** come from the volume key with HKDF-SHA256 and fixed labels, one per purpose (contents, names, and whatever else needs one).
- **Contents:** Volume v2 stores files as fixed 8 MiB blocks addressed by SHA-256 (`modalapi.BlockSize`). An encrypted file keeps that shape: a sequence of ciphertext blocks of exactly 8 MiB (the last one shorter), each holding one plaintext chunk of 8 MiB minus the per-block overhead, with the nonce stored in the block and derived deterministically from the contents subkey and the chunk's plaintext (an SIV-style construction). Rewriting one chunk re-encrypts one block; renames and copies move no data. The plaintext size follows from the ciphertext size.
- **Room for per-file keys.** The format must let a later version add files whose content key isn't derived directly from the volume key (write-only remote downloads sealed to a public key derived from the volume key), without migrating existing files and without breaking the rule above: such a file must still decrypt with the volume key and its own bytes alone. How (a small per-file header, or something better) is a design choice to make and justify.
- **Names:** each path component is encrypted deterministically with the names subkey, with its parent folder's plaintext path as associated data (as Cryptomator binds names to their directory), and encoded for Modal's path rules. Equal names in different folders don't look equal on Modal. Moving or renaming a folder re-encrypts the names of everything under it; that costs only computation, since a folder move on Modal already rewrites every path under it. Verify Modal's limits on component and full path length; a name too long to encrypt is refused with a message that says so, never truncated or stored in plaintext.
- **Integrity:** the AEAD tag authenticates every block and name. Anything that fails to decrypt is an error the user sees, never zeros, stale data or a skipped file.
- **Nothing on the volume besides the encrypted files is needed.** A user who doesn't know what `.modalfs/` is will one day delete it; that must never cost them their files.
- **`.modalfs/encryption.json`** is an optional plaintext marker: the format version and a key fingerprint (so modalfs can tell a volume is encrypted and say "this isn't the right key" before trying files). It is never needed to decrypt anything. If it's missing, modalfs still recognises the volume from its encrypted names, still unlocks it with the key, and writes the marker back.

Cipher choice: AES-256-GCM with a synthetic (HMAC-derived) nonce, or another construction with the same properties; justify it. Measured on an Apple M5 Max in Go: AES-256-GCM about 7.8 GB/s, HMAC-SHA256 about 2.9 GB/s, SHA-256 (already computed per block today) about 2.7 GB/s, against 6–11 MiB/s to Modal. Encryption costs a fraction of a percent of CPU and a few dozen bytes per 8 MiB.

## Decrypting without a mount

`modalfs volume decrypt <volume> --to <folder>` copies an encrypted volume's files to a local folder, decrypted, using only the key and the volume (no mount, no marker, no remembered state). It's the guarantee in executable form, and the E2E tests use it. The format section above is complete enough to write the same thing in another language.

## On this computer

- Everything local stays as it is: the cache, the write journal, conflict copies and the mount hold plaintext. Encryption happens where blocks and names leave for Modal; decryption where they arrive.
- A volume whose key this computer doesn't have is **locked**: it's listed, can't be mounted, and offers Unlock.

## Actions

- Create an encrypted volume: the key is shown, the user saves it (Copy; Save as file), and confirms they've saved it before the volume is created.
- Unlock with the key; optionally remember it on this computer. Lock: forget the key on this computer, after unmounting.
- Show the key, on a computer that remembers it.
- Decrypt to a folder (CLI).
- Remote downloads refuse encrypted volumes everywhere, with the reason.

There is no "change the key": a new key means re-encrypting every file. If a key may have leaked: first revoke the workspace's Modal API tokens, so nobody else can read the encrypted data (the key alone is useless without it; data already copied away stays readable with the old key, and nothing can change that); then create a new encrypted volume and copy the files over on this computer. A one-step re-encryption on this computer can come later, together with converting existing volumes; never on Modal, which would hand Modal the new key.

No auto-lock on sleep or logout: it would unmount volumes in use, and the computer is trusted.

## API and CLI (sketch)

- `POST /api/encryption/key` returns a fresh key without creating anything, so the UI can show it, have the user save it and confirm, before the volume exists. `POST /api/volumes` gains `encrypted: true` and that `key`.
- `POST /api/volumes/{id}/unlock` `{key, remember}`; `POST /api/volumes/{id}/lock`; `GET /api/volumes/{id}/key` (only where it's remembered).
- Volume and mount views say `encrypted` and `locked`.
- CLI: `modalfs volume create --encrypt`, `modalfs unlock <volume>` (key read from the terminal without echo, or from stdin for scripts), `modalfs lock <volume>`, `modalfs volume key <volume>`, `modalfs volume decrypt <volume> --to <folder>`.

## UI (built separately)

- Create volume: an "Encrypt" option with its consequences in one line each (Modal can't read it, Functions can't read it, remote downloads can't write to it, losing the key means losing the files). Then the key, shown once in a selectable mono block with Copy and Save as file, a "Remember on this computer" choice (on by default), and a checkbox "I've saved the key; without it my files can't be recovered" before Create. The desktop app's WebView can't download files, so the app gets a native Save dialog for this.
- A lock icon on encrypted volumes everywhere; hover lists what's hidden and what Modal still sees.
- Locked volumes: "Locked" state with Unlock (paste the key).
- The volume's "⋯" menu: Show key (where remembered), Lock on this computer.
- Remote downloads: encrypted volumes are disabled in the destination picker, with the reason.

## Later

- **Write-only remote downloads into encrypted volumes.** An X25519 key pair is derived from the volume key; the public key goes to the download container, which seals each file to it with a fresh file key. Only the volume key can read such files. The container sees the file while downloading it (it's the one fetching it), but nothing stored on Modal can be read with the public key.
- Converting an existing volume.
- A helper for Functions that are given the key on purpose.
