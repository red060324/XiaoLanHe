package entity

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid community content")

type Status string

const (
	StatusPublished Status = "published"
	StatusHidden    Status = "hidden"
	StatusDeleted   Status = "deleted"
)

type ReactionType string

const (
	ReactionLike    ReactionType = "like"
	ReactionHelpful ReactionType = "helpful"
	ReactionFunny   ReactionType = "funny"
)

type Author struct {
	ID          int64
	Username    string
	DisplayName string
}

type Game struct {
	ID   int64
	Slug string
	Name string
}

type ReactionSummary struct {
	Counts          map[ReactionType]int64
	ViewerReactions []ReactionType
}

type Post struct {
	ID           int64
	Author       Author
	Game         *Game
	Title        string
	Content      string
	Status       Status
	CommentCount int64
	Reactions    ReactionSummary
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type PostDraft struct {
	GameID  int64
	Title   string
	Content string
}

func (d PostDraft) Normalize() (PostDraft, error) {
	d.Title = strings.TrimSpace(d.Title)
	d.Content = strings.TrimSpace(d.Content)
	if d.GameID < 0 || utf8.RuneCountInString(d.Title) < 1 || utf8.RuneCountInString(d.Title) > 160 || utf8.RuneCountInString(d.Content) < 1 || utf8.RuneCountInString(d.Content) > 10000 {
		return PostDraft{}, ErrInvalid
	}
	return d, nil
}

type Comment struct {
	ID        int64
	PostID    int64
	Author    Author
	Content   string
	Status    Status
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NormalizeComment(content string) (string, error) {
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) < 1 || utf8.RuneCountInString(content) > 3000 {
		return "", ErrInvalid
	}
	return content, nil
}

func ParseReaction(value string) (ReactionType, error) {
	reaction := ReactionType(strings.ToLower(strings.TrimSpace(value)))
	switch reaction {
	case ReactionLike, ReactionHelpful, ReactionFunny:
		return reaction, nil
	default:
		return "", ErrInvalid
	}
}

func ParseModerationStatus(value string) (Status, error) {
	status := Status(strings.ToLower(strings.TrimSpace(value)))
	if status != StatusPublished && status != StatusHidden {
		return "", ErrInvalid
	}
	return status, nil
}
