package vfs

import "errors"

// renameDestinationError means a destination mutation was attempted before
// a rename failed. A failed RPC may have published its change. Callers must
// invalidate the replaced destination identity, while leaving source handles
// at their original paths unless the whole rename succeeded.
type renameDestinationError struct{ cause error }

func (e *renameDestinationError) Error() string                 { return e.cause.Error() }
func (e *renameDestinationError) Unwrap() error                 { return e.cause }
func (*renameDestinationError) DestinationMayHaveChanged() bool { return true }

func RenameMayHaveChangedDestination(err error) bool {
	var outcome interface{ DestinationMayHaveChanged() bool }
	return errors.As(err, &outcome) && outcome.DestinationMayHaveChanged()
}

// A failed remove can have discarded the local entry or published its RPC.
// Retire that identity conservatively, but do not retire it for preconditions
// such as ENOTEMPTY that failed before any mutation attempt.
type removeEntryError struct{ cause error }

func (e *removeEntryError) Error() string           { return e.cause.Error() }
func (e *removeEntryError) Unwrap() error           { return e.cause }
func (*removeEntryError) EntryMayHaveChanged() bool { return true }
func RemoveMayHaveChangedEntry(err error) bool {
	var outcome interface{ EntryMayHaveChanged() bool }
	return errors.As(err, &outcome) && outcome.EntryMayHaveChanged()
}
