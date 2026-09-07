package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/account/repository/password"
	mysqladapter "github.com/red060324/XiaoLanHe/internal/adapter/mysql"
	"github.com/red060324/XiaoLanHe/internal/config"
	mysqlmigrations "github.com/red060324/XiaoLanHe/migrations/mysql"
)

func main() {
	databaseURL := strings.TrimSpace(os.Getenv("XLH_DATABASE_URL"))
	adminPassword := os.Getenv("XLH_SEED_ADMIN_PASSWORD")
	if databaseURL == "" || len(adminPassword) < 8 || len(adminPassword) > 72 {
		slog.Error("XLH_DATABASE_URL and an 8-72 byte XLH_SEED_ADMIN_PASSWORD are required")
		os.Exit(1)
	}
	databaseConfig, err := config.LoadDatabaseConfig()
	if err != nil {
		fail("load database configuration", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := mysqladapter.Open(ctx, databaseURL, mysqladapter.Options{
		MaxOpenConnections: 2, MaxIdleConnections: 1, ConnectionMaxLifetime: databaseConfig.ConnectionMaxLifetime,
		ConnectionMaxIdleTime: databaseConfig.ConnectionMaxIdleTime, AllowInsecure: databaseConfig.AllowInsecure,
		TLS: mysqladapter.TLSOptions{CAFile: databaseConfig.TLSCAFile, ServerName: databaseConfig.TLSServerName, CertificateFile: databaseConfig.TLSCertificateFile, KeyFile: databaseConfig.TLSKeyFile},
	})
	if err != nil {
		fail("connect database", err)
	}
	defer db.Close()
	if err := mysqladapter.Migrate(ctx, db, mysqlmigrations.Files, mysqladapter.MigrationOptions{LockTimeout: databaseConfig.MigrationLockTimeout}); err != nil {
		fail("migrate database", err)
	}
	passwordHash, err := (password.Bcrypt{}).Hash(adminPassword)
	if err != nil {
		fail("hash admin password", err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		fail("begin seed", err)
	}
	defer func() { _ = tx.Rollback() }()

	username := strings.TrimSpace(os.Getenv("XLH_SEED_ADMIN_USERNAME"))
	if username == "" {
		username = "admin"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_account(user_name,display_name,password_hash,role,status)
		VALUES (?,'XiaoLanHe Admin',?,'admin','active') AS incoming
		ON DUPLICATE KEY UPDATE password_hash=incoming.password_hash,role='admin',status='active',updated_at=UTC_TIMESTAMP(6)`, username, passwordHash); err != nil {
		fail("seed admin", err)
	}

	gameID, err := upsertID(ctx, tx, `
		INSERT INTO game(slug,name,summary,description,developer,publisher,status)
		VALUES ('xiaolanhe-demo','小蓝盒 Demo','用于本地演示的游戏','可用于验证目录、价格和后续购买流程。','XiaoLanHe Studio','XiaoLanHe','active') AS incoming
		ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id),name=incoming.name,summary=incoming.summary,description=incoming.description,status='active',updated_at=UTC_TIMESTAMP(6)`)
	if err != nil {
		fail("seed game", err)
	}
	editionID, err := upsertID(ctx, tx, `
		INSERT INTO game_edition(game_id,code,name,description,status)
		VALUES (?,'standard','标准版','Demo 标准版','active') AS incoming
		ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id),name=incoming.name,description=incoming.description,status='active',updated_at=UTC_TIMESTAMP(6)`, gameID)
	if err != nil {
		fail("seed edition", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO game_price(edition_id,region_code,currency,amount_minor)
		VALUES (?,'GLOBAL','USD',1999) AS incoming
		ON DUPLICATE KEY UPDATE amount_minor=incoming.amount_minor,updated_at=UTC_TIMESTAMP(6)`, editionID); err != nil {
		fail("seed price", err)
	}
	campaignID, err := upsertID(ctx, tx, `
		INSERT INTO coupon_campaign(code,name,status,starts_at,ends_at)
		VALUES ('DEMO-WELCOME','Demo welcome campaign','active','2020-01-01 00:00:00.000000','2099-01-01 00:00:00.000000') AS incoming
		ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id),name=incoming.name,status='active',starts_at=incoming.starts_at,ends_at=incoming.ends_at,updated_at=UTC_TIMESTAMP(6)`)
	if err != nil {
		fail("seed coupon campaign", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO coupon_definition(campaign_id,code,name,discount_type,percentage_bps,currency,minimum_minor,total_stock,per_user_limit,game_id,edition_id)
		VALUES (?,'WELCOME20','Demo 20% off','percentage',2000,'USD',1000,1000,1,?,?) AS incoming
		ON DUPLICATE KEY UPDATE campaign_id=incoming.campaign_id,name=incoming.name,discount_type=incoming.discount_type,
			fixed_minor=NULL,percentage_bps=incoming.percentage_bps,currency=incoming.currency,minimum_minor=incoming.minimum_minor,
			total_stock=GREATEST(coupon_definition.claimed_stock,incoming.total_stock),per_user_limit=incoming.per_user_limit,
			game_id=incoming.game_id,edition_id=incoming.edition_id,updated_at=UTC_TIMESTAMP(6)`, campaignID, gameID, editionID); err != nil {
		fail("seed coupon", err)
	}
	if err := tx.Commit(); err != nil {
		fail("commit seed", err)
	}
	slog.Info("seed completed", "outcome", "success")
}

func upsertID(ctx context.Context, tx *sql.Tx, statement string, args ...any) (int64, error) {
	result, err := tx.ExecContext(ctx, statement, args...)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if id <= 0 {
		return 0, fmt.Errorf("upsert did not return a valid id")
	}
	return id, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func fail(action string, err error) {
	_ = err
	slog.Error(action, "outcome", "failed")
	os.Exit(1)
}
