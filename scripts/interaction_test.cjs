const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const source = fs.readFileSync('web/static/app.js', 'utf8');

function harness() {
  const listeners = new Map(), ids = new Map();
  const node = (id = '') => ({id, hidden: false, textContent: '', children: [], attrs: {}, focused: 0, scrolled: 0,
    setAttribute(k,v) { this.attrs[k]=v; },
    appendChild(n) { this.children.push(n); n.parentElement=this; },
    focus() { this.focused++; document.activeElement=this; },
    scrollIntoView() { this.scrolled++; },
    closest() { return null; },
    querySelector(q) { if(q === ':scope > .action-feedback') return this.children.find(n=>n.className?.includes('action-feedback')) || null; return null; }
  });
  const document = {activeElement:null,body:{},readyState:'loading',
    addEventListener(n,f) { (listeners.get(n) || listeners.set(n,[]).get(n)).push(f); },
    getElementById(id) { return ids.get(id) || null; },
    createElement() { return node(); },
    querySelector(q) { if(q.includes('.notice.error')) return document.serverError || null; if(q.includes('.notice.success')) return document.serverSuccess || null; return null; },
    querySelectorAll(q) { if(q === 'input[name="csrf"]') return tokens; if(q === 'form[data-picker-form]') return document.pickers || []; if(q === 'form[data-quantity-editor]') return document.quantities || []; return []; }
  };
  const tokens=[{value:'old-token'},{value:'old-token'}];
  const form=node('form-1'); form.tagName='FORM'; form.contains=el=>el===button; form.dataset={};
  const button=node('save-1'); button.closest=q=>q==='form'?form:null; button.disabled=false;
  const baseQuery=form.querySelector; form.querySelector=function(q){ return q==='button[id]'?button:baseQuery.call(this,q); };
  const global=node('network-error'); global.hidden=true;
  ids.set(form.id,form);ids.set(button.id,button);ids.set(global.id,global);document.activeElement=button;
  const xhr={headers:{},getResponseHeader(k){return this.headers[k] || null;}};
  vm.runInNewContext(source,{document,WeakMap,Number});
  const fire=(name,detail={})=>(listeners.get(name)||[]).forEach(f=>f({detail,target:detail.eventTarget}));
  const begin=()=>fire('htmx:beforeRequest',{xhr,elt:form,requestConfig:{triggeringEvent:{submitter:button}}});
  return {document,tokens,form,button,global,xhr,fire,begin,node,ids,feedback:()=>form.children.find(n=>n.className?.includes('action-feedback'))};
}

