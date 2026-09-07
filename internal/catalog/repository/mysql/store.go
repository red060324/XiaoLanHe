package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

const catalogReconciliationTimeout = 5 * time.Second

type catalogSaveAttempt struct {
	id               int64
	priceReplacement time.Time
	hasPriceMutation bool
}

const (
	existsSQL = `select exists(select 1 from game where id=? and status='active')`

	findPurchaseOfferSQL = `
		select g.id,g.slug,g.name,e.id,e.code,e.name,p.amount_minor,p.currency,p.region_code
		from game_edition e
		join game g on g.id=e.game_id
		join game_price p on p.edition_id=e.id
		where e.id=? and e.status='active' and g.status='active'
			and p.currency=? and p.region_code in (?,'GLOBAL')
			and p.active_from<=utc_timestamp(6)
			and (p.active_until is null or p.active_until>utc_timestamp(6))
		order by case when p.region_code=? then 0 else 1 end,p.active_from desc
		limit 1`

	listSQL = `
		select g.id,g.slug,g.name,g.summary,g.cover_url,g.release_date,
			exists(select 1 from game_entitlement ge join game_edition e on e.id=ge.edition_id where ge.user_id=? and ge.status='active' and e.game_id=g.id)
		from game g
		where g.status='active' and (?=0 or g.id<?)
			and (?='' or instr(lower(g.name),lower(?))>0 or instr(lower(g.slug),lower(?))>0)
		order by g.id desc limit ?`

	findBySlugSQL = `
		select g.id,g.slug,g.name,g.summary,g.description,g.developer,g.publisher,g.release_date,g.cover_url,
			exists(select 1 from game_entitlement ge join game_edition e on e.id=ge.edition_id where ge.user_id=? and ge.status='active' and e.game_id=g.id)
		from game g where g.slug=? and g.status='active'`

	insertGameSQL = `
		insert into game(slug,name,summary,description,developer,publisher,release_date,cover_url,status)
		values (?,?,?,?,?,?,?,?,'active')`

	updateGameSQL = `
		update game set slug=?,name=?,summary=?,description=?,developer=?,publisher=?,release_date=?,cover_url=?,status='active',updated_at=utc_timestamp(6)
		where id=?`

	lockGameSQL = `select id from game where id=? for update`

	upsertEditionSQL = `
		insert into game_edition(game_id,code,name,description,status)
		values (?,?,?,?,'active') as new
		on duplicate key update name=new.name,description=new.description,status='active',updated_at=utc_timestamp(6)`

	findEditionIDSQL  = `select id from game_edition where game_id=? and code=?`
	lockGameBySlugSQL = `select id from game where slug=? for update`

	lockActivePriceStartsSQL = `
		select active_from from game_price
		where edition_id=? and active_until is null
		order by region_code,currency,active_from,id for update`

	priceReplacementClockSQL = `
		select case
			when max_active_from is null or db_now>max_active_from then db_now
			else timestampadd(microsecond,1,max_active_from)
		end
		from (
			select utc_timestamp(6) as db_now,cast(? as datetime(6)) as max_active_from
		) as replacement_clock`

	closeEditionPricesSQL = `
		update game_price set active_until=?,updated_at=?
		where edition_id=? and active_until is null`

	insertPriceSQL = `
		insert into game_price(edition_id,region_code,currency,amount_minor,active_from) values (?,?,?,?,?)`

	deactivateAllEditionsSQL = `
		update game_edition set status='inactive',updated_at=utc_timestamp(6) where game_id=?`

	findByIDSQL = `
		select id,slug,name,summary,description,developer,publisher,release_date,cover_url
		from game where id=?`

	loadEditionsSQL = `
		select e.id,e.code,e.name,e.description,
			exists(select 1 from game_entitlement ge where ge.user_id=? and ge.edition_id=e.id and ge.status='active'),
			p.amount_minor,p.currency,p.region_code
		from game_edition e
		left join (
			select ranked.edition_id,ranked.amount_minor,ranked.currency,ranked.region_code
			from (
				select gp.edition_id,gp.amount_minor,gp.currency,gp.region_code,
					row_number() over (partition by gp.edition_id order by case when gp.region_code=? then 0 else 1 end,gp.active_from desc) as price_rank
				from game_price gp
				where gp.currency=? and gp.region_code in (?,'GLOBAL')
					and gp.active_from<=utc_timestamp(6)
					and (gp.active_until is null or gp.active_until>utc_timestamp(6))
			) ranked
			where ranked.price_rank=1
		) p on p.edition_id=e.id
		where e.game_id=? and e.status='active' order by e.id`

	loadCatalogDraftSQL = `
		select slug,name,summary,description,developer,publisher,release_date,cover_url,status
		from game where id=?`

	loadCatalogDraftEditionsSQL = `
		select e.id,e.code,e.name,e.description,p.amount_minor,p.currency,p.region_code,p.active_from
		from game_edition e
		left join game_price p on p.edition_id=e.id and p.active_until is null
		where e.game_id=? and e.status='active'
		order by e.id,p.region_code,p.currency,p.id`
)

