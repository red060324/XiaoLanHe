package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
)

const profileReconciliationTimeout = 5 * time.Second

const loadAssistantProfileSQL = `
	select coalesce(default_region, ''),
	       coalesce(json_extract(preferences, '$.assistant'), json_object()),
	       json_contains_path(preferences, 'one', '$.assistant'),
	       updated_at
	from player_profile
	where user_id = ?`

const replaceAssistantProfileSQL = `
	insert into player_profile(user_id, default_region, preferences)
	values (?, nullif(?, ''), json_object('assistant', cast(? as json))) as new
	on duplicate key update
		default_region = new.default_region,
		preferences = json_set(coalesce(player_profile.preferences, json_object()), '$.assistant',
			json_extract(new.preferences, '$.assistant')),
		updated_at = utc_timestamp(6)`

const readStoredAssistantProfileSQL = `
	select coalesce(default_region, ''),
	       json_extract(preferences, '$.assistant'),
	       updated_at
	from player_profile
	where user_id = ?`

const lockAssistantProfileUserSQL = `
	select id
	from user_account
	where id = ?
	for update`

const lockAssistantProfileSQL = `
	select id
	from player_profile
	where user_id = ?
	for update`

const clearAssistantProfileSQL = `
	update player_profile
	set default_region = null,
	    preferences = json_remove(preferences, '$.assistant'),
	    updated_at = utc_timestamp(6)
	where id = ?`

const removeEmptyPlayerProfileSQL = `
	delete from player_profile
	where id = ?
	  and default_game is null
	  and default_region is null
	  and json_type(preferences) = 'OBJECT'
	  and json_length(preferences) = 0`

type ProfileStore struct{ db *sql.DB }

func NewProfileStore(db *sql.DB) *ProfileStore { return &ProfileStore{db: db} }

type assistantPreferences struct {
	FavoriteGenres     []string `json:"favoriteGenres"`
	PreferredPlatforms []string `json:"preferredPlatforms"`
	PreferredLanguages []string `json:"preferredLanguages"`
	MaxPriceMinor      *int64   `json:"maxPriceMinor,omitempty"`
	Currency           string   `json:"currency,omitempty"`
}

func (s *ProfileStore) LoadAssistantProfile(ctx context.Context, userID int64) (entity.Profile, bool, error) {
	profile, found, err := loadAssistantProfile(ctx, s.db, userID)
	if err != nil {
		return entity.Profile{}, false, fmt.Errorf("load assistant profile: %w", err)
	}
	return profile, found, nil
}

type profileQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadAssistantProfile(ctx context.Context, queryer profileQueryer, userID int64) (entity.Profile, bool, error) {
	var raw []byte
	var profile entity.Profile
	var found bool
	err := queryer.QueryRowContext(ctx, loadAssistantProfileSQL, userID).Scan(
		&profile.DefaultRegion, &raw, &found, &profile.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.EmptyProfile(), false, nil
	}
	if err != nil {
		return entity.Profile{}, false, err
	}
	if !found {
		return entity.EmptyProfile(), false, nil
	}
	if err := decodeAssistantPreferences(raw, &profile); err != nil {
		return entity.Profile{}, false, fmt.Errorf("decode assistant profile: %w", err)
	}
	profile.UpdatedAt = profile.UpdatedAt.UTC()
	return profile, true, nil
}

func (s *ProfileStore) ReplaceAssistantProfile(ctx context.Context, userID int64, profile entity.Profile) (entity.Profile, error) {
	raw, err := json.Marshal(assistantPreferences{
		FavoriteGenres: profile.FavoriteGenres, PreferredPlatforms: profile.PreferredPlatforms,
		PreferredLanguages: profile.PreferredLanguages, MaxPriceMinor: profile.MaxPriceMinor, Currency: profile.Currency,
	})
	if err != nil {
		return entity.Profile{}, fmt.Errorf("encode assistant profile: %w", err)
	}

	expected := entity.Profile{DefaultRegion: profile.DefaultRegion}
	if err := decodeAssistantPreferences(raw, &expected); err != nil {
		return entity.Profile{}, fmt.Errorf("decode encoded assistant profile: %w", err)
	}

	stored, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (entity.Profile, error) {
		if err := lockAssistantProfileUser(ctx, tx, userID); err != nil {
			return entity.Profile{}, err
		}
		if _, err := tx.ExecContext(ctx, replaceAssistantProfileSQL, userID, profile.DefaultRegion, raw); err != nil {
			return entity.Profile{}, fmt.Errorf("replace assistant profile: %w", err)
		}

		var stored entity.Profile
		var storedRaw []byte
		if err := tx.QueryRowContext(ctx, readStoredAssistantProfileSQL, userID).Scan(
			&stored.DefaultRegion, &storedRaw, &stored.UpdatedAt,
		); err != nil {
			return entity.Profile{}, fmt.Errorf("read replaced assistant profile: %w", err)
		}
		if err := decodeAssistantPreferences(storedRaw, &stored); err != nil {
			return entity.Profile{}, fmt.Errorf("decode stored assistant profile: %w", err)
		}
		stored.UpdatedAt = stored.UpdatedAt.UTC()
		return stored, nil
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return stored, err
	}
	reconcileCtx, cancel := profileReconciliationContext(ctx)
	defer cancel()
	return s.reconcileReplacedAssistantProfile(reconcileCtx, userID, expected, err)
}

