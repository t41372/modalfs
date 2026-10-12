// Package fsync makes a file's data durable without stalling the Go process.
package fsync

import "os"

// File flushes f's data to stable storage, like f.Sync.
func File(f *os.File) error { return file(f) }
