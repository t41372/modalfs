package vfs

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/t41372/modalfs/internal/modalapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Modal's public Volume API lists symlinks, FIFOs and sockets by type, but
// can't read a link's target, create links, or change these entries (see
// docs/design/fs-metadata.md). modalfs shows them as what they are and
// refuses what it can't commit, saying why.

type unsupportedEntry struct {
	path string
	mode os.FileMode
	typ  int32
}

func (e *unsupportedEntry) Error() string {
	kind := "special file"
	switch {
	case e.mode&os.ModeSymlink != 0:
		kind = "symbolic link"
	case e.mode&os.ModeNamedPipe != 0:
		kind = "FIFO"
	case e.mode&os.ModeSocket != 0:
		kind = "socket"
	case e.mode&os.ModeIrregular != 0:
		return fmt.Sprintf("%s has an entry type modalfs doesn't know (%d); update modalfs", e.path, e.typ)
	}
	return fmt.Sprintf("%s is a %s, which Modal's Volume API can't read or change; change it inside a Modal container", e.path, kind)
}

func (e *unsupportedEntry) Unwrap() error { return syscall.ENOTSUP }

// specialEntry is nil for folders and regular files.
func specialEntry(e modalapi.Entry) error {
	if e.IsDir || e.Mode().IsRegular() {
		return nil
	}
	return &unsupportedEntry{path: e.Path, mode: e.Mode(), typ: int32(e.Type)}
}

// explain finds why Modal refused an operation on p: p, or a folder above
// it, is a symlink or another entry the API can't change. It lists afresh,
// so it runs only after Modal has refused something; otherwise, or if
// nothing explains it, it returns err.
func (fs *FS) explain(p string, err error) error {
	switch status.Code(err) {
	case codes.FailedPrecondition, codes.InvalidArgument, codes.Internal:
	default:
		return err
	}
	parts := strings.Split(clean(p), "/")
	for i := range parts {
		e, serr := fs.remoteStat(strings.Join(parts[:i+1], "/"), true)
		if serr != nil {
			return err
		}
		if why := specialEntry(e); why != nil {
			return why
		}
		if !e.IsDir {
			return err
		}
	}
	return err
}

// explainTree is explain for a folder Modal refused to copy: the entry
// inside it that the API can't copy.
func (fs *FS) explainTree(dir string, err error) error {
	if why := fs.explain(dir, err); why != err {
		return why
	}
	switch status.Code(err) {
	case codes.FailedPrecondition, codes.InvalidArgument, codes.Internal:
	default:
		return err
	}
	entries, lerr := fs.c.ListAll(fs.ctx, dir, true)
	if lerr != nil {
		return err
	}
	for _, e := range entries {
		if why := specialEntry(e); why != nil {
			return why
		}
	}
	return err
}

// unsupported reports an operation refused because of an entry the API
// can't change: in the log, and in Activity.
func (fs *FS) unsupported(p, op string, err error) {
	var why *unsupportedEntry
	if errors.As(err, &why) {
		detail := fmt.Sprintf("can't %s %s: %v", op, clean(p), err)
		fs.log.Error(detail)
		fs.event("error", clean(p), detail)
	}
}

// Readlink always fails: the API doesn't give a link's target. Activity
// says so once per mount, since listing a folder of links reads them all.
func (fs *FS) Readlink(p string) (string, error) {
	err := fmt.Errorf("%s: Modal's Volume API can't read a symbolic link's target; read it inside a Modal container: %w", clean(p), syscall.ENOTSUP)
	if fs.readlinkNoted.Swap(true) {
		fs.log.Debug(err.Error())
	} else {
		fs.log.Warn(err.Error())
		fs.event("error", clean(p), err.Error())
	}
	return "", err
}

func (fs *FS) Symlink(target, p string) error { return fs.refuseLink(p, "symbolic") }
func (fs *FS) Link(source, p string) error    { return fs.refuseLink(p, "hard") }

func (fs *FS) refuseLink(p, kind string) error {
	err := fmt.Errorf("Modal's Volume API can't create %s links; create %s inside a Modal container: %w", kind, clean(p), syscall.ENOTSUP)
	fs.log.Error(err.Error())
	fs.event("error", clean(p), err.Error())
	return err
}

// blockCommit stops committing a file whose path Modal won't take (it's
// become a symlink, say), keeping its changes and journal where they are.
// A remount tries again.
func (st *fileState) blockCommit(why error) error {
	st.mu.Lock()
	if st.blocked != nil {
		err := st.blocked
		st.mu.Unlock()
		return err
	}
	detail := fmt.Sprintf("can't upload %s: %v. Its changes are kept in %s. Move the entry aside inside a Modal container, then remount to upload them", st.path, why, st.dir)
	st.blocked = fmt.Errorf("%s: %w", detail, syscall.ENOTSUP)
	if st.timer != nil {
		st.timer.Stop()
	}
	err := st.blocked
	st.mu.Unlock()
	st.fs.log.Error(detail)
	st.fs.uploadFailed(err)
	st.fs.event("error", st.path, detail)
	return err
}
