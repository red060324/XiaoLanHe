package entity

import (
	"strings"
	"testing"
)

func TestPostDraftNormalize(t *testing.T) {
	t.Run("trims valid content", func(t *testing.T) {
		draft, err := (PostDraft{GameID: 2, Title: " 标题 ", Content: " 内容 "}).Normalize()
		if err != nil || draft.Title != "标题" || draft.Content != "内容" {
			t.Fatalf("draft=%+v err=%v", draft, err)
		}
	})

	t.Run("accepts unicode boundary", func(t *testing.T) {
		if _, err := (PostDraft{Title: strings.Repeat("界", 160), Content: "内容"}).Normalize(); err != nil {
			t.Fatal(err)
		}
	})

	for name, draft := range map[string]PostDraft{
		"negative game": {GameID: -1, Title: "标题", Content: "内容"},
		"blank title":   {Title: " \n ", Content: "内容"},
		"long title":    {Title: strings.Repeat("界", 161), Content: "内容"},
		"blank content": {Title: "标题", Content: " \n "},
		"long content":  {Title: "标题", Content: strings.Repeat("界", 10001)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := draft.Normalize(); err != ErrInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestNormalizeComment(t *testing.T) {
	content, err := NormalizeComment(" 评论 ")
	if err != nil || content != "评论" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if _, err := NormalizeComment(strings.Repeat("界", 3000)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{" \n ", strings.Repeat("界", 3001)} {
		if _, err := NormalizeComment(value); err != ErrInvalid {
			t.Fatalf("value length=%d err=%v", len(value), err)
		}
	}
}

func TestParseReaction(t *testing.T) {
	for input, want := range map[string]ReactionType{
		" LIKE ": ReactionLike, "helpful": ReactionHelpful, "Funny": ReactionFunny,
	} {
		got, err := ParseReaction(input)
		if err != nil || got != want {
			t.Fatalf("input=%q got=%q err=%v", input, got, err)
		}
	}
	if _, err := ParseReaction("love"); err != ErrInvalid {
		t.Fatalf("err=%v", err)
	}
}

func TestParseModerationStatus(t *testing.T) {
	for input, want := range map[string]Status{" PUBLISHED ": StatusPublished, "Hidden": StatusHidden} {
		got, err := ParseModerationStatus(input)
		if err != nil || got != want {
			t.Fatalf("input=%q got=%q err=%v", input, got, err)
		}
	}
	for _, input := range []string{"deleted", "draft", ""} {
		if _, err := ParseModerationStatus(input); err != ErrInvalid {
			t.Fatalf("input=%q err=%v", input, err)
		}
	}
}
