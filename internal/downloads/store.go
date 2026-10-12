package downloads

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/t41372/modalfs/internal/config"
)

// Store keeps the daemon's list of downloads in downloads.json (mode 0600:
// it holds live tokens and cookies). What happened to each download lives
// on its volume; this is the index and the call IDs to find it again.
type Store struct {
	mu   sync.Mutex
	path string
	jobs map[string]Job
}

// OpenStore opens dir/downloads.json, or starts an empty one.
func OpenStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "downloads.json"), jobs: map[string]Job{}}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Job
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s is damaged: %w", s.path, err)
	}
	for _, j := range list {
		s.jobs[j.ID] = j
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	list := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, j)
	}
	sort.Slice(list, func(a, b int) bool {
		return list[a].Created < list[b].Created || list[a].Created == list[b].Created && list[a].ID < list[b].ID
	})
	raw, _ := json.MarshalIndent(list, "", "  ")
	return config.WriteAtomic(s.path, raw)
}

// Put adds or replaces a job.
func (s *Store) Put(j Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[j.ID] = j
	return s.saveLocked()
}

// Update changes a job in place.
func (s *Store) Update(id string, f func(*Job)) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("download %s not found", id)
	}
	f(&j)
	s.jobs[id] = j
	return j, s.saveLocked()
}

func (s *Store) Get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok
}

// List returns the jobs, oldest first.
func (s *Store) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, j)
	}
	sort.Slice(list, func(a, b int) bool {
		return list[a].Created < list[b].Created || list[a].Created == list[b].Created && list[a].ID < list[b].ID
	})
	return list
}

// Remove forgets a job. Its records on the volume stay.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	return s.saveLocked()
}
