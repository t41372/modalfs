package modalfake

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path"
	"sort"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SeedEntry describes a container-created object for test setup. These
// helpers deliberately have no RPC: Modal's public API cannot create links.
type SeedEntry struct {
	Path   string
	Data   []byte
	Target string
	LinkTo string
	Mode   os.FileMode
	Mtime  int64
}

func (s *Server) Seed(id string, entries ...SeedEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.vol(id)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := clean(e.Path)
		if e.LinkTo != "" {
			f := v.files[clean(e.LinkTo)]
			if f == nil {
				return fmt.Errorf("seed hard link: %s is missing", e.LinkTo)
			}
			v.files[p] = f
			continue
		}
		f := &file{size: uint64(len(e.Data)), mtime: uint64(e.Mtime), mode: uint32(e.Mode.Perm()), kind: pb.FileEntry_FILE}
		if f.mtime == 0 {
			f.mtime = uint64(time.Now().Unix())
		}
		if f.mode == 0 {
			f.mode = 0o644
		}
		switch {
		case e.Mode&os.ModeSymlink != 0:
			f.kind = pb.FileEntry_SYMLINK
			f.target = e.Target
			f.size = uint64(len(e.Target))
		case e.Mode&os.ModeSocket != 0:
			f.kind = pb.FileEntry_SOCKET
			f.size = 0
		case e.Mode&os.ModeNamedPipe != 0:
			return fmt.Errorf("FIFO persistence is not supported by the measured Modal backend")
		default:
			for off := 0; off < len(e.Data); off += BlockSize {
				data := e.Data[off:min(off+BlockSize, len(e.Data))]
				hash := sha256.Sum256(data)
				dest := s.blockPath(hash[:])
				if err := os.MkdirAll(path.Dir(dest), 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(dest, data, 0o600); err != nil {
					return err
				}
				f.hashes = append(f.hashes, hash[:])
			}
		}
		v.files[p] = f
	}
	return nil
}

// Inspection includes metadata not exposed by Modal's public listing. Inode
// IDs are meaningful only within one inspection, just as in one container.
type Inspection struct {
	Path   string
	Type   string
	Mode   uint32
	Size   uint64
	Mtime  uint64
	Inode  int
	Nlink  int
	Target string
	SHA256 string
}

func (s *Server) Inspect(id string) ([]Inspection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.vol(id)
	if err != nil {
		return nil, err
	}
	var names []string
	links := map[*file]int{}
	for p, f := range v.files {
		names = append(names, p)
		links[f]++
	}
	sort.Strings(names)
	inodes := map[*file]int{}
	var out []Inspection
	for _, p := range names {
		f := v.files[p]
		if inodes[f] == 0 {
			inodes[f] = len(inodes) + 1
		}
		row := Inspection{Path: p, Type: f.kind.String(), Mode: f.mode, Size: f.size, Mtime: f.mtime, Inode: inodes[f], Nlink: links[f], Target: f.target}
		if f.kind == pb.FileEntry_FILE {
			h := sha256.New()
			for i, hash := range f.hashes {
				data, err := os.ReadFile(s.blockPath(hash))
				if err != nil {
					return nil, err
				}
				n := min(uint64(len(data)), f.size-uint64(i)*BlockSize)
				h.Write(data[:n])
			}
			row.SHA256 = fmt.Sprintf("%x", h.Sum(nil))
		}
		out = append(out, row)
	}
	return out, nil
}

// Modal refuses a symlink at the leaf too, not only in ancestor folders.
func (v *volume) checkPath(p string) error {
	for q := p; q != "" && q != "."; q = path.Dir(q) {
		if f := v.files[q]; f != nil && f.kind == pb.FileEntry_SYMLINK {
			return status.Errorf(codes.FailedPrecondition, "path %q contains a symlink parent: %q", "/"+p, "/"+q)
		}
	}
	// A socket is visible but isn't a directory to traverse.
	for q := path.Dir(p); q != "." && q != ""; q = path.Dir(q) {
		if f := v.files[q]; f != nil && f.kind != pb.FileEntry_FILE {
			return status.Error(codes.InvalidArgument, "unsupported parent type")
		}
	}
	return nil
}