func (s *Store) Exists(ctx context.Context, id int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, existsSQL, id).Scan(&exists)
	return exists, err
}

func (s *Store) FindPurchaseOffer(ctx context.Context, editionID int64, pricing catalog.Pricing) (entity.PurchaseOffer, error) {
	var offer entity.PurchaseOffer
	err := s.db.QueryRowContext(ctx, findPurchaseOfferSQL, editionID, pricing.Currency, pricing.Region, pricing.Region).Scan(
		&offer.GameID, &offer.GameSlug, &offer.GameName, &offer.EditionID, &offer.EditionCode, &offer.EditionName,
		&offer.AmountMinor, &offer.Currency, &offer.Region,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.PurchaseOffer{}, catalog.ErrNotFound
	}
	return offer, err
}

func (s *Store) List(ctx context.Context, filter catalog.ListFilter) ([]entity.Game, error) {
	rows, err := s.db.QueryContext(ctx, listSQL,
		filter.ViewerID, filter.BeforeID, filter.BeforeID,
		filter.Query, filter.Query, filter.Query, filter.Limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]entity.Game, 0, filter.Limit)
	for rows.Next() {
		var item entity.Game
		var releaseDate sql.NullTime
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Summary, &item.CoverURL, &releaseDate, &item.Owned); err != nil {
			return nil, err
		}
		item.ReleaseDate = nullableDate(releaseDate)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) FindBySlug(ctx context.Context, slug string, pricing catalog.Pricing, viewerID int64) (entity.Game, error) {
	var game entity.Game
	var releaseDate sql.NullTime
	err := s.db.QueryRowContext(ctx, findBySlugSQL, viewerID, slug).Scan(
		&game.ID, &game.Slug, &game.Name, &game.Summary, &game.Description, &game.Developer, &game.Publisher,
		&releaseDate, &game.CoverURL, &game.Owned,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Game{}, catalog.ErrNotFound
	}
	if err != nil {
		return entity.Game{}, err
	}
	game.ReleaseDate = nullableDate(releaseDate)
	game.Editions, err = s.loadEditions(ctx, game.ID, pricing, viewerID)
	return game, err
}

func (s *Store) Save(ctx context.Context, id int64, draft entity.Draft) (entity.Game, error) {
	var attempted catalogSaveAttempt
	attempt, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (catalogSaveAttempt, error) {
		current, err := saveCatalogAggregate(ctx, tx, id, draft)
		if err == nil {
			attempted = current
		}
		return current, err
	})
	if err == nil {
		return s.findByID(ctx, attempt.id)
	}
	if !mysqltx.IsCommitOutcomeUnknown(err) {
		return entity.Game{}, err
	}
	reconcileCtx, cancel := catalogReconciliationContext(ctx)
	defer cancel()
	return s.reconcileSave(reconcileCtx, id, draft, attempted, err)
}

