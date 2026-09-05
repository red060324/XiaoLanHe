package usecase

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

var ErrForbidden = errors.New("knowledge forbidden")
var knowledgeFilterPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

type Provider interface {
	Health(context.Context) (entity.Health, error)
	Search(context.Context, entity.SearchInput) (entity.SearchResult, error)
	Create(context.Context, string, string) (entity.AcceptedDocument, error)
	Track(context.Context, string) (entity.Track, error)
	List(context.Context, entity.ListInput) (entity.DocumentList, error)
	Delete(context.Context, string) (entity.DeleteResult, error)
}

type Service struct{ provider Provider }

func NewService(provider Provider) *Service { return &Service{provider: provider} }

func (s *Service) Ready(ctx context.Context) error {
	_, err := s.provider.Health(ctx)
	return err
}

func (s *Service) Search(ctx context.Context, in entity.SearchInput) (entity.SearchResult, error) {
	in.Query = strings.TrimSpace(in.Query)
	in.GameCode = strings.TrimSpace(in.GameCode)
	in.RegionCode = strings.TrimSpace(in.RegionCode)
	if in.Mode == "" {
		in.Mode = entity.ModeMix
	}
	if in.Limit == 0 {
		in.Limit = 5
	}
	if utf8.RuneCountInString(in.Query) < 1 || utf8.RuneCountInString(in.Query) > 100 || !in.Mode.Valid() || in.Limit < 1 || in.Limit > 10 || !optionalKnowledgeFilter(in.GameCode, 64) || !optionalKnowledgeFilter(in.RegionCode, 32) {
		return entity.SearchResult{}, entity.ErrInvalidInput
	}
	result, err := s.provider.Search(ctx, in)
	if err != nil {
		return entity.SearchResult{}, err
	}
	if len(result.Items) > in.Limit {
		result.Items = result.Items[:in.Limit]
	}
	return result, nil
}

func (s *Service) Create(ctx context.Context, principal auth.Principal, draft entity.DocumentDraft) (entity.AcceptedDocument, error) {
	if err := authorizeAdmin(principal); err != nil {
		return entity.AcceptedDocument{}, err
	}
	_, sourceKey, envelope, err := entity.NormalizeDraft(draft)
	if err != nil {
		return entity.AcceptedDocument{}, err
	}
	accepted, err := s.provider.Create(ctx, sourceKey, envelope)
	accepted.SourceKey = sourceKey
	if err != nil {
		return accepted, err
	}
	accepted.Status = "accepted"
	return accepted, nil
}

func (s *Service) Track(ctx context.Context, principal auth.Principal, trackID string) (entity.Track, error) {
	if err := authorizeAdmin(principal); err != nil {
		return entity.Track{}, err
	}
	if !opaqueID(trackID) {
		return entity.Track{}, entity.ErrInvalidInput
	}
	return s.provider.Track(ctx, trackID)
}

func (s *Service) List(ctx context.Context, principal auth.Principal, in entity.ListInput) (entity.DocumentList, error) {
	if err := authorizeAdmin(principal); err != nil {
		return entity.DocumentList{}, err
	}
	if in.Page == 0 {
		in.Page = 1
	}
	if in.PageSize == 0 {
		in.PageSize = 20
	}
	if in.SortField == "" {
		in.SortField = "updatedAt"
	}
	if in.SortDirection == "" {
		in.SortDirection = "desc"
	}
	validSort := in.SortField == "createdAt" || in.SortField == "updatedAt" || in.SortField == "documentId" || in.SortField == "sourceKey"
	validDirection := in.SortDirection == "asc" || in.SortDirection == "desc"
	in.Status = strings.ToUpper(strings.TrimSpace(in.Status))
	if in.Page < 1 || in.Page > 10_000 || in.PageSize < 10 || in.PageSize > 100 || !validSort || !validDirection || !validKnowledgeStatus(in.Status) {
		return entity.DocumentList{}, entity.ErrInvalidInput
	}
	return s.provider.List(ctx, in)
}

func (s *Service) Delete(ctx context.Context, principal auth.Principal, documentID string) (entity.DeleteResult, error) {
	if err := authorizeAdmin(principal); err != nil {
		return entity.DeleteResult{}, err
	}
	if !opaqueID(documentID) {
		return entity.DeleteResult{}, entity.ErrInvalidInput
	}
	return s.provider.Delete(ctx, documentID)
}

func authorizeAdmin(principal auth.Principal) error {
	if principal.UserID <= 0 {
		return auth.ErrUnauthenticated
	}
	if !principal.IsAdmin() {
		return ErrForbidden
	}
	return nil
}

var opaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func opaqueID(value string) bool { return opaqueIDPattern.MatchString(strings.TrimSpace(value)) }
func optionalKnowledgeFilter(value string, max int) bool {
	return value == "" || len(value) <= max && knowledgeFilterPattern.MatchString(value)
}
func validKnowledgeStatus(value string) bool {
	if value == "" {
		return true
	}
	switch value {
	case "PENDING", "PARSING", "ANALYZING", "PREPROCESSED", "PROCESSING", "PROCESSED", "FAILED":
		return true
	default:
		return false
	}
}
