package vfs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
)

// Namespace translates client names only at directory operations. Paths it
// returns are raw identities: file handles, I/O and journals must keep them.
// A nil Options.NameKey gives Linux its ordinary byte-exact namespace.
type Namespace struct {
	fs                 *FS
	maxClientNameBytes int
}

func (fs *FS) Names() *Namespace { return &Namespace{fs: fs} }

// WithClientNameLimit prevents publishing new names the frontend cannot
// display. Existing names remain accessible for byte-exact recovery.
func (n *Namespace) WithClientNameLimit(maxBytes int) *Namespace {
	return &Namespace{fs: n.fs, maxClientNameBytes: maxBytes}
}

func (n *Namespace) checkDestination(s nameSelection) error {
	storedBytes := len(path.Base(s.path))
	if storedBytes > maxName {
		return syscall.ENAMETOOLONG
	}
	if !s.exists && n.maxClientNameBytes > 0 && (storedBytes > n.maxClientNameBytes || len(s.key) > n.maxClientNameBytes) {
		return syscall.ENAMETOOLONG
	}
	return nil
}

func (fs *FS) nameKey(name string) (string, error) {
	if fs.opt.NameKey == nil {
		return name, nil
	}
	key, err := fs.opt.NameKey(name)
	if err != nil {
		return "", fmt.Errorf("cannot convert filename %q for this client: %w", name, err)
	}
	return key, nil
}

func (fs *FS) nameAliases(entries map[string]modalapi.Entry, previous map[string]string) (map[string]string, map[string]string, error) {
	if fs.opt.NameKey == nil {
		return nil, nil, nil
	}
	keys := make(map[string]string, len(entries))
	var aliases map[string]string
	for name := range entries {
		key, ok := previous[name]
		if !ok {
			var err error
			key, err = fs.nameKey(name)
			if err != nil {
				if errors.Is(err, syscall.EINVAL) {
					continue
				}
				return nil, nil, err
			}
		}
		keys[name] = key
		if key == name {
			continue
		}
		if aliases == nil {
			aliases = map[string]string{}
		}
		if _, ok := aliases[key]; ok {
			aliases[key] = ""
		} else {
			aliases[key] = name
		}
	}
	return aliases, keys, nil
}

// The local index is maintained at insertion/removal, not by scanning every
// pending file for each lookup. Keys are computed before taking fs.mu.
func (fs *FS) addLocalNameLocked(p, key string) {
	if fs.localKeys == nil {
		fs.localKeys = map[string]string{}
		fs.localNames = map[string]map[string]map[string]struct{}{}
	}
	if _, ok := fs.localKeys[p]; ok {
		return
	}
	dir, name := parentOf(p), path.Base(p)
	if fs.localNames[dir] == nil {
		fs.localNames[dir] = map[string]map[string]struct{}{}
	}
	if fs.localNames[dir][key] == nil {
		fs.localNames[dir][key] = map[string]struct{}{}
	}
	fs.localNames[dir][key][name] = struct{}{}
	fs.localKeys[p] = key
	fs.nameChangedLocked(dir, key)
}

func (fs *FS) removeLocalNameLocked(p string) {
	if fs.files[p] != nil || fs.eph[p] != nil {
		return
	}
	key, ok := fs.localKeys[p]
	if !ok {
		return
	}
	dir, name := parentOf(p), path.Base(p)
	delete(fs.localNames[dir][key], name)
	if len(fs.localNames[dir][key]) == 0 {
		delete(fs.localNames[dir], key)
	}
	if len(fs.localNames[dir]) == 0 {
		delete(fs.localNames, dir)
	}
	delete(fs.localKeys, p)
	fs.nameChangedLocked(dir, key)
}

func (fs *FS) newStateLocked(p string, exists bool, e modalapi.Entry, key string) *fileState {
	fs.nextID++
	st := newFileState(fs, p, fmt.Sprintf("%d-%d", time.Now().UnixNano(), fs.nextID), exists, e)
	if m := fs.mdb.Get(p, e.Size, e.Mtime); exists && m != nil {
		st.sure = m.Hashes
	}
	fs.files[p] = st
	fs.addLocalNameLocked(p, key)
	return st
}

type nameWatch struct {
	version uint64
	users   int
}

type nameSnapshot struct {
	keys    map[string]string
	entries map[string]modalapi.Entry
	aliases map[string]string
	watch   *nameWatch
	version uint64
}

var errNameChanged = errors.New("client name changed during namespace selection")

