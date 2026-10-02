const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {install, utf8Length, boundedDraft, LIMITS} = require('../web/static/order_messages.js');

// Dependency-free DOM/HTMX event harness: transport completion is deliberately
// controlled so tests can deliver old bodies and headers in arbitrary order.
class Element {
  constructor(id='') { Object.assign(this,{id,dataset:{},value:'',textContent:'',disabled:false,readOnly:false,hidden:false,isConnected:true,scrollTop:0,scrollHeight:500,clientHeight:240,offsetTop:0,offsetHeight:60,children:[]}); this.classes=new Set(); this.classList={toggle:(name,on)=>on?this.classes.add(name):this.classes.delete(name)}; }
  getBoundingClientRect(){const top=this.parentScroll?this.offsetTop-this.parentScroll.scrollTop+300:300;return {top,bottom:top+this.offsetHeight};}
  focus(){this.focused=true;}
  scrollIntoView(){this.scrolled=true;}
  replaceChildren(...children){this.children=children;this.cleared=children.length===0;}
  remove(){this.removed=true;}
  matches(selector){return (selector==='[data-messages-check]'&&this.id==='message-check')||(selector==='[data-messages-retry]'&&this.id==='message-retry');}
  closest(selector){if(selector==='#'+this.id)return this;if(selector==='[data-messages-navigation]'&&this.dataset.messagesNavigation)return this;return null;}
  querySelector(){return null;}
  querySelectorAll(){return [];}
}
function harness({phone=false, older=false, canSend=true, sendState='idle', body='', revision=0, version=1, expires=0, online=true}={}) {
  const listeners=new Map(), windowListeners=new Map(), ids=new Map(), timers=new Map(), parses=new Map(), calls=[];let timerID=0,parseID=0,currentRoot;
  const add=(map,name,fn)=>{if(!map.has(name))map.set(name,[]);map.get(name).push(fn);};
  const doc={readyState:'complete',hidden:false,addEventListener:(name,fn)=>add(listeners,name,fn),getElementById:id=>ids.get(id)||null,createElement:()=>new Element(),createTextNode:text=>({textContent:text})};
  const env={document:doc,navigator:{onLine:online},addEventListener:(name,fn)=>add(windowListeners,name,fn),setTimeout(fn,ms){timers.set(++timerID,{fn,ms});return timerID;},clearTimeout:id=>timers.delete(id),DOMParser:class{parseFromString(text){return {getElementById:id=>parses.get(text)?.id===id?parses.get(text):null};}}};
  const make=id=>{const el=new Element(id);ids.set(id,el);return el;};
  const form=make('order-message-form'),fields={};
  for(const name of ['csrf','conversation_key','assignment_id','assignment_version','command_key','body'])fields[name]=new Element(name==='body'?'order-message-body':'');
  Object.assign(fields.csrf,{value:'csrf-demo'});Object.assign(fields.conversation_key,{value:'conversation'});Object.assign(fields.assignment_id,{value:'1'});Object.assign(fields.assignment_version,{value:'1'});Object.assign(fields.command_key,{value:'key-a'});fields.body.value=body;
  ids.set('order-message-body',fields.body);
  form.querySelector=selector=>fields[selector.match(/^\[name="(.+)"\]$/)?.[1]]||null;
  const defaults={messagesContext:'conversation/1/1',messagesContextVersion:String(version),messagesRevision:String(revision),messagesExpires:String(expires),messagesCanSend:String(canSend),messagesReason:canSend?'':'Conversation closed',messagesAssignmentName:'Alex Demo',messagesOlder:String(older),messagesSendState:sendState};
  function region(id,meta={},values={}){
    const node=new Element(id);node.dataset={...defaults,...meta};node.fields={};
    for(const name of Object.keys(fields)){node.fields[name]=new Element();node.fields[name].value=values[name]??fields[name].value;}
    node.querySelector=selector=>selector==='.messages-scroll'?node.scroll:node.fields[selector.match(/^\[name="(.+)"\]$/)?.[1]]||null;
    node.scroll=new Element('scroll');node.items=[new Element('order-message-1'),new Element('order-message-2')];node.items[0].offsetTop=0;node.items[1].offsetTop=100;node.items.forEach(x=>{x.dataset.messageRevision=x.id.split('-').at(-1);x.parentScroll=node.scroll;});
    node.querySelectorAll=selector=>selector==='[data-message-revision]'?node.items:[];return node;
  }
  function installFeed(node){const old=ids.get('order-message-feed');if(old)old.isConnected=false;ids.set(node.id,node);node.items.forEach(x=>ids.set(x.id,x));node.owner=currentRoot;return node;}
  const root=make('order-messages-workspace');currentRoot=root;root.dataset={messagesPath:phone?'/handheld/messages':'/orders/1/messages',messagesFeedUrl:phone?'/handheld/messages?feed=1':'/orders/1/messages?feed=1',messagesPhone:String(phone)};
  const assignment=new Element('assignment');root.contains=element=>element===root||element.owner===root||element===form||element===fields.body;root.querySelector=selector=>selector==='[data-message-assignment]'?assignment:null;root.querySelectorAll=()=>[];
  installFeed(region('order-message-feed'));const composer=region('order-message-composer');ids.set(composer.id,composer);
  for(const id of ['message-limit','message-send','message-check','message-retry','message-uncertain','message-review','message-readonly','messages-notice','messages-authority','messages-new'])make(id);
  ids.get('messages-new').hidden=true;
  function fire(name,detail={},target,extra={}){const event={detail,target,prevented:false,stopped:false,preventDefault(){this.prevented=true;},stopImmediatePropagation(){this.stopped=true;},...extra};for(const fn of listeners.get(name)||[]){fn(event);if(event.stopped)break;}return event;}
  function win(name,extra={}){for(const fn of windowListeners.get(name)||[])fn(extra);}
  env.htmx={ajax(method,url,options){const xhr={status:0,responseText:'',headers:{},getResponseHeader(name){return this.headers[name]??null;},abort(){call.aborted=true;fire('htmx:afterRequest',{xhr:this});}};const call={method,url,options,xhr};calls.push(call);const event=fire('htmx:beforeRequest',{elt:options.source,xhr});call.blocked=event.prevented;return Promise.resolve();}};
  const controller=install(env);
  function respond(call,{status=200,headers={},meta={},values={},result,ack,kind}={}){
    const isPost=call.method==='POST';kind??=isPost?'order-message-composer':'order-message-feed';
    const node=region(kind,meta,values),token='response-'+(++parseID);parses.set(token,node);
    const hdr={'X-Messages-State':node.dataset.messagesCanSend==='true'?'open':'closed','X-Messages-Context':node.dataset.messagesContext,'X-Messages-Context-Version':node.dataset.messagesContextVersion,'X-Messages-Revision':node.dataset.messagesRevision,'X-Messages-Expires':node.dataset.messagesExpires,...headers};
    if(result)hdr['X-Messages-Result']=result;if(ack)hdr['X-Messages-Key']=ack;
    Object.assign(call.xhr,{status,headers:hdr,responseText:token});
    const load=fire('htmx:beforeOnLoad',{xhr:call.xhr});let swapped=false;
    if(!load.prevented){const swap=fire('htmx:beforeSwap',{xhr:call.xhr,shouldSwap:true});if(swap.detail.shouldSwap&&kind==='order-message-feed'&&call.options.swap!=='none'){installFeed(node);swapped=true;fire('htmx:afterSwap',{xhr:call.xhr,target:node});}}
    fire('htmx:afterRequest',{xhr:call.xhr});return {load,swapped,node};
  }
  function input(text){fields.body.value=text;fire('input',{},fields.body);}
  function submit(button='message-send'){return fire('submit',{},form,{submitter:ids.get(button)});}
  function navigation(kind='older'){const link=new Element('nav');link.owner=root;link.dataset.messagesNavigation=kind;env.htmx.ajax('GET',root.dataset.messagesPath+(kind==='older'?'?before=50':''),{source:link,target:'#order-message-feed',swap:'outerHTML'});return calls.at(-1);}
  function error(call,name='htmx:sendError'){fire(name,{xhr:call.xhr});fire('htmx:afterRequest',{xhr:call.xhr});}
  return {env,doc,root,fields,ids,timers,calls,controller,fire,win,respond,input,submit,navigation,error,region,installFeed,get feed(){return ids.get('order-message-feed');},get notice(){return ids.get('messages-notice').textContent;}};
}

