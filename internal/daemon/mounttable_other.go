//go:build !darwin && !linux

package daemon

import "errors"

func mountedPaths() (map[string]bool, error) {
	return nil, errors.New("no mount table on this system")
}
