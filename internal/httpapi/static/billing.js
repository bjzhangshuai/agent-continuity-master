const finance = {accounts: [], providers: [], invoices: [], book: null, selected: null, quote: null, issueKeys: new Map(), paymentKeys: new Map()};
const money = (amount, currency) => `${currency} ${(Number(amount) / 100).toFixed(2)}`;
const financeStatus = (status) => state.lang === "zh" ? ({open:"待支付", paid:"已支付", void:"已作废", disputed:"需人工核查", review:"退款/异常待核查", refunded:"渠道已退款", creating:"下单结果待确认", pending:"待付款", approved:"已授权待扣款", succeeded:"已收款", failed:"失败", canceled:"已关闭", expired:"已过期"}[status] || status) : status;
function financeButton(label, action) {
  const button = el("button", label); button.type = "button";
  button.addEventListener("click", async () => { button.disabled = true; try { await action(); } catch (error) { toast(error.message); } finally { button.disabled = false; } });
  return button;
}
function localDateInput(date) { return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16); }
async function loadBillingFinance(accounts = finance.accounts) {
  finance.accounts = accounts;
  const [config, data] = await Promise.all([api("/v1/billing/providers"), api("/v1/billing/invoices?limit=200")]);
  finance.providers = config.providers || []; finance.book = config.price_book; finance.invoices = data.invoices || [];
  $("payment-mode").textContent = finance.book.test_mode ? tr("测试价格 · 不可用于真实收款", "Test pricing · no live collection") : tr("正式价格表", "Production price book");
  $("payment-providers").replaceChildren(...finance.providers.map((provider) => {
    const card = el("article", "", `provider-card ${provider.enabled ? "enabled" : "disabled"}`);
    card.append(el("strong", provider.name), pill(provider.enabled ? provider.environment : tr("待配置", "Unconfigured"), provider.enabled ? "accent" : "neutral"), el("p", provider.enabled ? provider.currencies.join(" / ") : tr("商户凭据与回调尚未配置", "Merchant credentials and callbacks not configured"), "muted"));
    return card;
  }));
  const previous = $("invoice-account").value;
  $("invoice-account").replaceChildren(...accounts.map((account) => { const option = el("option", `${planLabel(account.plan)} · ${account.currency} · ${account.billing_account_id}`); option.value = account.billing_account_id; return option; }));
  if (accounts.some((account) => account.billing_account_id === previous)) $("invoice-account").value = previous;
  if (!$("invoice-from").value) { const end = new Date(); end.setHours(0, 0, 0, 0); $("invoice-to").value = localDateInput(end); $("invoice-from").value = localDateInput(new Date(end.getTime() - 86400000)); }
  renderInvoiceList(); renderFinanceSummary();
  if (finance.selected) await showInvoice(finance.selected, false);
}
function renderFinanceSummary() {
  const group = new Map();
  for (const d of finance.invoices) { const i = d.invoice; const key = `${i.test_mode ? "test" : "live"}/${i.currency}`; if (!group.has(key)) group.set(key, {currency:i.currency, test:i.test_mode, open:0, paid:0, refunded:0, receipts:0}); const g = group.get(key); if (i.status === "open") g.open += i.amount_minor; g.paid += (d.receipts || []).reduce((sum,r)=>sum+r.amount_minor,0); g.refunded += (d.credit_notes || []).reduce((sum,n)=>sum+n.amount_minor,0); g.receipts += (d.receipts || []).length; }
  $("finance-summary").replaceChildren(...Array.from(group.values(), (g) => { const card = el("article", "", "card"); card.append(el("span", g.test ? tr("本列表 · 模拟 / 沙箱账目", "Listed invoices · simulation / sandbox") : tr("本列表 · 正式账目", "Listed invoices · live"), "eyebrow"), el("h3", `${tr("待付", "Open")} ${money(g.open, g.currency)}`), el("p", `${tr("原收款", "Gross paid")} ${money(g.paid,g.currency)} · ${tr("已退款", "Refunded")} ${money(g.refunded,g.currency)} · ${tr("净收款", "Net paid")} ${money(g.paid-g.refunded,g.currency)}`), el("p", `${g.receipts} ${tr("份回执（最近 200 张账单范围）", "receipts (latest 200 invoices)")}`, "muted")); return card; }));
}
function renderInvoiceList() {
  const filtered = finance.invoices.filter((d) => !$("invoice-status").value || d.invoice.status === $("invoice-status").value);
  renderItems($("invoice-list"), filtered, (d) => { const i = d.invoice; const node = el("article", "", "feed-item invoice-row"); const heading = el("div", "", "card-heading"); heading.append(el("strong", i.number), pill(financeStatus(i.status), i.status === "paid" ? "healthy" : "neutral")); node.append(heading, el("p", `${money(i.amount_minor, i.currency)} · ${i.test_mode ? tr("测试账单", "Test invoice") : tr("正式账单", "Live invoice")}`), el("p", `${formatDate(i.period_start)} — ${formatDate(i.period_end)}`, "muted"), financeButton(tr("查看明细 / 支付", "Details / payment"), () => showInvoice(i.invoice_id))); return node; });
}
function lineTable(lines, currency) {
  const wrap = el("div", "", "billing-table-wrap"); const table = document.createElement("table"); table.className = "billing-table";
  const head = document.createElement("thead"); const row = document.createElement("tr"); [tr("消费项目", "Charge"), tr("用量", "Quantity"), tr("价格规则", "Rate"), tr("费用", "Amount")].forEach((v) => row.append(el("th", v))); head.append(row); table.append(head);
  const body = document.createElement("tbody"); lines.forEach((line) => { const r = document.createElement("tr"); r.append(el("td", line.description), el("td", `${line.quantity.toLocaleString()} ${line.unit}`), el("td", `${money(line.unit_price_minor, currency)} / ${line.unit_size} ${line.unit}`), el("td", money(line.amount_minor, currency))); body.append(r); }); table.append(body); wrap.append(table); return wrap;
}
function invoiceInput() { return {billing_account_id:$("invoice-account").value, period_start:new Date($("invoice-from").value).toISOString(), period_end:new Date($("invoice-to").value).toISOString()}; }
$("invoice-form").addEventListener("input", () => { finance.quote = null; $("invoice-issue").disabled = true; });
$("invoice-form").addEventListener("submit", async (event) => {
  event.preventDefault(); $("invoice-preview").disabled = true;
  try { const input = invoiceInput(); const quote = await api("/v1/billing/quote", {method:"POST", body:input}); finance.quote = {input, quote}; $("invoice-quote").className = "invoice-quote"; $("invoice-quote").replaceChildren(el("h3", `${tr("预计费用", "Estimated charge")} ${money(quote.amount_minor, quote.currency)}`), el("p", `${quote.price_book} · ${quote.test_mode ? tr("仅演示价格", "Test prices only") : tr("正式价格", "Production prices")}`, "payment-warning"), lineTable(quote.lines, quote.currency)); $("invoice-issue").disabled = false; } catch (error) { finance.quote = null; toast(error.message); } finally { $("invoice-preview").disabled = false; }
});
$("invoice-issue").addEventListener("click", async () => {
  if (!finance.quote) return; $("invoice-issue").disabled = true;
  try { const fingerprint = JSON.stringify(finance.quote.input); if (!finance.issueKeys.has(fingerprint)) finance.issueKeys.set(fingerprint, crypto.randomUUID()); const d = await api("/v1/billing/invoices", {method:"POST", body:finance.quote.input, headers:{"Idempotency-Key":finance.issueKeys.get(fingerprint)}}); finance.selected = d.invoice.invoice_id; await loadBillingFinance(); toast(tr("账单已生成；消费与价格快照已冻结", "Invoice issued with immutable usage and pricing")); } catch (error) { toast(error.message); $("invoice-issue").disabled = false; }
});
$("invoice-status").addEventListener("change", renderInvoiceList);
async function downloadBilling(path, name) {
  const response = await fetch(path, {headers:state.token?{Authorization:`Bearer ${state.token}`}:{}}); if (!response.ok) throw new Error(`Download failed: ${response.status}`);
  const href = URL.createObjectURL(await response.blob()); const link = el("a", name); link.href = href; link.download = name; document.body.append(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(href), 1000);
}
async function showInvoice(id, scroll = true) {
  const d = await api(`/v1/billing/invoices/${id}`); finance.selected = id; const i = d.invoice; const target = $("invoice-detail"); target.hidden = false; target.replaceChildren();
  const heading = el("div", "", "card-heading"); heading.append(el("h3", `${i.number} · ${money(i.amount_minor, i.currency)}`), pill(financeStatus(i.status), i.status === "paid" ? "healthy" : "neutral"));
  target.append(heading, el("p", `${tr("账期", "Period")}: ${formatDate(i.period_start)} — ${formatDate(i.period_end)} · ${tr("到期", "Due")}: ${formatDate(i.due_at)}`, "muted"), el("p", `${tr("价格快照", "Price snapshot")}: ${i.price_book} · ${i.test_mode ? tr("测试账单，不产生真实应收", "Test invoice, not a real receivable") : tr("正式账单", "Live invoice")}`, "payment-warning"), lineTable(i.lines, i.currency));
  const actions = el("div", "", "section-actions"); actions.append(financeButton(tr("下载消费明细 CSV", "Download charges CSV"), () => downloadBilling(`/v1/billing/invoices/${id}/export`, `${i.number}.csv`)));
  if (i.status === "open" && !d.payments.length) actions.append(financeButton(tr("作废未支付账单", "Void unpaid invoice"), async () => { await api(`/v1/billing/invoices/${id}/void`, {method:"POST"}); await loadBillingFinance(); }));
  target.append(actions);
  renderRefunds(target,d,()=>showInvoice(id,false));
  if (i.status === "open" && !d.payments.some((p) => ["creating","pending","approved","succeeded"].includes(p.status))) {
    const provider = document.createElement("select"); provider.setAttribute("aria-label", tr("选择付款渠道", "Choose payment provider")); const eligible = finance.providers.filter((p) => p.enabled && p.currencies.includes(i.currency) && i.test_mode === (p.environment !== "live"));
    provider.replaceChildren(...eligible.map((p) => { const option = el("option", p.name); option.value = p.id; return option; }));
    const create = financeButton(tr("创建付费链接", "Create payment link"), async () => { const key = `${id}/${provider.value}`; const previousKey = finance.paymentKeys.get(key); const finalAttempt = d.payments.some((p) => p.idempotency_key === previousKey && ["failed","canceled","expired"].includes(p.status)); if (!previousKey || finalAttempt) finance.paymentKeys.set(key, crypto.randomUUID()); try { await api(`/v1/billing/invoices/${id}/payments`, {method:"POST",body:{provider:provider.value},headers:{"Idempotency-Key":finance.paymentKeys.get(key)}}); } finally { await loadBillingFinance(); } }); create.disabled = !eligible.length; actions.append(provider, create);
    if (!eligible.length) target.append(el("p", tr("没有与本账单币种和环境匹配的已配置渠道。不会自动换汇或使用真实渠道支付测试账单。", "No configured provider matches the invoice currency and environment."), "payment-warning"));
  }
  target.append(el("h3", tr("支付单与付费链接", "Payments & payment links")));
  for (const p of d.payments) {
    const card = el("article", "", "feed-item"); card.append(el("strong", `${p.provider} · ${financeStatus(p.status)} · ${money(p.amount_minor, p.currency)}`), el("p", `${p.payment_id} · ${p.environment}`, "muted"), el("p", `${tr("渠道订单", "Provider order")}: ${p.provider_order_id || "—"} · ${tr("渠道流水", "Transaction")}: ${p.provider_transaction_id || "—"}`, "muted"));
    if (p.last_error) card.append(el("p", p.last_error, "payment-warning"));
    const controls = el("div", "", "section-actions"); const link = d.payment_links[p.payment_id];
    if (link) { const open = el("a", tr("打开安全收银台", "Open checkout")); open.href = link; open.target = "_blank"; open.rel = "noopener noreferrer"; controls.append(open, financeButton(tr("复制付费链接", "Copy payment link"), async () => { await navigator.clipboard.writeText(link); toast(tr("付费链接已复制（含独立支付权限，请谨慎分享）", "Payment link copied; share carefully")); })); }
    if (p.status === "creating") controls.append(financeButton(tr("用原支付单重试下单", "Retry same payment"), async () => { try { await api(`/v1/billing/invoices/${id}/payments`, {method:"POST",body:{provider:p.provider},headers:{"Idempotency-Key":p.idempotency_key}}); } finally { await loadBillingFinance(); } }));
    controls.append(financeButton(tr("主动查单 / 对账", "Query / reconcile"), async () => { await api(`/v1/billing/invoices/${id}/payments/${p.payment_id}/reconcile`, {method:"POST"}); await loadBillingFinance(); }));
    if (link) controls.append(financeButton(tr("撤销分享链接", "Revoke link"), async () => { await api(`/v1/billing/invoices/${id}/payments/${p.payment_id}/revoke-link`, {method:"POST"}); await loadBillingFinance(); }));
    if (p.provider === "demo" && p.status === "pending") for (const outcome of ["succeeded","failed"]) controls.append(financeButton(outcome === "succeeded" ? tr("模拟付款成功", "Simulate success") : tr("模拟付款失败", "Simulate failure"), async () => { await api(`/v1/billing/invoices/${id}/payments/${p.payment_id}/simulate`, {method:"POST",body:{outcome}}); await loadBillingFinance(); }));
    card.append(controls); target.append(card);
  }
  if (!d.payments.length) target.append(el("p", tr("尚无支付单。", "No payment attempts yet."), "muted"));
  target.append(el("h3", tr("支付回执（非税务发票）", "Payment receipts (not tax invoices)")));
  for (const receipt of d.receipts) { const row = el("div", "", "feed-item"); row.append(el("strong", `${receipt.receipt_id} · ${money(receipt.amount_minor, receipt.currency)}`), el("p", `${formatDate(receipt.paid_at)} · ${receipt.environment}`, "muted"), financeButton(tr("下载可打印回执", "Download printable receipt"), () => downloadBilling(`/v1/billing/invoices/${id}/receipts/${receipt.receipt_id}?format=html`, `${receipt.receipt_id}.html`))); target.append(row); }
  if (!d.receipts.length) target.append(el("p", tr("只有服务端确认成功付款后才生成回执；零金额账单无需付款。", "Receipts are created only after server-confirmed payment; zero-value invoices require no payment."), "muted"));
  const audit = document.createElement("details"); audit.append(el("summary", tr(`支付审计轨迹 · ${d.events.length} 条`, `Payment audit trail · ${d.events.length} events`))); d.events.forEach((e) => audit.append(el("p", `${formatDate(e.created_at)} · ${e.kind} · ${e.detail}`, "muted"))); target.append(audit);
  if (scroll) target.scrollIntoView({behavior:"smooth", block:"start"});
}