func saveCatalogAggregate(ctx context.Context, tx *sql.Tx, id int64, draft entity.Draft) (catalogSaveAttempt, error) {
	var err error
	if id == 0 {
		result, execErr := tx.ExecContext(ctx, insertGameSQL,
			draft.Slug, draft.Name, draft.Summary, draft.Description, draft.Developer, draft.Publisher, dateValue(draft.ReleaseDate), draft.CoverURL,
		)
		if execErr != nil {
			return catalogSaveAttempt{}, catalogError(execErr)
		}
		id, err = result.LastInsertId()
		if err != nil {
			return catalogSaveAttempt{}, err
		}
		if err := lockGame(ctx, tx, id); err != nil {
			return catalogSaveAttempt{}, err
		}
	} else {
		if err := lockGame(ctx, tx, id); err != nil {
			return catalogSaveAttempt{}, err
		}
		result, execErr := tx.ExecContext(ctx, updateGameSQL,
			draft.Slug, draft.Name, draft.Summary, draft.Description, draft.Developer, draft.Publisher, dateValue(draft.ReleaseDate), draft.CoverURL, id,
		)
		if execErr != nil {
			return catalogSaveAttempt{}, catalogError(execErr)
		}
		if _, rowsErr := result.RowsAffected(); rowsErr != nil {
			return catalogSaveAttempt{}, rowsErr
		}
	}

	if _, err = tx.ExecContext(ctx, deactivateAllEditionsSQL, id); err != nil {
		return catalogSaveAttempt{}, err
	}
	replacements := make([]priceReplacement, len(draft.Editions))
	for i, edition := range draft.Editions {
		if _, err = tx.ExecContext(ctx, upsertEditionSQL, id, edition.Code, edition.Name, edition.Description); err != nil {
			return catalogSaveAttempt{}, catalogError(err)
		}
		replacements[i].prices = edition.Prices
		if err = tx.QueryRowContext(ctx, findEditionIDSQL, id, edition.Code).Scan(&replacements[i].editionID); err != nil {
			return catalogSaveAttempt{}, err
		}
	}

	sort.Slice(replacements, func(i, j int) bool { return replacements[i].editionID < replacements[j].editionID })
	var latestActiveFrom time.Time
	var hasActivePrice bool
	for _, replacement := range replacements {
		activeFrom, found, lockErr := lockLatestActivePriceStart(ctx, tx, replacement.editionID)
		if lockErr != nil {
			return catalogSaveAttempt{}, lockErr
		}
		if found && (!hasActivePrice || activeFrom.After(latestActiveFrom)) {
			latestActiveFrom = activeFrom
			hasActivePrice = true
		}
	}

	if len(replacements) > 0 {
		var maxActiveFrom any
		if hasActivePrice {
			maxActiveFrom = latestActiveFrom
		}
		var replacement sql.NullTime
		if err = tx.QueryRowContext(ctx, priceReplacementClockSQL, maxActiveFrom).Scan(&replacement); err != nil {
			return catalogSaveAttempt{}, err
		}
		if !replacement.Valid {
			return catalogSaveAttempt{}, errors.New("catalog price replacement timestamp is not representable")
		}
		replacementAt := replacement.Time.UTC().Truncate(time.Microsecond)
		if hasActivePrice && !replacementAt.After(latestActiveFrom) {
			return catalogSaveAttempt{}, errors.New("catalog price replacement timestamp is not after active price")
		}
		for _, replacement := range replacements {
			if _, err = tx.ExecContext(ctx, closeEditionPricesSQL, replacementAt, replacementAt, replacement.editionID); err != nil {
				return catalogSaveAttempt{}, err
			}
			for _, price := range replacement.prices {
				if _, err = tx.ExecContext(ctx, insertPriceSQL, replacement.editionID, price.Region, price.Currency, price.AmountMinor, replacementAt); err != nil {
					return catalogSaveAttempt{}, catalogError(err)
				}
			}
		}
		return catalogSaveAttempt{id: id, priceReplacement: replacementAt, hasPriceMutation: true}, nil
	}
	return catalogSaveAttempt{id: id}, nil
}

