package usecase

import (
	"fmt"
	"strings"
	"sync"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
)

type EvidenceStore struct {
	mu     sync.RWMutex
	next   int
	values map[string]entity.Evidence
}

func NewEvidenceStore() *EvidenceStore {
	return &EvidenceStore{values: make(map[string]entity.Evidence)}
}

func (s *EvidenceStore) Add(value entity.Evidence) entity.Evidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	value.ID = fmt.Sprintf("ev_%d", s.next)
	value.Source = strings.TrimSpace(value.Source)
	value.Title = strings.TrimSpace(value.Title)
	value.Content = strings.TrimSpace(value.Content)
	value.URL = strings.TrimSpace(value.URL)
	s.values[value.ID] = value
	return value
}

func (s *EvidenceStore) Get(ids []string) ([]entity.Evidence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]entity.Evidence, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		value, ok := s.values[id]
		if !ok {
			return nil, entity.ErrInvalidAgentContract
		}
		seen[id] = true
		result = append(result, value)
	}
	return result, nil
}

func (s *EvidenceStore) IDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]string, 0, len(s.values))
	for index := 1; index <= s.next; index++ {
		result = append(result, fmt.Sprintf("ev_%d", index))
	}
	return result
}
