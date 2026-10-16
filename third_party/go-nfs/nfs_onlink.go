package nfs

import (
	"bytes"
	"context"
	"os"
	"syscall"

	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs-client/nfs/xdr"
)

// LINK3args is a source file handle followed by the destination diropargs3
// (RFC 1813 section 3.3.15). It has neither sattr3 nor a symlink target.
func onLink(ctx context.Context, w *response, userHandle Handler) error {
	w.errorFmt = errFormatterWithBody(make([]byte, 12)) // file attrs + directory WCC
	source, err := xdr.ReadOpaque(w.req.Body)
	if err != nil {
		return &NFSStatusError{NFSStatusInval, err}
	}
	var to DirOpArg
	if err := xdr.Read(w.req.Body, &to); err != nil {
		return &NFSStatusError{NFSStatusInval, err}
	}
	fs, sourcePath, err := userHandle.FromHandle(source)
	if err != nil {
		return &NFSStatusError{NFSStatusStale, err}
	}
	destFS, destPath, err := userHandle.FromHandle(to.Handle)
	if err != nil {
		return &NFSStatusError{NFSStatusStale, err}
	}
	if fs != destFS {
		return &NFSStatusError{NFSStatusXDev, nil}
	}
	if !billy.CapabilityCheck(fs, billy.WriteCapability) {
		return &NFSStatusError{NFSStatusROFS, os.ErrPermission}
	}
	if len(to.Filename) > PathNameMax {
		return &NFSStatusError{NFSStatusNameTooLong, os.ErrInvalid}
	}
	linker, ok := fs.(interface{ Link(string, string) error })
	if !ok {
		return &NFSStatusError{NFSStatusNotSupp, syscall.ENOTSUP}
	}
	if err := linker.Link(fs.Join(sourcePath...), fs.Join(append(destPath, string(to.Filename))...)); err != nil {
		return &NFSStatusError{statusFromNameError(err, NFSStatusIO), err}
	}
	writer := bytes.NewBuffer(nil)
	if err := xdr.Write(writer, uint32(NFSStatusOk)); err != nil {
		return err
	}
	if err := WritePostOpAttrs(writer, tryStat(fs, sourcePath)); err != nil {
		return err
	}
	if err := WriteWcc(writer, nil, tryStat(fs, destPath)); err != nil {
		return err
	}
	return w.Write(writer.Bytes())
}
