// Package nfsfront exposes a vfs.FS as a local NFSv3 server, so macOS can
// mount it with its built-in NFS client (no macFUSE / kernel extension).
package nfsfront

import (
	"errors"
	"hash/fnv"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/t41372/modalfs/internal/vfs"
	nfs "github.com/willscott/go-nfs"
	nfsfile "github.com/willscott/go-nfs/file"
)

// Serve runs the NFS server on l until it fails.
func Serve(l net.Listener, fs *vfs.FS, readOnly bool) error {
	b := &billyFS{fs: fs, ro: readOnly, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}
	h := newHandler(b)
	h.log = fs.Logger()
	return nfs.Serve(l, h)
}

type billyFS struct {
	fs       *vfs.FS
	ro       bool
	uid, gid uint32
}

type fileInfo struct {
	a        vfs.Attr
	path     string
	uid, gid uint32
}

func (fi fileInfo) Name() string       { return fi.a.Name }
func (fi fileInfo) Size() int64        { return fi.a.Size }
func (fi fileInfo) Mode() os.FileMode  { return fi.a.Mode }
func (fi fileInfo) ModTime() time.Time { return fi.a.Mtime }
func (fi fileInfo) IsDir() bool        { return fi.a.IsDir }
func (fi fileInfo) Sys() any {
	h := fnv.New64a()
	h.Write([]byte(fi.path))
	return &nfsfile.FileInfo{Nlink: 1, UID: fi.uid, GID: fi.gid, Fileid: h.Sum64()}
}

func norm(p string) string { return strings.Trim(path.Clean("/"+p), "/") }

func (b *billyFS) info(p string, a vfs.Attr) os.FileInfo {
	return fileInfo{a: a, path: p, uid: b.uid, gid: b.gid}
}

func (b *billyFS) Capabilities() billy.Capability {
	if b.ro {
		return billy.ReadCapability | billy.SeekCapability
	}
	return billy.ReadCapability | billy.WriteCapability | billy.ReadAndWriteCapability | billy.SeekCapability | billy.TruncateCapability
}

// Create opens name, creating it if needed, but never truncates: go-nfs
// applies size 0 itself when the client asks for it.
func (b *billyFS) Create(name string) (billy.File, error) {
	return b.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o644)
}

func (b *billyFS) Open(name string) (billy.File, error) {
	return b.OpenFile(name, os.O_RDONLY, 0)
}

func (b *billyFS) OpenFile(name string, flag int, _ os.FileMode) (billy.File, error) {
	p := norm(name)
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC) != 0 && b.ro {
		return nil, os.ErrPermission
	}
	if flag&os.O_CREATE != 0 {
		// exclusive, so vfs.Create never truncates an existing file;
		// O_TRUNC below does that only when asked
		if err := b.fs.Create(p, true); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		} else if errors.Is(err, os.ErrExist) && flag&os.O_EXCL != 0 {
			return nil, err
		}
	}
	a, err := b.fs.Stat(p)
	if err != nil {
		return nil, err
	}
	if a.IsDir && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return nil, errors.New("is a directory")
	}
	if flag&os.O_TRUNC != 0 && !a.IsDir && a.Size > 0 {
		if err := b.fs.Truncate(p, 0); err != nil {
			return nil, err
		}
	}
	return &file{fs: b.fs, p: p, name: name, appendMode: flag&os.O_APPEND != 0}, nil
}

func (b *billyFS) Stat(name string) (os.FileInfo, error) {
	p := norm(name)
	a, err := b.fs.Stat(p)
	if err != nil {
		return nil, err
	}
	return b.info(p, a), nil
}

func (b *billyFS) Lstat(name string) (os.FileInfo, error) { return b.Stat(name) }

func (b *billyFS) Rename(o, n string) error { return b.fs.Rename(norm(o), norm(n)) }
func (b *billyFS) Remove(name string) error { return b.fs.Remove(norm(name)) }

// SyncPath makes a file's written data durable (NFS COMMIT; see
// third_party/go-nfs/modalfs.go).
func (b *billyFS) SyncPath(name string) error { return b.fs.Sync(name) }

func (b *billyFS) Join(elem ...string) string {
	return path.Join(elem...)
}

func (b *billyFS) TempFile(dir, prefix string) (billy.File, error) {
	name := path.Join(dir, prefix+time.Now().Format("150405.000000000"))
	return b.Create(name)
}

func (b *billyFS) ReadDir(name string) ([]os.FileInfo, error) {
	p := norm(name)
	as, err := b.fs.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(as))
	for _, a := range as {
		out = append(out, b.info(path.Join(p, a.Name), a))
	}
	return out, nil
}

func (b *billyFS) MkdirAll(name string, _ os.FileMode) error {
	p := norm(name)
	if p == "" {
		return nil
	}
	if a, err := b.fs.Stat(p); err == nil {
		if a.IsDir {
			return nil
		}
		return os.ErrExist
	}
	if err := b.MkdirAll(path.Dir(p), 0); err != nil {
		return err
	}
	return b.fs.Mkdir(p)
}

func (b *billyFS) Symlink(target, name string) error    { return b.fs.Symlink(target, norm(name)) }
func (b *billyFS) Readlink(name string) (string, error) { return b.fs.Readlink(norm(name)) }
func (b *billyFS) Link(source, name string) error       { return b.fs.Link(norm(source), norm(name)) }
func (b *billyFS) LinkSupport() (hard, symbolic bool)   { return false, false }
func (b *billyFS) Chroot(_ string) (billy.Filesystem, error) {
	return nil, errors.New("chroot not supported")
}
func (b *billyFS) Root() string { return "/" }

// billy.Change: Modal Volumes do not store owner or settable mtimes; accept
// and ignore so that tools like cp -p / rsync / editors do not fail.
func (b *billyFS) Chmod(string, os.FileMode) error            { return nil }
func (b *billyFS) Lchown(string, int, int) error              { return nil }
func (b *billyFS) Chown(string, int, int) error               { return nil }
func (b *billyFS) Chtimes(string, time.Time, time.Time) error { return nil }

type file struct {
	fs         *vfs.FS
	p, name    string
	pos        int64
	appendMode bool
}

func (f *file) Name() string { return f.name }
func (f *file) Read(b []byte) (int, error) {
	n, err := f.ReadAt(b, f.pos)
	f.pos += int64(n)
	return n, err
}
func (f *file) ReadAt(b []byte, off int64) (int, error) {
	n, err := f.fs.ReadAt(f.p, b, off)
	if err == nil && n < len(b) {
		err = io.EOF
	}
	return n, err
}
func (f *file) Write(b []byte) (int, error) {
	if f.appendMode {
		if a, err := f.fs.Stat(f.p); err == nil {
			f.pos = a.Size
		}
	}
	n, err := f.fs.WriteAt(f.p, b, f.pos)
	f.pos += int64(n)
	return n, err
}
func (f *file) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.pos = off
	case io.SeekCurrent:
		f.pos += off
	case io.SeekEnd:
		a, err := f.fs.Stat(f.p)
		if err != nil {
			return 0, err
		}
		f.pos = a.Size + off
	}
	return f.pos, nil
}
func (f *file) Close() error           { return nil } // commits happen on idle (write-back)
func (f *file) Lock() error            { return nil }
func (f *file) Unlock() error          { return nil }
func (f *file) Truncate(n int64) error { return f.fs.Truncate(f.p, n) }
