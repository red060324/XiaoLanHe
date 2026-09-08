package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"

	accountmysql "github.com/red060324/XiaoLanHe/internal/account/repository/mysql"
	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	flashentity "github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	flashmysql "github.com/red060324/XiaoLanHe/internal/flashsale/repository/mysql"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	orderentity "github.com/red060324/XiaoLanHe/internal/order/entity"
	ordermysql "github.com/red060324/XiaoLanHe/internal/order/repository/mysql"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
	promotionentity "github.com/red060324/XiaoLanHe/internal/promotion/entity"
	promotionmysql "github.com/red060324/XiaoLanHe/internal/promotion/repository/mysql"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
	assistant "github.com/red060324/XiaoLanHe/internal/usecase"
)

const resetRedeemedCouponClaimsSQLPrefix = `UPDATE coupon_claim AS cl JOIN purchase_order AS o ON o.id=cl.redeemed_order_id SET cl.status='claimed',cl.redeemed_order_id=NULL WHERE o.user_id IN (`

// runMySQLLiveIntegrationSuite exercises behavior that mocks and schema text
// inspection cannot prove. Every row is namespaced so this remains safe when
// XLH_MYSQL_TEST_DSN points at a shared, already-migrated integration database.
func runMySQLLiveIntegrationSuite(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	t.Run("real driver reports changed rows", func(t *testing.T) {
		testMySQLChangedRows(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("schema enforces unicode time checks and uniqueness", func(t *testing.T) {
		testMySQLSchemaInvariants(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("conversation ownership converges under concurrency", func(t *testing.T) {
		testMySQLConversationOwnership(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("orders and payments preserve single row invariants", func(t *testing.T) {
		testMySQLOrderPaymentConcurrency(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("coupon claims serialize final stock and per-user limits", func(t *testing.T) {
		testMySQLCouponClaimConcurrency(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("same-scope flash-sale activation cannot overlap", func(t *testing.T) {
		testMySQLFlashSaleActivationConcurrency(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("read committed refreshes consistent reads", func(t *testing.T) {
		testMySQLReadCommittedVisibility(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("lock timeout retries discard and reread the whole transaction", func(t *testing.T) {
		testMySQLTransactionRetry(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("repository affected-row branches use changed-row semantics", func(t *testing.T) {
		testMySQLAffectedRowBranches(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("locking queries use bounded indexes", func(t *testing.T) {
		testMySQLLockingQueryPlans(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("release workers skip locks and fence expired leases", func(t *testing.T) {
		testMySQLReleaseJobLeases(t, ctx, newMySQLLiveFixture(t, db))
	})
	t.Run("release workers claim disjoint batches", func(t *testing.T) {
		testMySQLReleaseJobWorkers(t, ctx, newMySQLLiveFixture(t, db))
	})
}

type mysqlLiveFixture struct {
	db            *sql.DB
	prefix        string
	userIDs       []int64
	gameIDs       []int64
	gameSlugs     []string
	sessionKeys   []string
	activityIDs   []int64
	releaseJobIDs []int64
	couponIDs     []int64
	campaignIDs   []int64
}

func newMySQLLiveFixture(t *testing.T, db *sql.DB) *mysqlLiveFixture {
	t.Helper()
	fixture := &mysqlLiveFixture{
		db:     db,
		prefix: fmt.Sprintf("li%x", time.Now().UnixNano()),
	}
	t.Cleanup(func() { fixture.cleanup(t) })
	return fixture
}

func (f *mysqlLiveFixture) cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM flash_sale_release_job WHERE id IN (` + livePlaceholders(len(f.releaseJobIDs)) + `)`, int64Args(f.releaseJobIDs)},
		{`DELETE FROM flash_sale_reservation WHERE user_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE p FROM payment_record AS p JOIN purchase_order AS o ON o.id=p.order_id WHERE o.user_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE FROM game_entitlement WHERE user_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE i FROM purchase_order_item AS i JOIN purchase_order AS o ON o.id=i.order_id WHERE o.user_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{resetRedeemedCouponClaimsSQLPrefix + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE FROM purchase_order WHERE user_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE FROM coupon_claim WHERE user_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE FROM coupon_definition WHERE id IN (` + livePlaceholders(len(f.couponIDs)) + `)`, int64Args(f.couponIDs)},
		{`DELETE FROM coupon_campaign WHERE id IN (` + livePlaceholders(len(f.campaignIDs)) + `)`, int64Args(f.campaignIDs)},
		{`DELETE FROM flash_sale_activity WHERE id IN (` + livePlaceholders(len(f.activityIDs)) + `)`, int64Args(f.activityIDs)},
		{`DELETE l FROM flash_sale_scope_lock AS l JOIN game_edition AS e ON e.id=l.edition_id WHERE e.game_id IN (` + livePlaceholders(len(f.gameIDs)) + `)`, int64Args(f.gameIDs)},
		{`DELETE m FROM conversation_message AS m JOIN conversation_session AS s ON s.id=m.session_id WHERE s.session_key IN (` + livePlaceholders(len(f.sessionKeys)) + `)`, stringArgs(f.sessionKeys)},
		{`DELETE FROM conversation_session WHERE session_key IN (` + livePlaceholders(len(f.sessionKeys)) + `)`, stringArgs(f.sessionKeys)},
		{`DELETE FROM player_profile WHERE user_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE FROM user_session WHERE user_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE FROM community_reaction WHERE user_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE FROM community_comment WHERE author_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE FROM community_post WHERE author_id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE FROM user_account WHERE id IN (` + livePlaceholders(len(f.userIDs)) + `)`, int64Args(f.userIDs)},
		{`DELETE FROM game WHERE slug IN (` + livePlaceholders(len(f.gameSlugs)) + `)`, stringArgs(f.gameSlugs)},
	}
	for _, statement := range statements {
		if len(statement.args) == 0 {
			continue
		}
		if _, err := f.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Errorf("clean up MySQL live fixture: %v", err)
		}
	}
}

func TestLiveCleanupKeepsCouponClaimRedemptionCheckValid(t *testing.T) {
	for _, fragment := range []string{"SET cl.status='claimed',cl.redeemed_order_id=NULL", "o.id=cl.redeemed_order_id"} {
		if !strings.Contains(resetRedeemedCouponClaimsSQLPrefix, fragment) {
			t.Fatalf("coupon cleanup must reset status and link together; missing %q", fragment)
		}
	}
}

func testMySQLChangedRows(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	userID := fixture.insertUser(t, ctx, "rows")

	result, err := fixture.db.ExecContext(ctx, `UPDATE user_account SET display_name=display_name WHERE id=?`, userID)
	if err != nil {
		t.Fatal(err)
	}
	if affected := rowsAffected(t, result); affected != 0 {
		t.Fatalf("no-op update affected %d rows; live driver is not using clientFoundRows=false", affected)
	}

	result, err = fixture.db.ExecContext(ctx, `UPDATE user_account SET display_name=? WHERE id=?`, "changed 🎮", userID)
	if err != nil {
		t.Fatal(err)
	}
	if affected := rowsAffected(t, result); affected != 1 {
		t.Fatalf("changed update affected %d rows, want 1", affected)
	}
	result, err = fixture.db.ExecContext(ctx, `UPDATE user_account SET display_name=? WHERE id=?`, "changed 🎮", userID)
	if err != nil {
		t.Fatal(err)
	}
	if affected := rowsAffected(t, result); affected != 0 {
		t.Fatalf("repeated update affected %d rows; live driver is not using changed-row semantics", affected)
	}
}

func testMySQLAffectedRowBranches(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	userID := fixture.insertUser(t, ctx, "affected_branches")

	t.Run("insert update and guarded no-op", func(t *testing.T) {
		sessionKey := fixture.prefix + "-affected-session"
		fixture.sessionKeys = append(fixture.sessionKeys, sessionKey)
		result, err := fixture.db.ExecContext(ctx, `
			INSERT INTO conversation_session(session_key,user_id,metadata)
			VALUES (?,?,JSON_OBJECT())`, sessionKey, userID)
		if err != nil {
			t.Fatal(err)
		}
		if affected := rowsAffected(t, result); affected != 1 {
			t.Fatalf("insert affected %d rows, want 1", affected)
		}
		sessionID := lastInsertID(t, result)

		result, err = fixture.db.ExecContext(ctx, `
			UPDATE conversation_session
			SET user_id=COALESCE(user_id,NULLIF(?,0))
			WHERE id=?`, userID, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if affected := rowsAffected(t, result); affected != 0 {
			t.Fatalf("repository-shaped no-op update affected %d rows, want 0", affected)
		}

		result, err = fixture.db.ExecContext(ctx, `UPDATE conversation_session SET summary_text='changed' WHERE id=?`, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if affected := rowsAffected(t, result); affected != 1 {
			t.Fatalf("changed update affected %d rows, want 1", affected)
		}
	})

	t.Run("repository accepts exact no-op revocation", func(t *testing.T) {
		token := sha256.Sum256([]byte(fixture.prefix + "/revoke-session"))
		if _, err := fixture.db.ExecContext(ctx, `
			INSERT INTO user_session(user_id,token_hash,expires_at)
			VALUES (?,?,UTC_TIMESTAMP(6)+INTERVAL 1 HOUR)`, userID, token[:]); err != nil {
			t.Fatal(err)
		}
		store := accountmysql.NewStore(fixture.db)
		tokenHash := fmt.Sprintf("%x", token[:])
		if err := store.RevokeSession(ctx, tokenHash); err != nil {
			t.Fatalf("changed revocation: %v", err)
		}
		if err := store.RevokeSession(ctx, tokenHash); err != nil {
			t.Fatalf("exact no-op revocation: %v", err)
		}
	})

	t.Run("upsert insert no-op and change", func(t *testing.T) {
		offer := fixture.insertCatalog(t, ctx, "affected-upsert", 1900)
		result, err := fixture.db.ExecContext(ctx, `
			INSERT INTO flash_sale_scope_lock(edition_id,region_code,currency)
			VALUES (?,'GLOBAL','USD') AS incoming
			ON DUPLICATE KEY UPDATE edition_id=incoming.edition_id`, offer.EditionID)
		if err != nil {
			t.Fatal(err)
		}
		if affected := rowsAffected(t, result); affected != 1 {
			t.Fatalf("upsert insert affected %d rows, want 1", affected)
		}

		result, err = fixture.db.ExecContext(ctx, `
			INSERT INTO flash_sale_scope_lock(edition_id,region_code,currency)
			VALUES (?,'GLOBAL','USD') AS incoming
			ON DUPLICATE KEY UPDATE edition_id=incoming.edition_id`, offer.EditionID)
		if err != nil {
			t.Fatal(err)
		}
		if affected := rowsAffected(t, result); affected != 0 {
			t.Fatalf("upsert no-op affected %d rows, want 0", affected)
		}

		result, err = fixture.db.ExecContext(ctx, `
			INSERT INTO game_edition(game_id,code,name,description,status)
			VALUES (?,'standard','Renamed','changed','active') AS incoming
			ON DUPLICATE KEY UPDATE name=incoming.name,description=incoming.description,status='active',updated_at=UTC_TIMESTAMP(6)`, offer.GameID)
		if err != nil {
			t.Fatal(err)
		}
		if affected := rowsAffected(t, result); affected != 2 {
			t.Fatalf("upsert change affected %d rows, want MySQL's changed-row value 2", affected)
		}
	})
}

func testMySQLSchemaInvariants(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	slug := fixture.prefix + "-schema"
	fixture.gameSlugs = append(fixture.gameSlugs, slug, fixture.prefix+"-over-limit", fixture.prefix+"-bad-status")
	name := strings.Repeat("界", 159) + "🎮"
	description := strings.Repeat("🎮", 20_000)
	createdAt := time.Date(2026, 9, 7, 1, 2, 3, 456000000, time.UTC)
	result, err := fixture.db.ExecContext(ctx, `
		INSERT INTO game(slug,name,summary,description,developer,publisher,cover_url,created_at,updated_at)
		VALUES (?,?,'',?,'','','',?,?)`, slug, name, description, createdAt, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	gameID := lastInsertID(t, result)
	fixture.gameIDs = append(fixture.gameIDs, gameID)

	var storedName, storedDescription string
	var descriptionRunes, descriptionBytes int
	var storedCreatedAt time.Time
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT name,description,CHAR_LENGTH(description),OCTET_LENGTH(description),created_at
		FROM game WHERE id=?`, gameID).Scan(
		&storedName, &storedDescription, &descriptionRunes, &descriptionBytes, &storedCreatedAt,
	); err != nil {
		t.Fatal(err)
	}
	if storedName != name || storedDescription != description || descriptionRunes != 20_000 || descriptionBytes != 80_000 {
		t.Fatalf("utf8mb4 round trip name=%v description_runes=%d description_bytes=%d", storedName == name, descriptionRunes, descriptionBytes)
	}
	if storedCreatedAt.Location().String() != "UTC" || !storedCreatedAt.Equal(createdAt) {
		t.Fatalf("created_at=%s location=%s, want %s in UTC", storedCreatedAt, storedCreatedAt.Location(), createdAt)
	}
	sessionKey := fixture.prefix + "-unicode"
	fixture.sessionKeys = append(fixture.sessionKeys, sessionKey)
	result, err = fixture.db.ExecContext(ctx, `INSERT INTO conversation_session(session_key) VALUES (?)`, sessionKey)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := lastInsertID(t, result)
	message := strings.Repeat("🎮", 16_384)
	if _, err := fixture.db.ExecContext(ctx, `
		INSERT INTO conversation_message(session_id,role,content) VALUES (?,'user',?)`, sessionID, message); err != nil {
		t.Fatal(err)
	}
	var storedMessage string
	var messageRunes, messageBytes int
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT content,CHAR_LENGTH(content),OCTET_LENGTH(content)
		FROM conversation_message WHERE session_id=?`, sessionID).Scan(&storedMessage, &messageRunes, &messageBytes); err != nil {
		t.Fatal(err)
	}
	if storedMessage != message || messageRunes != 16_384 || messageBytes != 65_536 {
		t.Fatalf("message utf8mb4 round trip equal=%v runes=%d bytes=%d", storedMessage == message, messageRunes, messageBytes)
	}

	_, err = fixture.db.ExecContext(ctx, `INSERT INTO game(slug,name) VALUES (?,?)`, slug, "duplicate")
	requireMySQLErrorNumber(t, err, 1062)
	_, err = fixture.db.ExecContext(ctx, `INSERT INTO game(slug,name,description) VALUES (?,?,?)`, fixture.prefix+"-over-limit", "over limit", description+"🎮")
	requireMySQLErrorNumber(t, err, 3819)
	_, err = fixture.db.ExecContext(ctx, `INSERT INTO game(slug,name,status) VALUES (?,?,?)`, fixture.prefix+"-bad-status", "bad status", "unknown")
	requireMySQLErrorNumber(t, err, 3819)

	result, err = fixture.db.ExecContext(ctx, `INSERT INTO game_edition(game_id,code,name) VALUES (?,'standard','Standard')`, gameID)
	if err != nil {
		t.Fatal(err)
	}
	editionID := lastInsertID(t, result)
	if _, err := fixture.db.ExecContext(ctx, `
		INSERT INTO game_price(edition_id,region_code,currency,amount_minor,active_from)
		VALUES (?,'GLOBAL','USD',1999,?)`, editionID, createdAt); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.db.ExecContext(ctx, `
		INSERT INTO game_price(edition_id,region_code,currency,amount_minor,active_from)
		VALUES (?,'GLOBAL','USD',2999,?)`, editionID, createdAt.Add(time.Microsecond))
	requireMySQLErrorNumber(t, err, 1062)

	for index, activeFrom := range []time.Time{createdAt.Add(-4 * time.Hour), createdAt.Add(-2 * time.Hour)} {
		if _, err := fixture.db.ExecContext(ctx, `
			INSERT INTO game_price(edition_id,region_code,currency,amount_minor,active_from,active_until)
			VALUES (?,'GLOBAL','USD',?,?,?)`, editionID, 999+index, activeFrom, activeFrom.Add(time.Hour)); err != nil {
			t.Fatalf("insert closed price %d: %v", index, err)
		}
	}
	_, err = fixture.db.ExecContext(ctx, `
		INSERT INTO game_price(edition_id,region_code,currency,amount_minor,active_from)
		VALUES (?,'CN','CNY',-1,?)`, editionID, createdAt)
	requireMySQLErrorNumber(t, err, 3819)
}

func testMySQLConversationOwnership(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	firstUserID := fixture.insertUser(t, ctx, "conversation_a")
	secondUserID := fixture.insertUser(t, ctx, "conversation_b")
	store := NewConversationStore(fixture.db)

	const workers = 6
	sameOwnerKey := fixture.prefix + "-same-owner"
	fixture.sessionKeys = append(fixture.sessionKeys, sameOwnerKey)
	start := make(chan struct{})
	type result struct {
		id  int64
		err error
	}
	results := make(chan result, workers)
	for range workers {
		go func() {
			<-start
			id, err := store.FindOrCreateSession(ctx, sameOwnerKey, firstUserID)
			results <- result{id: id, err: err}
		}()
	}
	close(start)
	var sessionID int64
	for range workers {
		outcome := <-results
		if outcome.err != nil {
			t.Fatalf("same-owner session race: %v", outcome.err)
		}
		if sessionID == 0 {
			sessionID = outcome.id
		} else if outcome.id != sessionID {
			t.Fatalf("same-owner race returned session IDs %d and %d", sessionID, outcome.id)
		}
	}

	if err := store.SaveMessage(ctx, sessionID, "user", "并发会话 🎮", ""); err != nil {
		t.Fatal(err)
	}
	conversation, err := store.LoadContext(ctx, sessionID, 8)
	if err != nil || !strings.Contains(conversation, "并发会话 🎮") {
		t.Fatalf("conversation=%q err=%v", conversation, err)
	}

	competingKey := fixture.prefix + "-competing-owners"
	fixture.sessionKeys = append(fixture.sessionKeys, competingKey)
	start = make(chan struct{})
	competing := make(chan result, 2)
	for _, userID := range []int64{firstUserID, secondUserID} {
		go func(userID int64) {
			<-start
			id, err := store.FindOrCreateSession(ctx, competingKey, userID)
			competing <- result{id: id, err: err}
		}(userID)
	}
	close(start)
	succeeded, forbidden := 0, 0
	for range 2 {
		outcome := <-competing
		switch {
		case outcome.err == nil:
			succeeded++
		case errors.Is(outcome.err, assistant.ErrConversationForbidden):
			forbidden++
		default:
			t.Fatalf("competing-owner session race: %v", outcome.err)
		}
	}
	var rows int
	var ownerID int64
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT COUNT(*),COALESCE(MAX(user_id),0) FROM conversation_session WHERE session_key=?`, competingKey).Scan(&rows, &ownerID); err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || forbidden != 1 || rows != 1 || (ownerID != firstUserID && ownerID != secondUserID) {
		t.Fatalf("competing owners succeeded=%d forbidden=%d rows=%d owner=%d", succeeded, forbidden, rows, ownerID)
	}
}

func testMySQLCouponClaimConcurrency(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	store := promotionmysql.NewStore(fixture.db)

	t.Run("last stock admits exactly one user", func(t *testing.T) {
		firstUserID := fixture.insertUser(t, ctx, "coupon_last_a")
		secondUserID := fixture.insertUser(t, ctx, "coupon_last_b")
		_, code := fixture.insertCoupon(t, ctx, "coupon-last", 1, 1)

		type outcome struct {
			result promotion.ClaimResult
			err    error
		}
		start := make(chan struct{})
		outcomes := make(chan outcome, 2)
		for index, userID := range []int64{firstUserID, secondUserID} {
			go func(index int, userID int64) {
				<-start
				result, err := store.Claim(ctx, promotion.ClaimCommand{
					UserID: userID, Code: code, IdempotencyKey: fmt.Sprintf("%s.last.%d", fixture.prefix, index),
				})
				outcomes <- outcome{result: result, err: err}
			}(index, userID)
		}
		close(start)
		succeeded, exhausted := 0, 0
		for range 2 {
			outcome := <-outcomes
			switch {
			case outcome.err == nil && !outcome.result.Replayed:
				succeeded++
			case errors.Is(outcome.err, promotionentity.ErrExhausted):
				exhausted++
			default:
				t.Fatalf("last-stock claim result=%+v err=%v", outcome.result, outcome.err)
			}
		}
		var claimedStock, claims int64
		if err := fixture.db.QueryRowContext(ctx, `
			SELECT d.claimed_stock,COUNT(cl.id) FROM coupon_definition AS d
			LEFT JOIN coupon_claim AS cl ON cl.coupon_id=d.id WHERE d.code=? GROUP BY d.id`, code).Scan(&claimedStock, &claims); err != nil {
			t.Fatal(err)
		}
		if succeeded != 1 || exhausted != 1 || claimedStock != 1 || claims != 1 {
			t.Fatalf("succeeded=%d exhausted=%d claimed_stock=%d claims=%d", succeeded, exhausted, claimedStock, claims)
		}
	})

	t.Run("one user cannot exceed concurrent limit", func(t *testing.T) {
		userID := fixture.insertUser(t, ctx, "coupon_limit")
		_, code := fixture.insertCoupon(t, ctx, "coupon-limit", 8, 1)
		const workers = 6
		start := make(chan struct{})
		errorsByWorker := make(chan error, workers)
		for index := range workers {
			go func(index int) {
				<-start
				_, err := store.Claim(ctx, promotion.ClaimCommand{
					UserID: userID, Code: code, IdempotencyKey: fmt.Sprintf("%s.limit.%d", fixture.prefix, index),
				})
				errorsByWorker <- err
			}(index)
		}
		close(start)
		succeeded, limited := 0, 0
		for range workers {
			switch err := <-errorsByWorker; {
			case err == nil:
				succeeded++
			case errors.Is(err, promotion.ErrClaimLimit):
				limited++
			default:
				t.Fatalf("per-user claim error=%v", err)
			}
		}
		var claimedStock, claims int64
		if err := fixture.db.QueryRowContext(ctx, `
			SELECT d.claimed_stock,COUNT(cl.id) FROM coupon_definition AS d
			LEFT JOIN coupon_claim AS cl ON cl.coupon_id=d.id AND cl.user_id=?
			WHERE d.code=? GROUP BY d.id`, userID, code).Scan(&claimedStock, &claims); err != nil {
			t.Fatal(err)
		}
		if succeeded != 1 || limited != workers-1 || claimedStock != 1 || claims != 1 {
			t.Fatalf("succeeded=%d limited=%d claimed_stock=%d user_claims=%d", succeeded, limited, claimedStock, claims)
		}
	})
}

func testMySQLFlashSaleActivationConcurrency(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	creatorID := fixture.insertUser(t, ctx, "activity_creator")
	offer := fixture.insertCatalog(t, ctx, "activity-overlap", 5000)
	store := flashmysql.NewStore(fixture.db)
	now := time.Now().UTC().Truncate(time.Microsecond)

	drafts := make([]flashentity.Activity, 2)
	for index := range drafts {
		draft, err := store.CreateActivity(ctx, flashentity.Activity{
			Code:      fmt.Sprintf("LI-%s-%d", strings.ToUpper(liveHex(fixture.prefix, "activate")[:12]), index),
			EditionID: offer.EditionID, Region: "GLOBAL", Currency: "USD",
			SalePriceMinor: 2500 + int64(index), TotalStock: 10,
			StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour),
			PaymentTimeout: 15 * time.Minute, CreatedBy: creatorID,
		})
		if err != nil {
			t.Fatal(err)
		}
		fixture.activityIDs = append(fixture.activityIDs, draft.ID)
		drafts[index] = draft
	}

	type outcome struct {
		activity flashentity.Activity
		err      error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for _, draft := range drafts {
		go func(draft flashentity.Activity) {
			<-start
			activity, err := store.ActivateActivity(ctx, draft.ID, draft.Version+1, now)
			outcomes <- outcome{activity: activity, err: err}
		}(draft)
	}
	close(start)
	active, rejected := 0, 0
	for range 2 {
		outcome := <-outcomes
		switch {
		case outcome.err == nil && outcome.activity.Status == flashentity.StatusActive:
			active++
		case errors.Is(outcome.err, flashentity.ErrInvalidState):
			rejected++
		default:
			t.Fatalf("activation result=%+v err=%v", outcome.activity, outcome.err)
		}
	}
	var activeRows, scopeRows int
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM flash_sale_activity
		WHERE id IN (?,?) AND status='active'`, drafts[0].ID, drafts[1].ID).Scan(&activeRows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM flash_sale_scope_lock
		WHERE edition_id=? AND region_code='GLOBAL' AND currency='USD'`, offer.EditionID).Scan(&scopeRows); err != nil {
		t.Fatal(err)
	}
	if active != 1 || rejected != 1 || activeRows != 1 || scopeRows != 1 {
		t.Fatalf("active=%d rejected=%d active_rows=%d scope_rows=%d", active, rejected, activeRows, scopeRows)
	}
}

func testMySQLReadCommittedVisibility(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	userID := fixture.insertUser(t, ctx, "read_committed")

	readerConn, err := fixture.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer readerConn.Close()
	writerConn, err := fixture.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writerConn.Close()

	reader, err := readerConn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	readerOpen := true
	defer func() {
		if readerOpen {
			_ = reader.Rollback()
		}
	}()

	var firstRead string
	if err := reader.QueryRowContext(ctx, `SELECT display_name FROM user_account WHERE id=?`, userID).Scan(&firstRead); err != nil {
		t.Fatal(err)
	}

	writer, err := writerConn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	writerOpen := true
	defer func() {
		if writerOpen {
			_ = writer.Rollback()
		}
	}()
	const committedName = "committed between consistent reads"
	if _, err := writer.ExecContext(ctx, `UPDATE user_account SET display_name=? WHERE id=?`, committedName, userID); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	writerOpen = false

	var secondRead string
	if err := reader.QueryRowContext(ctx, `SELECT display_name FROM user_account WHERE id=?`, userID).Scan(&secondRead); err != nil {
		t.Fatal(err)
	}
	if firstRead != "Live read_committed" || secondRead != committedName {
		t.Fatalf("READ COMMITTED consistent reads before/after writer commit = %q/%q", firstRead, secondRead)
	}
	if err := reader.Commit(); err != nil {
		t.Fatal(err)
	}
	readerOpen = false
}

func testMySQLTransactionRetry(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	userID := fixture.insertUser(t, ctx, "lock_timeout_retry")
	gate, err := fixture.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	gateOpen := true
	defer func() {
		if gateOpen {
			_ = gate.Rollback()
		}
	}()
	if _, err := gate.ExecContext(ctx, `UPDATE user_account SET display_name='committed by blocker' WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	retryKeys := []string{fixture.prefix + "-retry-1", fixture.prefix + "-retry-2"}
	fixture.sessionKeys = append(fixture.sessionKeys, retryKeys...)

	firstAttemptStarted := make(chan struct{})
	secondAttemptStarted := make(chan struct{})
	firstFailure := make(chan error, 1)
	type outcome struct {
		value string
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		attempts := 0
		value, err := mysqltx.RunWithOptions(ctx, fixture.db, mysqltx.Options{
			MaxAttempts: 2, Backoff: func(int) time.Duration { return 0 },
		}, func(tx *sql.Tx) (string, error) {
			attempts++
			if attempts == 1 {
				if _, err := tx.ExecContext(ctx, `SET innodb_lock_wait_timeout=1`); err != nil {
					return "", err
				}
				close(firstAttemptStarted)
			} else {
				close(secondAttemptStarted)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_session(session_key,user_id) VALUES (?,NULL)`,
				fmt.Sprintf("%s-retry-%d", fixture.prefix, attempts)); err != nil {
				return "", err
			}
			var displayName string
			if err := tx.QueryRowContext(ctx, `SELECT display_name FROM user_account WHERE id=? FOR UPDATE`, userID).Scan(&displayName); err != nil {
				if attempts == 1 {
					firstFailure <- err
				}
				return "", err
			}
			return fmt.Sprintf("%d|%s", attempts, displayName), nil
		})
		done <- outcome{value: value, err: err}
	}()

	select {
	case <-firstAttemptStarted:
	case <-ctx.Done():
		t.Fatalf("retry transaction did not start: %v", ctx.Err())
	}
	var failure error
	select {
	case failure = <-firstFailure:
	case <-ctx.Done():
		t.Fatalf("waiting for lock timeout: %v", ctx.Err())
	}
	requireMySQLErrorNumber(t, failure, 1205)
	select {
	case <-secondAttemptStarted:
	case outcome := <-done:
		t.Fatalf("transaction stopped instead of retrying after 1205: value=%q err=%v", outcome.value, outcome.err)
	case <-ctx.Done():
		t.Fatalf("second retry attempt did not start: %v", ctx.Err())
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	gateOpen = false

	var result outcome
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatalf("retry transaction did not finish: %v", ctx.Err())
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.value != "2|committed by blocker" {
		t.Fatalf("retry result=%q, want fresh attempt two with the committed value", result.value)
	}
	var firstRows, secondRows int
	if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_session WHERE session_key=?`, retryKeys[0]).Scan(&firstRows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_session WHERE session_key=?`, retryKeys[1]).Scan(&secondRows); err != nil {
		t.Fatal(err)
	}
	if firstRows != 0 || secondRows != 1 {
		t.Fatalf("retry side effects: first_attempt_rows=%d second_attempt_rows=%d", firstRows, secondRows)
	}
}

func testMySQLLockingQueryPlans(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	userID := fixture.insertUser(t, ctx, "explain")
	offer := fixture.insertCatalog(t, ctx, "explain", 4200)
	_, couponCode := fixture.insertCoupon(t, ctx, "explain", 5, 2)
	created := fixture.createOrder(t, ctx, ordermysql.NewStore(fixture.db), userID, offer, "explain")
	now := time.Now().UTC().Truncate(time.Microsecond)
	for index := range 48 {
		fixture.insertActivity(t, ctx, offer.EditionID, userID, fmt.Sprintf("R%02d", index), fmt.Sprintf("explain-fill-%d", index), "active", now.Add(-2*time.Hour), now.Add(2*time.Hour))
	}
	activityID := fixture.insertActivity(t, ctx, offer.EditionID, userID, "GLOBAL", "explain", "active", now.Add(-time.Hour), now.Add(time.Hour))
	fixture.insertReleaseJob(t, ctx, activityID, userID, "explain-pending", now.Add(-time.Minute))
	expiredID := fixture.insertReleaseJob(t, ctx, activityID, userID, "explain-expired", now.Add(time.Hour))
	if _, err := fixture.db.ExecContext(ctx, `
		UPDATE flash_sale_release_job SET status='leased',attempts=1,lease_until=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE id=?`, expiredID); err != nil {
		t.Fatal(err)
	}
	for index := range 48 {
		jobID := fixture.insertReleaseJob(t, ctx, activityID, userID, fmt.Sprintf("explain-fill-%d", index), now.Add(24*time.Hour))
		if index%2 == 0 {
			if _, err := fixture.db.ExecContext(ctx, `
				UPDATE flash_sale_release_job SET status='leased',attempts=1,lease_until=UTC_TIMESTAMP(6)+INTERVAL 24 HOUR WHERE id=?`, jobID); err != nil {
				t.Fatal(err)
			}
		}
	}

	type indexAccess struct {
		name          string
		usedKeyPrefix []string
	}
	cases := []struct {
		name, query string
		args        []any
		wantAccess  map[string][]indexAccess
	}{
		{
			name: "price",
			query: `SELECT active_from FROM game_price
				WHERE edition_id=? AND active_until IS NULL
				ORDER BY region_code,currency,active_from,id FOR UPDATE`,
			args: []any{offer.EditionID},
			wantAccess: map[string][]indexAccess{
				"game_price": {
					{name: "uk_game_price_active_key", usedKeyPrefix: []string{"edition_id"}},
					{name: "uk_game_price_one_active", usedKeyPrefix: []string{"edition_id"}},
					{name: "idx_game_price_lookup", usedKeyPrefix: []string{"edition_id"}},
				},
			},
		},
		{
			name: "coupon",
			query: `SELECT d.id,d.code,d.name,d.discount_type,COALESCE(d.fixed_minor,0),COALESCE(d.percentage_bps,0),
				d.currency,d.minimum_minor,d.total_stock,d.claimed_stock,d.per_user_limit,
				COALESCE(d.game_id,0),COALESCE(d.edition_id,0),c.status,c.starts_at,c.ends_at,
				(SELECT COUNT(*) FROM coupon_claim cl WHERE cl.coupon_id=d.id AND cl.user_id=? AND cl.status IN ('claimed','redeemed'))
				FROM coupon_definition d JOIN coupon_campaign c ON c.id=d.campaign_id
				WHERE d.code=? FOR UPDATE`,
			args: []any{userID, couponCode},
			wantAccess: map[string][]indexAccess{
				"d": {{name: "uk_coupon_definition_code", usedKeyPrefix: []string{"code"}}},
				"c": {{name: "PRIMARY", usedKeyPrefix: []string{"id"}}},
				"cl": {
					{name: "idx_coupon_claim_coupon", usedKeyPrefix: []string{"coupon_id", "user_id"}},
					{name: "uk_coupon_claim_idempotency", usedKeyPrefix: []string{"user_id"}},
					{name: "idx_coupon_claim_user", usedKeyPrefix: []string{"user_id"}},
				},
			},
		},
		{
			name: "order",
			query: `SELECT o.id,i.edition_id FROM purchase_order o
				JOIN purchase_order_item i ON i.order_id=o.id
				WHERE o.order_no=? FOR UPDATE`,
			args: []any{created.OrderNo},
			wantAccess: map[string][]indexAccess{
				"o": {{name: "uk_purchase_order_no", usedKeyPrefix: []string{"order_no"}}},
				"i": {{name: "uk_purchase_order_item", usedKeyPrefix: []string{"order_id"}}},
			},
		},
		{
			name: "activity",
			query: `SELECT id FROM flash_sale_activity
				WHERE id<>? AND edition_id=? AND region_code=? AND currency=? AND status='active'
					AND starts_at<? AND ends_at>?
				ORDER BY id LIMIT 1 FOR UPDATE`,
			args: []any{int64(-1), offer.EditionID, "GLOBAL", "USD", now.Add(2 * time.Hour), now.Add(-2 * time.Hour)},
			wantAccess: map[string][]indexAccess{
				"flash_sale_activity": {{
					name:          "idx_flash_sale_activity_scope_window",
					usedKeyPrefix: []string{"edition_id", "region_code", "currency", "status", "starts_at"},
				}},
			},
		},
		{
			name: "release claimable",
			query: `SELECT id FROM flash_sale_release_job
				WHERE claimable_at<=CURRENT_TIMESTAMP(6)
				ORDER BY claimable_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`,
			wantAccess: map[string][]indexAccess{
				"flash_sale_release_job": {{
					name:          "idx_flash_sale_release_job_claimable",
					usedKeyPrefix: []string{"claimable_at"},
				}},
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			plan := explainJSON(t, ctx, fixture.db, test.query, test.args...)
			for table, accepted := range test.wantAccess {
				key, usedParts, ok := planAccessForTable(plan, table)
				if !ok {
					t.Fatalf("EXPLAIN did not contain an indexed access for table %q: %s", table, plan)
				}
				var wantedPrefix []string
				for _, access := range accepted {
					if access.name == key {
						wantedPrefix = access.usedKeyPrefix
						break
					}
				}
				if wantedPrefix == nil {
					t.Fatalf("EXPLAIN table %q used key %q, want one of %+v: %s", table, key, accepted, plan)
				}
				if !hasStringPrefix(usedParts, wantedPrefix) {
					t.Fatalf("EXPLAIN table %q used key %q with parts %v, want paired prefix %v: %s", table, key, usedParts, wantedPrefix, plan)
				}
			}
		})
	}
}

func explainJSON(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var plan string
	if err := db.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+query, args...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(plan)) {
		t.Fatalf("EXPLAIN returned invalid JSON: %q", plan)
	}
	return plan
}

func planAccessForTable(plan, table string) (string, []string, bool) {
	var value any
	if err := json.Unmarshal([]byte(plan), &value); err != nil {
		return "", nil, false
	}
	return findPlanAccess(value, table)
}

func findPlanAccess(value any, table string) (string, []string, bool) {
	switch value := value.(type) {
	case map[string]any:
		if name, ok := value["table_name"].(string); ok && name == table {
			key, _ := value["key"].(string)
			parts := make([]string, 0)
			if raw, ok := value["used_key_parts"].([]any); ok {
				for _, part := range raw {
					if part, ok := part.(string); ok {
						parts = append(parts, part)
					}
				}
			}
			return key, parts, key != ""
		}
		for _, nested := range value {
			if key, parts, ok := findPlanAccess(nested, table); ok {
				return key, parts, true
			}
		}
	case []any:
		for _, nested := range value {
			if key, parts, ok := findPlanAccess(nested, table); ok {
				return key, parts, true
			}
		}
	}
	return "", nil, false
}

func hasStringPrefix(values, prefix []string) bool {
	if len(values) < len(prefix) {
		return false
	}
	for index := range prefix {
		if values[index] != prefix[index] {
			return false
		}
	}
	return true
}

func testMySQLOrderPaymentConcurrency(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	store := ordermysql.NewStore(fixture.db)

	t.Run("same order payment replays exactly once", func(t *testing.T) {
		userID := fixture.insertUser(t, ctx, "payment_replay")
		offer := fixture.insertCatalog(t, ctx, "payment-replay", 2499)
		created := fixture.createOrder(t, ctx, store, userID, offer, "payment-replay")

		_, err := fixture.db.ExecContext(ctx, `
			INSERT INTO purchase_order_item(
				order_id,edition_id,game_id,game_slug_snapshot,game_name_snapshot,edition_code_snapshot,edition_name_snapshot,unit_price_minor,quantity
			) VALUES (?,?,?,?,?,?,?,?,1)`, created.ID, offer.EditionID, offer.GameID, offer.GameSlug, offer.GameName, offer.EditionCode, offer.EditionName, offer.AmountMinor)
		requireMySQLErrorNumber(t, err, 1062)

		command := order.PayCommand{
			OrderNo: created.OrderNo, UserID: userID, IdempotencyKey: fixture.prefix + ".same.pay",
			ProviderReference: fixture.prefix + ".same.provider", Now: time.Now().UTC(),
		}
		type outcome struct {
			result order.PayResult
			err    error
		}
		start := make(chan struct{})
		outcomes := make(chan outcome, 2)
		for range 2 {
			go func() {
				<-start
				result, err := store.Pay(ctx, command)
				outcomes <- outcome{result: result, err: err}
			}()
		}
		close(start)
		replays := 0
		for range 2 {
			outcome := <-outcomes
			if outcome.err != nil {
				t.Fatalf("same-order payment: %v", outcome.err)
			}
			if outcome.result.Replayed {
				replays++
			}
		}
		if replays != 1 {
			t.Fatalf("same-order payment replays=%d, want 1", replays)
		}

		if _, err := fixture.db.ExecContext(ctx, `
			INSERT INTO payment_record(order_id,provider,provider_reference,status,amount_minor,idempotency_key)
			VALUES (?,'sandbox',?,'failed',?,?)`, created.ID, fixture.prefix+".failed.provider", created.TotalMinor, fixture.prefix+".failed.pay"); err != nil {
			t.Fatalf("failed payment history should coexist: %v", err)
		}
		_, err = fixture.db.ExecContext(ctx, `
			INSERT INTO payment_record(order_id,provider,provider_reference,status,amount_minor,idempotency_key)
			VALUES (?,'sandbox',?,'paid',?,?)`, created.ID, fixture.prefix+".second.provider", created.TotalMinor, fixture.prefix+".second.pay")
		requireMySQLErrorNumber(t, err, 1062)

		var items, paidPayments, entitlements int
		if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purchase_order_item WHERE order_id=?`, created.ID).Scan(&items); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_record WHERE order_id=? AND status='paid'`, created.ID).Scan(&paidPayments); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM game_entitlement WHERE source_order_id=? AND status='active'`, created.ID).Scan(&entitlements); err != nil {
			t.Fatal(err)
		}
		if items != 1 || paidPayments != 1 || entitlements != 1 {
			t.Fatalf("items=%d paid_payments=%d entitlements=%d", items, paidPayments, entitlements)
		}
	})

	t.Run("different orders cannot grant the same entitlement", func(t *testing.T) {
		userID := fixture.insertUser(t, ctx, "payment_compete")
		offer := fixture.insertCatalog(t, ctx, "payment-compete", 3499)
		first := fixture.createOrder(t, ctx, store, userID, offer, "payment-compete-a")
		second := fixture.createOrder(t, ctx, store, userID, offer, "payment-compete-b")

		type outcome struct{ err error }
		start := make(chan struct{})
		outcomes := make(chan outcome, 2)
		for _, pending := range []struct {
			label   string
			orderNo string
		}{{"a", first.OrderNo}, {"b", second.OrderNo}} {
			go func(pending struct {
				label   string
				orderNo string
			}) {
				<-start
				_, err := store.Pay(ctx, order.PayCommand{
					OrderNo: pending.orderNo, UserID: userID,
					IdempotencyKey:    fixture.prefix + ".compete.pay." + pending.label,
					ProviderReference: fixture.prefix + ".compete.provider." + pending.label,
					Now:               time.Now().UTC(),
				})
				outcomes <- outcome{err: err}
			}(pending)
		}
		close(start)
		paid, alreadyOwned := 0, 0
		for range 2 {
			switch err := (<-outcomes).err; {
			case err == nil:
				paid++
			case errors.Is(err, order.ErrAlreadyOwned):
				alreadyOwned++
			default:
				t.Fatalf("competing payment: %v", err)
			}
		}

		var paidOrders, pendingOrders, payments, entitlements int
		if err := fixture.db.QueryRowContext(ctx, `
			SELECT COUNT(CASE WHEN status='paid' THEN 1 END),COUNT(CASE WHEN status='pending_payment' THEN 1 END)
			FROM purchase_order WHERE id IN (?,?)`, first.ID, second.ID).Scan(&paidOrders, &pendingOrders); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_record WHERE order_id IN (?,?) AND status='paid'`, first.ID, second.ID).Scan(&payments); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM game_entitlement WHERE user_id=? AND edition_id=? AND status='active'`, userID, offer.EditionID).Scan(&entitlements); err != nil {
			t.Fatal(err)
		}
		if paid != 1 || alreadyOwned != 1 || paidOrders != 1 || pendingOrders != 1 || payments != 1 || entitlements != 1 {
			t.Fatalf("paid=%d already_owned=%d paid_orders=%d pending_orders=%d payments=%d entitlements=%d", paid, alreadyOwned, paidOrders, pendingOrders, payments, entitlements)
		}
	})
}

func testMySQLReleaseJobLeases(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	requireNoDueReleaseJobs(t, ctx, fixture.db)

	userID := fixture.insertUser(t, ctx, "release_worker")
	offer := fixture.insertCatalog(t, ctx, "release-worker", 1599)
	now := time.Now().UTC().Truncate(time.Microsecond)
	activityID := fixture.insertActivity(t, ctx, offer.EditionID, userID, "GLOBAL", "activity", "active", now.Add(-time.Hour), now.Add(time.Hour))

	gate, err := fixture.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	gateOpen := true
	defer func() {
		if gateOpen {
			_ = gate.Rollback()
		}
	}()
	firstDue := time.Date(1000, time.January, 1, 0, 0, 0, 1_000, time.UTC)
	firstID := fixture.insertReleaseJob(t, ctx, activityID, userID, "first", firstDue)
	var lockedID int64
	if err := gate.QueryRowContext(ctx, `SELECT id FROM flash_sale_release_job WHERE id=? FOR UPDATE`, firstID).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	if lockedID != firstID {
		t.Fatalf("locked release job=%d, want %d", lockedID, firstID)
	}
	secondID := fixture.insertReleaseJob(t, ctx, activityID, userID, "second", firstDue.Add(time.Microsecond))
	if _, err := gate.ExecContext(ctx, `UPDATE flash_sale_release_job SET next_attempt_at='9999-12-31 23:59:59.999999' WHERE id=?`, firstID); err != nil {
		t.Fatal(err)
	}

	store := flashmysql.NewStore(fixture.db)
	type claimOutcome struct {
		jobs []flashsale.ReleaseJob
		err  error
	}
	workerCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	claimed := make(chan claimOutcome, 1)
	go func() {
		jobs, err := store.ClaimReleaseJobs(workerCtx, 1, 5*time.Second)
		claimed <- claimOutcome{jobs: jobs, err: err}
	}()

	var outcome claimOutcome
	select {
	case outcome = <-claimed:
	case <-workerCtx.Done():
		t.Fatalf("release worker did not skip a locked row: %v", workerCtx.Err())
	}
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if len(outcome.jobs) != 1 || outcome.jobs[0].ID != secondID || outcome.jobs[0].LeaseGeneration != 1 {
		t.Fatalf("claimed jobs=%+v, want second job %d at generation 1", outcome.jobs, secondID)
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	gateOpen = false

	var status string
	var attempts int
	var leaseActive bool
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT status,attempts,lease_until>CURRENT_TIMESTAMP(6) FROM flash_sale_release_job WHERE id=?`, secondID).Scan(
		&status, &attempts, &leaseActive,
	); err != nil {
		t.Fatal(err)
	}
	if status != "leased" || attempts != 1 || !leaseActive {
		t.Fatalf("first lease status=%q attempts=%d active=%v", status, attempts, leaseActive)
	}
	if _, err := fixture.db.ExecContext(ctx, `UPDATE flash_sale_release_job SET lease_until=UTC_TIMESTAMP(6)-INTERVAL 1 MICROSECOND WHERE id=?`, secondID); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ClaimReleaseJobs(ctx, 1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != secondID || jobs[0].LeaseGeneration != 2 {
		t.Fatalf("expired lease claim=%+v, want job %d at generation 2", jobs, secondID)
	}
	if err := store.CompleteReleaseJob(ctx, secondID, 1); !errors.Is(err, flashsale.ErrNotFound) {
		t.Fatalf("stale lease completion error=%v, want not found", err)
	}
	if err := store.CompleteReleaseJob(ctx, secondID, 2); err != nil {
		t.Fatal(err)
	}
	var leaseUntil sql.NullTime
	if err := fixture.db.QueryRowContext(ctx, `SELECT status,attempts,lease_until FROM flash_sale_release_job WHERE id=?`, secondID).Scan(&status, &attempts, &leaseUntil); err != nil {
		t.Fatal(err)
	}
	if status != "done" || attempts != 2 || leaseUntil.Valid {
		t.Fatalf("release job status=%q attempts=%d lease=%v", status, attempts, leaseUntil)
	}
}

func testMySQLReleaseJobWorkers(t *testing.T, ctx context.Context, fixture *mysqlLiveFixture) {
	t.Helper()
	requireNoDueReleaseJobs(t, ctx, fixture.db)
	userID := fixture.insertUser(t, ctx, "release_workers")
	offer := fixture.insertCatalog(t, ctx, "release-workers", 1700)
	now := time.Now().UTC().Truncate(time.Microsecond)
	activityID := fixture.insertActivity(t, ctx, offer.EditionID, userID, "GLOBAL", "release-workers", "active", now.Add(-time.Hour), now.Add(time.Hour))
	allIDs := make([]int64, 0, 4)
	for index := range 4 {
		id := fixture.insertReleaseJob(t, ctx, activityID, userID, fmt.Sprintf("worker-%d", index), now.Add(-time.Minute+time.Duration(index)*time.Microsecond))
		allIDs = append(allIDs, id)
	}

	workerA, err := fixture.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	workerAOpen := true
	defer func() {
		if workerAOpen {
			_ = workerA.Rollback()
		}
	}()
	rows, err := workerA.QueryContext(ctx, `SELECT id FROM flash_sale_release_job
		WHERE claimable_at<=CURRENT_TIMESTAMP(6)
		ORDER BY claimable_at,id LIMIT 2 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		t.Fatal(err)
	}
	workerAIDs := make([]int64, 0, 2)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		workerAIDs = append(workerAIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(workerAIDs) != 2 || workerAIDs[0] != allIDs[0] || workerAIDs[1] != allIDs[1] {
		t.Fatalf("worker A locked jobs %v, want first due jobs %v", workerAIDs, allIDs[:2])
	}

	type claimOutcome struct {
		jobs []flashsale.ReleaseJob
		err  error
	}
	workerBCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	workerBDone := make(chan claimOutcome, 1)
	go func() {
		jobs, err := flashmysql.NewStore(fixture.db).ClaimReleaseJobs(workerBCtx, 2, 10*time.Second)
		workerBDone <- claimOutcome{jobs: jobs, err: err}
	}()

	var workerB claimOutcome
	select {
	case workerB = <-workerBDone:
	case <-workerBCtx.Done():
		t.Fatalf("worker B did not skip worker A's uncommitted LIMIT 2 locks: %v", workerBCtx.Err())
	}
	if workerB.err != nil {
		t.Fatal(workerB.err)
	}
	if len(workerB.jobs) != 2 || workerB.jobs[0].ID != allIDs[2] || workerB.jobs[1].ID != allIDs[3] {
		t.Fatalf("worker B claimed jobs %+v while worker A held %v, want remaining jobs %v", workerB.jobs, workerAIDs, allIDs[2:])
	}
	for _, job := range workerB.jobs {
		if job.LeaseGeneration != 1 || job.Attempts != 1 {
			t.Fatalf("release job %d generation=%d attempts=%d, want 1/1", job.ID, job.LeaseGeneration, job.Attempts)
		}
	}
	updated, err := workerA.ExecContext(ctx, `
		UPDATE flash_sale_release_job
		SET status='leased',attempts=attempts+1,
			lease_until=TIMESTAMPADD(MICROSECOND,?,CURRENT_TIMESTAMP(6)),updated_at=CURRENT_TIMESTAMP(6)
		WHERE id IN (?,?)`, (10 * time.Second).Microseconds(), workerAIDs[0], workerAIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	if affected := rowsAffected(t, updated); affected != 2 {
		t.Fatalf("worker A updated %d locked jobs, want 2", affected)
	}
	if err := workerA.Commit(); err != nil {
		t.Fatal(err)
	}
	workerAOpen = false

	var leased, attemptsOne int
	args := int64Args(allIDs)
	if err := fixture.db.QueryRowContext(ctx, `
		SELECT SUM(status='leased'),SUM(attempts=1) FROM flash_sale_release_job
		WHERE id IN (`+livePlaceholders(len(args))+`)`, args...).Scan(&leased, &attemptsOne); err != nil {
		t.Fatal(err)
	}
	if leased != 4 || attemptsOne != 4 {
		t.Fatalf("leased=%d attempts_one=%d, want 4/4", leased, attemptsOne)
	}
}

func requireNoDueReleaseJobs(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var existingDue int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM flash_sale_release_job
		WHERE claimable_at<=CURRENT_TIMESTAMP(6)`).Scan(&existingDue); err != nil {
		t.Fatal(err)
	}
	if existingDue != 0 {
		t.Fatalf("live MySQL test database is not isolated: found %d pre-existing due release jobs", existingDue)
	}
}

func (f *mysqlLiveFixture) insertUser(t *testing.T, ctx context.Context, label string) int64 {
	t.Helper()
	username := f.prefix + "_" + liveHex(f.prefix, label)[:8]
	result, err := f.db.ExecContext(ctx, `INSERT INTO user_account(user_name,display_name) VALUES (?,?)`, username, "Live "+label)
	if err != nil {
		t.Fatal(err)
	}
	id := lastInsertID(t, result)
	f.userIDs = append(f.userIDs, id)
	return id
}

func (f *mysqlLiveFixture) insertCatalog(t *testing.T, ctx context.Context, label string, amount int64) catalogentity.PurchaseOffer {
	t.Helper()
	slug := f.prefix + "-" + label
	f.gameSlugs = append(f.gameSlugs, slug)
	gameName := "Live " + label + " 🎮"
	result, err := f.db.ExecContext(ctx, `INSERT INTO game(slug,name,description) VALUES (?,?,?)`, slug, gameName, "integration 🎮")
	if err != nil {
		t.Fatal(err)
	}
	gameID := lastInsertID(t, result)
	f.gameIDs = append(f.gameIDs, gameID)
	result, err = f.db.ExecContext(ctx, `INSERT INTO game_edition(game_id,code,name) VALUES (?,'standard','Standard')`, gameID)
	if err != nil {
		t.Fatal(err)
	}
	editionID := lastInsertID(t, result)
	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO game_price(edition_id,region_code,currency,amount_minor,active_from)
		VALUES (?,'GLOBAL','USD',?,UTC_TIMESTAMP(6)-INTERVAL 1 MINUTE)`, editionID, amount); err != nil {
		t.Fatal(err)
	}
	return catalogentity.PurchaseOffer{
		GameID: gameID, GameSlug: slug, GameName: gameName, EditionID: editionID,
		EditionCode: "standard", EditionName: "Standard", AmountMinor: amount, Currency: "USD", Region: "GLOBAL",
	}
}

func (f *mysqlLiveFixture) insertCoupon(t *testing.T, ctx context.Context, label string, totalStock int64, perUserLimit int) (int64, string) {
	t.Helper()
	codeSuffix := strings.ToUpper(liveHex(f.prefix, label)[:12])
	campaignCode := "CP-" + codeSuffix
	couponCode := "CO-" + codeSuffix
	result, err := f.db.ExecContext(ctx, `
		INSERT INTO coupon_campaign(code,name,status,starts_at,ends_at)
		VALUES (?,?,'active',UTC_TIMESTAMP(6)-INTERVAL 1 HOUR,UTC_TIMESTAMP(6)+INTERVAL 1 HOUR)`,
		campaignCode, "Live "+label)
	if err != nil {
		t.Fatal(err)
	}
	campaignID := lastInsertID(t, result)
	f.campaignIDs = append(f.campaignIDs, campaignID)
	result, err = f.db.ExecContext(ctx, `
		INSERT INTO coupon_definition(
			campaign_id,code,name,discount_type,fixed_minor,currency,total_stock,per_user_limit
		) VALUES (?,?,?,'fixed',100,'USD',?,?)`, campaignID, couponCode, "Live "+label, totalStock, perUserLimit)
	if err != nil {
		t.Fatal(err)
	}
	couponID := lastInsertID(t, result)
	f.couponIDs = append(f.couponIDs, couponID)
	return couponID, couponCode
}

func (f *mysqlLiveFixture) insertActivity(t *testing.T, ctx context.Context, editionID, creatorID int64, region, label, status string, startsAt, endsAt time.Time) int64 {
	t.Helper()
	code := strings.ToUpper("LI-" + liveHex(f.prefix, label)[:12])
	version := int64(0)
	var activatedAt any
	if status == string(flashentity.StatusActive) {
		version = 1
		activatedAt = startsAt.UTC()
	}
	result, err := f.db.ExecContext(ctx, `
		INSERT INTO flash_sale_activity(
			code,edition_id,region_code,currency,sale_price_minor,total_stock,allocated_stock,status,
			starts_at,ends_at,payment_timeout_seconds,version,created_by,activated_at
		) VALUES (?,?,?,'USD',999,10,0,?,?,?,?,?,?,?)`,
		code, editionID, region, status, startsAt.UTC(), endsAt.UTC(), 900, version, creatorID, activatedAt)
	if err != nil {
		t.Fatal(err)
	}
	id := lastInsertID(t, result)
	f.activityIDs = append(f.activityIDs, id)
	return id
}

func (f *mysqlLiveFixture) createOrder(t *testing.T, ctx context.Context, store *ordermysql.Store, userID int64, offer catalogentity.PurchaseOffer, label string) orderentity.Order {
	t.Helper()
	result, err := store.Create(ctx, order.CreateCommand{
		OrderNo: "ord_" + liveHex(f.prefix, label), UserID: userID,
		IdempotencyKey: f.prefix + ".create." + label, Offer: offer, TotalMinor: offer.AmountMinor, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Order
}

func (f *mysqlLiveFixture) insertReleaseJob(t *testing.T, ctx context.Context, activityID, userID int64, label string, nextAttemptAt time.Time) int64 {
	t.Helper()
	requestID := "fsr_1_" + liveHex(f.prefix, "request-"+label)
	digest := sha256.Sum256([]byte(f.prefix + "/digest/" + label))
	result, err := f.db.ExecContext(ctx, `
		INSERT INTO flash_sale_release_job(
			request_id,activity_id,user_id,idempotency_digest,reserved_at,reason,status,next_attempt_at
		) VALUES (?,?,?,?,UTC_TIMESTAMP(6),'admin_repair','pending',?)`,
		requestID, activityID, userID, digest[:], nextAttemptAt.UTC())
	if err != nil {
		t.Fatal(err)
	}
	id := lastInsertID(t, result)
	f.releaseJobIDs = append(f.releaseJobIDs, id)
	return id
}

func liveHex(prefix, label string) string {
	sum := sha256.Sum256([]byte(prefix + "/" + label))
	return fmt.Sprintf("%x", sum[:16])
}

func lastInsertID(t *testing.T, result sql.Result) int64 {
	t.Helper()
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func rowsAffected(t *testing.T, result sql.Result) int64 {
	t.Helper()
	rows, err := result.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func requireMySQLErrorNumber(t *testing.T, err error, number uint16) {
	t.Helper()
	var mysqlErr *drivermysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != number {
		t.Fatalf("MySQL error=%v, want number %d", err, number)
	}
}

func livePlaceholders(count int) string {
	if count == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func int64Args(values []int64) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}
