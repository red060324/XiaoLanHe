#!/usr/bin/env bash

set -euo pipefail

base_url="${XLH_SMOKE_BASE_URL:-http://127.0.0.1:18088}"
base_url="${base_url%/}"
run_id="${XLH_SMOKE_RUN_ID:-${GITHUB_RUN_ID:-local}${GITHUB_RUN_ATTEMPT:-0}$$}"
run_id="$(printf '%s' "$run_id" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9' | cut -c1-16)"
run_id="${run_id:-local$$}"
username="smoke_${run_id}"
password="smoke-password-1"
require_v45="${XLH_SMOKE_REQUIRE_V45:-false}"

case "$require_v45" in
  true|false) ;;
  *) echo "XLH_SMOKE_REQUIRE_V45 must be true or false" >&2; exit 2 ;;
esac
if [[ "$require_v45" == "true" && -z "${XLH_SMOKE_ADMIN_PASSWORD:-}" ]]; then
  echo "XLH_SMOKE_ADMIN_PASSWORD is required when XLH_SMOKE_REQUIRE_V45=true" >&2
  exit 2
fi
if [[ "$require_v45" == "true" && -z "${XLH_SMOKE_OPENAI_STUB_STATS_URL:-}" ]]; then
  echo "XLH_SMOKE_OPENAI_STUB_STATS_URL is required when XLH_SMOKE_REQUIRE_V45=true" >&2
  exit 2
fi

for command in curl jq; do
  command -v "$command" >/dev/null || { echo "$command is required" >&2; exit 1; }
done

smoke_dir="$(mktemp -d)"
body_file="$smoke_dir/body.json"
user_cookies="$smoke_dir/user.cookies"
admin_cookies="$smoke_dir/admin.cookies"
second_cookies="$smoke_dir/second-user.cookies"
trap 'rm -f "$body_file" "$user_cookies" "$admin_cookies" "$second_cookies"; rmdir "$smoke_dir"' EXIT

request() {
  local expected_status="$1"
  local method="$2"
  local path="$3"
  shift 3
  local actual_status
  actual_status="$(curl --silent --show-error --output "$body_file" --write-out '%{http_code}' --request "$method" "$base_url$path" "$@")"
  if [[ "$actual_status" != "$expected_status" ]]; then
    echo "$method $path: expected HTTP $expected_status, got $actual_status" >&2
    cat "$body_file" >&2
    exit 1
  fi
}

json_value() {
  jq -er "$@" "$body_file"
}

json_body() {
  jq -nc "$@"
}

request 200 GET /healthz
request 200 GET /readyz
request 200 GET /api/games
json_value '.items | type == "array"' >/dev/null
request 200 GET /api/games/xiaolanhe-demo
game_id="$(json_value '.game.id')"
edition_id="$(json_value '.game.editions[] | select(.code == "standard") | .id')"
json_value '.game.editions[] | select(.code == "standard") | .price.amountMinor == 1999' >/dev/null

if [[ -n "${XLH_SMOKE_ADMIN_PASSWORD:-}" ]]; then
  admin_login="$(json_body --arg password "$XLH_SMOKE_ADMIN_PASSWORD" '{username:"admin",password:$password}')"
  request 200 POST /api/auth/login --cookie-jar "$admin_cookies" --header 'Content-Type: application/json' --data "$admin_login"
  json_value '.user.role == "admin"' >/dev/null

  smoke_slug="smoke-game-$run_id"
  catalog_body="$(json_body --arg slug "$smoke_slug" '{slug:$slug,name:"Smoke Game",summary:"Black-box smoke catalog entry",description:"Created in an isolated rollout smoke.",developer:"XiaoLanHe",publisher:"XiaoLanHe",editions:[{code:"standard",name:"Standard",description:"Smoke edition",prices:[{region:"GLOBAL",currency:"USD",amountMinor:999}]}]}')"
  request 201 POST /api/admin/games --cookie "$admin_cookies" --header "Origin: $base_url" --header 'Content-Type: application/json' --data "$catalog_body"
  json_value --arg slug "$smoke_slug" '.game.slug == $slug' >/dev/null
  smoke_edition_id="$(json_value '.game.editions[] | select(.code == "standard") | .id')"
  request 200 GET "/api/games/$smoke_slug"
  json_value --arg slug "$smoke_slug" '.game.slug == $slug' >/dev/null
