// Package fusefront exposes a vfs.FS through FUSE (Linux; macOS with macFUSE).
package fusefront

import (
	"context"
	"errors"
	"os"
	"path"
	"runtime"
	"syscall"
	"time"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/t41372/modalfs/internal/vfs"
)

// maxClientNameBytes is the byte limit advertised by Statfs. Enforce it at
// this frontend boundary; the shared native filename key remains unbounded.
const maxClientNameBytes = 255

type node struct {
	gofs.Inode
	v      *vfs.FS
	ids    *bindings
	object *binding
}

func errno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	var e syscall.Errno
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, os.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, os.ErrPermission):
		return syscall.EACCES
	}
	return syscall.EIO
}

func newRoot(v *vfs.FS) *node {
	return &node{v: v, ids: newBindings(), object: &binding{ino: 1, kind: syscall.S_IFDIR}}
}

func fill(a vfs.Attr, out *fuse.Attr) {
	out.Size = uint64(a.Size)
	out.Blocks = (out.Size + 511) / 512
	out.Blksize = 1 << 20
	t := a.Mtime
	out.SetTimes(&t, &t, &t)
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())
	out.Nlink = 1
	out.Mode = fileMode(a)
}

func fileMode(a vfs.Attr) uint32 {
	mode := uint32(a.Mode.Perm())
	switch {
	case a.Mode&os.ModeDir != 0:
		return mode | syscall.S_IFDIR
	case a.Mode&os.ModeSymlink != 0:
		return mode | syscall.S_IFLNK
	case a.Mode&os.ModeSocket != 0:
		return mode | syscall.S_IFSOCK
	case a.Mode&os.ModeNamedPipe != 0:
		return mode | syscall.S_IFIFO
	default:
		return mode | syscall.S_IFREG // and a type modalfs doesn't know: its operations fail
	}
}

func (n *node) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	p, release, status := n.object.acquire()
	if status != 0 {
		return nil, status
	}
	defer release()
	target, err := n.v.Readlink(p)
	return []byte(target), errno(err)
}

func (n *node) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if len(name) > maxClientNameBytes {
		return nil, syscall.ENAMETOOLONG
	}

	n.ids.names.Lock()
	defer n.ids.names.Unlock()
	p, status := n.object.current()
	if status != 0 {
		return nil, status
	}
	return nil, errno(n.v.Symlink(target, path.Join(p, name)))
}

func (n *node) Link(ctx context.Context, target gofs.InodeEmbedder, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if len(name) > maxClientNameBytes {
		return nil, syscall.ENAMETOOLONG
	}

	other, ok := target.(*node)
	if !ok || other.ids != n.ids {
		return nil, syscall.EXDEV
	}
	n.ids.names.Lock()
	defer n.ids.names.Unlock()
	p, status := n.object.current()
	if status != 0 {
		return nil, status
	}
	source, status := other.object.current()
	if status != 0 {
		return nil, status
	}
	return nil, errno(n.v.Link(source, path.Join(p, name)))
}

func (n *node) newChild(ctx context.Context, raw string, a vfs.Attr) *gofs.Inode {
	kind := fileMode(a) & syscall.S_IFMT
	id := n.ids.get(raw, kind)
	return n.NewInode(ctx, &node{v: n.v, ids: n.ids, object: id}, gofs.StableAttr{Mode: kind, Ino: id.ino})
}

var _ = (gofs.NodeGetattrer)((*node)(nil))

func (n *node) attrAt(p string, out *fuse.AttrOut) syscall.Errno {
	a, err := n.v.Stat(p)
	if err != nil {
		return errno(err)
	}
	fill(a, &out.Attr)
	out.SetTimeout(time.Second)
	return 0
}

func (n *node) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	p, release, status := n.object.acquire()
	if status != 0 {
		return status
	}
	defer release()
	return n.attrAt(p, out)
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if len(name) > maxClientNameBytes {
		return nil, syscall.ENAMETOOLONG
	}

	n.ids.names.Lock()
	defer n.ids.names.Unlock()
	p, status := n.object.current()
	if status != 0 {
		return nil, status
	}
	raw, a, err := n.v.Names().Lookup(p, name)
	if err != nil {
		return nil, errno(err)
	}
	fill(a, &out.Attr)
	out.SetEntryTimeout(time.Second)
	out.SetAttrTimeout(time.Second)
	return n.newChild(ctx, raw, a), 0
}

func (n *node) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	n.ids.names.Lock()
	defer n.ids.names.Unlock()
	p, status := n.object.current()
	if status != 0 {
		return nil, status
	}
	as, err := n.v.Names().ReadDirLimit(p, maxClientNameBytes)
	if err != nil {
		return nil, errno(err)
	}
	es := make([]fuse.DirEntry, 0, len(as))
	for _, a := range as {
		es = append(es, fuse.DirEntry{Name: a.Name, Mode: fileMode(a) & syscall.S_IFMT})
	}
	return gofs.NewListDirStream(es), 0
}

func (n *node) Open(ctx context.Context, flags uint32) (gofs.FileHandle, uint32, syscall.Errno) {
	p, release, status := n.object.acquire()
	if status != 0 {
		return nil, 0, status
	}
	defer release()
	if flags&syscall.O_TRUNC != 0 {
		if err := n.v.Truncate(p, 0); err != nil {
			return nil, 0, errno(err)
		}
	}
	return nil, 0, 0
}

func (n *node) Read(ctx context.Context, fh gofs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	p, release, status := n.object.acquire()
	if status != 0 {
		return nil, status
	}
	defer release()
	count, err := n.v.ReadAt(p, dest, off)
	if err != nil {
		return nil, errno(err)
	}
	return fuse.ReadResultData(dest[:count]), 0
}

