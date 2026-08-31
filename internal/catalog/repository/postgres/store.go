package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Exists(ctx context.Context, id int64) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `select exists(select 1 from game where id=$1 and status='active')`, id).Scan(&exists)
	return exists, err
}

func (s *Store) FindPurchaseOffer(ctx context.Context, editionID int64, pricing catalog.Pricing) (entity.PurchaseOffer, error) {
	var offer entity.PurchaseOffer
	err := s.pool.QueryRow(ctx, `
		select g.id,g.slug,g.name,e.id,e.code,e.name,p.amount_minor,p.currency,p.region_code
		from game_edition e join game g on g.id=e.game_id
		join lateral (
			select amount_minor,currency,region_code from game_price
			where edition_id=e.id and currency=$2 and region_code in ($3,'GLOBAL')
				and active_from<=now() and (active_until is null or active_until>now())
			order by case when region_code=$3 then 0 else 1 end,active_from desc limit 1
		) p on true
		where e.id=$1 and e.status='active' and g.status='active'`, editionID, pricing.Currency, pricing.Region).
		Scan(&offer.GameID, &offer.GameSlug, &offer.GameName, &offer.EditionID, &offer.EditionCode, &offer.EditionName, &offer.AmountMinor, &offer.Currency, &offer.Region)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.PurchaseOffer{}, catalog.ErrNotFound
	}
	return offer, err
}

func (s *Store) List(ctx context.Context, filter catalog.ListFilter) ([]entity.Game, error) {
	rows, err := s.pool.Query(ctx, `
		select g.id,g.slug,g.name,g.summary,g.cover_url,g.release_date,
			exists(select 1 from game_entitlement ge join game_edition e on e.id=ge.edition_id where ge.user_id=$1 and ge.status='active' and e.game_id=g.id)
		from game g
		where g.status='active' and ($2::bigint=0 or g.id<$2)
			and ($3='' or lower(g.name) like '%'||lower($3)||'%' or lower(g.slug) like '%'||lower($3)||'%')
		order by g.id desc limit $4`, filter.ViewerID, filter.BeforeID, filter.Query, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]entity.Game, 0, filter.Limit)
	for rows.Next() {
		var item entity.Game
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Summary, &item.CoverURL, &item.ReleaseDate, &item.Owned); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) FindBySlug(ctx context.Context, slug string, pricing catalog.Pricing, viewerID int64) (entity.Game, error) {
	var game entity.Game
	err := s.pool.QueryRow(ctx, `
		select g.id,g.slug,g.name,g.summary,g.description,g.developer,g.publisher,g.release_date,g.cover_url,
			exists(select 1 from game_entitlement ge join game_edition e on e.id=ge.edition_id where ge.user_id=$1 and ge.status='active' and e.game_id=g.id)
		from game g where lower(g.slug)=lower($2) and g.status='active'`, viewerID, slug).
		Scan(&game.ID, &game.Slug, &game.Name, &game.Summary, &game.Description, &game.Developer, &game.Publisher, &game.ReleaseDate, &game.CoverURL, &game.Owned)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.Game{}, catalog.ErrNotFound
	}
	if err != nil {
		return entity.Game{}, err
	}
	game.Editions, err = s.loadEditions(ctx, game.ID, pricing)
	return game, err
}