func (s *Store) reconcileSave(ctx context.Context, id int64, draft entity.Draft, attempt catalogSaveAttempt, commitErr error) (entity.Game, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return entity.Game{}, errors.Join(commitErr, fmt.Errorf("begin catalog save reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	if id == 0 {
		err = tx.QueryRowContext(ctx, lockGameBySlugSQL, draft.Slug).Scan(&id)
	} else {
		err = lockGame(ctx, tx, id)
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, catalog.ErrNotFound) {
		return entity.Game{}, commitErr
	}
	if err != nil {
		return entity.Game{}, errors.Join(commitErr, fmt.Errorf("lock catalog save reconciliation target: %w", err))
	}
	if attempt.id != 0 && id != attempt.id {
		return entity.Game{}, errors.Join(commitErr, errors.New("catalog save durable game identity mismatch"))
	}

	stored, err := loadCatalogDraft(ctx, tx, id, attempt)
	if err != nil {
		return entity.Game{}, errors.Join(commitErr, fmt.Errorf("load catalog save reconciliation state: %w", err))
	}
	if !sameCatalogDraft(stored, draft) {
		return entity.Game{}, errors.Join(commitErr, errors.New("catalog save durable state mismatch"))
	}
	game, err := findByID(ctx, tx, id)
	if err != nil {
		return entity.Game{}, errors.Join(commitErr, fmt.Errorf("read reconciled catalog game: %w", err))
	}
	return game, nil
}

type priceReplacement struct {
	editionID int64
	prices    []entity.Price
}

type catalogQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadCatalogDraft(ctx context.Context, queryer catalogQueryer, id int64, attempt catalogSaveAttempt) (entity.Draft, error) {
	var draft entity.Draft
	var releaseDate sql.NullTime
	var status string
	if err := queryer.QueryRowContext(ctx, loadCatalogDraftSQL, id).Scan(
		&draft.Slug, &draft.Name, &draft.Summary, &draft.Description, &draft.Developer, &draft.Publisher,
		&releaseDate, &draft.CoverURL, &status,
	); err != nil {
		return entity.Draft{}, err
	}
	if status != "active" {
		return entity.Draft{}, errors.New("catalog game is not active after save")
	}
	draft.ReleaseDate = nullableDate(releaseDate)

	rows, err := queryer.QueryContext(ctx, loadCatalogDraftEditionsSQL, id)
	if err != nil {
		return entity.Draft{}, err
	}
	defer rows.Close()

	var lastEditionID int64
	for rows.Next() {
		var editionID int64
		var code, name, description string
		var amount sql.NullInt64
		var currency, region sql.NullString
		var activeFrom sql.NullTime
		if err := rows.Scan(&editionID, &code, &name, &description, &amount, &currency, &region, &activeFrom); err != nil {
			return entity.Draft{}, err
		}
		if amount.Valid != currency.Valid || amount.Valid != region.Valid || amount.Valid != activeFrom.Valid {
			return entity.Draft{}, errors.New("catalog reconciliation price columns have inconsistent nullability")
		}
		if editionID != lastEditionID {
			draft.Editions = append(draft.Editions, entity.EditionDraft{Code: code, Name: name, Description: description})
			lastEditionID = editionID
		}
		if amount.Valid {
			if !attempt.hasPriceMutation || !activeFrom.Time.UTC().Truncate(time.Microsecond).Equal(attempt.priceReplacement) {
				return entity.Draft{}, errors.New("catalog reconciliation active price generation mismatch")
			}
			last := &draft.Editions[len(draft.Editions)-1]
			last.Prices = append(last.Prices, entity.Price{AmountMinor: amount.Int64, Currency: currency.String, Region: region.String})
		}
	}
	if err := rows.Err(); err != nil {
		return entity.Draft{}, err
	}
	return draft, nil
}

func sameCatalogDraft(left, right entity.Draft) bool {
	if left.Slug != right.Slug || left.Name != right.Name || left.Summary != right.Summary ||
		left.Description != right.Description || left.Developer != right.Developer ||
		left.Publisher != right.Publisher || left.CoverURL != right.CoverURL ||
		!sameCatalogDate(left.ReleaseDate, right.ReleaseDate) || len(left.Editions) != len(right.Editions) {
		return false
	}

	expected := make(map[string]entity.EditionDraft, len(right.Editions))
	for _, edition := range right.Editions {
		expected[edition.Code] = edition
	}
	for _, edition := range left.Editions {
		want, ok := expected[edition.Code]
		if !ok || edition.Name != want.Name || edition.Description != want.Description || !sameCatalogPrices(edition.Prices, want.Prices) {
			return false
		}
		delete(expected, edition.Code)
	}
	return len(expected) == 0
}

func sameCatalogPrices(left, right []entity.Price) bool {
	if len(left) != len(right) {
		return false
	}
	expected := make(map[string]int64, len(right))
	for _, price := range right {
		expected[price.Region+"\x00"+price.Currency] = price.AmountMinor
	}
	for _, price := range left {
		key := price.Region + "\x00" + price.Currency
		amount, ok := expected[key]
		if !ok || amount != price.AmountMinor {
			return false
		}
		delete(expected, key)
	}
	return len(expected) == 0
}

func sameCatalogDate(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Format("2006-01-02") == right.Format("2006-01-02")
}

func catalogReconciliationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), catalogReconciliationTimeout)
}