fi

register_body="$(json_body --arg username "$username" --arg password "$password" '{username:$username,displayName:"Smoke Player",password:$password}')"
request 201 POST /api/auth/register --cookie-jar "$user_cookies" --header 'Content-Type: application/json' --data "$register_body"
user_id="$(json_value '.user.id')"
json_value --arg username "$username" '.user.username == $username' >/dev/null
request 200 GET /api/me --cookie "$user_cookies"
json_value --arg id "$user_id" '.user.id == $id' >/dev/null

if [[ "$require_v45" == "true" ]]; then
  command -v python3 >/dev/null || { echo "python3 is required for the V45 smoke" >&2; exit 2; }
  stub_stats_before="$(curl --fail --silent --show-error "$XLH_SMOKE_OPENAI_STUB_STATS_URL")"
  jq -e '
    [.router_responses, .tool_call_responses, .tool_result_responses,
     .answer_responses, .embedding_requests, .embedded_inputs]
    | all(type == "number" and . >= 0)
  ' <<<"$stub_stats_before" >/dev/null

  # The public search stays anonymous. Knowledge administration must reject both
  # anonymous and ordinary-user callers before the admin write is attempted.
  request 401 GET /api/admin/knowledge/documents
  json_value '.error.code == "unauthenticated"' >/dev/null
  request 403 GET /api/admin/knowledge/documents --cookie "$user_cookies"
  json_value '.error.code == "forbidden"' >/dev/null

  knowledge_marker="smokeguide${run_id}"
  knowledge_body="$(json_body --arg marker "$knowledge_marker" '{sourceType:"ci-smoke",title:("Smoke Guide " + $marker),gameCode:"smoke-game",regionCode:"GLOBAL",patchVersion:"ci-v1",contentText:("The protected smoke guide marker is " + $marker + ". The Azure Harbor shield is powered by the Moonstone relic.")}')"
  request 401 POST /api/knowledge/documents --header "Origin: $base_url" --header 'Content-Type: application/json' --data "$knowledge_body"
  json_value '.error.code == "unauthenticated"' >/dev/null
  request 403 POST /api/knowledge/documents --cookie "$user_cookies" --header "Origin: $base_url" --header 'Content-Type: application/json' --data "$knowledge_body"
  json_value '.error.code == "forbidden"' >/dev/null
  request 202 POST /api/knowledge/documents --cookie "$admin_cookies" --header "Origin: $base_url" --header 'Content-Type: application/json' --data "$knowledge_body"
  knowledge_track_id="$(json_value '.trackId')"
  knowledge_source_key="$(json_value '.sourceKey')"
  json_value '.status == "accepted"' >/dev/null

  knowledge_document_id=""
  for _ in $(seq 1 "${XLH_SMOKE_KNOWLEDGE_POLL_ATTEMPTS:-120}"); do
    request 200 GET "/api/admin/knowledge/tracks/$knowledge_track_id" --cookie "$admin_cookies"
    knowledge_status="$(jq -r '.documents[0].status // "PENDING"' "$body_file")"
    case "$knowledge_status" in
      PROCESSED)
        knowledge_document_id="$(json_value '.documents[0].documentId')"
        break
        ;;
      FAILED)
        echo "LightRAG smoke document failed to process" >&2
        cat "$body_file" >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  if [[ -z "$knowledge_document_id" ]]; then
    echo "LightRAG smoke document did not reach PROCESSED" >&2
    exit 1
  fi

  request 200 GET "/api/knowledge/search?query=$knowledge_marker&mode=mix&limit=5"
  json_value --arg marker "$knowledge_marker" --arg source "$knowledge_source_key" '
    .query == $marker and .provider == "lightrag" and .mode == "mix" and
    (.items | type == "array" and length > 0) and
    (.items | any(.sourceKey == $source and .evidenceId != "" and .kind != "" and (.text | contains($marker))))
  ' >/dev/null
  request 200 GET /api/admin/knowledge/documents --cookie "$admin_cookies"
  json_value --arg source "$knowledge_source_key" '.items | any(.sourceKey == $source and .status == "PROCESSED")' >/dev/null

  chat_body="$(json_body --arg marker "$knowledge_marker" '{message:("Use the local guide to explain " + $marker + " and name the shield relic.")}')"
  request 200 POST /api/chat/message --cookie "$user_cookies" --header 'Content-Type: application/json' --data "$chat_body"
  json_value --arg source "$knowledge_source_key" '
    (.sessionId | test("^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")) and
    (.createdAt | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?Z$")) and
    (.answer | type == "string") and
    (.answer | contains("CI_OPENAI_STUB_ANSWER")) and
    (.answer | contains("来源：")) and
    (.answer | contains($source)) and
    (.answer | contains("LightRAG source"))
  ' >/dev/null

  stub_stats_after_chat="$(curl --fail --silent --show-error "$XLH_SMOKE_OPENAI_STUB_STATS_URL")"
  jq -e --argjson before "$stub_stats_before" '
    .router_responses == ($before.router_responses + 1) and
    .tool_call_responses == ($before.tool_call_responses + 1) and
    .tool_result_responses == ($before.tool_result_responses + 1) and
    .answer_responses == ($before.answer_responses + 1) and
    .embedding_requests > $before.embedding_requests and
    .embedded_inputs > $before.embedded_inputs
  ' <<<"$stub_stats_after_chat" >/dev/null
fi

post_body="$(json_body --arg game_id "$game_id" '{gameId:$game_id,title:"Smoke-tested route",content:"The community write path works end to end."}')"
request 201 POST /api/community/posts --cookie "$user_cookies" --header "Origin: $base_url" --header 'Content-Type: application/json' --data "$post_body"
post_id="$(json_value '.post.id')"
request 201 POST "/api/community/posts/$post_id/comments" --cookie "$user_cookies" --header "Origin: $base_url" --header 'Content-Type: application/json' --data '{"content":"Smoke-tested comment"}'
comment_id="$(json_value '.comment.id')"
request 200 PUT "/api/community/posts/$post_id/reactions/helpful" --cookie "$user_cookies" --header "Origin: $base_url"
json_value '.reactionCounts.helpful == 1 and (.viewerReactions | index("helpful") != null)' >/dev/null
request 200 GET "/api/community/posts/$post_id" --cookie "$user_cookies"
json_value --arg id "$user_id" '.post.author.id == $id and .post.commentCount == 1' >/dev/null
request 200 GET "/api/community/posts/$post_id/comments"
json_value --arg id "$comment_id" '.items | any(.id == $id)' >/dev/null

request 200 GET /api/deals --cookie "$user_cookies"
json_value '.items | any(.code == "WELCOME20" and .remainingStock > 0)' >/dev/null
claim_key="claim-$run_id"
request 201 POST /api/coupons/WELCOME20/claims --cookie "$user_cookies" --header "Origin: $base_url" --header "Idempotency-Key: $claim_key"
claim_id="$(json_value '.claim.id')"
json_value '.replayed == false and .claim.status == "claimed"' >/dev/null
request 200 POST /api/coupons/WELCOME20/claims --cookie "$user_cookies" --header "Origin: $base_url" --header "Idempotency-Key: $claim_key"
json_value --arg id "$claim_id" '.replayed == true and .claim.id == $id' >/dev/null
request 200 GET /api/coupon-claims --cookie "$user_cookies"
json_value --arg id "$claim_id" '.items | any(.id == $id)' >/dev/null

order_key="order-$run_id"
order_body="$(json_body --arg edition_id "$edition_id" --arg claim_id "$claim_id" '{editionId:$edition_id,region:"GLOBAL",currency:"USD",couponClaimId:$claim_id}')"
request 201 POST /api/orders --cookie "$user_cookies" --header "Origin: $base_url" --header 'Content-Type: application/json' --header "Idempotency-Key: $order_key" --data "$order_body"
order_no="$(json_value '.order.orderNo')"
json_value '.replayed == false and .order.status == "pending_payment" and .order.subtotalMinor == 1999 and .order.discountMinor == 399 and .order.totalMinor == 1600' >/dev/null
request 200 POST /api/orders --cookie "$user_cookies" --header "Origin: $base_url" --header 'Content-Type: application/json' --header "Idempotency-Key: $order_key" --data "$order_body"
json_value --arg order_no "$order_no" '.replayed == true and .order.orderNo == $order_no' >/dev/null
request 200 GET /api/coupon-claims --cookie "$user_cookies"
json_value --arg id "$claim_id" '.items | all(.id != $id)' >/dev/null

payment_key="payment-$run_id"
request 200 POST "/api/orders/$order_no/payments/sandbox" --cookie "$user_cookies" --header "Origin: $base_url" --header "Idempotency-Key: $payment_key"
json_value '.replayed == false and .order.status == "paid" and .order.payment.provider == "sandbox"' >/dev/null
request 200 POST "/api/orders/$order_no/payments/sandbox" --cookie "$user_cookies" --header "Origin: $base_url" --header "Idempotency-Key: $payment_key"
json_value '.replayed == true and .order.status == "paid"' >/dev/null
request 200 GET "/api/orders/$order_no" --cookie "$user_cookies"
json_value --arg order_no "$order_no" '.order.orderNo == $order_no and .order.status == "paid"' >/dev/null
request 200 GET /api/orders --cookie "$user_cookies"
json_value --arg order_no "$order_no" '.items | any(.orderNo == $order_no and .status == "paid")' >/dev/null
request 200 GET /api/games/xiaolanhe-demo --cookie "$user_cookies"
json_value --arg edition_id "$edition_id" '.game.owned == true and (.game.editions | any(.id == $edition_id and .owned == true))' >/dev/null

if [[ "$require_v45" == "true" ]]; then
  read -r flash_starts_at flash_ends_at < <(python3 - <<'PY'
from datetime import datetime, timedelta, timezone
now = datetime.now(timezone.utc)
def render(value):
    return value.isoformat(timespec="microseconds").replace("+00:00", "Z")
print(render(now - timedelta(minutes=1)), render(now + timedelta(minutes=10)))
PY
  )
  flash_code="FS-$(printf '%s' "$run_id" | tr '[:lower:]' '[:upper:]')"
  flash_body="$(json_body --arg code "$flash_code" --arg edition_id "$smoke_edition_id" --arg starts_at "$flash_starts_at" --arg ends_at "$flash_ends_at" '{code:$code,editionId:$edition_id,region:"GLOBAL",currency:"USD",salePriceMinor:499,totalStock:1,startsAt:$starts_at,endsAt:$ends_at,paymentTimeoutSeconds:900}')"
  request 201 POST /api/admin/flash-sales --cookie "$admin_cookies" --header "Origin: $base_url" --header 'Content-Type: application/json' --data "$flash_body"
  flash_sale_id="$(json_value '.flashSale.id')"
  json_value '.flashSale.status == "draft" and .flashSale.totalStock == 1' >/dev/null
  request 200 POST "/api/admin/flash-sales/$flash_sale_id/activate" --cookie "$admin_cookies" --header "Origin: $base_url"
  json_value '.flashSale.status == "active" and .flashSale.availability == "available"' >/dev/null

  reservation_key="flash-$run_id"
  request 202 POST "/api/flash-sales/$flash_sale_id/reservations" --cookie "$user_cookies" --header "Origin: $base_url" --header "Idempotency-Key: $reservation_key"
  flash_request_id="$(json_value '.request.requestId')"
  json_value '.replayed == false and .request.status == "queued"' >/dev/null

  flash_order_no=""
  for _ in $(seq 1 "${XLH_SMOKE_FLASH_POLL_ATTEMPTS:-90}"); do
    request 200 GET "/api/flash-sale-requests/$flash_request_id" --cookie "$user_cookies"
    flash_status="$(json_value '.request.status')"
    case "$flash_status" in
      order_ready)
        flash_order_no="$(json_value '.request.orderNo')"
        json_value --arg activity_id "$flash_sale_id" '.request.activityId == $activity_id and .request.paymentExpiresAt != "" and .request.failureCode == ""' >/dev/null
        break
        ;;
      failed|expired)
        echo "flash-sale request reached terminal status $flash_status" >&2
        cat "$body_file" >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  if [[ -z "$flash_order_no" ]]; then
    echo "flash-sale request did not reach order_ready" >&2
    exit 1
  fi
  request 200 GET "/api/orders/$flash_order_no" --cookie "$user_cookies"
  json_value --arg edition_id "$smoke_edition_id" '.order.status == "pending_payment" and .order.subtotalMinor == 499 and .order.discountMinor == 0 and .order.totalMinor == 499 and .order.item.editionId == $edition_id' >/dev/null
  request 200 POST "/api/flash-sales/$flash_sale_id/reservations" --cookie "$user_cookies" --header "Origin: $base_url" --header "Idempotency-Key: $reservation_key"
  json_value --arg request_id "$flash_request_id" '.replayed == true and .request.requestId == $request_id' >/dev/null
  request 409 POST "/api/flash-sales/$flash_sale_id/reservations" --cookie "$user_cookies" --header "Origin: $base_url" --header "Idempotency-Key: other-$run_id"
  json_value '.error.code == "already_reserved"' >/dev/null

  second_username="smoke2_${run_id}"
  second_register_body="$(json_body --arg username "$second_username" --arg password "$password" '{username:$username,displayName:"Second Smoke Player",password:$password}')"
  request 201 POST /api/auth/register --cookie-jar "$second_cookies" --header 'Content-Type: application/json' --data "$second_register_body"
  request 409 POST "/api/flash-sales/$flash_sale_id/reservations" --cookie "$second_cookies" --header "Origin: $base_url" --header "Idempotency-Key: second-$run_id"
  json_value '.error.code == "stock_exhausted"' >/dev/null

  request 202 DELETE "/api/admin/knowledge/documents/$knowledge_document_id" --cookie "$admin_cookies" --header "Origin: $base_url"
  json_value --arg id "$knowledge_document_id" '.documentId == $id and .status == "deletion_started"' >/dev/null

  stub_stats_final="$(curl --fail --silent --show-error "$XLH_SMOKE_OPENAI_STUB_STATS_URL")"
  jq -e --argjson after_chat "$stub_stats_after_chat" '
    .router_responses == $after_chat.router_responses and
    .tool_call_responses == $after_chat.tool_call_responses and
    .tool_result_responses == $after_chat.tool_result_responses and
    .answer_responses == $after_chat.answer_responses and
    .embedding_requests >= $after_chat.embedding_requests and
    .embedded_inputs >= $after_chat.embedded_inputs
  ' <<<"$stub_stats_final" >/dev/null
fi

request 204 POST /api/auth/logout --cookie "$user_cookies" --header "Origin: $base_url"
request 401 GET /api/me --cookie "$user_cookies"
json_value '.error.code == "unauthenticated"' >/dev/null
request 200 GET /

if [[ "$require_v45" == "true" ]]; then
  echo "product smoke passed: account, catalog, community, commerce, LightRAG knowledge, baseline chat, Redis Lua, RocketMQ and MySQL flash sale"
else
  echo "product smoke passed: account, admin catalog, community, coupon, order, payment, entitlement, logout"
fi