func (fs *FS) watchNameLocked(dir, key string) *nameWatch {
	if fs.nameWatches == nil {
		fs.nameWatches = map[string]map[string]*nameWatch{}
	}
	if fs.nameWatches[dir] == nil {
		fs.nameWatches[dir] = map[string]*nameWatch{}
	}
	watch := fs.nameWatches[dir][key]
	if watch == nil {
		watch = &nameWatch{}
		fs.nameWatches[dir][key] = watch
	}
	watch.users++
	return watch
}

func (fs *FS) nameChangedLocked(dir, key string) {
	if watch := fs.nameWatches[dir][key]; watch != nil {
		watch.version++
	}
}

func (fs *FS) releaseNameSnapshot(dir, key string, snapshot nameSnapshot) {
	if snapshot.watch == nil {
		return
	}
	fs.mu.Lock()
	snapshot.watch.users--
	if snapshot.watch.users == 0 {
		delete(fs.nameWatches[dir], key)
		if len(fs.nameWatches[dir]) == 0 {
			delete(fs.nameWatches, dir)
		}
	}
	fs.mu.Unlock()
}

// A namespace selection gets its own request. It must not join an older
// in-flight listDir call: that request can predate a newly created name.
// Native conversion happens outside fs.mu and reuses cached keys by raw name.
func (fs *FS) freshNames(dir, key string) (nameSnapshot, error) {
	fs.mu.Lock()
	l := fs.dirs[dir]
	if l == nil {
		fs.sweepDirsLocked()
		fs.dirGen++
		l = &dirListing{gen: fs.dirGen, at: time.Now()}
		fs.dirs[dir] = l
	}
	gen, previous := l.gen, l.keys
	watch := fs.watchNameLocked(dir, key)
	version := watch.version
	fs.mu.Unlock()
	success := false
	defer func() {
		if !success {
			fs.releaseNameSnapshot(dir, key, nameSnapshot{watch: watch})
		}
	}()
	start := time.Now()
	entries, err := fs.c.List(fs.ctx, dir, false)
	if modalapi.IsNotFound(err) {
		return nameSnapshot{}, os.ErrNotExist
	}
	if err != nil {
		return nameSnapshot{}, err
	}
	m := make(map[string]modalapi.Entry, len(entries))
	for _, e := range entries {
		if e.Path == dir {
			return nameSnapshot{}, syscall.ENOTDIR
		}
		m[path.Base(e.Path)] = e
	}
	aliases, keys, err := fs.nameAliases(m, previous)
	if err != nil {
		return nameSnapshot{}, err
	}
	fs.mu.Lock()
	if current := fs.dirs[dir]; current != nil && current.gen == gen && !current.at.After(start) {
		current.at, current.entries, current.aliases, current.keys = start, m, aliases, keys
		current.revision++
		current.indexRevision = current.revision
	}
	fs.mu.Unlock()
	success = true
	return nameSnapshot{keys: keys, entries: m, aliases: aliases, watch: watch, version: version}, nil
}

type nameSelection struct {
	parent, path, key string
	entry             modalapi.Entry
	exists            bool
	remote            bool // entry is present in the remote snapshot, not only local state
	snapshot          nameSnapshot
}

func (fs *FS) selectionCurrentLocked(s nameSelection) bool {
	return s.snapshot.watch == nil || s.snapshot.watch.version == s.snapshot.version
}

func (fs *FS) nameRecovery(shorter bool) string {
	tool := "the Modal CLI"
	if fs.c != nil && fs.c.Cipher != nil {
		tool = "a byte-exact Linux modalfs mount unlocked with this volume's key"
	}
	target := "a distinct filename"
	if shorter {
		target = "a shorter, distinct filename"
	}
	return "use " + tool + " to copy the file to " + target + " before removing its old entry"
}

type nameProblemKind uint8

const (
	nameCollision nameProblemKind = 1 << iota
	nameUnconvertible
	nameTooLong
)

// A successful selection proves only that its key is unambiguous. It does
// not prove the frontend can display a long or unconvertible stored name.
func (fs *FS) clearNameCollisionLocked(dir, key string) {
	if l := fs.dirs[dir]; l != nil {
		remaining := l.reported[key] &^ nameCollision
		if remaining == 0 {
			delete(l.reported, key)
		} else {
			l.reported[key] = remaining
		}
	}
}

