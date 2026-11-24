package secret

import "sync"

// MemoryPINStore is a PINStore in memory, for tests. ExistsErr and ReadErr,
// when set, are what Exists and Read answer, as a locked keychain would.
type MemoryPINStore struct {
	ExistsErr, ReadErr error

	mu    sync.Mutex
	items map[string]string
	reads int
}

// Reads is how many times Read ran.
func (s *MemoryPINStore) Reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func (s *MemoryPINStore) Save(account, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]string{}
	}
	s.items[account] = value
	return nil
}

func (s *MemoryPINStore) Delete(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, account)
	return nil
}

func (s *MemoryPINStore) Exists(account string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ExistsErr != nil {
		return false, s.ExistsErr
	}
	_, ok := s.items[account]
	return ok, nil
}

func (s *MemoryPINStore) Read(account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.ReadErr != nil {
		return "", s.ReadErr
	}
	v, ok := s.items[account]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}
