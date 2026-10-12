// Package remote holds the Modal app that remote downloads run in: app.py
// defines the Function and the agent inside it (see its docstring).
package remote

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed app.py
var App []byte

// Version identifies this app.py. A deployed app carries it as its
// "modalfs-agent" tag, so modalfs knows when to deploy again.
func Version() string {
	h := sha256.Sum256(App)
	return hex.EncodeToString(h[:8])
}
