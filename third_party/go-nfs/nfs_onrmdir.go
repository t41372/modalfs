package nfs

import (
	"context"
)

func onRmDir(ctx context.Context, w *response, userHandle Handler) error {
	return removeEntry(ctx, w, userHandle, true)
}
