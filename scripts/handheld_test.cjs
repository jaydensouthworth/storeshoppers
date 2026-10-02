const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('web/static/handheld.js','utf8');
function harness({hash='', online=true, clipboardFail=false}={}) {
 const listeners=new Map(), windowListeners=new Map(), ids=new Map(), emitted=[], history=[], copied=[], timers=new Map(), clock={now:100000};let timerID=0;
 let current=null;
 const make=id=>({id,dataset:{},value:'',disabled:false,hidden:true,isConnected:true,textContent:'',focus(){this.focused=true;},select(){this.selected=true;},scrollIntoView(){this.scrolled=true;}});
 const note=make('pair-note'), copyStatus=make('copy-status'), error=make('error');
 const doc={readyState:'loading',addEventListener(n,f){if(!listeners.has(n))listeners.set(n,[]);listeners.get(n).push(f);},getElementById(id){return id==='handheld-workspace'?current:ids.get(id)||null;},querySelector(s){if(s==='[data-pairing-link-note]')return note;if(s==='[data-copy-pairing-status]')return copyStatus;if(s==='#handheld-workspace .handheld-feedback.error')return this.errorVisible?error:null;return null;},querySelectorAll(s){if(s==="[data-handheld-busy]")return current?current.form.inputs.filter(i=>i.dataset.handheldBusy):[];return s.startsWith('form[action=')&&current?[current.form]:[];},dispatchEvent(e){emitted.push(e.type);}};
 const win={setTimeout(f,ms){const id=++timerID;timers.set(id,{f,ms});return id;},clearTimeout(id){timers.delete(id);},location:{hash,pathname:'/handheld/',search:''},history:{replaceState(a,b,c){history.push(c);win.location.hash='';}},addEventListener(n,f){if(!windowListeners.has(n))windowListeners.set(n,[]);windowListeners.get(n).push(f);}};
 const navigator={onLine:online,clipboard:{async writeText(v){if(clipboardFail)throw Error('denied');copied.push(v);}}};
 const pairing=make('pairing-code'),copyInput=make('phone-pairing-code');ids.set(pairing.id,pairing);ids.set(copyInput.id,copyInput);ids.set('handheld-network',make('handheld-network'));ids.set('handheld-pending',make('handheld-pending'));
 function install(){if(current)current.form.inputs.forEach(i=>i.isConnected=false);const inputs=[make('code'),make('picked'),make('already-disabled')];inputs[0].value='SHOPDEMO-000001';inputs[1].value='2';inputs[2].disabled=true;const form={inputs,matches:s=>s==='form'||s==='[data-handheld-form]',closest:()=>form,querySelectorAll:()=>inputs};current={id:'handheld-workspace',dataset:{},form,contains:e=>e===form||e?.owner===current};return current;}
 install();
 vm.runInNewContext(source,{document:doc,window:win,navigator,WeakMap,URLSearchParams,Date:{now:()=>clock.now},CustomEvent:class{constructor(type){this.type=type;}}});
 function event(detail={},target){return {detail,target,prevented:false,stopped:false,preventDefault(){this.prevented=true;},stopImmediatePropagation(){this.stopped=true;}};}
 function fire(n,detail={},target){const e=event(detail,target);for(const f of listeners.get(n)||[])f(e);return e;}
 async function asyncFire(n,target){const e=event({},target);for(const f of listeners.get(n)||[])await f(e);return e;}
 function winFire(n){for(const f of windowListeners.get(n)||[])f({});}
 function begin(root=current,header=null,link=false){const xhr={getResponseHeader:()=>header};const elt=link?{owner:root,matches:()=>false,closest:()=>null}:root.form;fire('htmx:beforeRequest',{elt,xhr});return xhr;}
 return {doc,win,navigator,timers,clock,ids,pairing,copyInput,copyStatus,note,error,history,copied,emitted,install,fire,asyncFire,winFire,begin,get root(){return current;}};
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