test('rotated CSRF refreshes only tokens, keeps values, and never replays a mutation',()=>{
 const h=harness(); let submits=0;h.form.submit=()=>submits++;h.form.requestSubmit=()=>submits++;h.form.quantity='2';
 h.xhr.headers={'X-Shop-Error':'csrf-expired','X-Shop-CSRF':'refreshed-test-token'};h.begin();h.fire('htmx:responseError',{xhr:h.xhr});
 assert.deepEqual(h.tokens.map(t=>t.value),['refreshed-test-token','refreshed-test-token']);assert.equal(submits,0);assert.equal(h.form.quantity,'2');
 assert.match(h.feedback().textContent,/Review/);assert.equal(h.feedback().attrs.role,'alert');assert.equal(h.feedback().focused,1);assert.equal(h.feedback().scrolled,1);assert.equal(h.global.hidden,true);
});
test('reset or unknown session cannot acquire a replacement token from failure',()=>{
 const h=harness();h.xhr.headers={'X-Shop-Error':'csrf-expired'};h.begin();h.fire('htmx:responseError',{xhr:h.xhr});
 assert.equal(h.tokens[0].value,'old-token');assert.match(h.feedback().textContent,/Refresh this page/);
});
test('expired manager access is explained next to the submitted form',()=>{
 const h=harness();h.xhr.headers={'X-Shop-Error':'manager-expired'};h.begin();h.fire('htmx:responseError',{xhr:h.xhr});
 assert.match(h.feedback().textContent,/Manager access expired/);assert.equal(h.feedback().scrolled,1);
});
test('network failure retains draft and does not promise an uncommitted result',()=>{
 const h=harness();h.begin();h.fire('htmx:sendError',{xhr:h.xhr});
 assert.match(h.feedback().textContent,/couldn’t confirm/);assert.match(h.feedback().textContent,/current saved state/);
});
test('swapped validation errors reopen their editor and move into view',()=>{
 const h=harness();const disclosure={open:false,parentElement:null};h.form.closest=q=>q==='details'?disclosure:null;
 h.document.serverError={textContent:'Quantity changed elsewhere; review current stock.',hidden:false};h.begin();h.fire('htmx:afterSwap',{xhr:h.xhr,target:{id:'workspace'}});
 assert.equal(disclosure.open,true);assert.equal(h.document.serverError.hidden,true);assert.match(h.feedback().textContent,/review current stock/);assert.equal(h.feedback().scrolled,1);
});
test('product selection enables its own submit without changing version fields',()=>{
 const h=harness();const submit={disabled:true};let selected=null;const picker={version:'7',querySelector:q=>q==='[data-picker-submit]'?submit:selected};h.document.pickers=[picker];
 h.fire('htmx:afterSwap',{target:{id:'order-picker-sub-4'}});assert.equal(submit.disabled,true);
 selected={checked:true};h.fire('change',{eventTarget:{closest:()=>({})}});assert.equal(submit.disabled,false);assert.equal(picker.version,'7');
});
test('picked-stock question appears only when quantity drops below picked count',()=>{
 const h=harness();const choice={};const panel={hidden:false,querySelector:()=>choice};const input={value:'4'};
 h.document.quantities=[{dataset:{picked:'2'},querySelector:q=>q==='[data-picked-disposition]'?panel:input}];
 h.fire('DOMContentLoaded');assert.equal(panel.hidden,true);assert.equal(choice.disabled,true);assert.equal(choice.required,false);
 input.value='1';h.fire('input',{eventTarget:{matches:()=>true}});assert.equal(panel.hidden,false);assert.equal(choice.disabled,false);assert.equal(choice.required,true);
});
test('picker disables quantities for other products without overwriting their drafts',()=>{
 const h=harness();const submit={disabled:true};const inputs=[{dataset:{pickerQuantity:'1'},value:'3'},{dataset:{pickerQuantity:'73'},value:'500'}];
 const selected={value:'73'};h.document.pickers=[{querySelector:q=>q==='[data-picker-submit]'?submit:selected,querySelectorAll:()=>inputs}];
 h.fire('DOMContentLoaded');assert.equal(inputs[0].disabled,true);assert.equal(inputs[1].disabled,false);assert.equal(inputs[0].value,'3');
 selected.value='1';h.fire('change',{eventTarget:{closest:()=>({})}});assert.equal(inputs[0].disabled,false);assert.equal(inputs[1].disabled,true);assert.equal(inputs[1].value,'500');
});
test('actual weight preview is focused without submitting confirmation',()=>{
 const h=harness();const review=h.node('weight-review-8');let submissions=0;h.form.id='measure-line-8';h.form.submit=()=>submissions++;
 const query=h.document.querySelector;h.document.querySelector=q=>q==='.weight-review'?review:query(q);
 h.begin();h.fire('htmx:afterSwap',{xhr:h.xhr,target:{id:'workspace'}});
 assert.equal(review.focused,1);assert.equal(review.scrolled,1);assert.equal(submissions,0);
});
test('background tracker failure does not steal keyboard focus',()=>{
 const h=harness();h.fire('htmx:sendError',{xhr:{}});assert.equal(h.global.hidden,false);assert.equal(h.global.focused,0);assert.equal(h.global.scrolled,0);
});
test('successful add or substitution brings feedback to the resulting item',()=>{
 const h=harness();const row=h.node('working-line-8');h.ids.set(row.id,row);
 const oldFormQuery=h.form.querySelector.bind(h.form);h.form.querySelector=q=>q.includes('replacement_id')?{value:'2'}:oldFormQuery(q);
 const oldQuery=h.document.querySelector;h.document.querySelector=q=>q==='[data-workflow-product="2"]'?row:oldQuery(q);
 h.document.serverSuccess={textContent:'Replacement saved.',hidden:false};h.begin();h.fire('htmx:afterSwap',{xhr:h.xhr,target:{id:'workspace'}});
 assert.equal(h.form.children.length,0);assert.equal(row.children[0].textContent,'Replacement saved.');assert.equal(row.children[0].scrolled,1);
});
