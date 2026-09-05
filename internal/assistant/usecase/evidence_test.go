package usecase

import (
	"errors"
	"testing"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
)

func TestEvidenceStoreOwnsIDsAndRejectsForeignIDs(t *testing.T) {
	store := NewEvidenceStore()
	first := store.Add(entity.Evidence{ID: "forged", Source: " lightrag ", Content: " fact "})
	second := store.Add(entity.Evidence{Source: "catalog", Content: "game"})
	if first.ID != "ev_1" || second.ID != "ev_2" || first.Source != "lightrag" || first.Content != "fact" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	values, err := store.Get([]string{"ev_2", "ev_1", "ev_2"})
	if err != nil || len(values) != 2 || values[0].ID != "ev_2" {
		t.Fatalf("values=%+v err=%v", values, err)
	}
	if _, err := store.Get([]string{"ev_foreign"}); !errors.Is(err, entity.ErrInvalidAgentContract) {
		t.Fatalf("err=%v", err)
	}
}
