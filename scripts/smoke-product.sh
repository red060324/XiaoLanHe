#!/usr/bin/env bash

set -euo pipefail

base_url="${XLH_SMOKE_BASE_URL:-http://127.0.0.1:18088}"
base_url="${base_url%/}"
run_id="${XLH_SMOKE_RUN_ID:-${GITHUB_RUN_ID:-local}${GITHUB_RUN_ATTEMPT:-0}$$}"
run_id="$(printf '%s' "$run_id" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9' | cut -c1-16)"
username="smoke_${run_id}"
password="smoke-password-1"

for command in curl jq; do
  command -v "$command" >/dev/null || { echo "$command is required" >&2; exit 1; }
done

smoke_dir="$(mktemp -d)"
body_file="$smoke_dir/body.json"
user_cookies="$smoke_dir/user.cookies"
admin_cookies="$smoke_dir/admin.cookies"
trap 'rm -f "$body_file" "$user_cookies" "$admin_cookies"; rmdir "$smoke_dir"' EXIT

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
  request 200 GET "/api/games/$smoke_slug"
  json_value --arg slug "$smoke_slug" '.game.slug == $slug' >/dev/null
fi

register_body="$(json_body --arg username "$username" --arg password "$password" '{username:$username,displayName:"Smoke Player",password:$password}')"
request 201 POST /api/auth/register --cookie-jar "$user_cookies" --header 'Content-Type: application/json' --data "$register_body"
user_id="$(json_value '.user.id')"
json_value --arg username "$username" '.user.username == $username' >/dev/null
request 200 GET /api/me --cookie "$user_cookies"
json_value --arg id "$user_id" '.user.id == $id' >/dev/null

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

request 204 POST /api/auth/logout --cookie "$user_cookies" --header "Origin: $base_url"
request 401 GET /api/me --cookie "$user_cookies"
json_value '.error.code == "unauthenticated"' >/dev/null
request 200 GET /

echo "product smoke passed: account, admin catalog, community, coupon, order, payment, entitlement, logout"