test('Unicode limits count code points and UTF-8 bytes, not UTF-16 units',()=>{
  assert.equal(utf8Length('Aé中😀'),10);
  assert.equal([...boundedDraft('😀'.repeat(501))].length,500);
  assert.equal(utf8Length(boundedDraft('😀'.repeat(501))),2000);
  assert.equal(boundedDraft('e\u0301'.repeat(300)).length,500);
  assert.equal(boundedDraft('x'.repeat(501)).length,500);
});
test('dedicated shell has safe plaintext output, fallback forms, independent assets and theme options',()=>{
  const html=fs.readFileSync('web/templates/order_messages.html','utf8'),js=fs.readFileSync('web/static/order_messages.js','utf8'),css=fs.readFileSync('web/static/order_messages.css','utf8');
  for(const name of ['order-messages','order-messages-feed','order-messages-composer'])assert.ok(html.includes('{{define "'+name+'"}}'));
  assert.match(html,/{{\.Body}}/);assert.match(html,/formaction="{{\.Path}}\/check"/);assert.match(html,/theme-footer/);assert.match(html,/Fake demo notes only/);assert.match(html,/Messages do not approve substitutions/);assert.match(html,/Sent · {{\.Created}}/);assert.match(html,/eq \.Actor "worker"/);
  assert.doesNotMatch(html,/scanner\.js|handheld\.js|app\.js|hx-trigger=.*every|hx-swap-oob/);
  assert.doesNotMatch(js,/localStorage|sessionStorage|document\.cookie|Notification|requestPermission|innerHTML|fetch\(/);
  assert.match(css,/min-height:48px/);assert.match(css,/max-width:360px/);assert.match(css,/var\(--theme-/);
});
test('initial form is writable only with fresh active authority and nonblank text',()=>{
  const h=harness();assert.equal(h.ids.get('message-send').disabled,true);h.input('Fake note');assert.equal(h.ids.get('message-send').disabled,false);
  const closed=harness({canSend:false,body:'Saved draft'});assert.equal(closed.fields.body.readOnly,true);assert.equal(closed.ids.get('message-send').disabled,true);
});
test('invalid and oversized drafts never transmit, are bounded and require another deliberate send',()=>{
  for(const draft of ['   ','x'.repeat(501),'😀'.repeat(501)]){const h=harness();h.input(draft);h.submit();assert.equal(h.calls.length,0);assert.ok([...h.fields.body.value].length<=500);assert.ok(utf8Length(h.fields.body.value)<=2000);assert.match(h.notice,/Review/);}
});
test('500 astral code points can send, lock the payload and count 2000 bytes',()=>{
  const h=harness();h.input('😀'.repeat(500));h.submit();assert.equal(h.calls.length,1);assert.equal(h.calls[0].options.values.body,'😀'.repeat(500));assert.equal(h.fields.body.readOnly,true);assert.match(h.ids.get('message-limit').textContent,/500.*2,000|500.*2000/);h.submit();assert.equal(h.calls.length,1);
});
test('confirmed persistence clears only the acknowledged draft and rotates its key',()=>{
  const h=harness();h.input('Fake note');h.submit();const send=h.calls[0];h.respond(send,{result:'sent',ack:'key-a',values:{command_key:'key-b',body:''},meta:{messagesRevision:'1',messagesSendState:'saved'}});
  assert.equal(h.fields.body.value,'');assert.equal(h.fields.command_key.value,'key-b');assert.match(h.notice,/Sent/);assert.equal(h.calls.at(-1).method,'GET');assert.equal(h.controller.getState().uncertain,null);
});
test('a late acknowledgment never clears newer text',()=>{
  const h=harness();h.input('Submitted');h.submit();h.input('Newer local text');h.respond(h.calls[0],{result:'sent',ack:'key-a',values:{command_key:'key-b',body:''},meta:{messagesRevision:'1'}});
  assert.equal(h.fields.body.value,'Newer local text');assert.equal(h.fields.command_key.value,'key-b');
});
test('a mismatched acknowledgment is uncertain and cannot mint a new key',()=>{
  const h=harness();h.input('Exact original');h.submit();h.respond(h.calls[0],{result:'sent',ack:'another-key',values:{command_key:'key-b',body:''}});
  assert.equal(h.fields.body.value,'Exact original');assert.equal(h.fields.command_key.value,'key-a');assert.ok(h.controller.getState().uncertain);
});
test('poll completion cannot clear a pending send or unlock its text',()=>{
  const h=harness();h.controller.refresh();const poll=h.calls[0];h.input('Sending');h.submit();const send=h.calls[1];h.respond(poll);assert.equal(h.fields.body.readOnly,true);assert.ok(h.controller.getState().pending);
  h.respond(send,{result:'sent',ack:'key-a',values:{command_key:'key-b'},meta:{messagesRevision:'1'}});assert.equal(h.fields.body.value,'');
});
test('newer send revision suppresses an old poll body',()=>{
  const h=harness();h.controller.refresh();const poll=h.calls[0];h.input('Sending');h.submit();h.respond(h.calls[1],{result:'sent',ack:'key-a',values:{command_key:'key-b'},meta:{messagesRevision:'1'}});
  const result=h.respond(poll);assert.equal(result.load.prevented,true);assert.equal(result.swapped,false);assert.equal(h.controller.getState().revision,1);
});
test('newest navigation wins and suppresses old feed bodies and late redirect headers',()=>{
  const h=harness();h.controller.refresh();const poll=h.calls[0];const old=h.navigation('older'),latest=h.navigation('latest');
  assert.equal(h.respond(old,{headers:{'HX-Redirect':'/bad'}}).load.prevented,true);
  assert.equal(h.respond(poll,{headers:{'HX-Location':'/bad'}}).load.prevented,true);
  assert.equal(h.respond(latest,{meta:{messagesRevision:'8'}}).swapped,true);assert.equal(h.controller.getState().revision,8);
});
test('unexpected navigation headers never run for a live send',()=>{
  for(const header of ['HX-Redirect','HX-Location','HX-Refresh']){const h=harness();h.input('Exact note');h.submit();assert.equal(h.respond(h.calls[0],{headers:{[header]:'/elsewhere'}}).load.prevented,true);assert.ok(h.controller.getState().uncertain);assert.equal(h.fields.body.value,'Exact note');}
});
test('definite validation and temporary rate refusal retain bounded text with a fresh explicit-send key',()=>{
  const h=harness();h.input('Retained note');h.submit();h.respond(h.calls[0],{result:'invalid',values:{command_key:'key-b',body:'Retained note'},meta:{messagesServerError:'Please wait before sending another note.'}});
  assert.equal(h.fields.body.value,'Retained note');assert.equal(h.fields.command_key.value,'key-b');assert.equal(h.controller.getState().uncertain,null);assert.equal(h.fields.body.readOnly,false);assert.equal(h.calls.length,1);assert.match(h.notice,/Please wait/);
});
test('quota refusal erects a barrier that a stale or later ordinary poll cannot reopen',()=>{
  const h=harness();h.controller.refresh();const oldPoll=h.calls[0];h.input('Quota note');h.submit();h.respond(h.calls[1],{result:'invalid',headers:{'X-Messages-State':'quota'},values:{command_key:'key-b'},meta:{messagesCanSend:'false',messagesReason:'Storage limit reached'}});
  assert.equal(h.respond(oldPoll).load.prevented,true);h.controller.refresh();h.respond(h.calls.at(-1));assert.equal(h.controller.getState().state,'quota');assert.equal(h.ids.get('message-send').disabled,true);assert.equal(h.ids.get('message-review').hidden,false);
});
test('closure after send starts cannot be undone by an older saved acknowledgment',()=>{
  const h=harness();h.input('A note');h.submit();const send=h.calls[0];h.controller.refresh();h.respond(h.calls[1],{meta:{messagesCanSend:'false',messagesContextVersion:'2',messagesReason:'Order is ready'}});
  h.respond(send,{result:'sent',ack:'key-a',values:{command_key:'key-b'},meta:{messagesRevision:'1',messagesContextVersion:'1'}});
  assert.equal(h.controller.getState().state,'closed');assert.equal(h.fields.body.readOnly,true);assert.equal(h.ids.get('message-send').disabled,true);
});
test('changed assignment freezes draft for explicit lookup/review without silently retargeting it',()=>{
  const h=harness();h.input('For original shopper');h.controller.refresh();h.respond(h.calls[0],{meta:{messagesContext:'conversation/2/2',messagesContextVersion:'2',messagesAssignmentName:'River Demo'}});
  assert.equal(h.fields.assignment_id.value,'1');assert.equal(h.fields.body.value,'For original shopper');assert.equal(h.controller.getState().review,true);assert.equal(h.fields.body.readOnly,true);
  h.fire('click',{},h.ids.get('message-review'));const check=h.calls.at(-1);assert.equal(check.url,'/orders/1/messages/check');assert.equal(check.options.values.assignment_id,'1');
  h.respond(check,{result:'stale',values:{command_key:'key-b',assignment_id:'2',assignment_version:'2'},meta:{messagesContext:'conversation/2/2',messagesContextVersion:'2'}});
  assert.equal(h.fields.assignment_id.value,'2');assert.equal(h.controller.getState().review,false);assert.equal(h.fields.body.value,'For original shopper');assert.equal(h.ids.get('message-send').disabled,false);
});
test('network, timeout and server 503 preserve the exact uncertain key and payload',()=>{
  for(const failure of ['htmx:sendError','htmx:timeout','503']){const h=harness();h.input('  Exact uncertain note 😀\n');h.submit();if(failure==='503')h.respond(h.calls[0],{status:503});else h.error(h.calls[0],failure);
    assert.equal(h.fields.body.value,'  Exact uncertain note 😀\n');assert.equal(h.fields.command_key.value,'key-a');assert.equal(h.fields.body.readOnly,true);assert.equal(h.ids.get('message-uncertain').hidden,false);assert.equal(h.ids.get('message-send').disabled,true);assert.equal(h.calls.length,1);
  }
});
test('Check send result posts to check endpoint, absence stays uncertain, retry reuses identical key/body',()=>{
  const h=harness();h.input('Exact payload');h.submit();const original=h.calls[0].options.values;h.error(h.calls[0]);h.submit('message-check');const check=h.calls[1];assert.equal(check.url,'/orders/1/messages/check');assert.deepEqual(check.options.values,original);
  h.respond(check,{result:'absent',values:{command_key:'should-never-replace'}});assert.equal(h.fields.command_key.value,'key-a');assert.equal(h.fields.body.readOnly,true);assert.equal(h.calls.length,2);
  h.submit('message-retry');assert.equal(h.calls[2].url,'/orders/1/messages');assert.deepEqual(h.calls[2].options.values,original);
});
test('lookup finding an existing message clears exact pending intent without a duplicate send',()=>{
  const h=harness();h.input('Look up');h.submit();h.error(h.calls[0]);h.submit('message-check');h.respond(h.calls[1],{result:'sent',ack:'key-a',values:{command_key:'key-b',body:''},meta:{messagesRevision:'1'}});
  assert.equal(h.fields.body.value,'');assert.equal(h.controller.getState().uncertain,null);assert.equal(h.calls.filter(x=>x.method==='POST'&&x.url==='/orders/1/messages').length,1);
});
test('hiding, offline and history restoration pause writes and never replay a send',()=>{
  for(const action of ['hide','offline','history']){const h=harness();h.input('Pending note');h.submit();const old=h.calls[0];
    if(action==='hide'){h.doc.hidden=true;h.fire('visibilitychange');}else if(action==='offline'){h.env.navigator.onLine=false;h.win('offline');}else h.fire('htmx:historyRestore');
    assert.equal(h.ids.get('message-send').disabled,true);assert.equal(h.fields.body.value,'Pending note');assert.ok(h.controller.getState().uncertain);assert.equal(h.respond(old,{result:'sent',ack:'key-a',values:{command_key:'key-b'}}).load.prevented,true);assert.equal(h.calls.filter(x=>x.method==='POST').length,1);
  }
});
test('reconnect and Back/Forward refresh authorization before explicit retry',()=>{
  for(const action of ['online','pageshow','popstate']){const h=harness();h.input('Kept draft');h.controller.suspend();assert.equal(h.fields.body.readOnly,true);h.win(action,{persisted:true});const refresh=h.calls.at(-1);assert.equal(refresh.method,'GET');assert.equal(h.ids.get('message-send').disabled,true);h.respond(refresh);assert.equal(h.ids.get('message-send').disabled,false);assert.equal(h.calls.filter(x=>x.method==='POST').length,0);}
});
test('older history does not poll and a successful send does not jump to latest',()=>{
  const h=harness({older:true});assert.equal(h.timers.size,0);h.input('A new note');h.submit();h.respond(h.calls[0],{result:'sent',ack:'key-a',values:{command_key:'key-b'},meta:{messagesRevision:'1'}});assert.equal(h.calls.length,1);assert.equal(h.feed.dataset.messagesOlder,'true');
  h.win('pageshow',{persisted:true});assert.equal(h.calls.at(-1).options.swap,'none');h.respond(h.calls.at(-1),{meta:{messagesRevision:'1',messagesOlder:'false'}});assert.equal(h.feed.dataset.messagesOlder,'true');assert.equal(h.timers.size,0);
});
test('polling preserves a reading anchor and offers new-message hint instead of jumping',()=>{
  const h=harness();h.feed.scroll.scrollTop=100;h.feed.scroll.scrollHeight=1000;h.feed.scroll.clientHeight=200;h.controller.refresh();const poll=h.calls[0];const result=h.respond(poll,{meta:{messagesRevision:'3'}});
  assert.equal(result.swapped,false);assert.equal(h.feed.scroll.scrollTop,100);assert.equal(h.ids.get('messages-new').hidden,false);h.fire('click',{},h.ids.get('messages-new'));const latest=h.respond(h.calls.at(-1),{meta:{messagesRevision:'3'}});assert.equal(latest.node.scroll.scrollTop,latest.node.scroll.scrollHeight);assert.equal(h.ids.get('messages-new').hidden,true);
});
test('polling slows when quiet and never schedules while hidden or offline',()=>{
  const h=harness();for(let i=0;i<3;i++){h.controller.refresh();h.respond(h.calls.at(-1));}assert.ok([...h.timers.values()].some(timer=>timer.ms===LIMITS.quietPoll));
  h.doc.hidden=true;h.fire('visibilitychange');assert.equal(h.timers.size,0);h.env.navigator.onLine=false;h.controller.refresh();assert.equal(h.timers.size,0);
});
test('phone revocation removes stale transcript/draft and obsolete responses cannot restore access',()=>{
  const h=harness({phone:true});h.input('Private fake note');h.submit();const oldSend=h.calls[0];h.controller.refresh();h.respond(h.calls[1],{status:403,headers:{'X-Messages-Access':'ended','X-Messages-State':'revoked'}});
  assert.equal(h.feed.cleared,true);assert.equal(h.fields.body.value,'');assert.equal(h.fields.command_key.value,'');assert.equal(h.controller.getState().revoked,true);assert.equal(h.respond(oldSend,{result:'sent',ack:'key-a',values:{command_key:'key-b'}}).load.prevented,true);assert.equal(h.fields.body.readOnly,true);h.controller.resume();assert.equal(h.calls.length,2);
});
test('known phone expiry removes transcript immediately and leaves reconnect navigation outside the lock',()=>{
  const h=harness({phone:true,expires:1,body:'Old note'});assert.equal(h.controller.getState().revoked,true);assert.equal(h.feed.cleared,true);assert.equal(h.fields.body.value,'');assert.match(h.notice,/expired/);
});
test('unrelated HTMX traffic never locks or changes message state',()=>{
  const h=harness();h.input('Draft');const xhr={getResponseHeader:()=>null};h.fire('htmx:beforeRequest',{xhr,elt:new Element('other')});h.fire('htmx:sendError',{xhr});assert.equal(h.fields.body.readOnly,false);assert.equal(h.controller.getState().pending,null);
});

test('known customer session expiry also removes stale messages and locks sending',()=>{
  const h=harness({expires:1,body:'Old customer note'});assert.equal(h.controller.getState().revoked,true);assert.equal(h.feed.cleared,true);assert.equal(h.fields.body.value,'');assert.match(h.notice,/customer session expired/);
});
test('terminal closed conversations stop polling',()=>{
  const h=harness();h.controller.refresh();h.respond(h.calls[0],{headers:{'X-Messages-State':'closed'},meta:{messagesCanSend:'false',messagesState:'closed',messagesContextVersion:'2'}});assert.equal(h.timers.size,0);
});
test('unassigned conversations keep polling and require review when a shopper becomes assigned',()=>{
  const h=harness();h.controller.refresh();h.respond(h.calls[0],{headers:{'X-Messages-State':'unassigned'},meta:{messagesCanSend:'false',messagesState:'unassigned',messagesContext:'conversation/0/0',messagesContextVersion:'2'}});assert.ok(h.timers.size>0);
  h.controller.refresh();h.respond(h.calls.at(-1),{meta:{messagesContext:'conversation/2/2',messagesContextVersion:'3'}});assert.equal(h.controller.getState().state,'open');assert.equal(h.controller.getState().review,true);assert.equal(h.ids.get('message-send').disabled,true);assert.equal(h.ids.get('message-review').hidden,false);
});

test('an explicit lookup can reopen a resolved quota barrier but absence preserves the exact key',()=>{
  const h=harness();h.input('Quota note');h.submit();h.respond(h.calls[0],{result:'invalid',headers:{'X-Messages-State':'quota'},values:{command_key:'key-b'},meta:{messagesCanSend:'false',messagesReason:'Storage limit reached'}});
  h.controller.refresh();h.respond(h.calls.at(-1));h.fire('click',{},h.ids.get('message-review'));const check=h.calls.at(-1);h.respond(check,{result:'absent',values:{command_key:'key-b'}});
  assert.equal(h.controller.getState().state,'open');assert.equal(h.controller.getState().review,false);assert.equal(h.fields.command_key.value,'key-b');assert.equal(h.ids.get('message-retry').disabled,false);assert.equal(h.fields.body.readOnly,true);
});

test('expiry discovered in a send response cannot restore draft or key after access ends',()=>{
  const h=harness({phone:true});h.input('An old note');h.submit();h.respond(h.calls[0],{result:'invalid',values:{command_key:'key-b',body:'An old note'},meta:{messagesExpires:'1'}});
  assert.equal(h.controller.getState().revoked,true);assert.equal(h.fields.body.value,'');assert.equal(h.fields.command_key.value,'');assert.equal(h.feed.cleared,true);
});

function requestDeadline(h){const entry=[...h.timers].find(([,timer])=>timer.ms===LIMITS.requestTimeout);assert.ok(entry,'request has a finite deadline');return {id:entry[0],fn:entry[1].fn};}
function expireRequest(h,deadline){h.timers.delete(deadline.id);deadline.fn();}
test('dedicated HTMX requests and controller sends both have a finite 15-second timeout',()=>{
  const html=fs.readFileSync('web/templates/order_messages.html','utf8');assert.match(html,/"timeout":15000/);assert.equal(LIMITS.requestTimeout,15000);
});
test('a completely stalled send becomes exact-key uncertain without an automatic POST',()=>{
  const h=harness();h.input('  Exact stalled note 😀\n');h.submit();const send=h.calls[0],original={...send.options.values};expireRequest(h,requestDeadline(h));
  assert.equal(h.controller.getState().pending,null);assert.deepEqual(h.controller.getState().uncertain,original);assert.equal(h.fields.command_key.value,'key-a');assert.equal(h.fields.body.value,original.body);assert.equal(h.fields.body.readOnly,true);assert.equal(h.ids.get('message-check').disabled,false);assert.equal(h.ids.get('message-retry').disabled,false);assert.equal(send.aborted,true);assert.equal(h.calls.filter(x=>x.method==='POST').length,1);
  assert.equal(h.respond(send,{result:'sent',ack:'key-a',values:{command_key:'late-key',body:''}}).load.prevented,true);assert.equal(h.fields.body.value,original.body);assert.equal(h.fields.command_key.value,'key-a');
});
test('a stalled check returns to uncertain with the original payload and no automatic retry',()=>{
  const h=harness();h.input('Check this exact note');h.submit();h.error(h.calls[0]);h.submit('message-check');const check=h.calls[1],original={...check.options.values};expireRequest(h,requestDeadline(h));
  assert.equal(check.url,'/orders/1/messages/check');assert.equal(check.aborted,true);assert.deepEqual(h.controller.getState().uncertain,original);assert.equal(h.ids.get('message-check').disabled,false);assert.equal(h.calls.filter(x=>x.method==='POST').length,2);assert.equal(h.fields.body.value,original.body);
});
test('old completion and old deadline cannot overwrite a newer lookup ownership',()=>{
  const h=harness();h.input('Original intent');h.submit();const send=h.calls[0],deadline=requestDeadline(h);expireRequest(h,deadline);h.submit('message-check');const check=h.calls[1];
  deadline.fn();assert.equal(h.controller.getState().pending.body,'Original intent');assert.equal(check.aborted,undefined);
  assert.equal(h.respond(send,{result:'sent',ack:'key-a',values:{command_key:'late-key'}}).load.prevented,true);assert.equal(h.ids.get('message-check').disabled,true);assert.ok(h.controller.getState().pending);
  h.respond(check,{result:'sent',ack:'key-a',values:{command_key:'confirmed-key',body:''},meta:{messagesRevision:'1'}});assert.equal(h.fields.command_key.value,'confirmed-key');assert.equal(h.fields.body.value,'');assert.equal(h.controller.getState().uncertain,null);
});
test('suspend cancels request deadlines while preserving uncertainty; resume only refreshes authority',()=>{
  const h=harness();h.input('Hidden pending note');h.submit();const deadline=requestDeadline(h);h.doc.hidden=true;h.fire('visibilitychange');assert.equal(h.timers.has(deadline.id),false);deadline.fn();assert.equal(h.calls[0].aborted,undefined);assert.equal(h.controller.getState().fresh,false);assert.equal(h.controller.getState().uncertain.body,'Hidden pending note');
  h.doc.hidden=false;h.fire('visibilitychange');assert.equal(h.calls.at(-1).method,'GET');assert.equal(h.calls.filter(x=>x.method==='POST').length,1);assert.equal(h.ids.get('message-retry').disabled,true);
});
test('history navigation has its own lane and cannot strand a stalled send forever',()=>{
  const h=harness();h.input('A pending note');h.submit();const deadline=requestDeadline(h);const history=h.navigation('older');h.respond(history,{meta:{messagesOlder:'true'}});assert.ok(h.controller.getState().pending);expireRequest(h,deadline);
  assert.equal(h.controller.getState().pending,null);assert.equal(h.controller.getState().uncertain.body,'A pending note');assert.equal(h.feed.dataset.messagesOlder,'true');assert.equal(h.calls.filter(x=>x.method==='POST').length,1);
});
test('completion clears its request deadline and a queued old callback cannot resurrect uncertainty',()=>{
  const h=harness();h.input('Saved on time');h.submit();const deadline=requestDeadline(h);h.respond(h.calls[0],{result:'sent',ack:'key-a',values:{command_key:'key-b',body:''},meta:{messagesRevision:'1'}});assert.equal(h.timers.has(deadline.id),false);deadline.fn();assert.equal(h.controller.getState().uncertain,null);assert.equal(h.fields.body.value,'');assert.equal(h.fields.command_key.value,'key-b');
});

test('enhanced pagination creates no HTMX history entries or raw cache-miss restoration',()=>{
  const html=fs.readFileSync('web/templates/order_messages.html','utf8');assert.match(html,/"historyEnabled":false/);assert.match(html,/"historyCacheSize":0/);assert.match(html,/hx-history="false"/);assert.doesNotMatch(html,/hx-push-url|hx-history-elt/);assert.match(html,/href="{{\.OlderURL}}"/);assert.match(html,/href="{{\.LatestURL}}"/);
  const h=harness();h.input('Keep composer');assert.equal(h.fire('htmx:historyCacheMiss',{xhr:{}}).prevented,true);assert.equal(h.fire('htmx:historyCacheHit',{}).prevented,true);assert.equal(h.calls.length,0);assert.equal(h.fields.body.value,'Keep composer');
});
test('in-document Older and Latest navigation preserves composer identity and draft',()=>{
  const h=harness();h.input('Keep this unsent draft');const root=h.root,composer=h.ids.get('order-message-composer'),body=h.fields.body,key=h.fields.command_key.value;
  h.respond(h.navigation('older'),{meta:{messagesOlder:'true',messagesRevision:'9'}});assert.equal(h.feed.dataset.messagesOlder,'true');h.respond(h.navigation('latest'),{meta:{messagesOlder:'false',messagesRevision:'9'}});
  assert.equal(h.root,root);assert.equal(h.ids.get('order-message-composer'),composer);assert.equal(h.fields.body,body);assert.equal(body.value,'Keep this unsent draft');assert.equal(h.fields.command_key.value,key);assert.equal(h.calls.filter(x=>x.method==='POST').length,0);
});
test('in-document history navigation preserves pending and already-uncertain payloads',()=>{
  for(const failFirst of [false,true]){const h=harness();h.input('  Exact history note 😀\n');h.submit();const send=h.calls[0],original={...send.options.values};if(failFirst)h.error(send);
    h.respond(h.navigation('older'),{meta:{messagesOlder:'true',messagesRevision:'4'}});assert.deepEqual(h.controller.getState()[failFirst?'uncertain':'pending'],original);assert.equal(h.fields.body.value,original.body);assert.equal(h.fields.command_key.value,original.command_key);assert.equal(h.fields.body.readOnly,true);
    if(!failFirst)h.error(send);h.submit('message-check');assert.equal(h.calls.at(-1).url,'/orders/1/messages/check');assert.deepEqual(h.calls.at(-1).options.values,original);assert.equal(h.calls.filter(x=>x.method==='POST'&&x.url==='/orders/1/messages').length,1);
  }
});
test('a concurrent send at revision 51 does not discard an unchanged-context Older page from revision 50',()=>{
  const h=harness({revision:50});const history=h.navigation('older');h.input('New message');h.submit();h.respond(h.calls[1],{result:'sent',ack:'key-a',values:{command_key:'key-b',body:''},meta:{messagesRevision:'51'}});
  const page=h.respond(history,{meta:{messagesOlder:'true',messagesRevision:'50'}});assert.equal(page.swapped,true);assert.equal(h.feed.dataset.messagesOlder,'true');assert.equal(h.controller.getState().revision,51);assert.equal(h.fields.command_key.value,'key-b');assert.equal(h.fields.body.value,'');
});
test('lower-revision historical body never rolls back a concurrent restrictive composer state',()=>{
  const h=harness({revision:50});const history=h.navigation('older');h.input('Quota note');h.submit();h.respond(h.calls[1],{result:'invalid',headers:{'X-Messages-State':'quota'},values:{command_key:'key-b'},meta:{messagesCanSend:'false',messagesRevision:'51',messagesReason:'Storage limit reached'}});
  assert.equal(h.respond(history,{meta:{messagesOlder:'true',messagesRevision:'50'}}).swapped,true);assert.equal(h.controller.getState().state,'quota');assert.equal(h.controller.getState().revision,51);assert.equal(h.fields.body.readOnly,true);assert.equal(h.ids.get('message-send').disabled,true);
});
test('a held Older response cannot restore a feed after access ends',()=>{
  const h=harness({phone:true});const history=h.navigation('older');h.input('A pending note');h.submit();h.respond(h.calls[1],{status:403,headers:{'X-Messages-Access':'ended','X-Messages-State':'revoked'}});
  assert.equal(h.respond(history,{meta:{messagesOlder:'true'}}).load.prevented,true);assert.equal(h.feed.cleared,true);assert.equal(h.fire('htmx:historyCacheMiss',{xhr:{}}).prevented,true);assert.equal(h.controller.getState().revoked,true);
});
