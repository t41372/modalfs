//go:build !darwin

package filename

// Key returns nil on platforms whose mounts use literal filename bytes.
func Key() (func(string) (string, error), error) { return nil, nil }