func (s *Store) Save(ctx context.Context, id int64, draft entity.Draft) (game entity.Game, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return entity.Game{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if id == 0 {
		err = tx.QueryRow(ctx, `
			insert into game(slug,name,summary,description,developer,publisher,release_date,cover_url,status)
			values ($1,$2,$3,$4,$5,$6,$7,$8,'active') returning id`,
			draft.Slug, draft.Name, draft.Summary, draft.Description, draft.Developer, draft.Publisher, draft.ReleaseDate, draft.CoverURL).Scan(&id)
	} else {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, `
			update game set slug=$2,name=$3,summary=$4,description=$5,developer=$6,publisher=$7,release_date=$8,cover_url=$9,status='active',updated_at=now()
			where id=$1`, id, draft.Slug, draft.Name, draft.Summary, draft.Description, draft.Developer, draft.Publisher, draft.ReleaseDate, draft.CoverURL)
		if err == nil && tag.RowsAffected() == 0 {
			return entity.Game{}, catalog.ErrNotFound
		}
	}
	if err != nil {
		if uniqueViolation(err) {
			return entity.Game{}, catalog.ErrConflict
		}
		return entity.Game{}, err
	}
	codes := make([]string, 0, len(draft.Editions))
	for _, edition := range draft.Editions {
		codes = append(codes, edition.Code)
		var editionID int64
		err = tx.QueryRow(ctx, `
			insert into game_edition(game_id,code,name,description,status)
			values ($1,$2,$3,$4,'active')
			on conflict (game_id,code) do update set name=excluded.name,description=excluded.description,status='active',updated_at=now()
			returning id`, id, edition.Code, edition.Name, edition.Description).Scan(&editionID)
		if err != nil {
			return entity.Game{}, err
		}
		for _, price := range edition.Prices {
			if _, err = tx.Exec(ctx, `
				update game_price set active_until=now(),updated_at=now()
				where edition_id=$1 and region_code=$2 and currency=$3 and active_until is null`, editionID, price.Region, price.Currency); err != nil {
				return entity.Game{}, err
			}
			if _, err = tx.Exec(ctx, `
				insert into game_price(edition_id,region_code,currency,amount_minor) values ($1,$2,$3,$4)`, editionID, price.Region, price.Currency, price.AmountMinor); err != nil {
				return entity.Game{}, err
			}
		}
	}
	if _, err = tx.Exec(ctx, `update game_edition set status='inactive',updated_at=now() where game_id=$1 and not (code=any($2::text[]))`, id, codes); err != nil {
		return entity.Game{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return entity.Game{}, err
	}
	return s.findByID(ctx, id)
}

func (s *Store) findByID(ctx context.Context, id int64) (entity.Game, error) {
	var game entity.Game
	err := s.pool.QueryRow(ctx, `
		select id,slug,name,summary,description,developer,publisher,release_date,cover_url
		from game where id=$1`, id).
		Scan(&game.ID, &game.Slug, &game.Name, &game.Summary, &game.Description, &game.Developer, &game.Publisher, &game.ReleaseDate, &game.CoverURL)
	if err != nil {
		return entity.Game{}, err
	}
	game.Editions, err = s.loadEditions(ctx, id, catalog.Pricing{Region: "GLOBAL", Currency: "USD"})
	return game, err
}

func (s *Store) loadEditions(ctx context.Context, gameID int64, pricing catalog.Pricing) ([]entity.Edition, error) {
	rows, err := s.pool.Query(ctx, `
		select e.id,e.code,e.name,e.description,p.amount_minor,p.currency,p.region_code
		from game_edition e
		left join lateral (
			select amount_minor,currency,region_code from game_price
			where edition_id=e.id and currency=$2 and region_code in ($3,'GLOBAL')
				and active_from<=now() and (active_until is null or active_until>now())
			order by case when region_code=$3 then 0 else 1 end,active_from desc limit 1
		) p on true
		where e.game_id=$1 and e.status='active' order by e.id`, gameID, pricing.Currency, pricing.Region)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]entity.Edition, 0)
	for rows.Next() {
		var edition entity.Edition
		var amount *int64
		var currency, region *string
		if err := rows.Scan(&edition.ID, &edition.Code, &edition.Name, &edition.Description, &amount, &currency, &region); err != nil {
			return nil, err
		}
		if amount != nil {
			edition.Prices = []entity.Price{{AmountMinor: *amount, Currency: *currency, Region: *region}}
		}
		items = append(items, edition)
	}
	return items, rows.Err()
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

var _ catalog.Store = (*Store)(nil)
