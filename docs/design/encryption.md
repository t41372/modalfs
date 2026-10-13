# End-to-end encrypted volumes

Status: built. `just test-e2e` (and `just test-e2e-live <profile> -run TestEncrypt`) write the verification report, scenario by scenario with the raw listing of what Modal stores, to `$MODALFS_E2E_ARTIFACTS` (default `$TMPDIR/modalfs-e2e`).

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
- Ciphertext sizes and modification times. Version 1 has fixed overhead, so plaintext file sizes and the byte lengths of name components can be inferred exactly; there is no padding to hide lengths.
- Which plaintext chunks have identical contents and which names are equal (both are encrypted deterministically; this keeps deduplication, cheap copies, renames that move no data, and block-level uploads).
- When files change, and how much.

It does not see file contents, file/folder names, the label, or the key. The Modal volume name itself remains visible; an encrypted label is separate from that deployment name.

## The key as text

`mfs1-` followed by the 32 key bytes and a short checksum in Crockford base32, grouped for reading (for example `mfs1-7K3Q-…`). The prefix names the format version; the checksum catches a mistyped or truncated key before anything is tried with it. Pasting tolerates spaces, dashes and case.

## Format (version 1)

This specification is sufficient to decrypt a raw volume tree without modalfs, its marker, its local state, or any account information. All sizes below are bytes. Names are byte strings (normally UTF-8). Paths have no leading or trailing `/`.

### Text key

Generate 32 cryptographically random bytes. Append the first four bytes of `SHA256(UTF8("modalfs/key/v1") || 0x00 || key)`. Encode these 36 bytes in unpadded Crockford base32, alphabet `0123456789ABCDEFGHJKMNPQRSTVWXYZ`, most significant bits first, then prefix `mfs1-`. Group the encoded text every four characters with dashes. The last group has two characters. The parser accepts case, whitespace and dashes in the payload, and Crockford's O→0 and I/L→1 aliases. It verifies the four-byte checksum and canonical zero padding bits. The checksum detects typing mistakes; it is not another key.

The fingerprint is the first 16 bytes of `SHA256(UTF8("modalfs/fingerprint/v1") || 0x00 || key)`, encoded as 32 lowercase hexadecimal characters. Neither this fingerprint nor a volume ID is an input to decryption.

### Keys and deterministic authenticated encryption

Use HKDF-SHA256 with the 32-byte volume key as input key material, empty salt, and the following exact UTF-8 info strings, each producing 32 bytes:

- `modalfs/v1/contents/key`
- `modalfs/v1/contents/nonce`
- `modalfs/v1/names/key`
- `modalfs/v1/names/nonce`

For either purpose, given plaintext `P` and associated data `A`, compute `N = HMAC-SHA256(nonce_key, uint64_be(len(A)) || A || P)[0:24]`. Encrypt with XChaCha20-Poly1305 using `key`, nonce `N` and associated data `A`. Its output `C` includes the 16-byte authentication tag. The stored envelope is `N || C`. On opening, verify both the AEAD tag and the recomputed synthetic nonce. Fail on any error; never return unauthenticated plaintext.