func lockGame(ctx context.Context, tx *sql.Tx, id int64) error {
	var lockedID int64
	err := tx.QueryRowContext(ctx, lockGameSQL, id).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.ErrNotFound
	}
	return err
}

func lockLatestActivePriceStart(ctx context.Context, tx *sql.Tx, editionID int64) (time.Time, bool, error) {
	rows, err := tx.QueryContext(ctx, lockActivePriceStartsSQL, editionID)
	if err != nil {
		return time.Time{}, false, err
	}
	defer rows.Close()

	var latest time.Time
	found := false
	for rows.Next() {
		var activeFrom time.Time
		if err := rows.Scan(&activeFrom); err != nil {
			return time.Time{}, false, err
		}
		activeFrom = activeFrom.UTC().Truncate(time.Microsecond)
		if !found || activeFrom.After(latest) {
			latest = activeFrom
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, false, err
	}
	return latest, found, nil
}

func (s *Store) findByID(ctx context.Context, id int64) (entity.Game, error) {
	return findByID(ctx, s.db, id)
}

func findByID(ctx context.Context, queryer catalogQueryer, id int64) (entity.Game, error) {
	var game entity.Game
	var releaseDate sql.NullTime
	err := queryer.QueryRowContext(ctx, findByIDSQL, id).Scan(
		&game.ID, &game.Slug, &game.Name, &game.Summary, &game.Description, &game.Developer, &game.Publisher, &releaseDate, &game.CoverURL,
	)
	if err != nil {
		return entity.Game{}, err
	}
	game.ReleaseDate = nullableDate(releaseDate)
	game.Editions, err = loadEditions(ctx, queryer, id, catalog.Pricing{Region: "GLOBAL", Currency: "USD"}, 0)
	return game, err
}

func (s *Store) loadEditions(ctx context.Context, gameID int64, pricing catalog.Pricing, viewerID int64) ([]entity.Edition, error) {
	return loadEditions(ctx, s.db, gameID, pricing, viewerID)
}

func loadEditions(ctx context.Context, queryer catalogQueryer, gameID int64, pricing catalog.Pricing, viewerID int64) ([]entity.Edition, error) {
	rows, err := queryer.QueryContext(ctx, loadEditionsSQL, viewerID, pricing.Region, pricing.Currency, pricing.Region, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]entity.Edition, 0)
	for rows.Next() {
		var edition entity.Edition
		var amount sql.NullInt64
		var currency, region sql.NullString
		if err := rows.Scan(&edition.ID, &edition.Code, &edition.Name, &edition.Description, &edition.Owned, &amount, &currency, &region); err != nil {
			return nil, err
		}
		if amount.Valid != currency.Valid || amount.Valid != region.Valid {
			return nil, errors.New("catalog price columns have inconsistent nullability")
		}
		if amount.Valid {
			edition.Prices = []entity.Price{{AmountMinor: amount.Int64, Currency: currency.String, Region: region.String}}
		}
		items = append(items, edition)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func nullableDate(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	year, month, day := value.Time.Date()
	date := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	return &date
}

func dateValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Format("2006-01-02")
}

func catalogError(err error) error {
	if duplicateKey(err) {
		return catalog.ErrConflict
	}
	return err
}

func duplicateKey(err error) bool {
	var mysqlErr *drivermysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

var _ catalog.Store = (*Store)(nil)