func (fs *FS) nameProblem(dir, key string, kind nameProblemKind, detail string) error {
	fs.mu.Lock()
	report := true
	if l := fs.dirs[dir]; l != nil {
		if l.reported == nil {
			l.reported = map[string]nameProblemKind{}
		}
		report = l.reported[key] == 0
		l.reported[key] |= kind
	}
	fs.mu.Unlock()
	if report {
		fs.log.Error(detail)
		fs.event("unreadable", path.Join(dir, key), detail)
	}
	return fmt.Errorf("%s: %w", detail, syscall.ENOTSUP)
}

func (n *Namespace) selectName(parent, name string, fresh bool) (nameSelection, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return nameSelection{}, syscall.EINVAL
	}
	parent = clean(parent)
	key, err := n.fs.nameKey(name)
	if err != nil {
		return nameSelection{}, err
	}
	for {
		var snapshot nameSnapshot
		var err error
		if fresh {
			snapshot, err = n.fs.freshNames(parent, key)
		} else {
			snapshot, err = n.fs.cachedNames(parent)
		}
		if err != nil {
			return nameSelection{}, err
		}
		selection := nameSelection{parent: parent, key: key, snapshot: snapshot}
		found, ambiguous := "", false
		match := func(candidate string) {
			if hidden(parent, candidate) {
				return
			}
			if found != "" && found != candidate {
				ambiguous = true
			}
			found = candidate
		}
		n.fs.mu.Lock()
		if !n.fs.selectionCurrentLocked(selection) {
			n.fs.mu.Unlock()
			n.fs.releaseNameSnapshot(parent, key, snapshot)
			continue
		}
		if _, ok := snapshot.entries[key]; ok {
			match(key)
		}
		if candidate, ok := snapshot.aliases[key]; ok {
			if candidate == "" {
				ambiguous = true
			} else {
				match(candidate)
			}
		}
		for candidate := range n.fs.localNames[parent][key] {
			if ambiguous {
				break
			}
			match(candidate)
		}
		if !ambiguous {
			n.fs.clearNameCollisionLocked(parent, key)
		}
		n.fs.mu.Unlock()
		if ambiguous {
			n.fs.releaseNameSnapshot(parent, key, snapshot)
			detail := fmt.Sprintf("cannot access %q in %q: multiple files have the same client filename; %s", name, parent, n.fs.nameRecovery(false))
			return nameSelection{}, n.fs.nameProblem(parent, key, nameCollision, detail)
		}
		selection.exists = found != ""
		if found == "" {
			found = name
		}
		selection.path = path.Join(parent, found)
		selection.entry, selection.remote = snapshot.entries[found]
		return selection, nil
	}
}

func (n *Namespace) Lookup(parent, name string) (string, Attr, error) {
	s, err := n.selectName(parent, name, false)
	if err != nil {
		return "", Attr{}, err
	}
	if !s.exists {
		return "", Attr{}, os.ErrNotExist
	}
	a, err := n.fs.Stat(s.path)
	return s.path, a, err
}

// Create never truncates unless size is supplied. New-file reservation and
// its base entry use the same fresh snapshot, with the selected key checked
// under fs.mu at insertion. Two equivalent local creates cannot both win.
func (n *Namespace) Create(parent, name string, exclusive bool, size *int64) (string, Attr, error) {
	return n.CreateGuarded(parent, name, exclusive, size, nil)
}

func (n *Namespace) CreateGuarded(parent, name string, exclusive bool, size *int64, guard func(string, bool) func(error)) (string, Attr, error) {
	n.fs.nameMu.Lock()
	defer n.fs.nameMu.Unlock()
	if n.fs.opt.ReadOnly {
		return "", Attr{}, syscall.EROFS
	}
	if reservedPath(path.Join(parent, name)) {
		return "", Attr{}, fmt.Errorf("name is reserved for modalfs metadata: %w", syscall.EACCES)
	}
	if size != nil && *size < 0 {
		return "", Attr{}, syscall.EINVAL
	}
	for {
		selection, err := n.selectName(parent, name, true)
		if err != nil {
			return "", Attr{}, err
		}
		raw, attr, err := n.createSelected(selection, exclusive, size, guard)
		if errors.Is(err, errNameChanged) {
			continue
		}
		return raw, attr, err
	}
}