func (n *node) Write(ctx context.Context, fh gofs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	p, release, status := n.object.acquire()
	if status != 0 {
		return 0, status
	}
	defer release()
	count, err := n.v.WriteExistingAt(p, data, off)
	return uint32(count), errno(err)
}

func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*gofs.Inode, gofs.FileHandle, uint32, syscall.Errno) {
	if len(name) > maxClientNameBytes {
		return nil, nil, 0, syscall.ENAMETOOLONG
	}

	n.ids.names.Lock()
	defer n.ids.names.Unlock()
	p, status := n.object.current()
	if status != 0 {
		return nil, nil, 0, status
	}
	var size *int64
	if flags&syscall.O_TRUNC != 0 {
		zero := int64(0)
		size = &zero
	}
	raw, a, err := n.v.Names().WithClientNameLimit(maxClientNameBytes).CreateGuarded(p, name, flags&syscall.O_EXCL != 0, size, n.ids.createGuard)
	if err != nil {
		return nil, nil, 0, errno(err)
	}
	fill(a, &out.Attr)
	return n.newChild(ctx, raw, a), nil, 0, 0
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if len(name) > maxClientNameBytes {
		return nil, syscall.ENAMETOOLONG
	}

	n.ids.names.Lock()
	defer n.ids.names.Unlock()
	p, status := n.object.current()
	if status != 0 {
		return nil, status
	}
	raw, a, err := n.v.Names().WithClientNameLimit(maxClientNameBytes).MkdirGuarded(p, name, n.ids.removeGuard)
	if err != nil {
		return nil, errno(err)
	}
	fill(a, &out.Attr)
	return n.newChild(ctx, raw, a), 0
}

func (n *node) remove(name string, kind vfs.RemoveKind) syscall.Errno {
	if len(name) > maxClientNameBytes {
		return syscall.ENAMETOOLONG
	}

	n.ids.names.Lock()
	defer n.ids.names.Unlock()
	p, status := n.object.current()
	if status != 0 {
		return status
	}
	_, err := n.v.Names().RemoveGuarded(p, name, kind, n.ids.removeGuard)
	return errno(err)
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.remove(name, vfs.RemoveFile)
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.remove(name, vfs.RemoveDir)
}

func (n *node) Rename(ctx context.Context, name string, newParent gofs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if len(name) > maxClientNameBytes || len(newName) > maxClientNameBytes {
		return syscall.ENAMETOOLONG
	}

	if flags != 0 {
		return syscall.EINVAL
	}
	other, ok := newParent.(*node)
	if !ok || other.ids != n.ids {
		return syscall.EXDEV
	}
	n.ids.names.Lock()
	defer n.ids.names.Unlock()
	oldParent, status := n.object.current()
	if status != 0 {
		return status
	}
	nextParent, status := other.object.current()
	if status != 0 {
		return status
	}
	_, _, err := n.v.Names().WithClientNameLimit(maxClientNameBytes).RenameGuarded(oldParent, name, nextParent, newName, n.ids.renameGuard)
	return errno(err)
}

func (n *node) Setattr(ctx context.Context, fh gofs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	p, release, status := n.object.acquire()
	if status != 0 {
		return status
	}
	defer release()
	if size, ok := in.GetSize(); ok {
		if err := n.v.Truncate(p, int64(size)); err != nil {
			return errno(err)
		}
	}
	return n.attrAt(p, out)
}

// Fsync makes the data durable on local disk, like an NFS COMMIT; the upload
// to Modal follows on its own schedule (or with modalfs sync).
func (n *node) Fsync(ctx context.Context, fh gofs.FileHandle, flags uint32) syscall.Errno {
	p, release, status := n.object.acquire()
	if status != 0 {
		return status
	}
	defer release()
	if err := n.v.Sync(p); err != nil {
		return errno(err)
	}
	n.v.Release(p)
	return 0
}

func (n *node) Flush(ctx context.Context, fh gofs.FileHandle) syscall.Errno {
	p, release, status := n.object.acquire()
	if status != 0 {
		return status
	}
	defer release()
	n.v.Release(p)
	return 0
}

func (n *node) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	const total = 1 << 50
	out.Bsize = 1 << 20
	out.Frsize = 1 << 20
	out.Blocks = total / (1 << 20)
	out.Bfree = out.Blocks
	out.Bavail = out.Blocks
	out.Files = 1 << 30
	out.Ffree = 1 << 30
	out.NameLen = maxClientNameBytes
	return 0
}

// Mount mounts v at dir and returns the server (call Wait / Unmount).
func Mount(dir string, v *vfs.FS, volName string, readOnly bool, debug bool) (*fuse.Server, error) {
	root := newRoot(v)
	one := time.Second
	opts := &gofs.Options{
		EntryTimeout: &one,
		AttrTimeout:  &one,
		MountOptions: fuse.MountOptions{
			FsName:       "modal:" + volName,
			Name:         "modalfs",
			Debug:        debug,
			MaxWrite:     1 << 20,
			MaxReadAhead: 1 << 20,
		},
	}
	if readOnly {
		opts.MountOptions.Options = append(opts.MountOptions.Options, "ro")
	}
	if runtime.GOOS == "darwin" {
		// macFUSE: show the volume name in Finder, skip AppleDouble files
		opts.MountOptions.Options = append(opts.MountOptions.Options, "volname="+volName, "noappledouble", "noapplexattr")
	}
	return gofs.Mount(dir, root, opts)
}