func (s *ProfileStore) ClearAssistantProfile(ctx context.Context, userID int64) error {
	_, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		if err := lockAssistantProfileUser(ctx, tx, userID); err != nil {
			return struct{}{}, err
		}
		var id int64
		err := tx.QueryRowContext(ctx, lockAssistantProfileSQL, userID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, nil
		}
		if err != nil {
			return struct{}{}, fmt.Errorf("lock assistant profile: %w", err)
		}
		if _, err := tx.ExecContext(ctx, clearAssistantProfileSQL, id); err != nil {
			return struct{}{}, fmt.Errorf("clear assistant profile: %w", err)
		}
		if _, err := tx.ExecContext(ctx, removeEmptyPlayerProfileSQL, id); err != nil {
			return struct{}{}, fmt.Errorf("remove empty player profile: %w", err)
		}
		return struct{}{}, nil
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return err
	}
	reconcileCtx, cancel := profileReconciliationContext(ctx)
	defer cancel()
	return s.reconcileClearedAssistantProfile(reconcileCtx, userID, err)
}

func (s *ProfileStore) reconcileReplacedAssistantProfile(ctx context.Context, userID int64, expected entity.Profile, commitErr error) (entity.Profile, error) {
	tx, err := s.beginProfileReconciliation(ctx, userID)
	if err != nil {
		return entity.Profile{}, errors.Join(commitErr, fmt.Errorf("begin assistant profile replacement reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	stored, found, err := loadAssistantProfile(ctx, tx, userID)
	if err != nil {
		return entity.Profile{}, errors.Join(commitErr, fmt.Errorf("reconcile assistant profile replacement: %w", err))
	}
	if !found || !sameAssistantProfile(stored, expected) {
		return entity.Profile{}, errors.Join(commitErr, errors.New("assistant profile replacement durable state mismatch"))
	}
	return stored, nil
}

func (s *ProfileStore) reconcileClearedAssistantProfile(ctx context.Context, userID int64, commitErr error) error {
	tx, err := s.beginProfileReconciliation(ctx, userID)
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("begin assistant profile clear reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	_, found, err := loadAssistantProfile(ctx, tx, userID)
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("reconcile assistant profile clear: %w", err))
	}
	if found {
		return errors.Join(commitErr, errors.New("assistant profile clear durable state mismatch"))
	}
	return nil
}

func (s *ProfileStore) beginProfileReconciliation(ctx context.Context, userID int64) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, profileTxOptions())
	if err != nil {
		return nil, err
	}
	if err := lockAssistantProfileUser(ctx, tx, userID); err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return nil, errors.Join(err, fmt.Errorf("rollback assistant profile reconciliation: %w", rollbackErr))
		}
		return nil, err
	}
	return tx, nil
}

func sameAssistantProfile(left, right entity.Profile) bool {
	return left.DefaultRegion == right.DefaultRegion && left.Currency == right.Currency &&
		slices.Equal(left.FavoriteGenres, right.FavoriteGenres) &&
		slices.Equal(left.PreferredPlatforms, right.PreferredPlatforms) &&
		slices.Equal(left.PreferredLanguages, right.PreferredLanguages) &&
		sameOptionalInt64(left.MaxPriceMinor, right.MaxPriceMinor)
}

func sameOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func lockAssistantProfileUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	var lockedUserID int64
	if err := tx.QueryRowContext(ctx, lockAssistantProfileUserSQL, userID).Scan(&lockedUserID); err != nil {
		return fmt.Errorf("lock assistant profile user: %w", err)
	}
	return nil
}

func profileTxOptions() *sql.TxOptions {
	return &sql.TxOptions{Isolation: sql.LevelReadCommitted}
}

func profileReconciliationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), profileReconciliationTimeout)
}

func decodeAssistantPreferences(raw []byte, profile *entity.Profile) error {
	var preferences assistantPreferences
	if err := json.Unmarshal(raw, &preferences); err != nil {
		return err
	}
	profile.FavoriteGenres = nonNil(preferences.FavoriteGenres)
	profile.PreferredPlatforms = nonNil(preferences.PreferredPlatforms)
	profile.PreferredLanguages = nonNil(preferences.PreferredLanguages)
	profile.MaxPriceMinor = preferences.MaxPriceMinor
	profile.Currency = preferences.Currency
	return nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

var _ assistant.ProfileStore = (*ProfileStore)(nil)