func (n *Namespace) createSelected(s nameSelection, exclusive bool, size *int64, guard func(string, bool) func(error)) (raw string, attr Attr, err error) {
	defer n.fs.releaseNameSnapshot(s.parent, s.key, s.snapshot)
	if err := n.checkDestination(s); err != nil {
		return "", Attr{}, err
	}
	if err := n.fs.checkPath(s.path); err != nil {
		return "", Attr{}, err
	}
	if guard != nil {
		if finish := guard(s.path, s.exists); finish != nil {
			defer func() { finish(err) }()
		}
	}
	createdEntry := false
	defer func() {
		if err != nil && createdEntry {
			err = &removeEntryError{cause: err}
		}
	}()
	n.fs.mu.Lock()
	if !n.fs.selectionCurrentLocked(s) {
		n.fs.mu.Unlock()
		return "", Attr{}, errNameChanged
	}
	if s.exists && exclusive {
		n.fs.mu.Unlock()
		return "", Attr{}, os.ErrExist
	}
	if s.remote && s.entry.IsDir {
		n.fs.mu.Unlock()
		return "", Attr{}, syscall.EISDIR
	}
	if why := specialEntry(s.entry); s.remote && why != nil {
		n.fs.mu.Unlock()
		return "", Attr{}, why
	}
	if n.fs.isJunk(s.path) {
		e := n.fs.eph[s.path]
		if e == nil {
			e = &ephFile{mtime: time.Now(), used: time.Now()}
			n.fs.eph[s.path] = e
			createdEntry = true
			n.fs.addLocalNameLocked(s.path, s.key)
		}
		n.fs.mu.Unlock()
		if size != nil {
			if _, err := n.fs.ephTruncate(s.path, *size); err != nil {
				return "", Attr{}, err
			}
		}
		a, _ := n.fs.ephStat(s.path)
		return s.path, a, nil
	}
	st := n.fs.files[s.path]
	if st == nil && s.exists && size == nil {
		n.fs.mu.Unlock()
		return s.path, entryAttr(s.entry), nil
	}
	created := st == nil && !s.exists
	if st == nil {
		st = n.fs.newStateLocked(s.path, s.exists, s.entry, s.key)
		createdEntry = !s.exists
	}
	n.fs.mu.Unlock()
	if created {
		st.touch()
	}
	if size != nil {
		// A background commit may detach st after the reservation. The raw
		// path still denotes this object under nameMu; use the ordinary
		// truncate retry rather than exposing an internal errDetached.
		if err := n.fs.Truncate(s.path, *size); err != nil {
			return "", Attr{}, err
		}
		attr, err = n.fs.Stat(s.path)
		return s.path, attr, err
	}
	return s.path, st.attr(), nil
}

func (n *Namespace) Mkdir(parent, name string) (string, Attr, error) {
	return n.MkdirGuarded(parent, name, nil)
}

func (n *Namespace) MkdirGuarded(parent, name string, guard func(string) func(error)) (string, Attr, error) {
	if n.fs.opt.ReadOnly {
		return "", Attr{}, syscall.EROFS
	}
	if reservedPath(path.Join(parent, name)) {
		return "", Attr{}, fmt.Errorf("name is reserved for modalfs metadata: %w", syscall.EACCES)
	}
	n.fs.nameMu.Lock()
	defer n.fs.nameMu.Unlock()
	for {
		selected, err := n.selectName(parent, name, true)
		if err != nil {
			return "", Attr{}, err
		}
		p, a, err := n.mkdirSelected(selected, guard)
		if errors.Is(err, errNameChanged) {
			continue
		}
		return p, a, err
	}
}
func (n *Namespace) mkdirSelected(s nameSelection, guard func(string) func(error)) (raw string, attr Attr, err error) {
	defer n.fs.releaseNameSnapshot(s.parent, s.key, s.snapshot)
	if s.exists {
		return "", Attr{}, os.ErrExist
	}
	if err := n.checkDestination(s); err != nil {
		return "", Attr{}, err
	}
	if err := n.fs.checkPath(s.path); err != nil {
		return "", Attr{}, err
	}
	if guard != nil {
		if finish := guard(s.path); finish != nil {
			defer func() { finish(err) }()
		}
	}
	n.fs.mu.Lock()
	current := n.fs.selectionCurrentLocked(s)
	n.fs.mu.Unlock()
	if !current {
		return "", Attr{}, errNameChanged
	}
	attempted := false
	err = n.fs.mkdir(s.path, func() error { attempted = true; return nil })
	if err != nil {
		if attempted {
			err = &removeEntryError{cause: err}
		}
		return "", Attr{}, err
	}
	attr, err = n.fs.Stat(s.path)
	if err != nil {
		return "", Attr{}, &removeEntryError{cause: err}
	}
	return s.path, attr, nil
}

