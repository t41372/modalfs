package vfs

import (
	"fmt"
	"path"

	"github.com/t41372/modalfs/internal/modalapi"
)

// The epoch fences detached work, not foreground reads. Work from an old
// epoch may return its bytes, but cannot attach hashes to a current path.
// Hash-addressed blocks themselves remain safe to share.
func (fs *FS) contentEpoch() uint64 {
	fs.cachePublishMu.RLock()
	defer fs.cachePublishMu.RUnlock()
	return fs.cacheEpoch
}

func (fs *FS) forgetContent(paths ...string) error {
	fs.cachePublishMu.Lock()
	defer fs.cachePublishMu.Unlock()
	fs.cacheEpoch++
	var first error
	for _, p := range paths {
		if err := fs.mdb.DeleteTree(p); err != nil && first == nil {
			first = fmt.Errorf("cannot invalidate cached hashes for %q before publishing data: %w", p, err)
		}
	}
	return first
}

// After a publication attempt the remote outcome is already established.
// A cache error must not turn a completed move/commit into a false failure:
// DeleteTree quarantines disk fallback, and the MDB error handler reports
// that cache relearning continues in memory until this volume is remounted.
func (fs *FS) finishContentMutation(paths ...string) {
	_ = fs.forgetContent(paths...)
}

func (fs *FS) publishManifest(p string, m *manifest) {
	fs.cachePublishMu.Lock()
	defer fs.cachePublishMu.Unlock()
	fs.cacheEpoch++
	_ = fs.mdb.DeleteTree(p) // quarantines stale disk records on failure
	fs.mdb.Put(p, m)
}

// Copy returns no version or hash proof. Neither the source sidecar nor an
// older destination sidecar proves which manifest the server copied.
func (fs *FS) purgeContentSidecars(paths ...string) error {
	seen := map[string]bool{}
	for _, p := range paths {
		for _, metadata := range []string{fs.sidecarPath(p), path.Join(MetaDir, "manifests", p)} {
			if seen[metadata] {
				continue
			}
			seen[metadata] = true
			if err := fs.c.Remove(fs.ctx, metadata, true); err != nil && !modalapi.IsNotFound(err) {
				return fmt.Errorf("cannot invalidate sidecar %q before changing data: %w", metadata, err)
			}
		}
	}
	return nil
}

func (fs *FS) reportManifestCacheError(err error) {
	detail := fmt.Sprintf("Local path/hash cache is unavailable: %v. This mount now relearns hashes in memory; your files and pending writes are preserved. Remount the volume to rebuild its disk metadata cache", err)
	fs.log.Warn(detail)
	fs.event("unreadable", "local path/hash cache", detail)
}
