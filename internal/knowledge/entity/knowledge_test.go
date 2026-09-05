package entity

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeDraft(t *testing.T) {
	draft := DocumentDraft{SourceType: " guide ", Title: " Build Guide ", SourceURL: "https://example.com/guide", GameCode: "game", RegionCode: "CN", PatchVersion: "1.0", ContentText: " body "}
	normalized, firstKey, envelope, err := NormalizeDraft(draft)
	if err != nil {
		t.Fatal(err)
	}
	_, secondKey, _, err := NormalizeDraft(draft)
	if err != nil || firstKey != secondKey || !IsManagedSource(firstKey) {
		t.Fatalf("keys=%q/%q err=%v", firstKey, secondKey, err)
	}
	if normalized.ContentText != "body" || !strings.Contains(envelope, "XiaoLanHe-Knowledge-v1\nTitle: Build Guide") || !strings.HasSuffix(envelope, "\n\nbody") {
		t.Fatalf("normalized=%+v envelope=%q", normalized, envelope)
	}
	draft.ContentText = "changed"
	_, changedKey, _, _ := NormalizeDraft(draft)
	if changedKey == firstKey {
		t.Fatal("content must contribute to deterministic source key")
	}
}

func TestNormalizeDraftRejectsUnsafeMetadata(t *testing.T) {
	valid := DocumentDraft{SourceType: "guide", Title: "title", ContentText: "body"}
	for name, mutate := range map[string]func(*DocumentDraft){
		"header injection": func(d *DocumentDraft) { d.Title = "title\nInjected: yes" },
		"unsafe URL":       func(d *DocumentDraft) { d.SourceURL = "javascript:alert(1)" },
		"blank content":    func(d *DocumentDraft) { d.ContentText = "   " },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if _, _, _, err := NormalizeDraft(candidate); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestManagedSource(t *testing.T) {
	valid := "xlh-" + strings.Repeat("a", 64) + ".txt"
	if !IsManagedSource(valid) || !IsManagedSource("xlh-legacy-42.txt") {
		t.Fatal("expected managed sources")
	}
	for _, value := range []string{"../" + valid, "other.txt", "xlh-legacy-0.txt", "xlh-" + strings.Repeat("A", 64) + ".txt"} {
		if IsManagedSource(value) {
			t.Fatalf("managed=%q", value)
		}
	}
}
