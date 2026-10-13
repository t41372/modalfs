//go:build !darwin && !linux

package vfs

func diskFree(string) int64 { return -1 }
