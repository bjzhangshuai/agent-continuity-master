#!/bin/sh
# Uses only the explicit local simulator. Never creates a real payment.
set -eu
api_url=${AGENTCLOUD_API_URL:-http://127.0.0.1:18080}
case "$api_url" in http://127.0.0.1:*|http://localhost:*) ;; *) echo 'Refusing billing demo against a non-loopback server' >&2; exit 1;; esac
credentials_file=${AGENTCLOUD_CREDENTIALS_FILE:-/Users/jansurezhang/Library/Application Support/agent-continuity/credentials.json}
access_token=$(jq -er .access_token "$credentials_file")
api() {
  method=$1; path=$2; body=${3:-}; key=${4:-}
  if [ -n "$body" ]; then
    curl --fail-with-body --silent --show-error --max-time 20 -X "$method" -H "Authorization: Bearer $access_token" -H 'Content-Type: application/json' -H "Idempotency-Key: $key" --data-binary "$body" "$api_url$path"
  else
    curl --fail-with-body --silent --show-error --max-time 20 -X "$method" -H "Authorization: Bearer $access_token" -H 'Content-Type: application/json' -H "Idempotency-Key: $key" "$api_url$path"
  fi
}
config=$(api GET /v1/billing/providers)
printf '%s' "$config" | jq -e '.price_book.test_mode == true and any(.providers[]; .id == "demo" and .enabled == true)' >/dev/null || { echo 'Refusing: simulator/test price book required' >&2; exit 1; }
accounts=$(api GET /v1/billing/accounts)
account_id=$(printf '%s' "$accounts" | jq -r '.billing_accounts[] | select((.organization_id // "") == "" and .currency == "CNY") | .billing_account_id' | head -1)
if [ -z "$account_id" ]; then
  account_id=$(api POST /v1/billing/accounts '{"plan":"pro","currency":"CNY"}' | jq -er .billing_account_id)
fi
# Existing account usage is read-only. Each generated bill uses a non-overlapping,
# immutable slice of this history; pricing is explicitly test-only.
usage=$(api GET '/v1/usage?limit=1000')
boundaries=$(printf '%s' "$usage" | jq -c '[.usage[] | select((.organization_id // "") == "") | .recorded_at] | unique | sort | if length < 3 then error("At least three distinct usage timestamps required") else [((.[0] | sub("\\.[0-9]+Z$";"Z") | fromdateiso8601) - 1 | todateiso8601), .[(length / 3 | floor)], .[(length * 2 / 3 | floor)], (now | todateiso8601)] end')
index=0
while [ "$index" -lt 3 ]; do
  start=$(printf '%s' "$boundaries" | jq -r ".[$index]"); next=$((index+1)); end=$(printf '%s' "$boundaries" | jq -r ".[$next]")
  body=$(jq -nc --arg a "$account_id" --arg s "$start" --arg e "$end" '{billing_account_id:$a,period_start:$s,period_end:$e}')
  existing=$(api GET '/v1/billing/invoices?limit=200' | jq -c --arg k "billing-demo-20260903-$index" '.invoices[] | select(.invoice.idempotency_key==$k)')
  if [ -n "$existing" ]; then doc=$existing; else doc=$(api POST /v1/billing/invoices "$body" "billing-demo-20260903-$index"); fi
  invoice_id=$(printf '%s' "$doc" | jq -er .invoice.invoice_id)
  status=$(printf '%s' "$doc" | jq -r .invoice.status)
  if [ "$status" = open ]; then
    pay=$(api POST "/v1/billing/invoices/$invoice_id/payments" '{"provider":"demo"}' "billing-demo-payment-$index")
    payment_id=$(printf '%s' "$pay" | jq -er .payment.payment_id)
    case "$index" in 0) api POST "/v1/billing/invoices/$invoice_id/payments/$payment_id/simulate" '{"outcome":"succeeded"}' >/dev/null;; 2) api POST "/v1/billing/invoices/$invoice_id/payments/$payment_id/simulate" '{"outcome":"failed"}' >/dev/null;; esac
  fi
  api GET "/v1/billing/invoices/$invoice_id" | jq '{number:.invoice.number,status:.invoice.status,amount_minor:.invoice.amount_minor,currency:.invoice.currency,test_mode:.invoice.test_mode,payments:(.payments|length),receipts:(.receipts|length)}'
  index=$next
done
