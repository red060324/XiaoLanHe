package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
)

type ProfileStore struct{ pool *pgxpool.Pool }

func NewProfileStore(pool *pgxpool.Pool) *ProfileStore { return &ProfileStore{pool: pool} }

type assistantPreferences struct {
	FavoriteGenres     []string `json:"favoriteGenres"`
	PreferredPlatforms []string `json:"preferredPlatforms"`
	PreferredLanguages []string `json:"preferredLanguages"`
	MaxPriceMinor      *int64   `json:"maxPriceMinor,omitempty"`
	Currency           string   `json:"currency,omitempty"`
}

func (s *ProfileStore) LoadAssistantProfile(ctx context.Context, userID int64) (entity.Profile, bool, error) {
	var raw []byte
	var profile entity.Profile
	var found bool
	err := s.pool.QueryRow(ctx, `
		select coalesce(default_region,''), coalesce(preferences->'assistant','{}'::jsonb),
		       preferences ? 'assistant', updated_at
		from player_profile where user_id=$1`, userID).Scan(&profile.DefaultRegion, &raw, &found, &profile.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.EmptyProfile(), false, nil
	}
	if err != nil {
		return entity.Profile{}, false, fmt.Errorf("load assistant profile: %w", err)
	}
	if !found {
		return entity.EmptyProfile(), false, nil
	}
	var preferences assistantPreferences
	if err := json.Unmarshal(raw, &preferences); err != nil {
		return entity.Profile{}, false, fmt.Errorf("decode assistant profile: %w", err)
	}
	profile.FavoriteGenres = nonNil(preferences.FavoriteGenres)
	profile.PreferredPlatforms = nonNil(preferences.PreferredPlatforms)
	profile.PreferredLanguages = nonNil(preferences.PreferredLanguages)
	profile.MaxPriceMinor = preferences.MaxPriceMinor
	profile.Currency = preferences.Currency
	return profile, found, nil
}

func (s *ProfileStore) ReplaceAssistantProfile(ctx context.Context, userID int64, profile entity.Profile) (entity.Profile, error) {
	raw, err := json.Marshal(assistantPreferences{
		FavoriteGenres: profile.FavoriteGenres, PreferredPlatforms: profile.PreferredPlatforms,
		PreferredLanguages: profile.PreferredLanguages, MaxPriceMinor: profile.MaxPriceMinor, Currency: profile.Currency,
	})
	if err != nil {
		return entity.Profile{}, fmt.Errorf("encode assistant profile: %w", err)
	}
	var stored entity.Profile
	var storedRaw []byte
	err = s.pool.QueryRow(ctx, `
		insert into player_profile(user_id,default_region,preferences)
		values ($1,nullif($2,''),jsonb_build_object('assistant',$3::jsonb))
		on conflict (user_id) where user_id is not null do update set
			default_region=excluded.default_region,
			preferences=jsonb_set(coalesce(player_profile.preferences,'{}'::jsonb),'{assistant}',$3::jsonb,true),
			updated_at=now()
		returning coalesce(default_region,''),preferences->'assistant',updated_at`, userID, profile.DefaultRegion, raw).
		Scan(&stored.DefaultRegion, &storedRaw, &stored.UpdatedAt)
	if err != nil {
		return entity.Profile{}, fmt.Errorf("replace assistant profile: %w", err)
	}
	var preferences assistantPreferences
	if err := json.Unmarshal(storedRaw, &preferences); err != nil {
		return entity.Profile{}, fmt.Errorf("decode stored assistant profile: %w", err)
	}
	stored.FavoriteGenres = nonNil(preferences.FavoriteGenres)
	stored.PreferredPlatforms = nonNil(preferences.PreferredPlatforms)
	stored.PreferredLanguages = nonNil(preferences.PreferredLanguages)
	stored.MaxPriceMinor = preferences.MaxPriceMinor
	stored.Currency = preferences.Currency
	return stored, nil
}

func (s *ProfileStore) ClearAssistantProfile(ctx context.Context, userID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin clear assistant profile: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	err = tx.QueryRow(ctx, `select id from player_profile where user_id=$1 for update`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return fmt.Errorf("lock assistant profile: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		update player_profile
		set default_region=null,preferences=preferences-'assistant',updated_at=now()
		where id=$1`, id); err != nil {
		return fmt.Errorf("clear assistant profile: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		delete from player_profile
		where id=$1 and default_game is null and default_region is null and preferences='{}'::jsonb`, id); err != nil {
		return fmt.Errorf("remove empty player profile: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit clear assistant profile: %w", err)
	}
	return nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

var _ assistant.ProfileStore = (*ProfileStore)(nil)
