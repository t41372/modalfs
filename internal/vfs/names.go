package vfs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/t41372/modalfs/internal/modalapi"
	"golang.org/x/text/unicode/norm"
)

// DecomposeMacOSPath matches Foundation's filesystem spelling: canonical
// decomposition with the HFS exclusions for symbols and compatibility
// ideographs. Plain NFD would incorrectly merge names such as K and K.
// https://developer.apple.com/documentation/coreservices/1399897-encoding_variants_for_unicode
func DecomposeMacOSPath(p string) string {
	var out strings.Builder
	start := 0
	for i, r := range p {
		excluded := r >= 0x2000 && r <= 0x2fff || r >= 0xf900 && r <= 0xfaff || r >= 0x2f800 && r <= 0x2faff
		if excluded && !norm.NFD.IsNormalString(string(r)) {
			out.WriteString(norm.NFD.String(p[start:i]))
			out.WriteRune(r)
			start = i + utf8.RuneLen(r)
		}
	}
	if start == 0 {
		return norm.NFD.String(p)
	}
	out.WriteString(norm.NFD.String(p[start:]))
	return out.String()
}

// nameAliases indexes only names whose decomposed spelling differs. An
// empty value means several remote names have that spelling.
func nameAliases(entries map[string]modalapi.Entry) map[string]string {
	var aliases map[string]string
	for name := range entries {
		decomposed := DecomposeMacOSPath(name)
		if decomposed == name {
			continue
		}
		if aliases == nil {
			aliases = map[string]string{}
		}
		if _, exists := aliases[decomposed]; exists {
			aliases[decomposed] = ""
		} else {
			aliases[decomposed] = name
		}
	}
	return aliases
}

// ResolveNormalizedPath translates macOS's decomposed Unicode paths to the
// volume's actual spellings, including parent folders. Missing components
// keep their spelling for creation. It never chooses between distinct files
// whose names macOS considers equivalent.
func (fs *FS) ResolveNormalizedPath(p string) (string, error) {
	p = clean(p)
	if p == "" {
		return p, nil
	}
	parts := strings.Split(p, "/")
	dir := ""
	for i, name := range parts {
		entries, err := fs.listDir(dir, false)
		if errors.Is(err, os.ErrNotExist) {
			return path.Join(dir, strings.Join(parts[i:], "/")), nil
		}
		if err != nil {
			return "", err
		}
		key := DecomposeMacOSPath(name)
		found, ambiguous := "", false
		match := func(candidate string) {
			if found != "" && found != candidate {
				ambiguous = true
			}
			found = candidate
		}
		fs.mu.Lock()
		var aliases map[string]string
		if l := fs.dirs[dir]; l != nil && l.entries != nil {
			entries, aliases = l.entries, l.aliases
		} else {
			aliases = nameAliases(entries) // the listing was invalidated meanwhile
		}
		if _, exists := entries[key]; exists {
			match(key)
		}
		if alias, exists := aliases[key]; exists {
			if alias == "" {
				ambiguous = true
			} else {
				match(alias)
			}
		}
		// A new local file is visible before it reaches the remote listing.
		for local := range fs.files {
			if parentOf(local) == dir && DecomposeMacOSPath(path.Base(local)) == key {
				match(path.Base(local))
			}
		}
		for local := range fs.eph {
			if parentOf(local) == dir && DecomposeMacOSPath(path.Base(local)) == key {
				match(path.Base(local))
			}
		}
		fs.mu.Unlock()
		if ambiguous {
			detail := fmt.Sprintf("can't access %q on macOS: several names in %q have the same Unicode spelling; copy one to a distinct name with the Modal CLI, then remove its old entry", name, dir)
			fs.log.Error(detail)
			fs.event("error", p, detail)
			return "", fmt.Errorf("%s: %w", detail, syscall.ENOTSUP)
		}
		if found == "" {
			return path.Join(dir, strings.Join(parts[i:], "/")), nil
		}
		dir = path.Join(dir, found)
	}
	return dir, nil
}
