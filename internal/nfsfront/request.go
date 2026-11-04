package nfsfront

import (
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

// A binding is held for each complete raw request. Namespace mutations take
// only the affected bindings for writing; unrelated data I/O remains live.
type handleBinding struct {
	mu    sync.RWMutex
	id    [24]byte
	parts []string
	stale bool
}

type requestHandler struct {
	*handler
	raw   bool
	held  map[*handleBinding]bool
	locks []*handleBinding
}

func (h *handler) BeginRequest(header rpc.Header) (nfs.Handler, func()) {
	if header.Prog != 100003 {
		return h, func() {}
	}
	namespace := false
	switch nfs.NFSProcedure(header.Proc) {
	case nfs.NFSProcedureLookup, nfs.NFSProcedureCreate, nfs.NFSProcedureMkDir,
		nfs.NFSProcedureSymlink, nfs.NFSProcedureMkNod, nfs.NFSProcedureRemove,
		nfs.NFSProcedureRmDir, nfs.NFSProcedureRename, nfs.NFSProcedureLink,
		nfs.NFSProcedureReadDir, nfs.NFSProcedureReadDirPlus:
		namespace = true
	}
	if namespace {
		h.namesMu.Lock()
	}
	request := &requestHandler{handler: h, raw: !namespace, held: map[*handleBinding]bool{}}
	return request, func() {
		for i := len(request.locks) - 1; i >= 0; i-- {
			request.locks[i].mu.RUnlock()
		}
		if namespace {
			h.namesMu.Unlock()
		}
	}
}

func (r *requestHandler) FromHandle(handle []byte) (billy.Filesystem, []string, error) {
	if !r.raw {
		return r.handler.FromHandle(handle)
	}
	binding, err := r.binding(handle)
	if err != nil {
		return nil, nil, err
	}
	if !r.held[binding] {
		binding.mu.RLock()
		if _, err := r.bindingPath(binding); err != nil {
			binding.mu.RUnlock()
			return nil, nil, err
		}
		r.held[binding] = true
		r.locks = append(r.locks, binding)
	}
	parts, err := r.bindingPath(binding)
	return r.fs, parts, err
}

func underPath(p, prefix string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }

// A namespace request owns namesMu, so no late LOOKUP/READDIR registration
// can appear inside the selected subtrees while the mutation is in flight.
func (h *handler) lockBindings(prefixes ...string) []*handleBinding {
	h.mu.RLock()
	var bindings []*handleBinding
	for _, binding := range h.paths {
		p := strings.Join(binding.parts, "/")
		for _, prefix := range prefixes {
			if underPath(p, prefix) {
				bindings = append(bindings, binding)
				break
			}
		}
	}
	h.mu.RUnlock()
	sort.Slice(bindings, func(i, j int) bool { return string(bindings[i].id[:]) < string(bindings[j].id[:]) })
	for _, binding := range bindings {
		binding.mu.Lock()
	}
	return bindings
}
func unlockBindings(bindings []*handleBinding) {
	for i := len(bindings) - 1; i >= 0; i-- {
		bindings[i].mu.Unlock()
	}
}

func (h *handler) GuardRemove(_ billy.Filesystem, parts []string) func(error) {
	p := strings.Join(parts, "/")
	bindings := h.lockBindings(p)
	return func(err error) {
		var outcome interface{ EntryMayHaveChanged() bool }
		changed := err == nil || errors.As(err, &outcome) && outcome.EntryMayHaveChanged()
		if changed {
			h.mu.Lock()
			for _, binding := range bindings {
				h.invalidateBindingLocked(binding)
			}
			h.mu.Unlock()
		}
		unlockBindings(bindings)
	}
}

func (h *handler) GuardRename(_ billy.Filesystem, oldParts, newParts []string) func(error) {
	oldp, newp := strings.Join(oldParts, "/"), strings.Join(newParts, "/")
	if oldp == newp {
		return func(error) {}
	}
	bindings := h.lockBindings(oldp, newp)
	return func(err error) {
		var outcome interface{ DestinationMayHaveChanged() bool }
		changed := err == nil || errors.As(err, &outcome) && outcome.DestinationMayHaveChanged()
		h.mu.Lock()
		if changed {
			for _, binding := range bindings {
				if underPath(strings.Join(binding.parts, "/"), newp) {
					h.invalidateBindingLocked(binding)
				}
			}
		}
		if err == nil {
			for _, binding := range bindings {
				if h.paths[binding.id] != binding {
					continue
				}
				p := strings.Join(binding.parts, "/")
				if underPath(p, oldp) {
					target := newp + strings.TrimPrefix(p, oldp)
					delete(h.byPath, p)
					h.byPath[target] = binding.id
					binding.parts = strings.Split(target, "/")
				}
			}
		}
		h.mu.Unlock()
		unlockBindings(bindings)
	}
}

func (h *handler) GuardCreate(fs billy.Filesystem, parts []string, existed bool) func(error) {
	if existed {
		return func(error) {}
	}
	return h.GuardRemove(fs, parts)
}
func (h *handler) GuardMkdir(fs billy.Filesystem, parts []string) func(error) {
	return h.GuardCreate(fs, parts, false)
}
