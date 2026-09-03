#!/bin/sh
set -eu

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
api_url=${AGENTCLOUD_API_URL:-http://127.0.0.1:18080}
case "$api_url" in http://127.0.0.1:18080|http://localhost:18080) ;; *) echo "Refusing treasury seed outside the local demo endpoint" >&2; exit 1;; esac
state_dir=${AGENTCLOUD_STATE_DIR:-"$HOME/Library/Application Support/agent-continuity"}
credentials_file="$state_dir/credentials.json"
AGENTCLOUD_TOKEN='' AGENTCLOUD_API_URL="$api_url" AGENTCLOUD_STATE_DIR="$state_dir" "$repository_dir/bin/agentcloud" status >/dev/null
access_token=$(jq -er '.access_token' "$credentials_file")
api() {
  method=$1; path=$2; body=${3:-}; key=${4:-}
  curl --fail-with-body --silent --show-error --max-time 20 -X "$method" \
    -H "Authorization: Bearer $access_token" -H 'Content-Type: application/json' -H "Idempotency-Key: $key" \
    ${body:+--data-binary} ${body:+"$body"} "$api_url$path"
}
view=$(api GET /v1/treasury)
[ "$(printf %s "$view" | jq -r '.demo_enabled')" = true ] || { echo "Treasury demo must be enabled for the designated operator" >&2; exit 1; }
invoices=$(api GET '/v1/billing/invoices?limit=200')
paid_ids=$(printf %s "$invoices" | jq -r '.invoices[] | select(.invoice.status=="paid" and .invoice.test_mode==true and (.receipts|length)==1 and .receipts[0].provider=="demo") | .invoice.invoice_id')
[ -n "$paid_ids" ] || { echo "Complete a demo payment first. No fictional balance will be injected." >&2; exit 1; }
for invoice_id in $paid_ids; do
  doc=$(api POST /v1/treasury/collections/import "$(jq -nc --arg id "$invoice_id" '{invoice_id:$id}')")
done
collection_ids=$(printf %s "$doc" | jq -r '.collections[] | select(.environment=="simulation" and .status=="pending") | .collection_id')
for collection_id in $collection_ids; do doc=$(api POST "/v1/treasury/collections/$collection_id/settle-demo"); done
bank_doc=$(api POST /v1/treasury/destinations '{"kind":"bank","label":"我的结算银行卡 · 演示","currency":"CNY"}')
bank=$(printf %s "$bank_doc" | jq -r '.destinations[] | select(.label=="我的结算银行卡 · 演示") | .destination_id')
api POST "/v1/treasury/destinations/$bank/verify-demo" >/dev/null
wallet_doc=$(api POST /v1/treasury/destinations '{"kind":"alipay","label":"我的支付宝账户 · 演示","currency":"CNY"}')
wallet=$(printf %s "$wallet_doc" | jq -r '.destinations[] | select(.label=="我的支付宝账户 · 演示") | .destination_id')
api POST "/v1/treasury/destinations/$wallet/verify-demo" >/dev/null
api POST /v1/treasury/destinations '{"kind":"paypal","label":"我的 PayPal 账户 · 待验证演示","currency":"USD"}' >/dev/null

seed_payout() {
  scenario=$1; amount=$2; destination=$3; target=$4
  current=$(api GET /v1/treasury)
  key="treasury-demo-v1-$scenario"
  existing=$(printf %s "$current" | jq -r --arg key "$key" '.document.payouts[] | select(.idempotency_key==$key) | .payout_id')
  if [ -n "$existing" ]; then echo "Preserved existing $scenario: $existing"; return; fi
  input=$(jq -nc --arg destination "$destination" --argjson amount "$amount" '{destination_id:$destination,amount_minor:$amount,currency:"CNY",provider:"demo"}')
  result=$(api POST /v1/treasury/payouts "$input" "$key")
  payout_id=$(printf %s "$result" | jq -er --arg key "$key" '.payouts[] | select(.idempotency_key==$key) | .payout_id')
  if [ "$target" = requested ]; then echo "Created $scenario: pending approval"; return; fi
  api POST "/v1/treasury/payouts/$payout_id/decision" '{"decision":"approve"}' >/dev/null
  api POST "/v1/treasury/payouts/$payout_id/submit" >/dev/null
  case "$target" in
    paid|failed) api POST "/v1/treasury/payouts/$payout_id/simulate" "$(jq -nc --arg value "$target" '{outcome:$value}')" >/dev/null ;;
    returned) api POST "/v1/treasury/payouts/$payout_id/simulate" '{"outcome":"paid"}' >/dev/null; api POST "/v1/treasury/payouts/$payout_id/simulate" '{"outcome":"returned"}' >/dev/null ;;
    processing) ;;
  esac
  echo "Created $scenario: $target"
}
# Five independent examples funded only by existing paid demo receipts.
seed_payout bank-paid 100 "$bank" paid
seed_payout wallet-processing 50 "$wallet" processing
seed_payout bank-failed 30 "$bank" failed
seed_payout bank-returned 10 "$bank" returned
seed_payout wallet-approval 25 "$wallet" requested
api GET /v1/treasury | jq '{balances,payouts:[.document.payouts[]|{payout_id,amount_minor,currency,status,environment}]}'
