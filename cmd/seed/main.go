package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/account/repository/password"
	"github.com/red060324/XiaoLanHe/internal/adapter/postgres"
	"github.com/red060324/XiaoLanHe/migrations"
)

func main() {
	databaseURL := os.Getenv("XLH_DATABASE_URL")
	adminPassword := os.Getenv("XLH_SEED_ADMIN_PASSWORD")
	if databaseURL == "" || len(adminPassword) < 8 || len(adminPassword) > 72 {
		slog.Error("XLH_DATABASE_URL and an 8-72 byte XLH_SEED_ADMIN_PASSWORD are required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fail("connect database", err)
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool, migrations.Files); err != nil {
		fail("migrate database", err)
	}
	passwordHash, err := (password.Bcrypt{}).Hash(adminPassword)
	if err != nil {
		fail("hash admin password", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		fail("begin seed", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	username := os.Getenv("XLH_SEED_ADMIN_USERNAME")
	if username == "" {
		username = "admin"
	}
	if _, err := tx.Exec(ctx, `
		insert into user_account(user_name,display_name,password_hash,role,status)
		values ($1,'XiaoLanHe Admin',$2,'admin','active')
		on conflict ((lower(user_name))) do update set password_hash=excluded.password_hash,role='admin',status='active',updated_at=now()`, username, passwordHash); err != nil {
		fail("seed admin", err)
	}
	var gameID int64
	if err := tx.QueryRow(ctx, `
		insert into game(slug,name,summary,description,developer,publisher,status)
		values ('xiaolanhe-demo','小蓝盒 Demo','用于本地演示的游戏','可用于验证目录、价格和后续购买流程。','XiaoLanHe Studio','XiaoLanHe','active')
		on conflict ((lower(slug))) do update set name=excluded.name,summary=excluded.summary,description=excluded.description,status='active',updated_at=now()
		returning id`).Scan(&gameID); err != nil {
		fail("seed game", err)
	}
	var editionID int64
	if err := tx.QueryRow(ctx, `
		insert into game_edition(game_id,code,name,description,status)
		values ($1,'standard','标准版','Demo 标准版','active')
		on conflict (game_id,code) do update set name=excluded.name,description=excluded.description,status='active',updated_at=now()
		returning id`, gameID).Scan(&editionID); err != nil {
		fail("seed edition", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into game_price(edition_id,region_code,currency,amount_minor)
		values ($1,'GLOBAL','USD',1999)
		on conflict (edition_id,region_code,currency) where active_until is null
		do update set amount_minor=excluded.amount_minor,updated_at=now()`, editionID); err != nil {
		fail("seed price", err)
	}
	if err := tx.Commit(ctx); err != nil {
		fail("commit seed", err)
	}
	slog.Info("seed completed", "admin", username, "game", "xiaolanhe-demo")
}

func fail(action string, err error) {
	slog.Error(action, "error", err)
	os.Exit(1)
}
