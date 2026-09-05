package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	accountpg "github.com/red060324/XiaoLanHe/internal/account/repository/postgres"
	account "github.com/red060324/XiaoLanHe/internal/account/usecase"
	adapterpg "github.com/red060324/XiaoLanHe/internal/adapter/postgres"
	assistantentity "github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistantpg "github.com/red060324/XiaoLanHe/internal/assistant/repository/postgres"
	"github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalogpg "github.com/red060324/XiaoLanHe/internal/catalog/repository/postgres"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	communityentity "github.com/red060324/XiaoLanHe/internal/community/entity"
	communitypg "github.com/red060324/XiaoLanHe/internal/community/repository/postgres"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
	flashentity "github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	flashorder "github.com/red060324/XiaoLanHe/internal/flashsale/repository/order"
	flashpg "github.com/red060324/XiaoLanHe/internal/flashsale/repository/postgres"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	orderentity "github.com/red060324/XiaoLanHe/internal/order/entity"
	orderpg "github.com/red060324/XiaoLanHe/internal/order/repository/postgres"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	promotionentity "github.com/red060324/XiaoLanHe/internal/promotion/entity"
	promotionpg "github.com/red060324/XiaoLanHe/internal/promotion/repository/postgres"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
	assistantusecase "github.com/red060324/XiaoLanHe/internal/usecase"
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
	// Extensions are database-scoped, while this test deliberately migrates
	// several isolated schemas. Install pgvector in public once so a migration
	// in the first temporary schema cannot make the vector type invisible to
	// every later schema through CREATE EXTENSION IF NOT EXISTS.
	if _, err := adminPool.Exec(ctx, `create extension if not exists vector with schema public`); err != nil {
		t.Fatalf("install pgvector in public: %v", err)
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
	testFlashSaleUpgrade(t, ctx, adminPool, databaseURL, migrations.Files)
	testAdvancedAIUpgrade(t, ctx, adminPool, databaseURL, migrations.Files)
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
	if err := pool.QueryRow(ctx, `select count(*) from schema_migration`).Scan(&versions); err != nil || versions != 7 {
		t.Fatalf("migration versions=%d err=%v", versions, err)
	}

	changed := fstest.MapFS{}
	for _, name := range []string{"001_initial_schema.sql", "002_account_catalog.sql", "003_community.sql", "004_promotion.sql", "005_order_payment.sql", "006_flash_sale.sql", "007_advanced_ai.sql"} {
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
	other, err := accountStore.Register(ctx, "phase_other", "Phase Other", strings.Repeat("q", 60), strings.Repeat("d", 64), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	conversationStore := adapterpg.NewConversationStore(pool)
	const conversationKey = "11111111-1111-4111-8111-111111111111"
	guestSessionID, err := conversationStore.FindOrCreateSession(ctx, conversationKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	claimedSessionID, err := conversationStore.FindOrCreateSession(ctx, conversationKey, user.ID)
	if err != nil || claimedSessionID != guestSessionID {
		t.Fatalf("claimed session=%d guest=%d err=%v", claimedSessionID, guestSessionID, err)
	}
	if sameOwnerID, err := conversationStore.FindOrCreateSession(ctx, conversationKey, user.ID); err != nil || sameOwnerID != guestSessionID {
		t.Fatalf("same owner session=%d guest=%d err=%v", sameOwnerID, guestSessionID, err)
	}
	if _, err := conversationStore.FindOrCreateSession(ctx, conversationKey, other.ID); !errors.Is(err, assistantusecase.ErrConversationForbidden) {
		t.Fatalf("cross-user conversation error=%v", err)
	}
	if _, err := conversationStore.FindOrCreateSession(ctx, conversationKey, 0); !errors.Is(err, assistantusecase.ErrConversationForbidden) {
		t.Fatalf("anonymous owned conversation error=%v", err)
	}

	profileStore := assistantpg.NewProfileStore(pool)
	price := int64(20_000)
	if _, err := profileStore.ReplaceAssistantProfile(ctx, user.ID, assistantentity.Profile{
		FavoriteGenres: []string{"strategy"}, PreferredPlatforms: []string{"pc"},
		PreferredLanguages: []string{"zh-CN"}, DefaultRegion: "CN", MaxPriceMinor: &price, Currency: "CNY",
	}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []struct{ role, content string }{
		{"user", "older user"}, {"assistant", "older assistant"},
		{"user", "recent user"}, {"assistant", "recent assistant"},
	} {
		if err := conversationStore.SaveMessage(ctx, claimedSessionID, message.role, message.content, ""); err != nil {
			t.Fatal(err)
		}
	}
	memoryStore := assistantpg.NewMemoryStore(pool)
	candidate, needed, err := memoryStore.PrepareSummary(ctx, claimedSessionID, 2, 1)
	if err != nil || !needed || len(candidate.Messages) != 2 || candidate.PriorWatermark != 0 {
		t.Fatalf("summary candidate=%+v needed=%v err=%v", candidate, needed, err)
	}
	casResults := make(chan bool, 2)
	casErrors := make(chan error, 2)
	for range 2 {
		go func() {
			updated, updateErr := memoryStore.UpdateSummary(ctx, claimedSessionID, 0, candidate.ThroughMessageID, "prefers strategy games", "summary-v1")
			casResults <- updated
			casErrors <- updateErr
		}()
	}
	updatedCount := 0
	for range 2 {
		if updateErr := <-casErrors; updateErr != nil {
			t.Fatal(updateErr)
		}
		if <-casResults {
			updatedCount++
		}
	}
	if updatedCount != 1 {
		t.Fatalf("summary CAS successes=%d", updatedCount)
	}
	contextText, err := conversationStore.LoadContext(ctx, claimedSessionID, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"prefers strategy games", "recent user", "recent assistant"} {
		if !strings.Contains(contextText, expected) {
			t.Fatalf("context missing %q: %s", expected, contextText)
		}
	}
	if strings.Contains(contextText, "defaultRegion") || strings.Contains(contextText, "favoriteGenres") {
		t.Fatalf("typed profile leaked into conversation text: %s", contextText)
	}
	if strings.Contains(contextText, "older user") || strings.Contains(contextText, "older assistant") {
		t.Fatalf("context repeated summarized messages: %s", contextText)
	}
	if updated, err := memoryStore.UpdateSummary(ctx, claimedSessionID, candidate.ThroughMessageID, candidate.Messages[0].ID, "backward", "summary-v1"); err != nil || updated {
		t.Fatal(err)
	}

	knowledgeStore := adapterpg.NewKnowledgeStore(pool)
	if _, err := knowledgeStore.CreateDocument(ctx, assistantusecase.KnowledgeDocument{SourceType: "guide", Title: "Dragon Guide", ContentText: "dragon build guide"}, []string{"dragon build guide"}, nil); err != nil {
		t.Fatal(err)
	}
	t.Run("knowledge wildcard query is literal", func(t *testing.T) {
		items, err := knowledgeStore.SearchKeyword(ctx, "dragon", "", "", 10)
		if err != nil || len(items) != 1 {
			t.Fatalf("knowledge text search=%+v err=%v", items, err)
		}
		items, err = knowledgeStore.SearchKeyword(ctx, "%", "", "", 10)
		if err != nil || len(items) != 0 {
			t.Fatalf("literal knowledge wildcard search=%+v err=%v", items, err)
		}
	})

	catalogStore := catalogpg.NewStore(pool)
	game, err := catalogStore.Save(ctx, 0, entity.Draft{
		Slug: "phase-game", Name: "Phase Game",
		Editions: []entity.EditionDraft{
			{Code: "standard", Name: "Standard", Prices: []entity.Price{
				{Region: "GLOBAL", Currency: "USD", AmountMinor: 1999},
				{Region: "CN", Currency: "CNY", AmountMinor: 9900},
			}},
			{Code: "deluxe", Name: "Deluxe", Prices: []entity.Price{{Region: "GLOBAL", Currency: "USD", AmountMinor: 2999}}},
		},
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
	if !found.Owned || len(found.Editions) != 2 || !found.Editions[0].Owned || found.Editions[1].Owned || len(found.Editions[0].Prices) != 1 || found.Editions[0].Prices[0].AmountMinor != 9900 {
		t.Fatalf("catalog result=%+v", found)
	}
	if _, err := catalogStore.Save(ctx, game.ID, entity.Draft{
		Slug: "phase-game", Name: "Phase Game",
		Editions: []entity.EditionDraft{{Code: "standard", Name: "Standard", Prices: []entity.Price{
			{Region: "GLOBAL", Currency: "USD", AmountMinor: 1999},
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if offer, err := catalogStore.FindPurchaseOffer(ctx, game.Editions[0].ID, catalog.Pricing{Region: "GLOBAL", Currency: "USD"}); err != nil || offer.AmountMinor != 1999 {
		t.Fatalf("replacement offer=%+v err=%v", offer, err)
	}
	if offer, err := catalogStore.FindPurchaseOffer(ctx, game.Editions[0].ID, catalog.Pricing{Region: "CN", Currency: "CNY"}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("omitted offer=%+v err=%v", offer, err)
	}
	literalCatalogWildcard, err := catalog.NewService(catalogStore).List(ctx, catalog.ListInput{Query: "%", Limit: 10})
	if err != nil || len(literalCatalogWildcard.Items) != 0 {
		t.Fatalf("literal catalog wildcard search=%+v err=%v", literalCatalogWildcard, err)
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
	t.Run("published post with no comments returns an empty list", func(t *testing.T) {
		comments, err := communityStore.ListComments(ctx, community.CommentFilter{PostID: first.ID, Limit: 10})
		if err != nil || len(comments) != 0 {
			t.Fatalf("published post comments=%+v err=%v", comments, err)
		}
	})
	if _, err := communityService.ModeratePost(ctx, auth.Principal{UserID: 1, Role: auth.RoleAdmin}, second.ID, "hidden"); err != nil {
		t.Fatal(err)
	}
	if _, err := communityService.GetPost(ctx, second.ID, other.ID); !errors.Is(err, community.ErrNotFound) {
		t.Fatalf("hidden public post error=%v", err)
	}
	t.Run("hidden post rejects direct comment listing", func(t *testing.T) {
		if _, err := communityStore.ListComments(ctx, community.CommentFilter{PostID: second.ID, Limit: 10}); !errors.Is(err, community.ErrNotFound) {
			t.Fatalf("hidden post comments error=%v", err)
		}
	})
	t.Run("hidden post rejects direct comment writes", func(t *testing.T) {
		if _, err := communityStore.CreateComment(ctx, second.ID, other.ID, "late comment"); !errors.Is(err, community.ErrNotFound) {
			t.Fatalf("hidden post comment error=%v", err)
		}
	})
	t.Run("hidden post rejects direct reaction writes", func(t *testing.T) {
		if _, err := communityStore.SetReaction(ctx, second.ID, other.ID, communityentity.ReactionFunny, true); !errors.Is(err, community.ErrNotFound) {
			t.Fatalf("hidden post reaction error=%v", err)
		}
	})

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
	t.Run("coupon claim rejects a campaign that expires while waiting for its lock", func(t *testing.T) {
		var setupNow time.Time
		if err := pool.QueryRow(ctx, `select clock_timestamp()`).Scan(&setupNow); err != nil {
			t.Fatal(err)
		}
		expiringCampaignID := insertCampaign(t, ctx, pool, "CLAIM-EXPIRING-CAMPAIGN", "active", setupNow.Add(-time.Hour), setupNow.Add(time.Hour))
		expiringCouponID := insertCoupon(t, ctx, pool, expiringCampaignID, "CLAIMEXPIRE20", 1, 1, game.ID, game.Editions[0].ID)
		const idempotencyKey = "claim-expiring-campaign.01"
		cleanupCouponClaimByIdempotency(t, pool, other.ID, expiringCouponID, idempotencyKey)

		var commandNow time.Time
		if err := pool.QueryRow(ctx, `select clock_timestamp()`).Scan(&commandNow); err != nil {
			t.Fatal(err)
		}
		result, claimErr := claimPromotionAcrossDatabaseBoundary(t, ctx, pool, promotion.ClaimCommand{
			UserID: other.ID, Code: "CLAIMEXPIRE20", IdempotencyKey: idempotencyKey, Now: commandNow,
		}, func(boundaryCtx context.Context, boundaryTx pgx.Tx) (time.Time, error) {
			var endsAt time.Time
			err := boundaryTx.QueryRow(boundaryCtx, `
				update coupon_campaign set ends_at=clock_timestamp()+interval '100 milliseconds',updated_at=clock_timestamp()
				where id=$1 returning ends_at`, expiringCampaignID).Scan(&endsAt)
			return endsAt, err
		})
		var stock, claims int64
		if err := pool.QueryRow(ctx, `
			select claimed_stock,(select count(*) from coupon_claim where coupon_id=$1)
			from coupon_definition where id=$1`, expiringCouponID).Scan(&stock, &claims); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(claimErr, promotionentity.ErrUnavailable) || stock != 0 || claims != 0 {
			t.Fatalf("expired campaign claim=%+v error=%v stock=%d claims=%d", result, claimErr, stock, claims)
		}
	})

	insertCoupon(t, ctx, pool, campaignID, "ORDER20", 1, 1, game.ID, game.Editions[0].ID)
	orderCoupon, err := promotionService.Claim(ctx, otherPrincipal, "ORDER20", "order-coupon.01")
	if err != nil {
		t.Fatal(err)
	}
	availableClaims, err := promotionService.ListClaims(ctx, otherPrincipal, "", 20)
	if err != nil || len(availableClaims.Items) == 0 || availableClaims.Items[0].ID != orderCoupon.Claim.ID {
		t.Fatalf("available claims before order=%+v err=%v", availableClaims, err)
	}
	orderService := order.NewService(orderpg.NewStore(pool), catalog.NewService(catalogStore), promotionService)
	staleCampaignID := insertCampaign(t, ctx, pool, "STALE-CAMPAIGN", "active", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	insertCoupon(t, ctx, pool, staleCampaignID, "STALE20", 1, 1, game.ID, game.Editions[0].ID)
	staleClaim, err := promotionService.Claim(ctx, otherPrincipal, "STALE20", "stale-coupon.01")
	if err != nil {
		t.Fatal(err)
	}
	staleOffer, err := catalog.NewService(catalogStore).PurchaseOffer(ctx, game.Editions[0].ID, "CN", "USD")
	if err != nil {
		t.Fatal(err)
	}
	staleQuote, err := promotionService.QuoteClaim(ctx, other.ID, staleClaim.Claim.ID, staleOffer.AmountMinor, staleOffer.Currency, staleOffer.GameID, staleOffer.EditionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update coupon_campaign set status='paused' where id=$1`, staleCampaignID); err != nil {
		t.Fatal(err)
	}
	staleTotal, err := orderentity.CalculateTotals(staleOffer.AmountMinor, staleQuote.DiscountMinor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orderpg.NewStore(pool).Create(ctx, order.CreateCommand{
		OrderNo: "ord_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", UserID: other.ID, IdempotencyKey: "order-stale.01",
		Offer: staleOffer, Quote: staleQuote, TotalMinor: staleTotal, Now: time.Now().UTC(),
	}); !errors.Is(err, order.ErrCouponIneligible) {
		t.Fatalf("stale coupon order error=%v", err)
	}
	t.Run("stale coupon definition rejects direct order create", func(t *testing.T) {
		definitionCouponID := insertCoupon(t, ctx, pool, campaignID, "STALEDEF20", 1, 1, game.ID, game.Editions[0].ID)
		definitionClaim, err := promotionService.Claim(ctx, otherPrincipal, "STALEDEF20", "stale-definition.01")
		if err != nil {
			t.Fatal(err)
		}
		definitionQuote, err := promotionService.QuoteClaim(ctx, other.ID, definitionClaim.Claim.ID, staleOffer.AmountMinor, staleOffer.Currency, staleOffer.GameID, staleOffer.EditionID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `update coupon_definition set percentage_bps=2500,updated_at=now() where id=$1`, definitionCouponID); err != nil {
			t.Fatal(err)
		}
		definitionTotal, err := orderentity.CalculateTotals(staleOffer.AmountMinor, definitionQuote.DiscountMinor)
		if err != nil {
			t.Fatal(err)
		}
		const idempotencyKey = "order-stale-definition.01"
		_, createErr := orderpg.NewStore(pool).Create(ctx, order.CreateCommand{
			OrderNo: "ord_cccccccccccccccccccccccccccccccc", UserID: other.ID, IdempotencyKey: idempotencyKey,
			Offer: staleOffer, Quote: definitionQuote, TotalMinor: definitionTotal, Now: time.Now().UTC(),
		})
		var created int
		if err := pool.QueryRow(ctx, `select count(*) from purchase_order where user_id=$1 and idempotency_key=$2`, other.ID, idempotencyKey).Scan(&created); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(createErr, order.ErrCouponIneligible) || created != 0 {
			t.Fatalf("stale definition order error=%v count=%d", createErr, created)
		}
	})
	stalePriceGame := saveGame(t, ctx, catalogStore, "order-stale-price", 1999)
	stalePriceOffer, err := catalog.NewService(catalogStore).PurchaseOffer(ctx, stalePriceGame.Editions[0].ID, "GLOBAL", "USD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogStore.Save(ctx, stalePriceGame.ID, entity.Draft{
		Slug: "order-stale-price", Name: "order-stale-price",
		Editions: []entity.EditionDraft{{Code: "standard", Name: "Standard", Prices: []entity.Price{{Region: "GLOBAL", Currency: "USD", AmountMinor: 2499}}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := orderpg.NewStore(pool).Create(ctx, order.CreateCommand{
		OrderNo: "ord_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", UserID: other.ID, IdempotencyKey: "order-stale.02",
		Offer: stalePriceOffer, TotalMinor: stalePriceOffer.AmountMinor, Now: time.Now().UTC(),
	}); !errors.Is(err, order.ErrPriceUnavailable) {
		t.Fatalf("stale price order error=%v", err)
	}
	t.Run("order create rejects a price that expires while waiting for its lock", func(t *testing.T) {
		expiringPriceGame := saveGame(t, ctx, catalogStore, "order-expiring-price", 2599)
		expiringOffer, err := catalog.NewService(catalogStore).PurchaseOffer(ctx, expiringPriceGame.Editions[0].ID, "GLOBAL", "USD")
		if err != nil {
			t.Fatal(err)
		}
		var commandNow time.Time
		if err := pool.QueryRow(ctx, `select clock_timestamp()`).Scan(&commandNow); err != nil {
			t.Fatal(err)
		}
		const idempotencyKey = "order-expiring-price.01"
		cleanupOrderByIdempotency(t, pool, other.ID, idempotencyKey)
		result, createErr := createOrderAcrossDatabaseBoundary(t, ctx, pool, order.CreateCommand{
			OrderNo: "ord_dddddddddddddddddddddddddddddddd", UserID: other.ID, IdempotencyKey: idempotencyKey,
			Offer: expiringOffer, TotalMinor: expiringOffer.AmountMinor, Now: commandNow,
		}, func(boundaryCtx context.Context, boundaryTx pgx.Tx) (time.Time, error) {
			var activeUntil time.Time
			err := boundaryTx.QueryRow(boundaryCtx, `
				update game_price set active_until=clock_timestamp()+interval '100 milliseconds',updated_at=clock_timestamp()
				where edition_id=$1 and region_code='GLOBAL' and currency='USD' and active_until is null
				returning active_until`, expiringOffer.EditionID).Scan(&activeUntil)
			return activeUntil, err
		})
		var created int
		if err := pool.QueryRow(ctx, `select count(*) from purchase_order where user_id=$1 and idempotency_key=$2`, other.ID, idempotencyKey).Scan(&created); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(createErr, order.ErrPriceUnavailable) || created != 0 {
			t.Fatalf("expired price order=%+v error=%v count=%d", result, createErr, created)
		}
	})
	t.Run("order create rejects a coupon campaign that expires while waiting for its lock", func(t *testing.T) {
		expiringCouponGame := saveGame(t, ctx, catalogStore, "order-expiring-campaign", 2899)
		var setupNow time.Time
		if err := pool.QueryRow(ctx, `select clock_timestamp()`).Scan(&setupNow); err != nil {
			t.Fatal(err)
		}
		expiringCampaignID := insertCampaign(t, ctx, pool, "ORDER-EXPIRING-CAMPAIGN", "active", setupNow.Add(-time.Hour), setupNow.Add(time.Hour))
		insertCoupon(t, ctx, pool, expiringCampaignID, "ORDEREXPIRE20", 1, 1, expiringCouponGame.ID, expiringCouponGame.Editions[0].ID)
		expiringClaim, err := promotionService.Claim(ctx, otherPrincipal, "ORDEREXPIRE20", "order-expiring-claim.01")
		if err != nil {
			t.Fatal(err)
		}
		expiringOffer, err := catalog.NewService(catalogStore).PurchaseOffer(ctx, expiringCouponGame.Editions[0].ID, "GLOBAL", "USD")
		if err != nil {
			t.Fatal(err)
		}
		expiringQuote, err := promotionService.QuoteClaim(ctx, other.ID, expiringClaim.Claim.ID, expiringOffer.AmountMinor, expiringOffer.Currency, expiringOffer.GameID, expiringOffer.EditionID)
		if err != nil {
			t.Fatal(err)
		}
		expiringTotal, err := orderentity.CalculateTotals(expiringOffer.AmountMinor, expiringQuote.DiscountMinor)
		if err != nil {
			t.Fatal(err)
		}
		var commandNow time.Time
		if err := pool.QueryRow(ctx, `select clock_timestamp()`).Scan(&commandNow); err != nil {
			t.Fatal(err)
		}
		const idempotencyKey = "order-expiring-campaign.01"
		cleanupOrderByIdempotency(t, pool, other.ID, idempotencyKey)
		result, createErr := createOrderAcrossDatabaseBoundary(t, ctx, pool, order.CreateCommand{
			OrderNo: "ord_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", UserID: other.ID, IdempotencyKey: idempotencyKey,
			Offer: expiringOffer, Quote: expiringQuote, TotalMinor: expiringTotal, Now: commandNow,
		}, func(boundaryCtx context.Context, boundaryTx pgx.Tx) (time.Time, error) {
			var endsAt time.Time
			err := boundaryTx.QueryRow(boundaryCtx, `
				update coupon_campaign set ends_at=clock_timestamp()+interval '100 milliseconds',updated_at=clock_timestamp()
				where id=$1 returning ends_at`, expiringCampaignID).Scan(&endsAt)
			return endsAt, err
		})
		var created int
		if err := pool.QueryRow(ctx, `select count(*) from purchase_order where user_id=$1 and idempotency_key=$2`, other.ID, idempotencyKey).Scan(&created); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(createErr, order.ErrCouponIneligible) || created != 0 {
			t.Fatalf("expired campaign order=%+v error=%v count=%d", result, createErr, created)
		}
	})
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
	availableClaims, err = promotionService.ListClaims(ctx, otherPrincipal, "", 20)
	if err != nil {
		t.Fatalf("available claims after order=%+v err=%v", availableClaims, err)
	}
	for _, claim := range availableClaims.Items {
		if claim.ID == orderCoupon.Claim.ID {
			t.Fatalf("reserved claim remains available: %+v", availableClaims)
		}
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

	t.Run("flash sale activation interval and concurrency guard", func(t *testing.T) {
		flashGame := saveGame(t, ctx, catalogStore, "flash-activation-guard", 1999)
		flashStore := flashpg.NewStore(pool)
		now := time.Now().UTC()
		createActivity := func(code string, startsAt, endsAt time.Time) flashentity.Activity {
			t.Helper()
			activity, err := flashStore.CreateActivity(ctx, flashentity.Activity{
				Code: code, EditionID: flashGame.Editions[0].ID, Region: "GLOBAL", Currency: "USD",
				SalePriceMinor: 999, TotalStock: 10, StartsAt: startsAt, EndsAt: endsAt,
				PaymentTimeout: 15 * time.Minute, CreatedBy: user.ID,
			})
			if err != nil {
				t.Fatal(err)
			}
			return activity
		}
		editable := createActivity("ACTIVATE-EDITED", now.Add(2*time.Hour), now.Add(3*time.Hour))
		editable.TotalStock = 11
		editable, err := flashStore.UpdateDraft(ctx, editable)
		if err != nil || editable.Version != 1 {
			t.Fatalf("update draft=%+v err=%v", editable, err)
		}
		if _, err := flashStore.ActivateActivity(ctx, editable.ID, 1, now); !errors.Is(err, flashentity.ErrInvalidState) {
			t.Fatalf("stale activation version error=%v", err)
		}
		if _, err := flashStore.ActivateActivity(ctx, editable.ID, 2, now); err != nil {
			t.Fatalf("activate edited draft: %v", err)
		}

		base := createActivity("ACTIVATE-BASE", now.Add(-10*time.Minute), now.Add(10*time.Minute))
		if _, err := flashStore.ActivateActivity(ctx, base.ID, 1, now); err != nil {
			t.Fatalf("activate base: %v", err)
		}
		adjacent := createActivity("ACTIVATE-ADJACENT", base.EndsAt, base.EndsAt.Add(10*time.Minute))
		if _, err := flashStore.ActivateActivity(ctx, adjacent.ID, 1, now); err != nil {
			t.Fatalf("activate adjacent interval: %v", err)
		}
		overlapping := createActivity("ACTIVATE-OVERLAP", base.EndsAt.Add(-time.Minute), base.EndsAt.Add(time.Minute))
		if _, err := flashStore.ActivateActivity(ctx, overlapping.ID, 1, now); !errors.Is(err, flashentity.ErrInvalidState) {
			t.Fatalf("overlapping activation error=%v", err)
		}

		concurrentStart := base.EndsAt.Add(30 * time.Minute)
		first := createActivity("ACTIVATE-RACE-A", concurrentStart, concurrentStart.Add(10*time.Minute))
		second := createActivity("ACTIVATE-RACE-B", concurrentStart, concurrentStart.Add(10*time.Minute))
		start := make(chan struct{})
		activationErrs := make(chan error, 2)
		for _, activity := range []flashentity.Activity{first, second} {
			go func(activity flashentity.Activity) {
				<-start
				_, err := flashStore.ActivateActivity(ctx, activity.ID, 1, now)
				activationErrs <- err
			}(activity)
		}
		close(start)
		succeeded, rejected := 0, 0
		for range 2 {
			switch err := <-activationErrs; {
			case err == nil:
				succeeded++
			case errors.Is(err, flashentity.ErrInvalidState):
				rejected++
			default:
				t.Fatalf("concurrent activation error=%v", err)
			}
		}
		var active, draft int
		if err := pool.QueryRow(ctx, `
			select count(*) filter (where status='active'),count(*) filter (where status='draft')
			from flash_sale_activity where id in ($1,$2)`, first.ID, second.ID).Scan(&active, &draft); err != nil {
			t.Fatal(err)
		}
		if succeeded != 1 || rejected != 1 || active != 1 || draft != 1 {
			t.Fatalf("activation race succeeded=%d rejected=%d active=%d draft=%d", succeeded, rejected, active, draft)
		}
	})

	t.Run("flash sale final stock replay and concurrent expiry", func(t *testing.T) {
		flashGame := saveGame(t, ctx, catalogStore, "flash-final-stock", 1999)
		third, err := accountStore.Register(ctx, "phase_flash_third", "Flash Third", strings.Repeat("r", 60), strings.Repeat("e", 64), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		flashStore := flashpg.NewStore(pool)
		activity, err := flashStore.CreateActivity(ctx, flashentity.Activity{
			Code: "FINAL-STOCK", EditionID: flashGame.Editions[0].ID, Region: "GLOBAL", Currency: "USD",
			SalePriceMinor: 999, TotalStock: 2, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour),
			PaymentTimeout: 15 * time.Minute, CreatedBy: user.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		activity, err = flashStore.ActivateActivity(ctx, activity.ID, 1, now)
		if err != nil {
			t.Fatal(err)
		}
		flashOrders := order.NewService(orderpg.NewStore(pool), catalog.NewService(catalogStore), promotionService)
		flashService := flashsale.NewService(flashStore, catalog.NewService(catalogStore), nil, flashorder.NewService(flashOrders))
		users := []int64{user.ID, other.ID, third.ID}
		events := make([]flashsale.Event, len(users))
		for index, userID := range users {
			digest := strings.Repeat(fmt.Sprintf("%02x", index+1), 32)
			events[index] = flashsale.Event{
				Version: 1, RequestID: fmt.Sprintf("fsr_%s_%s", strconv.FormatInt(activity.ID, 36), digest[:32]),
				ActivityID: activity.ID, ActivityVersion: activity.Version, UserID: userID, ReservedAt: now,
				IdempotencyDigest: digest,
			}
		}
		replayErrs := make(chan error, 8)
		for range 8 {
			go func() { replayErrs <- flashService.Fulfil(ctx, events[0]) }()
		}
		for range 8 {
			if err := <-replayErrs; err != nil {
				t.Fatalf("duplicate fulfil: %v", err)
			}
		}
		allocation, err := flashStore.Allocate(ctx, events[0])
		if err != nil || allocation.IdempotencyDigest != events[0].IdempotencyDigest || !allocation.ReservedAt.Equal(events[0].ReservedAt) {
			t.Fatalf("allocation replay=%+v err=%v", allocation, err)
		}
		mismatched := events[0]
		mismatched.IdempotencyDigest = strings.Repeat("7f", 32)
		if _, err := flashStore.Allocate(ctx, mismatched); !errors.Is(err, flashsale.ErrAlreadyReserved) {
			t.Fatalf("mismatched replay digest error=%v", err)
		}
		mismatched = events[0]
		mismatched.ReservedAt = mismatched.ReservedAt.Add(time.Microsecond)
		if _, err := flashStore.Allocate(ctx, mismatched); !errors.Is(err, flashsale.ErrAlreadyReserved) {
			t.Fatalf("mismatched replay time error=%v", err)
		}
		guardErrs := make(chan error, 2)
		for _, event := range events[1:] {
			go func(event flashsale.Event) { guardErrs <- flashService.Fulfil(ctx, event) }(event)
		}
		for range 2 {
			if err := <-guardErrs; err != nil {
				t.Fatalf("final guard fulfil: %v", err)
			}
		}
		duplicateDigest := strings.Repeat("63", 32)
		duplicateUserEvent := flashsale.Event{
			Version: 1, RequestID: fmt.Sprintf("fsr_%s_%s", strconv.FormatInt(activity.ID, 36), duplicateDigest[:32]),
			ActivityID: activity.ID, ActivityVersion: activity.Version, UserID: users[0], ReservedAt: now,
			IdempotencyDigest: duplicateDigest,
		}
		if err := flashService.Fulfil(ctx, duplicateUserEvent); err != nil {
			t.Fatalf("duplicate-user final guard: %v", err)
		}
		var allocated, reservations, ready, failed, flashOrdersCount, releaseJobs int
		if err := pool.QueryRow(ctx, `select allocated_stock from flash_sale_activity where id=$1`, activity.ID).Scan(&allocated); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `select count(*),count(*) filter (where status='order_ready'),count(*) filter (where status='failed') from flash_sale_reservation where activity_id=$1`, activity.ID).Scan(&reservations, &ready, &failed); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `select count(*) from purchase_order where source_type='flash_sale' and source_reference in ($1,$2,$3)`, events[0].RequestID, events[1].RequestID, events[2].RequestID).Scan(&flashOrdersCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `select count(*) from flash_sale_release_job where activity_id=$1`, activity.ID).Scan(&releaseJobs); err != nil {
			t.Fatal(err)
		}
		if allocated != 2 || reservations != 3 || ready != 2 || failed != 1 || flashOrdersCount != 2 || releaseJobs != 2 {
			t.Fatalf("allocated=%d reservations=%d ready=%d failed=%d orders=%d release_jobs=%d", allocated, reservations, ready, failed, flashOrdersCount, releaseJobs)
		}

		if _, err := pool.Exec(ctx, `update purchase_order set payment_expires_at=statement_timestamp()-interval '1 second' where source_reference=$1`, events[0].RequestID); err != nil {
			t.Fatal(err)
		}
		expiryCounts := make(chan int, 2)
		expiryErrs := make(chan error, 2)
		for range 2 {
			go func() { count, err := flashStore.ExpireDue(ctx, 10); expiryCounts <- count; expiryErrs <- err }()
		}
		expired := 0
		for range 2 {
			expired += <-expiryCounts
			if err := <-expiryErrs; err != nil {
				t.Fatalf("expire due: %v", err)
			}
		}
		if err := pool.QueryRow(ctx, `select allocated_stock from flash_sale_activity where id=$1`, activity.ID).Scan(&allocated); err != nil {
			t.Fatal(err)
		}
		if expired != 1 || allocated != 1 {
			t.Fatalf("expired=%d allocated=%d", expired, allocated)
		}
	})

	secondGame := saveGame(t, ctx, catalogStore, "order-game-two", 2999)
	secondOrder, err := orderService.Create(ctx, otherPrincipal, order.CreateInput{EditionID: secondGame.Editions[0].ID, IdempotencyKey: "order-create.02"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update purchase_order set created_at='2026-08-30T00:00:00Z' where user_id=$1 and source_type='flash_sale'`, other.ID); err != nil {
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

	singleEntitlementGame := saveGame(t, ctx, catalogStore, "order-single-entitlement", 2499)
	firstPending, err := orderService.Create(ctx, otherPrincipal, order.CreateInput{EditionID: singleEntitlementGame.Editions[0].ID, IdempotencyKey: "order-single.01"})
	if err != nil {
		t.Fatal(err)
	}
	secondPending, err := orderService.Create(ctx, otherPrincipal, order.CreateInput{EditionID: singleEntitlementGame.Editions[0].ID, IdempotencyKey: "order-single.02"})
	if err != nil {
		t.Fatal(err)
	}
	duplicatePaymentErrs := make(chan error, 2)
	for index, pending := range []order.CreateResult{firstPending, secondPending} {
		go func(index int, orderNo string) {
			_, err := orderService.Pay(ctx, otherPrincipal, orderNo, "payment-single.0"+strconv.Itoa(index+1))
			duplicatePaymentErrs <- err
		}(index, pending.Order.OrderNo)
	}
	duplicatePaid, duplicateOwned := 0, 0
	for range 2 {
		switch err := <-duplicatePaymentErrs; {
		case err == nil:
			duplicatePaid++
		case errors.Is(err, order.ErrAlreadyOwned):
			duplicateOwned++
		default:
			t.Fatalf("duplicate edition payment error=%v", err)
		}
	}
	var duplicatePayments, duplicateEntitlements, duplicatePending int
	if err := pool.QueryRow(ctx, `select count(*) from payment_record where order_id in ($1,$2)`, firstPending.Order.ID, secondPending.Order.ID).Scan(&duplicatePayments); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from game_entitlement where user_id=$1 and edition_id=$2 and status='active'`, other.ID, singleEntitlementGame.Editions[0].ID).Scan(&duplicateEntitlements); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from purchase_order where id in ($1,$2) and status='pending_payment'`, firstPending.Order.ID, secondPending.Order.ID).Scan(&duplicatePending); err != nil {
		t.Fatal(err)
	}
	if duplicatePaid != 1 || duplicateOwned != 1 || duplicatePayments != 1 || duplicateEntitlements != 1 || duplicatePending != 1 {
		t.Fatalf("duplicate edition paid=%d owned=%d payments=%d entitlements=%d pending=%d", duplicatePaid, duplicateOwned, duplicatePayments, duplicateEntitlements, duplicatePending)
	}
}

func testFlashSaleUpgrade(t *testing.T, ctx context.Context, adminPool *pgxpool.Pool, databaseURL string, files fs.FS) {
	t.Helper()
	const schema = "xlh_flash_upgrade_test"
	if _, err := adminPool.Exec(ctx, "drop schema if exists "+schema+" cascade; create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminPool.Exec(context.Background(), "drop schema if exists "+schema+" cascade") })
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
	defer pool.Close()
	legacy := fstest.MapFS{}
	for _, name := range []string{"001_initial_schema.sql", "002_account_catalog.sql", "003_community.sql", "004_promotion.sql", "005_order_payment.sql"} {
		body, readErr := fs.ReadFile(files, name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		legacy[name] = &fstest.MapFile{Data: body}
	}
	if err := adapterpg.Migrate(ctx, pool, legacy); err != nil {
		t.Fatalf("legacy migrate: %v", err)
	}
	var userID, gameID, editionID int64
	if err := pool.QueryRow(ctx, `insert into user_account(user_name,display_name) values ('upgrade-user','Upgrade User') returning id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `insert into game(slug,name) values ('upgrade-game','Upgrade Game') returning id`).Scan(&gameID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `insert into game_edition(game_id,code,name) values ($1,'standard','Standard') returning id`, gameID).Scan(&editionID); err != nil {
		t.Fatal(err)
	}
	var orderID int64
	if err := pool.QueryRow(ctx, `
		insert into purchase_order(order_no,user_id,status,currency,region_code,subtotal_minor,discount_minor,total_minor,idempotency_key)
		values ('ord_11111111111111111111111111111111',$1,'pending_payment','USD','GLOBAL',1999,0,1999,'upgrade-order.01') returning id`, userID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into purchase_order_item(order_id,edition_id,game_id,game_slug_snapshot,game_name_snapshot,edition_code_snapshot,edition_name_snapshot,unit_price_minor,quantity)
		values ($1,$2,$3,'upgrade-game','Upgrade Game','standard','Standard',1999,1)`, orderID, editionID, gameID); err != nil {
		t.Fatal(err)
	}
	if err := adapterpg.Migrate(ctx, pool, files); err != nil {
		t.Fatalf("upgrade migrate: %v", err)
	}
	var versions int
	var sourceType string
	var sourceReference *string
	var paymentExpiresAt *time.Time
	if err := pool.QueryRow(ctx, `select source_type,source_reference,payment_expires_at from purchase_order where id=$1`, orderID).Scan(&sourceType, &sourceReference, &paymentExpiresAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from schema_migration`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if sourceType != "standard" || sourceReference != nil || paymentExpiresAt != nil || versions != 7 {
		t.Fatalf("source=%s reference=%v expiry=%v versions=%d", sourceType, sourceReference, paymentExpiresAt, versions)
	}
}

func testAdvancedAIUpgrade(t *testing.T, ctx context.Context, adminPool *pgxpool.Pool, databaseURL string, files fs.FS) {
	t.Helper()
	const schema = "xlh_advanced_ai_upgrade_test"
	if _, err := adminPool.Exec(ctx, "drop schema if exists "+schema+" cascade; create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminPool.Exec(context.Background(), "drop schema if exists "+schema+" cascade") })
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
	defer pool.Close()
	legacy := fstest.MapFS{}
	for _, name := range []string{"001_initial_schema.sql", "002_account_catalog.sql", "003_community.sql", "004_promotion.sql", "005_order_payment.sql", "006_flash_sale.sql"} {
		body, readErr := fs.ReadFile(files, name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		legacy[name] = &fstest.MapFile{Data: body}
	}
	if err := adapterpg.Migrate(ctx, pool, legacy); err != nil {
		t.Fatalf("legacy migrate: %v", err)
	}
	var userID, sessionID, messageID int64
	if err := pool.QueryRow(ctx, `insert into user_account(user_name) values ('advanced-upgrade-user') returning id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into player_profile(user_id,preferences) values ($1,'{}'),($1,'{}')`, userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `insert into conversation_session(session_key,user_id,metadata) values ('advanced-upgrade-session',$1,'{"summary_text":"legacy summary"}') returning id`, userID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `insert into conversation_message(session_id,role,content) values ($1,'user','legacy') returning id`, sessionID).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	if err := adapterpg.Migrate(ctx, pool, files); err == nil || !strings.Contains(err.Error(), "duplicate player_profile rows") {
		t.Fatalf("duplicate profile preflight err=%v", err)
	}
	if _, err := pool.Exec(ctx, `delete from player_profile where id=(select max(id) from player_profile where user_id=$1)`, userID); err != nil {
		t.Fatal(err)
	}
	if err := adapterpg.Migrate(ctx, pool, files); err != nil {
		t.Fatalf("advanced migration: %v", err)
	}
	var summary string
	if err := pool.QueryRow(ctx, `select summary_text from conversation_session where id=$1`, sessionID).Scan(&summary); err != nil || summary != "legacy summary" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
	if _, err := pool.Exec(ctx, `update conversation_session set summary_through_message_id=$1,summary_prompt_version='summary-v1' where id=$2`, messageID, sessionID); err != nil {
		t.Fatalf("summary constraints: %v", err)
	}
	if _, err := pool.Exec(ctx, `update conversation_session set summary_prompt_version='bad version' where id=$1`, sessionID); err == nil {
		t.Fatal("expected prompt version constraint")
	}
	if _, err := pool.Exec(ctx, `insert into player_profile(user_id,preferences) values ($1,'{}')`, userID); err == nil {
		t.Fatal("expected unique profile constraint")
	}
	var forbiddenTables int
	if err := pool.QueryRow(ctx, `
		select count(*) from information_schema.tables
		where table_schema=$1 and (table_name like '%lightrag%' or table_name like '%projection%' or table_name like '%outbox%')`, schema).Scan(&forbiddenTables); err != nil || forbiddenTables != 0 {
		t.Fatalf("forbidden advanced knowledge tables=%d err=%v", forbiddenTables, err)
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

func cleanupOrderByIdempotency(t *testing.T, pool *pgxpool.Pool, userID int64, idempotencyKey string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `delete from purchase_order where user_id=$1 and idempotency_key=$2`, userID, idempotencyKey); err != nil {
			t.Errorf("clean up probe order %q: %v", idempotencyKey, err)
		}
	})
}

func cleanupCouponClaimByIdempotency(t *testing.T, pool *pgxpool.Pool, userID, couponID int64, idempotencyKey string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `
			with deleted as (
				delete from coupon_claim where user_id=$1 and idempotency_key=$2 returning coupon_id
			)
			update coupon_definition
			set claimed_stock=greatest(0,claimed_stock-(select count(*) from deleted)),updated_at=clock_timestamp()
			where id=$3`, userID, idempotencyKey, couponID); err != nil {
			t.Errorf("clean up probe coupon claim %q: %v", idempotencyKey, err)
		}
	})
}

func claimPromotionAcrossDatabaseBoundary(
	t *testing.T,
	parentCtx context.Context,
	pool *pgxpool.Pool,
	command promotion.ClaimCommand,
	setBoundary func(context.Context, pgx.Tx) (time.Time, error),
) (promotion.ClaimResult, error) {
	t.Helper()
	return runAcrossDatabaseBoundary(t, parentCtx, pool, command.UserID, command.Now, func(operationCtx context.Context) (promotion.ClaimResult, error) {
		return promotionpg.NewStore(pool).Claim(operationCtx, command)
	}, setBoundary)
}

func createOrderAcrossDatabaseBoundary(
	t *testing.T,
	parentCtx context.Context,
	pool *pgxpool.Pool,
	command order.CreateCommand,
	setBoundary func(context.Context, pgx.Tx) (time.Time, error),
) (order.CreateResult, error) {
	t.Helper()
	return runAcrossDatabaseBoundary(t, parentCtx, pool, command.UserID, command.Now, func(operationCtx context.Context) (order.CreateResult, error) {
		return orderpg.NewStore(pool).Create(operationCtx, command)
	}, setBoundary)
}

func runAcrossDatabaseBoundary[T any](
	t *testing.T,
	parentCtx context.Context,
	pool *pgxpool.Pool,
	userID int64,
	commandNow time.Time,
	operation func(context.Context) (T, error),
	setBoundary func(context.Context, pgx.Tx) (time.Time, error),
) (T, error) {
	t.Helper()
	testCtx, cancel := context.WithTimeout(parentCtx, 5*time.Second)
	defer cancel()

	gateTx, err := pool.Begin(testCtx)
	if err != nil {
		t.Fatalf("begin order lock gate: %v", err)
	}
	gateOpen := true
	defer func() {
		if gateOpen {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), time.Second)
			defer rollbackCancel()
			_ = gateTx.Rollback(rollbackCtx)
		}
	}()
	var gatePID int32
	if err := gateTx.QueryRow(testCtx, `select pg_backend_pid()`).Scan(&gatePID); err != nil {
		t.Fatalf("query order lock gate pid: %v", err)
	}
	if _, err := gateTx.Exec(testCtx, `select pg_advisory_xact_lock($1)`, userID); err != nil {
		t.Fatalf("acquire order lock gate: %v", err)
	}

	type operationOutcome struct {
		result T
		err    error
	}
	outcomes := make(chan operationOutcome, 1)
	go func() {
		result, err := operation(testCtx)
		outcomes <- operationOutcome{result: result, err: err}
	}()

	if err := waitForAdvisoryLockWaiter(testCtx, gateTx, gatePID); err != nil {
		t.Fatalf("wait for order lock contention: %v", err)
	}
	boundary, err := setBoundary(testCtx, gateTx)
	if err != nil {
		t.Fatalf("set order validity boundary: %v", err)
	}
	if !commandNow.Before(boundary) {
		t.Fatalf("command time %s is not before validity boundary %s", commandNow, boundary)
	}
	if err := waitForDatabaseTime(testCtx, gateTx, boundary); err != nil {
		t.Fatalf("wait for order validity boundary: %v", err)
	}
	if err := gateTx.Commit(testCtx); err != nil {
		t.Fatalf("release order lock gate: %v", err)
	}
	gateOpen = false

	select {
	case outcome := <-outcomes:
		return outcome.result, outcome.err
	case <-testCtx.Done():
		t.Fatalf("wait for locked operation result: %v", testCtx.Err())
		var zero T
		return zero, testCtx.Err()
	}
}

func waitForAdvisoryLockWaiter(ctx context.Context, gateTx pgx.Tx, gatePID int32) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := gateTx.QueryRow(ctx, `
			select exists(
				select 1 from pg_locks held join pg_locks waiting
					on waiting.locktype=held.locktype
					and waiting.database is not distinct from held.database
					and waiting.classid is not distinct from held.classid
					and waiting.objid is not distinct from held.objid
					and waiting.objsubid is not distinct from held.objsubid
				where held.pid=$1 and held.locktype='advisory' and held.granted and not waiting.granted
			)`, gatePID).Scan(&waiting)
		if err != nil {
			return err
		}
		if waiting {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForDatabaseTime(ctx context.Context, gateTx pgx.Tx, boundary time.Time) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var reached bool
		if err := gateTx.QueryRow(ctx, `select clock_timestamp()>=$1`, boundary).Scan(&reached); err != nil {
			return err
		}
		if reached {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
