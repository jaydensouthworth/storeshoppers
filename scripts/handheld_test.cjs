const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('web/static/handheld.js','utf8');
function harness({hash='', online=true, clipboardFail=false}={}) {
 const listeners=new Map(), windowListeners=new Map(), ids=new Map(), emitted=[], history=[], copied=[], timers=new Map(), clock={now:100000};let timerID=0;
 let current=null;
 const make=id=>({id,dataset:{},value:'',disabled:false,hidden:true,isConnected:true,textContent:'',focus(){this.focused=true;},select(){this.selected=true;},scrollIntoView(){this.scrolled=true;}});
 const note=make('pair-note'), copyStatus=make('copy-status'), error=make('error'), review=make('review');
 const doc={readyState:'loading',addEventListener(n,f){if(!listeners.has(n))listeners.set(n,[]);listeners.get(n).push(f);},getElementById(id){return id==='handheld-workspace'?current:ids.get(id)||null;},querySelector(s){if(s==='[data-pairing-link-note]')return note;if(s==='[data-copy-pairing-status]')return copyStatus;if(s==='#handheld-workspace .handheld-feedback.error')return this.errorVisible?error:null;if(s==='#handheld-workspace [data-handheld-weight-review]')return this.reviewVisible?review:null;return null;},querySelectorAll(s){if(s==="[data-handheld-busy]")return current?current.forms.flatMap(form=>form.inputs).filter(i=>i.dataset.handheldBusy):[];return s==="form[data-handheld-authorized]"&&current?current.forms.filter(form=>form.authorized):[];},dispatchEvent(e){emitted.push(e.type);}};
 const win={setTimeout(f,ms){const id=++timerID;timers.set(id,{f,ms});return id;},clearTimeout(id){timers.delete(id);},location:{hash,pathname:'/handheld/',search:''},history:{replaceState(a,b,c){history.push(c);win.location.hash='';}},addEventListener(n,f){if(!windowListeners.has(n))windowListeners.set(n,[]);windowListeners.get(n).push(f);}};
 const navigator={onLine:online,clipboard:{async writeText(v){if(clipboardFail)throw Error('denied');copied.push(v);}}};
 const pairing=make('pairing-code'),copyInput=make('phone-pairing-code');ids.set(pairing.id,pairing);ids.set(copyInput.id,copyInput);ids.set('handheld-network',make('handheld-network'));ids.set('handheld-pending',make('handheld-pending'));
 function install(){
  if(current)current.forms.flatMap(form=>form.inputs).forEach(i=>i.isConnected=false);
  const forms=['/handheld/pick','/handheld/scan','/handheld/weight/preview','/handheld/weight/confirm','/handheld/report','/handheld/disconnect','/handheld/connect'].map(action=>{
   const inputs=[make('code'),make('picked'),make('already-disabled'),make('disposition'),make('note'),make('send')];
   inputs[0].value='SHOPDEMO-000001';inputs[1].value='2';inputs[2].disabled=true;inputs[3].value='writeoff';inputs[4].value='Fake demo note';
   const authorized=!['/handheld/connect','/handheld/disconnect'].includes(action);
   const form={inputs,action,authorized,matches:s=>s==='form'||s==='[data-handheld-form]'||(s==='[data-handheld-authorized]'&&authorized),closest:()=>form,querySelectorAll:()=>inputs};
   return form;
  });
  current={id:'handheld-workspace',dataset:{},form:forms[0],forms,contains:e=>forms.includes(e)||e?.owner===current};return current;
 }
 install();
 vm.runInNewContext(source,{document:doc,window:win,navigator,WeakMap,URLSearchParams,Date:{now:()=>clock.now},CustomEvent:class{constructor(type){this.type=type;}}});
 function event(detail={},target){return {detail,target,prevented:false,stopped:false,preventDefault(){this.prevented=true;},stopImmediatePropagation(){this.stopped=true;}};}
 function fire(n,detail={},target){const e=event(detail,target);for(const f of listeners.get(n)||[])f(e);return e;}
 async function asyncFire(n,target){const e=event({},target);for(const f of listeners.get(n)||[])await f(e);return e;}
 function winFire(n){for(const f of windowListeners.get(n)||[])f({});}
 function begin(root=current,header=null,link=false,form=root.form){const xhr={getResponseHeader:()=>header};const elt=link?{owner:root,matches:()=>false,closest:()=>null}:form;fire('htmx:beforeRequest',{elt,xhr});return xhr;}
 return {doc,win,navigator,timers,clock,ids,pairing,copyInput,copyStatus,note,error,review,history,copied,emitted,install,fire,asyncFire,winFire,begin,get root(){return current;}};
}
test('pairing fragment fills only the visible connect draft and is cleared without redemption or persistence',()=>{
 const h=harness({hash:'#pair=ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ'});h.fire('DOMContentLoaded');
 assert.equal(h.pairing.value,'ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ');assert.deepEqual(h.history,['/handheld/']);assert.equal(h.note.hidden,false);assert.equal(h.copied.length,0);
 h.pairing.value='Edited';h.winFire('pageshow');assert.equal(h.pairing.value,'Edited');
});
test('invalid or oversized pairing links are cleared and never populate a code',()=>{
 for(const value of ['bad','<script>alert(1)</script>','A'.repeat(1000)]){const h=harness({hash:'#pair='+encodeURIComponent(value)});h.fire('DOMContentLoaded');assert.equal(h.pairing.value,'');assert.equal(h.win.location.hash,'');assert.match(h.ids.get('handheld-network').textContent,/not valid/);}
});
test('unrelated fragments stay intact while new pairing links on connected phones are cleared safely',()=>{
 const h=harness({hash:'#handheld-main'});h.fire('DOMContentLoaded');assert.equal(h.history.length,0);
 const other=harness({hash:'#pair=ABCDEFGHIJKLMNOPQRSTUVWXYZ'});other.ids.delete('pairing-code');other.fire('DOMContentLoaded');assert.equal(other.history.length,1);assert.equal(other.win.location.hash,'');assert.match(other.ids.get('handheld-network').textContent,/already connected/);assert.ok(other.emitted.includes('handheld:pause'));assert.equal(other.copied.length,0);
});
test('offline submit keeps entered values and blocks transport without claiming a save',()=>{
 const h=harness({online:false});const e=h.fire('submit',{},h.root.form);assert.ok(e.prevented&&e.stopped);assert.equal(h.root.form.inputs[0].value,'SHOPDEMO-000001');assert.match(h.ids.get('handheld-network').textContent,/Nothing was sent/);
});
test('one pending request locks only its form and restores only formerly enabled controls',()=>{
 const h=harness(),root=h.root,xhr=h.begin();assert.ok(root.form.inputs.every(i=>i.disabled));assert.equal(h.ids.get('handheld-pending').hidden,false);h.fire('htmx:afterRequest',{xhr});assert.equal(root.form.inputs[0].disabled,false);assert.equal(root.form.inputs[2].disabled,true);assert.equal(h.ids.get('handheld-pending').hidden,true);
});
test('a late response cannot replace a newly navigated task',()=>{
 const h=harness(),xhr=h.begin();h.install();const e=h.fire('htmx:beforeSwap',{xhr,shouldSwap:true});assert.equal(e.detail.shouldSwap,false);
});
test('revocation stops scanners and cannot be undone by request cleanup',()=>{
 const h=harness(),root=h.root,xhr=h.begin(root,'revoked');h.fire('htmx:beforeSwap',{xhr,shouldSwap:true});h.fire('htmx:afterRequest',{xhr});assert.ok(h.emitted.includes('handheld:revoked'));assert.ok(root.form.inputs.every(i=>i.disabled));
});
test('ambiguous network failure keeps drafts and requests checking state without replay',()=>{
 for(const event of ['htmx:sendError','htmx:responseError']){const h=harness(),xhr=h.begin();h.fire(event,{xhr});h.fire('htmx:afterRequest',{xhr});assert.equal(h.root.form.inputs[1].value,'2');assert.match(h.ids.get('handheld-network').textContent,/couldn’t confirm/);assert.match(h.ids.get('handheld-network').textContent,/will not resend/);assert.equal(h.root.form.inputs[1].disabled,false);}
});
test('unrelated requests cannot show phone errors or lock its form',()=>{
 const h=harness();const xhr={getResponseHeader(){return null;}};h.fire('htmx:beforeRequest',{xhr,elt:{}});h.fire('htmx:sendError',{xhr});assert.equal(h.ids.get('handheld-network').hidden,true);assert.equal(h.root.form.inputs[0].disabled,false);
});
test('rendered validation errors receive visible focus after phone swaps',()=>{
 const h=harness();h.doc.errorVisible=true;h.fire('htmx:afterSwap',{target:{id:'handheld-workspace'}});assert.ok(h.error.focused&&h.error.scrolled);
});
test('copying a pairing code requires a deliberate button click and never reads clipboard',async()=>{
 const h=harness();h.copyInput.value='Test one-use code';h.fire('DOMContentLoaded');assert.equal(h.copied.length,0);const button={closest:s=>s==='[data-copy-pairing]'?{}:null};await h.asyncFire('click',button);assert.deepEqual(h.copied,['Test one-use code']);assert.match(h.copyStatus.textContent,/Code copied/);
});
test('clipboard refusal selects the same code for ordinary copying',async()=>{
 const h=harness({clipboardFail:true});await h.asyncFire('click',{closest:()=>({})});assert.ok(h.copyInput.focused&&h.copyInput.selected);assert.match(h.copyStatus.textContent,/selected code/);
});
test('handheld enhancement has no automatic code redemption, image upload or browser storage',()=>{
 assert.doesNotMatch(source,/localStorage|sessionStorage|fetch\(|XMLHttpRequest|requestSubmit\(|\.submit\(/);
 const template=fs.readFileSync('web/templates/handheld.html','utf8');
 assert.match(template,/name="pairing_code"/);assert.match(template,/action="\/handheld\/scan"/);assert.match(template,/action="\/handheld\/pick"/);assert.match(template,/hx-select="#handheld-workspace"/);
 assert.match(template,/value="{{\.CodeDraft}}"/);assert.match(template,/value="{{\.PickedDraft}}"/);
});

test('newest navigation wins regardless of response order and stale errors stay quiet',()=>{
 const h=harness(),first=h.begin(),next=h.begin(h.root,null,true);
 assert.equal(h.fire('htmx:beforeOnLoad',{xhr:first}).prevented,true);
 assert.equal(h.fire('htmx:beforeOnLoad',{xhr:next}).prevented,false);
 assert.equal(h.fire('htmx:beforeSwap',{xhr:first,shouldSwap:true}).detail.shouldSwap,false);
 h.fire('htmx:sendError',{xhr:first});assert.equal(h.ids.get('handheld-network').hidden,true);
 h.fire('htmx:afterRequest',{xhr:first});assert.equal(h.ids.get('handheld-pending').hidden,false);
 assert.equal(h.fire('htmx:beforeSwap',{xhr:next,shouldSwap:true}).detail.shouldSwap,true);
 h.fire('htmx:afterRequest',{xhr:next});assert.equal(h.ids.get('handheld-pending').hidden,true);
});
test('known expiry stops scanning and locks mutation with an explicit reconnect message',()=>{
 const h=harness();h.root.dataset.handheldExpires='101';h.fire('DOMContentLoaded');
 assert.equal(h.timers.size,1);const timer=[...h.timers.values()][0];assert.equal(timer.ms,1000);timer.f();
 assert.ok(h.emitted.includes('handheld:revoked'));assert.ok(h.root.form.inputs.every(i=>i.disabled));assert.match(h.ids.get('handheld-network').textContent,/connection expired/);
});
test('a timer from a removed workspace cannot revoke a newer connection',()=>{
 const h=harness();h.root.dataset.handheldExpires='101';h.fire('DOMContentLoaded');const old=[...h.timers.values()][0];h.install();old.f();assert.equal(h.emitted.length,0);
});
test('restored pages clear temporary request locks but keep expired access locked',()=>{
 const h=harness();h.begin();h.winFire('pageshow');assert.equal(h.root.form.inputs[0].disabled,false);assert.equal(h.root.form.inputs[2].disabled,true);
 h.root.dataset.handheldExpires='99';h.winFire('pageshow');assert.ok(h.root.form.inputs.every(i=>i.disabled));
});


test('all scoped phone forms are marked for revocation while connection management stays usable',()=>{
 const template=fs.readFileSync('web/templates/handheld.html','utf8');
 const forms=[...template.matchAll(/<form\b[^>]*>/g)].map(m=>m[0]);
 for(const path of ['/handheld/pick','/handheld/scan','/handheld/weight/preview','/handheld/weight/confirm','/handheld/report']){
  const form=forms.find(f=>f.includes(`action="${path}"`));assert.ok(form,path);
  for(const attr of ['method="post"',`hx-post="${path}"`,'data-handheld-form','data-handheld-authorized','hx-target="#handheld-workspace"','hx-select="#handheld-workspace"','hx-swap="outerHTML"','hx-sync="this:drop"'])assert.ok(form.includes(attr),`${path} ${attr}`);
 }
 for(const path of ['/handheld/connect','/handheld/disconnect'])assert.ok(!forms.find(f=>f.includes(`action="${path}"`)).includes('data-handheld-authorized'));
});

test('revocation locks every counted, scan, weight and report field but preserves reconnect and disconnect',()=>{
 const h=harness();const drafts=h.root.forms.map(form=>form.inputs.map(input=>input.value));
 const xhr=h.begin(h.root,'revoked');h.fire('htmx:beforeSwap',{xhr,shouldSwap:true});h.fire('htmx:afterRequest',{xhr});
 for(const [index,form] of h.root.forms.entries()){
  assert.deepEqual(form.inputs.map(input=>input.value),drafts[index]);
  assert.equal(form.inputs[0].disabled,form.authorized,form.action);
  assert.equal(form.inputs[3].disabled,form.authorized,`stock select: ${form.action}`);
  assert.equal(form.inputs[4].disabled,form.authorized,`note textarea: ${form.action}`);
  if(form.authorized){assert.ok(form.inputs.every(input=>input.disabled));const submit=h.fire('submit',{},form);assert.ok(submit.prevented&&submit.stopped);assert.equal(h.fire('htmx:beforeRequest',{elt:form,xhr:{}}).prevented,true);}
 }
 h.winFire('pageshow');h.fire('htmx:historyRestore');
 for(const form of h.root.forms.filter(form=>form.authorized))assert.ok(form.inputs.every(input=>input.disabled));
 assert.equal(h.root.forms.find(form=>form.action==='/handheld/disconnect').inputs[0].disabled,false);
});

test('each weight and report request locks only its submitted form and keeps offline or failed drafts',()=>{
 for(const path of ['/handheld/weight/preview','/handheld/weight/confirm','/handheld/report']){
  const h=harness(),form=h.root.forms.find(form=>form.action===path),draft=form.inputs.map(input=>input.value);
  const xhr=h.begin(h.root,null,false,form);assert.ok(form.inputs.every(input=>input.disabled));assert.equal(h.root.form.inputs[0].disabled,false);
  h.fire('htmx:sendError',{xhr});h.fire('htmx:afterRequest',{xhr});assert.deepEqual(form.inputs.map(input=>input.value),draft);assert.equal(form.inputs[2].disabled,true);assert.equal(form.inputs[4].disabled,false);
  h.navigator.onLine=false;assert.equal(h.fire('submit',{},form).prevented,true);assert.equal(h.fire('htmx:beforeRequest',{elt:form,xhr:{}}).prevented,true);assert.deepEqual(form.inputs.map(input=>input.value),draft);
  h.navigator.onLine=true;h.winFire('online');assert.deepEqual(form.inputs.map(input=>input.value),draft);assert.match(h.ids.get('handheld-network').textContent,/Nothing was sent/);
 }
});

test('restoring a review rejects pre-Back responses and never restores an expired confirmation',()=>{
 for(const restore of ['pageshow','htmx:historyRestore']){
  const h=harness(),form=h.root.forms.find(form=>form.action==='/handheld/weight/confirm'),xhr=h.begin(h.root,null,false,form);
  if(restore==='pageshow')h.winFire(restore);else h.fire(restore);
  assert.equal(form.inputs[0].disabled,false);assert.equal(h.fire('htmx:beforeOnLoad',{xhr}).prevented,true);assert.equal(h.fire('htmx:beforeSwap',{xhr,shouldSwap:true}).detail.shouldSwap,false);
  h.root.dataset.handheldExpires='99';if(restore==='pageshow')h.winFire(restore);else h.fire(restore);
  assert.ok(h.root.forms.filter(form=>form.authorized).every(form=>form.inputs.every(input=>input.disabled)));
 }
});

test('fresh connection replaces expired draft locks without letting old errors revoke it',()=>{
 const h=harness(),old=h.root,xhr=h.begin(old,'revoked');old.dataset.handheldExpires='99';h.fire('DOMContentLoaded');assert.equal(old.dataset.handheldRevoked,'true');
 const next=h.install();next.dataset.handheldExpires='200';h.fire('htmx:afterSwap',{target:{id:'handheld-workspace'}});
 h.fire('htmx:responseError',{xhr});assert.equal(next.dataset.handheldRevoked,undefined);assert.equal(next.form.inputs[0].disabled,false);assert.equal(h.fire('htmx:beforeOnLoad',{xhr}).prevented,true);
});

test('weight preview separates whole-gram draft, stock choice, reviewed amounts and snapshot-bound confirmation',()=>{
 const template=fs.readFileSync('web/templates/handheld.html','utf8');
 const weightEntry=template.split('{{define "handheld-weight-form"}}')[1].split('{{define "handheld-weight-review"}}')[0];
 const review=template.split('{{define "handheld-weight-review"}}')[1].split('{{define "handheld-report-form"}}')[0];
 assert.match(template,/else if and \.Recognition \.Recognition\.CanMeasure/);
 assert.match(weightEntry,/name="actual" type="text" inputmode="numeric" pattern="\[0-9\]\+"/);assert.match(weightEntry,/value="{{if \$draft}}{{\$draft\.Actual}}{{end}}"/);
 assert.match(weightEntry,/<option value="">Choose if reducing the allocation<\/option>/);assert.match(weightEntry,/value="restock"/);assert.match(weightEntry,/value="writeoff"/);assert.doesNotMatch(weightEntry,/<option[^>]+value="(?:restock|writeoff)" selected/);
 for(const field of ['Working target','Current allocation','Entered actual weight','Retained order rate','Current line amount','New line amount','Line amount change','Stock consequence','Proposed working order total','not a final total'])assert.ok(review.includes(field),field);
 for(const [field,binding] of [['line_id','LineID'],['assignment_version','AssignmentVersion'],['version','Version'],['pick_version','PickVersion'],['code','Code'],['format','Format'],['source','Source'],['command_key','Key'],['review','Review'],['actual','Actual'],['disposition','Disposition'],['note','Note']])assert.ok(review.includes(`name="${field}" value="{{\$c.${binding}}}"`),field);
 assert.match(template,/name="resume_weight" value="1"/);assert.match(template,/value="{{\.WeightDraft\.Actual}}"/);assert.match(template,/Measured zero saved/);assert.match(template,/fresh preview before confirming/);
});

test('item report has bounded choices and optional note with no scan prerequisite',()=>{
 const template=fs.readFileSync('web/templates/handheld.html','utf8'),report=template.split('{{define "handheld-report-form"}}')[1];
 assert.match(report,/Ask manager to review this item/);assert.match(report,/name="command_key" value="{{\.ReportKey}}"/);
 for(const kind of ['unavailable','damaged','weight_stock'])assert.ok(report.includes(`value="${kind}"`));
 assert.match(report,/name="note" maxlength="240"/);assert.doesNotMatch(report,/name="(?:code|format|source)"/);assert.match(report,/does not change this item’s picked quantity or stock/);
 assert.match(template,/{{end}}{{end}}\s*{{template "handheld-report-form" \.}}/);
});


test('a new explicit weight review receives focus while validation errors take priority',()=>{
 const h=harness();h.doc.reviewVisible=true;h.fire('htmx:afterSwap',{target:{id:'handheld-workspace'}});assert.ok(h.review.focused&&h.review.scrolled);
 h.review.focused=false;h.doc.errorVisible=true;h.fire('htmx:afterSwap',{target:{id:'handheld-workspace'}});assert.ok(h.error.focused&&h.error.scrolled);assert.equal(h.review.focused,false);
});


test('late cleanup cannot unlock a newer submission of the same restored form',()=>{
 const h=harness(),form=h.root.forms.find(form=>form.action==='/handheld/weight/confirm');
 const old=h.begin(h.root,null,false,form);h.winFire('pageshow');const next=h.begin(h.root,null,false,form);
 h.fire('htmx:afterRequest',{xhr:old});assert.ok(form.inputs.every(input=>input.disabled));assert.equal(h.ids.get('handheld-pending').hidden,false);
 h.fire('htmx:afterRequest',{xhr:next});assert.equal(form.inputs[0].disabled,false);assert.equal(form.inputs[2].disabled,true);assert.equal(h.ids.get('handheld-pending').hidden,true);
});