// RemoveKind is what a removal was asked to delete. The kernel chose unlink
// or rmdir from what it last saw of the entry, and another writer may have
// replaced it with the other kind since, so the entry is checked again here.
type RemoveKind int

const (
	RemoveAny  RemoveKind = iota // whatever the name is
	RemoveFile                   // unlink: a file; a folder is EISDIR
	RemoveDir                    // rmdir: a folder; a file is ENOTDIR
)

func (k RemoveKind) check(isDir bool) error {
	switch {
	case k == RemoveFile && isDir:
		return syscall.EISDIR
	case k == RemoveDir && !isDir:
		return syscall.ENOTDIR
	}
	return nil
}

func (n *Namespace) Remove(parent, name string) (string, error) {
	return n.RemoveGuarded(parent, name, RemoveAny, nil)
}

// The API contract explicitly refuses symlink mutations. Reject these
// before a mutation guard so a known unchanged link keeps its identity.
// Other special types have different capabilities: sockets can be moved
// and removed, so the content-I/O classifier cannot be used here.
func (n *Namespace) rejectSymlinkMutation(s nameSelection, operation string) error {
	if !s.remote || s.entry.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	err := specialEntry(s.entry)
	n.fs.unsupported(s.path, operation, err)
	return err
}

func (n *Namespace) RemoveGuarded(parent, name string, kind RemoveKind, guard func(string) func(error)) (raw string, err error) {
	if n.fs.opt.ReadOnly {
		return "", syscall.EROFS
	}
	n.fs.nameMu.Lock()
	defer n.fs.nameMu.Unlock()
	s, err := n.selectName(parent, name, true)
	if err != nil {
		return "", err
	}
	defer n.fs.releaseNameSnapshot(s.parent, s.key, s.snapshot)
	if !s.exists {
		return "", os.ErrNotExist
	}
	// a name known here only (a pending file, a local-only one) is a file
	if err := kind.check(s.remote && s.entry.IsDir); err != nil {
		return "", err
	}
	if err := n.rejectSymlinkMutation(s, "delete"); err != nil {
		return "", err
	}
	if guard != nil {
		if finish := guard(s.path); finish != nil {
			defer func() { finish(err) }()
		}
	}
	attempted := false
	err = n.fs.remove(s.path, kind, func() error { attempted = true; return nil })
	if err != nil && attempted {
		err = &removeEntryError{cause: err}
	}
	return s.path, err
}

func (n *Namespace) Rename(oldParent, oldName, newParent, newName string) (string, string, error) {
	return n.RenameGuarded(oldParent, oldName, newParent, newName, nil)
}

func (n *Namespace) RenameGuarded(oldParent, oldName, newParent, newName string, guard func(string, string) func(error)) (oldRaw, newRaw string, err error) {
	if n.fs.opt.ReadOnly {
		return "", "", syscall.EROFS
	}
	if reservedPath(path.Join(newParent, newName)) {
		return "", "", fmt.Errorf("name is reserved for modalfs metadata: %w", syscall.EACCES)
	}
	n.fs.nameMu.Lock()
	defer n.fs.nameMu.Unlock()
	source, err := n.selectName(oldParent, oldName, true)
	if err != nil {
		return "", "", err
	}
	defer n.fs.releaseNameSnapshot(source.parent, source.key, source.snapshot)
	if !source.exists {
		return "", "", os.ErrNotExist
	}
	destination, err := n.selectName(newParent, newName, true)
	if err != nil {
		return "", "", err
	}
	defer n.fs.releaseNameSnapshot(destination.parent, destination.key, destination.snapshot)
	for _, selected := range []nameSelection{source, destination} {
		if err := n.rejectSymlinkMutation(selected, "move"); err != nil {
			return "", "", err
		}
	}
	if err := n.checkDestination(destination); err != nil {
		return "", "", err
	}
	if guard != nil {
		if finish := guard(source.path, destination.path); finish != nil {
			defer func() { finish(err) }()
		}
	}
	err = n.fs.Rename(source.path, destination.path)
	return source.path, destination.path, err
}

// ReadDir omits only unaddressable names and reports why in Activity. One
// canonical collision must not make unrelated files in the folder disappear.
func (n *Namespace) ReadDir(parent string) ([]Attr, error) {
	return n.ReadDirLimit(parent, n.maxClientNameBytes)
}

