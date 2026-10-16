# Symlinks, hard links and permissions

Status: built. modalfs shows every entry as its real type and refuses, with a reason, what Modal's Volume API can't do. Permissions, link targets and hard links stay out of reach until the API offers them. `TestContractFilesystemMetadata` (`just test-modal-contract <profile>`, daily in CI) checks the API behaviour below against real Modal; when Modal changes, it fails and says what to revisit here.

## Why

People keep code in Volumes and work on it from Sandboxes, Functions and their own computers through modalfs. Code trees have symlinks (virtualenvs, `node_modules/.bin`), executable scripts, and sometimes hard links. A Volume v2 supports all of these inside a container, where Modal serves it with its own filesystem. modalfs only has the public API, which is narrower.

## What the public API does

Measured on real Modal on 2026-10-15, with Sandboxes making and inspecting entries on one side and the API on the other. Every observation came after `sync` on the mount point and the Sandbox's confirmed exit, since a Sandbox terminated without waiting may not have published its last writes.

- **Listing** gives a path, a type (file, folder, symlink, socket), a size and a modification time in whole seconds. A symlink's size is its target's length and its time is the link's own. There are no permissions, no inode numbers or link counts, and no link targets. A recursive listing doesn't follow folder links. A FIFO made in a container doesn't persist.
- **Symlinks can't be read, created or changed.** Reading, writing, copying or removing a link, or any path below one, fails with `FailedPrecondition` ("contains a symlink parent"). A recursive copy of a folder holding a link deeper down fails with `Internal`, leaving the destination as it was. A recursive removal of a folder holding links works, without following them. An upload whose `mode` has the symlink type bits makes an ordinary file.
- **Permissions are ignored.** Whatever `mode` an upload sends (none, `0751`, special or type bits), the container sees `0666`. An upload over a container's `0751` file leaves it `0666`. `modal volume put` behaves the same. A copy keeps the source's mode.
- **Hard links can't be made, or kept through an upload.** An upload to one name of a hard link replaces only that name, splitting it from the others. A copy makes an independent file. Removing one name keeps the others.
- **Modification times** are set by Modal: an upload gets the commit time, and a copy a new time. Nothing in the API sets one.
- **Sockets** are listed, and can be removed and copied.

## What modalfs does

- **Types.** Each entry shows as what it is: a symlink as a symlink with its size and time, a socket as a socket. An entry of a type modalfs doesn't know shows as a file whose reads fail with "update modalfs".
- **Refusals.** These fail at once with "Operation not supported" (`ENOTSUP`), and Activity says why and what to do (change it inside a Modal container):
  - creating a symlink or a hard link;
  - writing to a path that is a symlink on Modal, before anything is kept locally;
  - deleting or moving a symlink, and moving a folder with a symlink inside it. Modal refuses these itself; modalfs explains the refusal, and takes away the folder a refused move made if nothing was copied into it yet.
- **Reading a link** (and so reading or writing through one, which the kernel turns into a read of the link) fails too. Activity says so the first time on each mount; after that it's only in the log, because listing a folder of links reads every one of them.
- **A file that becomes a link while it has changes waiting** (a container replaced it): the commit stops. The changes and their journal stay in the cache, Activity says where, and modalfs doesn't retry until the next mount. Until then the path shows the waiting changes, like any change not uploaded yet.
- **chmod, chown and utimes** are accepted and ignored, as before. Refusing them couldn't make permissions work, and would break `cp -p`, `rsync -a`, `install -m`, pip and npm installs, git and editors, which work today and lose only the permissions.
- **macOS:** its NFS client turns "Operation not supported" from the server into "Input/output error". Activity and the log say the real reason.

None of this adds API calls when operations succeed. Types come from the listings modalfs already makes, and a refusal is explained by looking again only after Modal has refused.

## What users need to know

- Saving a file through modalfs, like any upload through the API, leaves it `0666` in containers and splits it from its hard links. A script saved from a laptop no longer runs in a Sandbox (`permission denied`), and git in the Sandbox sees a mode change (`100755 => 100644`) it may commit. With `git config core.fileMode false` in a repository on a volume, git ignores the change and its commits keep the file executable (checked in a Sandbox); running the script there still needs `chmod +x` again.
- Tools on a mount, measured on macOS: `git clone` works, writing links as small text files and setting `core.symlinks=false` in the repository, so a Sandbox's git keeps them as text files too (clone such repositories inside a Sandbox); `cp`, `cp -p` and `install -m` work without keeping permissions; `ditto` and `rsync -a` copy the files but fail on links (exit 1 and 23).

## Rejected

- **Keeping targets or modes in modalfs's own files on the volume.** A container would see something different from the laptop, and existing metadata still couldn't be read.
- **A Sandbox to read or set metadata.** It's compute the user didn't ask for (principle 2).
- **`ln` as a copy.** A copy behaves differently from a link the first time either is changed.
- **Refusing chmod.** See above; the owner chose compatibility.
- **Read-only mounts by default**, to keep permissions and links intact. It would stop the editing modalfs exists for.
- **Checking types before every read, write, commit and deletion.** It costs a listing on every hot path to catch races that Modal refuses on its own anyway.

## Not handled

- A rename is a copy and then a removal. If a container's newer write hasn't reached the API yet, the copy can take the previous version; in experiments the copy was the last published version, never damaged data.
- Deleting a file with waiting changes, when Modal then refuses the deletion, drops the changes, as before.
- On Linux, CI runs these tests on a real FUSE mount against the fake Modal. Against real Modal, Linux FUSE was checked by hand.

## What would close the gaps

An API that returns permissions, link targets and inode identity, creates links, keeps an existing file's mode on upload (or applies the one sent), and sets modification times.
