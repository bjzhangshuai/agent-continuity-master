const refundKeys=new Map();
function renderRefunds(root,d,refresh,merchant=false){
  const id=d.invoice.invoice_id,base=`/v1/${merchant?'treasury/receivables':'billing/invoices'}/${id}`;
  root.append(el('h3',tr('退款与票据','Refunds & statements')),financeButton(tr('下载账单与退款凭证（非税票）','Download statement (not tax invoice)'),()=>downloadBilling(`${base}/statement`,`${d.invoice.number}-statement.html`)));
  const status=v=>tr(({requested:'待审批',approved:'已批准',rejected:'已驳回',submitting:'提交结果待确认',pending:'渠道处理中',succeeded:'退款成功',failed:'退款失败',review:'待核查'})[v]||v,v);
  if(!merchant&&d.can_pay){for(const p of d.payments.filter(p=>['succeeded','review'].includes(p.status))){
    const available=p.amount_minor-(d.refunds||[]).filter(r=>r.payment_id===p.payment_id&&!['failed','rejected'].includes(r.status)).reduce((sum,r)=>sum+r.amount_minor,0);if(available<=0)continue;
    const form=el('form','','invoice-form'),amount=document.createElement('input'),reason=document.createElement('input');amount.type='number';amount.min='1';amount.max=String(available);amount.step='1';amount.value=String(available);amount.required=true;amount.setAttribute('aria-label',tr('退款金额（分）','Refund amount (minor units)'));reason.placeholder=tr('退款原因','Refund reason');reason.maxLength=240;reason.required=true;reason.setAttribute('aria-label',reason.placeholder);const button=el('button',tr('申请原路退款','Request refund'));button.type='submit';
    form.append(el('p',`${p.provider} · ${tr('可退','Refundable')} ${money(available,p.currency)} · ${tr('以下金额以分填写','Enter minor units below')}`),amount,reason,button);
    form.addEventListener('submit',async e=>{e.preventDefault();button.disabled=true;try{const body={payment_id:p.payment_id,amount_minor:Number(amount.value),reason:reason.value},fingerprint=id+JSON.stringify(body);if(!refundKeys.has(fingerprint))refundKeys.set(fingerprint,crypto.randomUUID());await api(`${base}/refunds`,{method:'POST',body,headers:{'Idempotency-Key':refundKeys.get(fingerprint)}});await refresh();}catch(e){toast(e.message);}finally{button.disabled=false;}});root.append(form);
  }}
  for(const r of d.refunds||[]){const card=el('article','','feed-item');card.append(el('strong',`${money(r.amount_minor,r.currency)} · ${status(r.status)}`),el('p',`${r.refund_id} · ${r.reason}`),el('p',`${tr('申请人 / 审批人','Requester / approver')}: ${r.requested_by} / ${r.decided_by||'—'}`));if(r.last_error)card.append(el('p',r.last_error,'payment-warning'));
    const action=(label,name,body)=>financeButton(label,async()=>{await api(`${base}/refunds/${r.refund_id}/${name}`,{method:'POST',body});await refresh();});
    if(merchant&&r.status==='requested'){const reason=document.createElement('input');reason.placeholder=tr('审批意见（必填）','Decision reason (required)');reason.maxLength=240;reason.setAttribute('aria-label',reason.placeholder);card.append(reason);for(const decision of ['approved','rejected'])card.append(financeButton(decision==='approved'?tr('批准退款','Approve refund'):tr('驳回申请','Reject refund'),async()=>{if(!reason.value.trim())throw Error(reason.placeholder);await api(`${base}/refunds/${r.refund_id}/decision`,{method:'POST',body:{decision,reason:reason.value}});await refresh();}));}
    if(merchant&&r.status==='approved')card.append(action(tr('提交原路退款','Submit refund'),'submit'));
    if(['pending','submitting','review'].includes(r.status)){if(merchant||d.can_pay)card.append(action(tr('查询退款结果','Query refund'),'reconcile'));if(merchant&&d.invoice.test_mode&&d.payments.find(p=>p.payment_id===r.payment_id)?.provider==='demo')for(const outcome of ['succeeded','failed'])card.append(action(tr(`模拟${status(outcome)}（不转账）`,`Simulate ${outcome}`),'simulate',{outcome}));}
    root.append(card);
  }
  for(const note of d.credit_notes||[])root.append(el('p',`${tr('退款贷项凭证','Refund credit note')}: ${note.credit_note_id} · ${money(note.amount_minor,note.currency)}`));
}