This uses the existing `golang.org/x/crypto/chacha20poly1305` implementation, rather than implementing a cipher. The [Go implementation](https://pkg.go.dev/golang.org/x/crypto/chacha20poly1305) provides the 24-byte nonce variant. A deterministic HMAC nonce with 192 bits avoids the much smaller birthday bound of a 96-bit GCM nonce. This is a synthetic-nonce construction, not standardized AES-GCM-SIV. It adds eight bytes per envelope compared with GCM. The independent HMAC key prevents plaintext-guessing attacks using an unkeyed content digest. It is slower than hardware AES-GCM, but preserves deterministic blocks using an existing dependency and ample nonce space.

### Contents

Each raw Modal block is `H || N || C`, where the eight-byte header `H` is hexadecimal `4d 46 53 31 00 00 00 00` (ASCII `MFS1` followed by four zero bytes). The entire header is the contents associated data. Byte 4 is the key mode (0 = key derived directly from the volume key); bytes 5–7 are reserved and must be zero. Version 1 refuses any other header. The envelope adds 48 bytes: 8 header, 24 nonce, 16 tag.

Split plaintext into chunks of 8,388,560 bytes (8 MiB minus 48). Encrypt each separately. Every raw block is exactly 8,388,608 bytes except the final block. Hash and upload the **actual ciphertext bytes**, including the header, nonce and tag, using SHA-256. The final block is not padded for hashing or upload. Modal's `ClientPadsBlocks` download option may omit trailing zero bytes; restore them to the expected ciphertext length before authenticating.

An empty file is one authenticated 48-byte block, not a zero-byte raw file. A zero-byte raw file in an encrypted namespace is an error. For plaintext size `P`, ciphertext size is `P + 48 * max(1, ceil(P / 8388560))`. For ciphertext size `S`, let `n = ceil(S / 8388608)`; plaintext size is `S - 48*n`. Reject `S < 48`, a final block shorter than 48, or a 48-byte final block after another block. Remote empty user files are authenticated when statted/read, even though there are no plaintext bytes to return. Internal keep markers are different: their authenticated names alone describe the folder, so their disposable contents are not required to mount or recover user data.

Blocks have no path, position, file ID or volume ID in their associated data. Identical chunks within a volume therefore deduplicate, edits upload only changed chunks, and file renames and copies retain their block manifests. **This authenticates each block, not the whole Modal manifest.** It detects damaged bytes and invalid names, but does not prove file order, freshness, absence of deletion, or protection against an actively malicious server replaying/reordering valid blocks or truncating a file by whole blocks. Modal has no authenticated manifest or compare-and-swap. Adding whole-file authentication while preserving these update and deduplication properties would be a separate format design; no mandatory external metadata is used here.

The key-mode field reserves room for future sealed per-file keys. Such a mode can put its encapsulated file key and header length in an extended first-block header, authenticated as part of that file. It must remain recoverable from the volume key and that file's bytes alone. Future modes may need to read that header to compute plaintext size. No version-1 file needs migration; current readers reject future modes explicitly instead of guessing.

### Names

For each component, use the names purpose and associated data `UTF8("modalfs/v1/name") || 0x00 || plaintext_parent_path`. The parent of a root component is the empty string. Store `mfs1_` followed by unpadded RFC 4648 base64url encoding of `N || C`. The prefix identifies the name format; the volume key and already-decrypted ancestors suffice to recover each component. Reject invalid base64, authentication failures, empty components, `.`, `..`, `/` or NUL in a decrypted component. Equal components in different plaintext parents have different ciphertext.

The root `.modalfs` directory is a reserved system directory and keeps that spelling. Every component beneath it is encrypted, except the optional plaintext file `.modalfs/encryption.json`. Labels therefore live at `.modalfs/<encrypted volume.json>` and contain encrypted JSON. Sidecars live beneath encrypted `manifests` and encrypted path components; their plaintext internal basename is the user's basename, without the ordinary `.json` suffix, so a maximum-length user name still has a valid sidecar name. All sidecar contents, including paths and hashes, are encrypted. Sidecars remain optional caches: a read or authentication failure is reported visibly, the cache is not used, and hashes are rebuilt from authenticated original file blocks. Corruption of an original block fails the commit and leaves its journal intact; a later successful commit can replace the broken sidecar. Missing or stale sidecars remain ordinary cache misses. Empty-directory `.modalfs_keep` files have encrypted names and authenticated empty contents.

Creation writes an encrypted root `.modalfs_keep` sentinel. It keeps an otherwise empty encrypted volume recognizable and verifiable after deleting `.modalfs/`. The sentinel is not required to decrypt any user file. If someone removes absolutely every encrypted object and the marker, no data or evidence of encryption remains; a fresh machine cannot infer the former key or format of an empty tree.

The live control-plane experiment on profile `topsoil-drakes` accepted 255-byte components and rejected 256-byte components. With the 5-byte prefix and 40-byte envelope, the maximum plaintext component is **147 bytes**, not 147 Unicode characters. Longer names fail synchronously with `ENAMETOOLONG` and a visible event explaining the limit; the small vendored NFS mapping preserves that errno for create, mkdir and rename, as FUSE already does; no truncation, plaintext escape or name lookup file is used. The live API accepted full paths through 1,048,576 bytes with legal components. No extra 4 KiB path cap is invented; server errors are reported. The local filesystem/NFS client can impose its own smaller path limits.

A folder rename re-encodes every descendant path because the plaintext parents change. It preflights destination paths, copies file manifests individually, and removes the original tree only after all copies succeed. This costs O(number of files) control-plane calls, not just local computation. A partial copy failure leaves the sources intact and may leave destination copies; it reports failure. Modal's existing lack of atomic tree moves and compare-and-swap remains.

### Marker and discovery

The optional marker is UTF-8 JSON: `{"version":1,"fingerprint":"<32 lowercase hex digits>"}`. It is the only plaintext **file** modalfs writes to an encrypted volume. A missing marker triggers conservative discovery through the `mfs1_` prefix. An ordinary plaintext volume containing a root name with that prefix is ambiguous and is treated as encrypted; rename that plaintext entry through Modal if necessary. This reserves the prefix instead of silently treating a damaged encrypted tree as plaintext. Unlock accepts the key once one encrypted root name authenticates with it, then restores a missing marker; if encrypted names exist and none authenticates, it's the wrong key. It is a key check, not a scan of every descendant; names inside each folder are authenticated when that folder is listed, and recovery validates its complete exported tree. If no encrypted root entries remain, an authenticated name immediately under `.modalfs/` can also prove the key. A name that doesn't decrypt (a plaintext file another tool such as `modal volume put` wrote, or a damaged name) is left out of the mount and reported once as an error, rather than making its folder, or the whole volume, unreadable. Name authentication itself proves possession of the key; verifying a sidecar or sentinel body here would wrongly make disposable metadata mandatory. Recovery excludes `.modalfs/` before parsing file sizes and tolerates a damaged keep-marker body. User-file sizes and contents are always validated. A malformed or unsupported marker is an explicit error during ordinary discovery; recovery with `volume decrypt` ignores the marker entirely.

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

## API and CLI

The API follows the existing account-scoped volume routes. Every route uses the daemon's existing socket/session authentication.

- `POST /api/encryption/key` → `{key}` generates a fresh key without creating a volume.
- `POST /api/accounts/{id}/volumes` accepts `{name, environment, encrypted: true, key, remember}`. `remember` defaults to true. The caller shows the key and obtains the saved-key confirmation before making this call. It returns the ordinary volume record. Encryption is initialized only on a newly created volume (`CREATE_FAIL_IF_EXISTS`). If initialization fails after creation, the error identifies the partially created volume; modalfs neither claims success nor silently deletes it.
- `POST /api/accounts/{id}/volumes/{vol}/unlock` accepts `{key, remember, environment}` → `{encrypted:true, locked:false}`. `remember` defaults to false. Choosing false also removes a previously remembered key, keeping the supplied key only in memory.
- `POST /api/accounts/{id}/volumes/{vol}/lock` → `{ok:true}`. Refuses while the volume is mounted, starting, stopping or finishing uploads. After unmounting, deletes the remembered secret and memory key. Secret-store deletion errors are reported. Remembering again updates the recorded store instead of migrating from a fallback file to a newly available keychain, so locking cannot leave an old secret copy behind. Requests that already acquired an adapter may finish; lock isn't a wipe of trusted local plaintext caches or an interruption of a user-requested recovery.
- `GET /api/accounts/{id}/volumes/{vol}/key` → `{key}`, only when remembered on this computer.
- `POST /api/accounts/{id}/volumes/{vol}/decrypt` accepts `{key, to, environment}`; `to` is an absolute local empty folder outside modalfs mounts. Its path is resolved component by component, checking for a served mount before any filesystem operation can enter it, including through symlinks. Returns `{files, bytes, to, failed}`. Reads raw Modal data without the marker, remembered key, block cache or journal. Streams each file into a temporary file and publishes only complete, authenticated files with an exclusive rename, without overwriting existing names. macOS uses `RENAME_EXCL`; Linux uses `RENAME_NOREPLACE`. A destination filesystem without this operation fails clearly; there is no overwrite fallback. It obtains the entire signed-URL list for one file in a single Modal request, then streams block bodies; it never combines URLs from separate manifests. This uses O(number of chunks) URL memory and is bounded by the API response limit. If URLs expire during a very long recovery, that file fails without being published; retry the whole file set into a new empty folder. A file or name that fails doesn't stop the rest: everything that decrypts is recovered, and the response's `failed` lists each `{path, error}` that didn't (the CLI prints them and exits 1). Internal metadata and keep files aren't exported; keep files materialize empty user folders.
- `GET /api/accounts/{id}/volumes/lookup?name=NAME&env=ENV` returns only raw Modal volume identity information. CLI actions use this name lookup and then the ID-based routes, so a malformed marker or missing remembered secret cannot prevent recovery, key replacement or forgetting a key. `volume ls` still uses the enriched listing.
- Volume-list and mount-state records include `encrypted`, `locked` and `key_remembered` (omitted when false). The volume list takes them from this computer's records, or else from whether the volume has a marker (one read per volume, eight at a time); a volume whose marker was deleted shows as encrypted once it's mounted or unlocked. Locked labels fall back to the Modal volume name; cached plaintext usage is not shown while locked. Browsing and usage use decrypted names and plaintext sizes when unlocked.
- Remote download add, explicit resume and launch reject encrypted destinations before deployment or agent startup. Daemon startup only resumes monitoring existing calls; network failures during startup must not abandon a Function that is still running. The agent never receives an encryption key.

CLI:

- `modalfs volume create NAME --encrypt [--remember=false]`: displays the generated key and requires typing `saved` before creating. `--yes` explicitly acknowledges saving the key for scripts; capture the displayed key securely.
- `modalfs unlock VOLUME [--remember]`: reads the key from the terminal without echo, or one line from stdin. A checksum error is reported before trying the key on data.
- `modalfs lock VOLUME`, after unmounting.
- `modalfs volume key VOLUME` prints a remembered key.
- `modalfs volume decrypt VOLUME --to FOLDER` reads the key from terminal/stdin and recovers without a mount.
- All volume commands accept `--account` and `--env`. The foreground debugging command `run/serve --key-stdin` can also open encrypted volumes; without a key it refuses them.

Keys use the same OS keychain/0600 secret-file mechanism as token secrets, under `volume/<volume ID>`. Configuration stores only the immutable volume ID, key fingerprint and secret-store location. Conflict copies in encrypted volumes shorten the original basename as needed, retaining its extension when it fits, and include a timestamp and random identifier. The full original path remains in the conflict event; the random identifier prevents collisions between shortened names. Local plaintext cache entries are addressed by the ciphertext hash; validating one re-encrypts it before comparing that hash. Journals record their chunk size and key fingerprint; a mismatch refuses recovery before moving or discarding any dirty block. There is no encrypted cache or local secure-erasure promise: this computer and its owner are trusted.

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

## Verification and trade-offs in implementation

The format and adapter preserve block-level uploads without keeping a second ciphertext cache: local dirty blocks and cached data stay plaintext, while manifests use SHA-256 of encrypted wire blocks. Hashing a new block encrypts it once; an upload encrypts it again only if Modal needs it. Cache validation also recomputes its ciphertext hash. This spends extra CPU to keep disk usage and recovery machinery small. The existing `x/crypto` dependency is now direct; no new crypto library was added.

The volume list adds one marker read per volume, in parallel; one that fails is logged and the row shows no lock, since mounting, unlocking and downloads still run the full discovery and fail explicitly. Actions resolve identity through the separate raw lookup route, so a damaged encryption marker or remembered secret cannot prevent recovery or repair. Remembered keys are keyed by immutable volume ID; mounts retain the existing account/environment/volume pinning. Lock retains trusted local plaintext data, and already-started API work may finish.

Recovery exports user files and empty folders, not internal labels, sidecars or keep files. Permissions are private local defaults; Modal's existing timestamp/permission limitations remain. A recovery is a consistent URL set per file, not a transaction across the whole volume. Keep the source stable when a cross-file snapshot matters. Recovering very large files is subject to Modal's response size and signed-URL lifetime limits; errors preserve source data and completed output rather than publishing a mixed or partial file.

`TestEncryptionModalContract` probes real component/full-path limits with exact-path reads, actual-length block hashes, rejected digest mismatches, short final blocks and trailing-zero reconstruction. Its artifact records the raw root listing; recursively listing the extreme one-megabyte path would expand all ancestor paths to gigabytes without adding evidence. `TestEncryptedVolume` drives separate daemons and real mounts from fresh homes, inspects raw Modal storage, and uses an independent decoder written from the specification (without either production format package). The tests use only scratch volumes and isolated homes. JSON artifacts record scenarios, raw listings, ciphertext hashes and recovered plaintext hashes; they intentionally omit encryption keys. Repeatability means rerunning the tests, not decrypting an archived listing after its scratch volume has been deleted.

Run with artifacts inside this worktree:

```sh
MODALFS_E2E_ARTIFACTS="$PWD/dist/encryption-artifacts" just test-e2e
MODALFS_E2E_ARTIFACTS="$PWD/dist/encryption-artifacts" just test-e2e-live topsoil-drakes -run TestEncrypt -v
```

The BDFL advisor challenged nonce collision bounds, cache-hash semantics, empty-file authentication, metadata-independent recovery, conflict-name growth, directory-rename costs, secret-store transitions, startup download monitoring, symlinked export paths, and coherent file manifests during recovery. The resulting implementation uses the wider XChaCha nonce, a volume-scoped adapter, authenticated empty user files, name-based key verification, bounded unique conflict names, source-preserving manifest copies, stable secret-store provenance, existing-call monitoring, a mount-aware path resolver, and one URL set per recovered file. The advisor also rejected making a damaged optional sidecar block safe writes: modalfs reports the error and rebuilds hashes from authenticated user data instead.

The native app, web UI, conversion of existing volumes, and future public-key download mode are intentionally outside this change. The format reserves the future key mode but does not implement it.
