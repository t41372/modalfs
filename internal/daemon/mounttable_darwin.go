package daemon

import "syscall"

// mntNowait (MNT_NOWAIT) returns the kernel's list without asking each
// filesystem for fresh numbers, so a stalled NFS server can't hang the call.
const mntNowait = 2

// mountedPaths returns the mount points in the kernel's mount table.
func mountedPaths() (map[string]bool, error) {
	n, err := syscall.Getfsstat(nil, mntNowait)
	if err != nil {
		return nil, err
	}
	buf := make([]syscall.Statfs_t, n+16) // room for mounts made meanwhile
	n, err = syscall.Getfsstat(buf, mntNowait)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, n)
	for _, s := range buf[:n] {
		b := make([]byte, 0, 64)
		for _, c := range s.Mntonname {
			if c == 0 {
				break
			}
			b = append(b, byte(c))
		}
		out[string(b)] = true
	}
	return out, nil
}
