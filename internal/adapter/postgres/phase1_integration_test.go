package postgres_test

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"strconv"
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
	communityentity "github.com/red060324/XiaoLanHe/internal/community/entity"
	communitypg "github.com/red060324/XiaoLanHe/internal/community/repository/postgres"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
	orderentity "github.com/red060324/XiaoLanHe/internal/order/entity"
	orderpg "github.com/red060324/XiaoLanHe/internal/order/repository/postgres"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	promotionentity "github.com/red060324/XiaoLanHe/internal/promotion/entity"
	promotionpg "github.com/red060324/XiaoLanHe/internal/promotion/repository/postgres"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
	"github.com/red060324/XiaoLanHe/migrations"
)

func TestProductPostgres(t *testing.T) {
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
	const schema = "xlh_product_test"
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
	if err := pool.QueryRow(ctx, `select count(*) from schema_migration`).Scan(&versions); err != nil || versions != 5 {
		t.Fatalf("migration versions=%d err=%v", versions, err)
	}

	changed := fstest.MapFS{}
	for _, name := range []string{"001_initial_schema.sql", "002_account_catalog.sql", "003_community.sql", "004_promotion.sql", "005_order_payment.sql"} {
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

	other, err := accountStore.Register(ctx, "phase_other", "Phase Other", strings.Repeat("q", 60), strings.Repeat("d", 64), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	communityStore := communitypg.NewStore(pool)
	communityService := community.NewService(communityStore, catalog.NewService(catalogStore))
	owner := auth.Principal{UserID: user.ID, Role: auth.RoleUser}
	otherPrincipal := auth.Principal{UserID: other.ID, Role: auth.RoleUser}
	first, err := communityService.CreatePost(ctx, owner, communityentity.PostDraft{GameID: game.ID, Title: "First", Content: "First post"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := communityService.CreatePost(ctx, owner, communityentity.PostDraft{GameID: game.ID, Title: "Second", Content: "Second post"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update community_post set created_at='2026-08-31T00:00:00Z' where id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update community_post set created_at='2026-08-31T00:00:01Z' where id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	searchPage, err := communityService.ListPosts(ctx, community.ListPostsInput{Query: "second", Limit: 10})
	if err != nil || len(searchPage.Items) != 1 || searchPage.Items[0].ID != second.ID {
		t.Fatalf("community search=%+v err=%v", searchPage, err)
	}
	literalWildcard, err := communityService.ListPosts(ctx, community.ListPostsInput{Query: "%", Limit: 10})
	if err != nil || len(literalWildcard.Items) != 0 {
		t.Fatalf("literal wildcard search=%+v err=%v", literalWildcard, err)
	}
	page, err := communityService.ListPosts(ctx, community.ListPostsInput{GameID: game.ID, ViewerID: user.ID, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID || page.NextCursor == "" {
		t.Fatalf("first community page=%+v err=%v", page, err)
	}
	third, err := communityService.CreatePost(ctx, owner, communityentity.PostDraft{GameID: game.ID, Title: "Third", Content: "Third post"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update community_post set created_at='2026-08-31T00:00:02Z' where id=$1`, third.ID); err != nil {
		t.Fatal(err)
	}
	next, err := communityService.ListPosts(ctx, community.ListPostsInput{GameID: game.ID, ViewerID: user.ID, Cursor: page.NextCursor, Limit: 1})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != first.ID {
		t.Fatalf("next community page=%+v err=%v", next, err)
	}

	comment, err := communityService.CreateComment(ctx, otherPrincipal, second.ID, "Useful guide")
	if err != nil || comment.Author.ID != other.ID {
		t.Fatalf("comment=%+v err=%v", comment, err)
	}
	reactionErrs := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := communityService.SetReaction(ctx, otherPrincipal, second.ID, "helpful", true)
			reactionErrs <- err
		}()
	}
	for range 8 {
		if err := <-reactionErrs; err != nil {
			t.Fatalf("reaction: %v", err)
		}
	}
	detail, err := communityService.GetPost(ctx, second.ID, other.ID)
	if err != nil || detail.CommentCount != 1 || detail.Reactions.Counts[communityentity.ReactionHelpful] != 1 {
		t.Fatalf("community detail=%+v err=%v", detail, err)
	}
	if _, err := communityService.ModeratePost(ctx, auth.Principal{UserID: 1, Role: auth.RoleAdmin}, second.ID, "hidden"); err != nil {
		t.Fatal(err)
	}
	if _, err := communityService.GetPost(ctx, second.ID, other.ID); !errors.Is(err, community.ErrNotFound) {
		t.Fatalf("hidden public post error=%v", err)
	}

	promotionService := promotion.NewService(promotionpg.NewStore(pool))
	campaignID := insertCampaign(t, ctx, pool, "phase-campaign", "active", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	lastCouponID := insertCoupon(t, ctx, pool, campaignID, "LASTONE", 1, 1, game.ID, game.Editions[0].ID)
	idempotentCouponID := insertCoupon(t, ctx, pool, campaignID, "IDEMP20", 10, 1, game.ID, game.Editions[0].ID)
	insertCoupon(t, ctx, pool, campaignID, "OTHER20", 10, 1, game.ID, game.Editions[0].ID)

	dealPage, err := promotionService.List(ctx, promotion.ListInput{GameID: game.ID, ViewerID: user.ID})
	if err != nil || len(dealPage.Items) != 3 {
		t.Fatalf("promotion page=%+v err=%v", dealPage, err)
	}

	claimIDs := make(chan int64, 8)
	claimErrs := make(chan error, 8)
	for range 8 {
		go func() {
			result, err := promotionService.Claim(ctx, owner, "IDEMP20", "same-key.001")
			claimErrs <- err
			claimIDs <- result.Claim.ID
		}()
	}
	var firstClaimID int64
	for range 8 {
		if err := <-claimErrs; err != nil {
			t.Fatalf("idempotent claim: %v", err)
		}
		claimID := <-claimIDs
		if firstClaimID == 0 {
			firstClaimID = claimID
		} else if claimID != firstClaimID {
			t.Fatalf("claim ids differ: first=%d got=%d", firstClaimID, claimID)
		}
	}
	assertCouponCounts(t, ctx, pool, idempotentCouponID, 1, 1)
	if _, err := promotionService.Claim(ctx, owner, "IDEMP20", "other-key.001"); !errors.Is(err, promotion.ErrClaimLimit) {
		t.Fatalf("claim limit error=%v", err)
	}
	if _, err := promotionService.Claim(ctx, owner, "OTHER20", "same-key.001"); !errors.Is(err, promotion.ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error=%v", err)
	}

	stockErrs := make(chan error, 2)
	for _, principal := range []auth.Principal{owner, otherPrincipal} {
		go func(principal auth.Principal) {
			_, err := promotionService.Claim(ctx, principal, "LASTONE", "last-stock."+strconv.FormatInt(principal.UserID, 10))
			stockErrs <- err
		}(principal)
	}
	succeeded, exhausted := 0, 0
	for range 2 {
		switch err := <-stockErrs; {
		case err == nil:
			succeeded++
		case errors.Is(err, promotionentity.ErrExhausted):
			exhausted++
		default:
			t.Fatalf("last stock error=%v", err)
		}
	}
	if succeeded != 1 || exhausted != 1 {
		t.Fatalf("last stock succeeded=%d exhausted=%d", succeeded, exhausted)
	}
	assertCouponCounts(t, ctx, pool, lastCouponID, 1, 1)

	insertCoupon(t, ctx, pool, campaignID, "ORDER20", 1, 1, game.ID, game.Editions[0].ID)
	orderCoupon, err := promotionService.Claim(ctx, otherPrincipal, "ORDER20", "order-coupon.01")
	if err != nil {
		t.Fatal(err)
	}
	orderService := order.NewService(orderpg.NewStore(pool), catalog.NewService(catalogStore), promotionService)
	createInput := order.CreateInput{EditionID: game.Editions[0].ID, Region: "CN", Currency: "USD", CouponClaimID: orderCoupon.Claim.ID, IdempotencyKey: "order-create.01"}
	orderResults := make(chan order.CreateResult, 8)
	orderErrs := make(chan error, 8)
	for range 8 {
		go func() {
			result, err := orderService.Create(ctx, otherPrincipal, createInput)
			orderResults <- result
			orderErrs <- err
		}()
	}
	var createdOrder orderentity.Order
	for range 8 {
		if err := <-orderErrs; err != nil {
			t.Fatalf("idempotent order: %v", err)
		}
		result := <-orderResults
		if createdOrder.ID == 0 {
			createdOrder = result.Order
		} else if result.Order.ID != createdOrder.ID {
			t.Fatalf("order ids differ: first=%d got=%d", createdOrder.ID, result.Order.ID)
		}
	}
	if createdOrder.Item.Region != "CN" || createdOrder.SubtotalMinor != 1999 || createdOrder.DiscountMinor != 399 || createdOrder.TotalMinor != 1600 {
		t.Fatalf("order snapshot=%+v", createdOrder)
	}
	var orderCount int
	if err := pool.QueryRow(ctx, `select count(*) from purchase_order where user_id=$1 and idempotency_key=$2`, other.ID, createInput.IdempotencyKey).Scan(&orderCount); err != nil || orderCount != 1 {
		t.Fatalf("order count=%d err=%v", orderCount, err)
	}
	if _, err := pool.Exec(ctx, `update game_price set active_until=now() where edition_id=$1 and currency='USD' and active_until is null`, game.Editions[0].ID); err != nil {
		t.Fatal(err)
	}
	if replay, err := orderService.Create(ctx, otherPrincipal, createInput); err != nil || !replay.Replayed || replay.Order.ID != createdOrder.ID {
		t.Fatalf("order replay=%+v err=%v", replay, err)
	}
	conflict := createInput
	conflict.Region = "GLOBAL"
	if _, err := orderService.Create(ctx, otherPrincipal, conflict); !errors.Is(err, order.ErrIdempotencyConflict) {
		t.Fatalf("order idempotency conflict=%v", err)
	}
	if _, err := orderService.Get(ctx, owner, createdOrder.OrderNo); !errors.Is(err, order.ErrForbidden) {
		t.Fatalf("cross-user order error=%v", err)
	}

	paymentResults := make(chan order.PayResult, 8)
	paymentErrs := make(chan error, 8)
	for range 8 {
		go func() {
			result, err := orderService.Pay(ctx, otherPrincipal, createdOrder.OrderNo, "payment-same.01")
			paymentResults <- result
			paymentErrs <- err
		}()
	}
	for range 8 {
		if err := <-paymentErrs; err != nil {
			t.Fatalf("idempotent payment: %v", err)
		}
		result := <-paymentResults
		if result.Order.Status != orderentity.StatusPaid || result.Order.Payment == nil {
			t.Fatalf("payment result=%+v", result)
		}
	}
	var payments, entitlements int
	var claimStatus string
	var redeemedOrderID int64
	if err := pool.QueryRow(ctx, `select count(*) from payment_record where order_id=$1`, createdOrder.ID).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from game_entitlement where user_id=$1 and edition_id=$2 and status='active'`, other.ID, game.Editions[0].ID).Scan(&entitlements); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select status,redeemed_order_id from coupon_claim where id=$1`, orderCoupon.Claim.ID).Scan(&claimStatus, &redeemedOrderID); err != nil {
		t.Fatal(err)
	}
	if payments != 1 || entitlements != 1 || claimStatus != "redeemed" || redeemedOrderID != createdOrder.ID {
		t.Fatalf("payments=%d entitlements=%d claim=%s redeemed_order=%d", payments, entitlements, claimStatus, redeemedOrderID)
	}
	if _, err := orderService.Pay(ctx, otherPrincipal, createdOrder.OrderNo, "payment-other.01"); !errors.Is(err, orderentity.ErrInvalidState) {
		t.Fatalf("different payment key error=%v", err)
	}
	owned, err := catalogStore.FindBySlug(ctx, "phase-game", catalog.Pricing{Region: "GLOBAL", Currency: "USD"}, other.ID)
	if err != nil || !owned.Owned {
		t.Fatalf("paid catalog=%+v err=%v", owned, err)
	}

	secondGame := saveGame(t, ctx, catalogStore, "order-game-two", 2999)
	secondOrder, err := orderService.Create(ctx, otherPrincipal, order.CreateInput{EditionID: secondGame.Editions[0].ID, IdempotencyKey: "order-create.02"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update purchase_order set created_at='2026-08-31T00:00:00Z' where id=$1`, createdOrder.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update purchase_order set created_at='2026-08-31T00:00:01Z' where id=$1`, secondOrder.Order.ID); err != nil {
		t.Fatal(err)
	}
	orderPage, err := orderService.List(ctx, otherPrincipal, order.ListInput{Limit: 1})
	if err != nil || len(orderPage.Items) != 1 || orderPage.Items[0].ID != secondOrder.Order.ID || orderPage.NextCursor == "" {
		t.Fatalf("first order page=%+v err=%v", orderPage, err)
	}
	thirdGame := saveGame(t, ctx, catalogStore, "order-game-three", 3999)
	thirdOrder, err := orderService.Create(ctx, otherPrincipal, order.CreateInput{EditionID: thirdGame.Editions[0].ID, IdempotencyKey: "order-create.03"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update purchase_order set created_at='2026-08-31T00:00:02Z' where id=$1`, thirdOrder.Order.ID); err != nil {
		t.Fatal(err)
	}
	nextOrderPage, err := orderService.List(ctx, otherPrincipal, order.ListInput{Cursor: orderPage.NextCursor, Limit: 1})
	if err != nil || len(nextOrderPage.Items) != 1 || nextOrderPage.Items[0].ID != createdOrder.ID {
		t.Fatalf("next order page=%+v err=%v", nextOrderPage, err)
	}
}

func saveGame(t *testing.T, ctx context.Context, store *catalogpg.Store, slug string, amount int64) entity.Game {
	t.Helper()
	game, err := store.Save(ctx, 0, entity.Draft{Slug: slug, Name: slug, Editions: []entity.EditionDraft{{Code: "standard", Name: "Standard", Prices: []entity.Price{{Region: "GLOBAL", Currency: "USD", AmountMinor: amount}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return game
}

func insertCampaign(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code, status string, startsAt, endsAt time.Time) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `insert into coupon_campaign(code,name,status,starts_at,ends_at) values ($1,$1,$2,$3,$4) returning id`, code, status, startsAt, endsAt).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertCoupon(t *testing.T, ctx context.Context, pool *pgxpool.Pool, campaignID int64, code string, stock int64, perUser int, gameID, editionID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `
		insert into coupon_definition(campaign_id,code,name,discount_type,percentage_bps,currency,minimum_minor,total_stock,per_user_limit,game_id,edition_id)
		values ($1,$2,$2,'percentage',2000,'USD',1000,$3,$4,$5,$6) returning id`, campaignID, code, stock, perUser, gameID, editionID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertCouponCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, couponID, wantStock, wantClaims int64) {
	t.Helper()
	var stock, claims int64
	if err := pool.QueryRow(ctx, `select claimed_stock,(select count(*) from coupon_claim where coupon_id=$1) from coupon_definition where id=$1`, couponID).Scan(&stock, &claims); err != nil {
		t.Fatal(err)
	}
	if stock != wantStock || claims != wantClaims {
		t.Fatalf("coupon=%d stock=%d claims=%d want=%d,%d", couponID, stock, claims, wantStock, wantClaims)
	}
}
