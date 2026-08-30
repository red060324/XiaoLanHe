package postgres_test

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	accountpg "github.com/red060324/XiaoLanHe/internal/account/repository/postgres"
	account "github.com/red060324/XiaoLanHe/internal/account/usecase"
	adapterpg "github.com/red060324/XiaoLanHe/internal/adapter/postgres"
	"github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalogpg "github.com/red060324/XiaoLanHe/internal/catalog/repository/postgres"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	"github.com/red060324/XiaoLanHe/migrations"
)

func TestPhaseOnePostgres(t *testing.T) {
	databaseURL := os.Getenv("XLH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("XLH_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	const schema = "xlh_phase1_test"
	if _, err := adminPool.Exec(ctx, "drop schema if exists "+schema+" cascade; create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
		adminPool.Close()
	})

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if err := adapterpg.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}
	if err := adapterpg.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("repeat migrate: %v", err)
	}
	errCh := make(chan error, 2)
	for range 2 {
		go func() { errCh <- adapterpg.Migrate(ctx, pool, migrations.Files) }()
	}
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent migrate: %v", err)
		}
	}
	var versions int
	if err := pool.QueryRow(ctx, `select count(*) from schema_migration`).Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("migration versions=%d err=%v", versions, err)
	}

	changed := fstest.MapFS{}
	for _, name := range []string{"001_initial_schema.sql", "002_account_catalog.sql"} {
		body, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			t.Fatal(err)
		}
		changed[name] = &fstest.MapFile{Data: body}
	}
	changed["002_account_catalog.sql"].Data = append(changed["002_account_catalog.sql"].Data, []byte("\n-- changed")...)
	if err := adapterpg.Migrate(ctx, pool, changed); err == nil || !strings.Contains(err.Error(), "checksum changed") {
		t.Fatalf("checksum change error=%v", err)
	}

	accountStore := accountpg.NewStore(pool)
	user, err := accountStore.Register(ctx, "phase_user", "Phase User", strings.Repeat("p", 60), strings.Repeat("a", 64), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountStore.Register(ctx, "PHASE_USER", "Duplicate", strings.Repeat("p", 60), strings.Repeat("b", 64), time.Now().Add(time.Hour)); !errors.Is(err, account.ErrConflict) {
		t.Fatalf("duplicate account error=%v", err)
	}
	if err := accountStore.ReplaceSession(ctx, user.ID, strings.Repeat("a", 64), strings.Repeat("c", 64), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := accountStore.FindSession(ctx, strings.Repeat("a", 64), time.Now()); !errors.Is(err, account.ErrUnauthenticated) {
		t.Fatalf("old session error=%v", err)
	}
	if principal, err := accountStore.FindSession(ctx, strings.Repeat("c", 64), time.Now()); err != nil || principal.UserID != user.ID {
		t.Fatalf("new session principal=%+v err=%v", principal, err)
	}

	catalogStore := catalogpg.NewStore(pool)
	game, err := catalogStore.Save(ctx, 0, entity.Draft{
		Slug: "phase-game", Name: "Phase Game",
		Editions: []entity.EditionDraft{{Code: "standard", Name: "Standard", Prices: []entity.Price{
			{Region: "GLOBAL", Currency: "USD", AmountMinor: 1999},
			{Region: "CN", Currency: "CNY", AmountMinor: 9900},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into game_entitlement(user_id,edition_id) values ($1,$2)`, user.ID, game.Editions[0].ID); err != nil {
		t.Fatal(err)
	}
	found, err := catalogStore.FindBySlug(ctx, "phase-game", catalog.Pricing{Region: "CN", Currency: "CNY"}, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found.Owned || len(found.Editions) != 1 || len(found.Editions[0].Prices) != 1 || found.Editions[0].Prices[0].AmountMinor != 9900 {
		t.Fatalf("catalog result=%+v", found)
	}
}