// ReadDirLimit applies a frontend's client-name byte limit after exact name
// conversion. Zero leaves the limit to that frontend (for example macFUSE).
func (n *Namespace) ReadDirLimit(parent string, maxClientNameBytes int) ([]Attr, error) {
	parent = clean(parent)
	if n.fs.opt.NameKey == nil && maxClientNameBytes <= 0 {
		return n.fs.ReadDir(parent)
	}
	entries, err := n.fs.ReadDir(parent)
	if err != nil {
		return nil, err
	}
	snapshot, err := n.fs.cachedNames(clean(parent))
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(entries))
	valid := make([]bool, len(entries))
	counts := map[string]int{}
	problems := map[string]nameProblemKind{}
	for i, e := range entries {
		var conversionErr error
		var ok bool
		keys[i], ok = snapshot.keys[e.Name]
		if !ok {
			n.fs.mu.Lock()
			keys[i], ok = n.fs.localKeys[path.Join(clean(parent), e.Name)]
			n.fs.mu.Unlock()
		}
		if !ok {
			keys[i], conversionErr = n.fs.nameKey(e.Name)
		}
		if conversionErr != nil {
			if errors.Is(conversionErr, syscall.EINVAL) {
				problems[e.Name] |= nameUnconvertible
				_ = n.fs.nameProblem(parent, e.Name, nameUnconvertible, fmt.Sprintf("filename %q in %q cannot be shown by this client: %v; the file is kept on the volume; %s", e.Name, parent, conversionErr, n.fs.nameRecovery(false)))
				continue
			}
			return nil, conversionErr
		}
		valid[i] = true
		counts[keys[i]]++
	}
	out := make([]Attr, 0, len(entries))
	for i, e := range entries {
		if !valid[i] {
			continue
		}
		if maxClientNameBytes > 0 && (len(keys[i]) > maxClientNameBytes || len(e.Name) > maxClientNameBytes) {
			problems[keys[i]] |= nameTooLong
			detail := fmt.Sprintf("filename %q in %q cannot be shown by this client: its stored spelling is %d bytes and converted spelling is %d bytes (limit %d); the file is kept on the volume; %s", e.Name, parent, len(e.Name), len(keys[i]), maxClientNameBytes, n.fs.nameRecovery(true))
			_ = n.fs.nameProblem(parent, keys[i], nameTooLong, detail)
			continue
		}
		if counts[keys[i]] > 1 {
			problems[keys[i]] |= nameCollision
			detail := fmt.Sprintf("files with client filename %q in %q cannot be shown because their names collide; %s", keys[i], parent, n.fs.nameRecovery(false))
			_ = n.fs.nameProblem(parent, keys[i], nameCollision, detail)
			continue
		}
		out = append(out, e)
	}
	// Suppress repeated notices while a problem remains. Once a complete
	// listing observes it resolved, a later recurrence needs a new notice.
	n.fs.mu.Lock()
	if l := n.fs.dirs[parent]; l != nil {
		for key := range l.reported {
			l.reported[key] &= problems[key]
			if l.reported[key] == 0 {
				delete(l.reported, key)
			}
		}
	}
	n.fs.mu.Unlock()
	return out, nil
}

func (fs *FS) cachedNames(dir string) (nameSnapshot, error) {
	for {
		if _, err := fs.listDir(dir, false); err != nil {
			return nameSnapshot{}, err
		}
		fs.mu.Lock()
		l := fs.dirs[dir]
		if l == nil || l.entries == nil {
			fs.mu.Unlock()
			continue
		}
		entries, previous, gen, revision := l.entries, l.keys, l.gen, l.revision
		if l.indexRevision == revision {
			snapshot := nameSnapshot{keys: l.keys, entries: entries, aliases: l.aliases}
			fs.mu.Unlock()
			return snapshot, nil
		}
		fs.mu.Unlock()
		aliases, keys, err := fs.nameAliases(entries, previous)
		if err != nil {
			return nameSnapshot{}, err
		}
		fs.mu.Lock()
		current := fs.dirs[dir]
		if current == nil || current.gen != gen || current.revision != revision || current.entries == nil {
			fs.mu.Unlock()
			continue
		}
		current.aliases, current.keys, current.indexRevision = aliases, keys, revision
		fs.mu.Unlock()
		return nameSnapshot{keys: keys, entries: entries, aliases: aliases}, nil
	}
}

func (fs *FS) rawNameAmbiguousLocked(dir, name, key string, snapshot nameSnapshot) bool {
	if _, ok := snapshot.entries[key]; ok && key != name {
		return true
	}
	if alias, ok := snapshot.aliases[key]; ok && alias != name {
		return true
	}
	for other := range fs.localNames[dir][key] {
		if other != name {
			return true
		}
	}
	return false
}
