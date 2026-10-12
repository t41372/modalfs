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

type node struct {
	gofs.Inode
	v *vfs.FS
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

func (n *node) p() string { return n.Path(nil) }

func fill(a vfs.Attr, out *fuse.Attr) {
	out.Size = uint64(a.Size)
	out.Blocks = (out.Size + 511) / 512
	out.Blksize = 1 << 20
	t := a.Mtime
	out.SetTimes(&t, &t, &t)
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())
	out.Nlink = 1
	if a.IsDir {
		out.Mode = syscall.S_IFDIR | 0o755
	} else {
		out.Mode = syscall.S_IFREG | 0o644
	}
}

func (n *node) newChild(ctx context.Context, a vfs.Attr) *gofs.Inode {
	mode := uint32(syscall.S_IFREG)
	if a.IsDir {
		mode = syscall.S_IFDIR
	}
	return n.NewInode(ctx, &node{v: n.v}, gofs.StableAttr{Mode: mode})
}

var _ = (gofs.NodeGetattrer)((*node)(nil))

func (n *node) Getattr(ctx context.Context, fh gofs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	a, err := n.v.Stat(n.p())
	if err != nil {
		return errno(err)
	}
	fill(a, &out.Attr)
	out.SetTimeout(time.Second)
	return 0
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	a, err := n.v.Stat(path.Join(n.p(), name))
	if err != nil {
		return nil, errno(err)
	}
	fill(a, &out.Attr)
	out.SetEntryTimeout(time.Second)
	out.SetAttrTimeout(time.Second)
	return n.newChild(ctx, a), 0
}

func (n *node) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	as, err := n.v.ReadDir(n.p())
	if err != nil {
		return nil, errno(err)
	}
	es := make([]fuse.DirEntry, 0, len(as))
	for _, a := range as {
		m := uint32(syscall.S_IFREG)
		if a.IsDir {
			m = syscall.S_IFDIR
		}
		es = append(es, fuse.DirEntry{Name: a.Name, Mode: m})
	}
	return gofs.NewListDirStream(es), 0
}

func (n *node) Open(ctx context.Context, flags uint32) (gofs.FileHandle, uint32, syscall.Errno) {
	if flags&syscall.O_TRUNC != 0 {
		if err := n.v.Truncate(n.p(), 0); err != nil {
			return nil, 0, errno(err)
		}
	}
	return nil, 0, 0
}

func (n *node) Read(ctx context.Context, fh gofs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	c, err := n.v.ReadAt(n.p(), dest, off)
	if err != nil {
		return nil, errno(err)
	}
	return fuse.ReadResultData(dest[:c]), 0
}

func (n *node) Write(ctx context.Context, fh gofs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	c, err := n.v.WriteAt(n.p(), data, off)
	return uint32(c), errno(err)
}

func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*gofs.Inode, gofs.FileHandle, uint32, syscall.Errno) {
	p := path.Join(n.p(), name)
	if err := n.v.Create(p, flags&syscall.O_EXCL != 0); err != nil {
		return nil, nil, 0, errno(err)
	}
	a, err := n.v.Stat(p)
	if err != nil {
		return nil, nil, 0, errno(err)
	}
	fill(a, &out.Attr)
	return n.newChild(ctx, a), nil, 0, 0
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	p := path.Join(n.p(), name)
	if err := n.v.Mkdir(p); err != nil {
		return nil, errno(err)
	}
	a, err := n.v.Stat(p)
	if err != nil {
		return nil, errno(err)
	}
	fill(a, &out.Attr)
	return n.newChild(ctx, a), 0
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	return errno(n.v.Remove(path.Join(n.p(), name)))
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	return errno(n.v.Remove(path.Join(n.p(), name)))
}

func (n *node) Rename(ctx context.Context, name string, newParent gofs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	// RENAME_NOREPLACE / RENAME_EXCHANGE are not implemented; a plain
	// rename would overwrite. EINVAL is what callers expect for this.
	if flags != 0 {
		return syscall.EINVAL
	}
	np := newParent.EmbeddedInode().Path(nil)
	return errno(n.v.Rename(path.Join(n.p(), name), path.Join(np, newName)))
}

func (n *node) Setattr(ctx context.Context, fh gofs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if sz, ok := in.GetSize(); ok {
		if err := n.v.Truncate(n.p(), int64(sz)); err != nil {
			return errno(err)
		}
	}
	// mode / owner / times are not representable in Modal Volumes: ignore.
	return n.Getattr(ctx, fh, out)
}

// Fsync makes the data durable on local disk, like an NFS COMMIT; the upload
// to Modal follows on its own schedule (or with modalfs sync).
func (n *node) Fsync(ctx context.Context, fh gofs.FileHandle, flags uint32) syscall.Errno {
	if err := n.v.Sync(n.p()); err != nil {
		return errno(err)
	}
	n.v.Release(n.p())
	return 0
}

func (n *node) Flush(ctx context.Context, fh gofs.FileHandle) syscall.Errno {
	n.v.Release(n.p())
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
	out.NameLen = 255
	return 0
}

// Mount mounts v at dir and returns the server (call Wait / Unmount).
func Mount(dir string, v *vfs.FS, volName string, readOnly bool, debug bool) (*fuse.Server, error) {
	root := &node{v: v}
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
