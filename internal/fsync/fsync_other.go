//go:build !darwin

package fsync

import "os"

func file(f *os.File) error { return f.Sync() }
