const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync('web/static/picking_metrics.js', 'utf8');
const prefix = 'handheld:picking:v1:';
const storageKey = (order = '42', assignment = '7:1') => prefix + JSON.stringify([order, assignment]);
const dataName = name => name.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase());

class Element {
  constructor(tag = 'div', attrs = {}, parent = null) {
    this.tag = tag; this.attrs = {...attrs}; this.parentElement = parent; this.children = [];
    this.dataset = {}; this.textContent = ''; this.title = '';
    for (const [name, value] of Object.entries(attrs)) if (name.startsWith('data-')) this.dataset[dataName(name)] = value;
    if (parent) parent.children.push(this);
  }
  get id() { return this.attrs.id; }
  getAttribute(name) { return name.startsWith('data-') ? this.dataset[dataName(name)] ?? null : this.attrs[name] ?? null; }
  setAttribute(name, value) { this.attrs[name] = value; }
  matches(selector) {
    return selector.split(',').some(part => {
      part = part.trim();
      if (part.startsWith('.')) return (this.attrs.class || '').split(' ').includes(part.slice(1));
      const match = part.match(/^(\w+)?(?:\[([^=\]]+)(?:="([^"]*)")?\])?$/);
      if (!match) return false;
      return (!match[1] || match[1] === this.tag) && (!match[2] || (this.getAttribute(match[2]) !== null && (match[3] === undefined || this.getAttribute(match[2]) === match[3])));
    });
  }
  closest(selector) { return this.matches(selector) ? this : this.parentElement?.closest(selector) || null; }
  contains(node) { return node === this || this.children.some(child => child.contains(node)); }
  querySelectorAll(selector) { return this.children.flatMap(child => [...(child.matches(selector) ? [child] : []), ...child.querySelectorAll(selector)]); }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
}

function store() {
  const values = new Map();
  return {
    values, failRead: false, failWrite: false, failGetter: false,
    getItem(key) { if (this.failRead) throw Error('storage denied'); return values.get(key) ?? null; },
    setItem(key, value) { if (this.failWrite) throw Error('quota exceeded'); values.set(key, value); }
  };
}

function harness({storage = store(), order = '42', assignment = '7:1', event, delta, rejected, complete = false, navType = 'navigate', hidden = false, noRoot = false, readyState = 'loading', start = 0} = {}) {
  const listeners = new Map(), windowListeners = new Map(), timers = new Map();
  const clock = {now: start}; let timerID = 0, current = null;
  const on = (map, type, fn) => map.set(type, [...(map.get(type) || []), fn]);
  const document = {
    readyState, hidden,
    addEventListener(type, fn) { on(listeners, type, fn); },
    getElementById(id) { return id === 'handheld-workspace' ? current : null; }
  };
  const window = {
    performance: {now: () => clock.now, getEntriesByType: () => [{type: navType}]},
    get sessionStorage() { if (storage.failGetter) throw Error('blocked'); return storage; },
    addEventListener(type, fn) { on(windowListeners, type, fn); },
    setTimeout(fn, ms) { const id = ++timerID; timers.set(id, {fn, due: clock.now + ms}); return id; },
    clearTimeout(id) { timers.delete(id); }
  };
  function install({order = '42', assignment = '7:1', event, delta, rejected, complete = false, revoked = false} = {}) {
    const attrs = {id: 'handheld-workspace', 'data-handheld-order': order, 'data-handheld-assignment': assignment};
    if (event !== undefined) attrs['data-pick-event'] = event;
    if (delta !== undefined) attrs['data-pick-line-delta'] = delta;
    if (rejected !== undefined) attrs['data-pick-rejected'] = rejected;
    if (complete) attrs['data-picking-complete'] = 'true';
    if (revoked) attrs['data-handheld-revoked'] = 'true';
    current = new Element('div', attrs);
    current.displays = {};
    for (const name of ['active', 'rate', 'net', 'status', 'pause']) current.displays[name] = new Element(name === 'pause' ? 'button' : 'span', {['data-picking-' + name]: ''}, current);
    current.item = new Element('section', {class: 'handheld-item'}, current);
    current.list = new Element('section', {class: 'handheld-pick-list'}, current);
    current.open = new Element('a', {href: '/handheld/?line=99'}, current.list);
    current.pick = new Element('form', {action: '/handheld/pick'}, current.item);
    current.input = new Element('input', {name: 'picked'}, current.pick);
    current.save = new Element('button', {type: 'submit'}, current.pick);
    current.scan = new Element('form', {action: '/handheld/scan'}, current.item);
    current.camera = new Element('button', {'data-scanner-start': ''}, current.scan);
    current.weight = new Element('form', {action: '/handheld/weight/preview'}, current.item);
    current.grams = new Element('input', {name: 'actual'}, current.weight);
    current.confirmWeight = new Element('form', {action: '/handheld/weight/confirm'}, current.item);
    current.undo = new Element('form', {action: '/handheld/pick/undo', class: 'handheld-undo', 'data-picking-activity': ''}, current);
    current.undoButton = new Element('button', {type: 'submit'}, current.undo);
    for (const [form, value] of [[current.pick, 'pick-command'], [current.confirmWeight, 'weight-command'], [current.undo, 'undo-command']]) {
      const input = new Element('input', {name: 'command_key', type: 'hidden'}, form); input.value = value;
    }
    current.reportArea = new Element('details', {class: 'handheld-report'}, current.item);
    current.reportSummary = new Element('summary', {}, current.reportArea);
    current.report = new Element('form', {action: '/handheld/report'}, current.reportArea);
    current.reportNote = new Element('textarea', {}, current.report);
    current.substitute = new Element('a', {href: '/handheld/substitutions?line=99'}, current.item);
    current.unrelated = new Element('button', {}, current);
    return current;
  }
  if (!noRoot) install({order, assignment, event, delta, rejected, complete});
  vm.runInNewContext(source, {window, document});
  function fire(type, target = current, detail = {}, extra = {}) {
    const event = {type, target, detail, isTrusted: true, prevented: false, defaultPrevented: false, preventDefault() { this.prevented = true; this.defaultPrevented = true; }, ...extra};
    for (const fn of listeners.get(type) || []) fn(event);
    return event;
  }
  function winFire(type, extra = {}) { for (const fn of windowListeners.get(type) || []) fn({type, ...extra}); }
  function advance(ms, fireTimers = true) {
    const until = clock.now + ms;
    if (fireTimers) {
      for (;;) {
        const next = [...timers.entries()].filter(([, timer]) => timer.due <= until).sort((a, b) => a[1].due - b[1].due)[0];
        if (!next) break;
        clock.now = Math.max(clock.now, next[1].due); timers.delete(next[0]); next[1].fn();
      }
    }
    clock.now = until;
  }
  function swap(options = {}) { install(options); fire('htmx:afterSwap'); return current; }
  function activity(detail = {}) { fire('handheld:picking-activity', document, detail); }
  function snapshot(order = '42', assignment = '7:1') { return JSON.parse(storage.values.get(storageKey(order, assignment))); }
  function display(name) { return current.displays[name].textContent; }
  function boot() { fire('DOMContentLoaded'); winFire('pageshow'); }
  return {document, window, storage, clock, timers, install, swap, fire, winFire, advance, activity, snapshot, display, boot,
    remove() { current = null; fire('htmx:afterSwap'); }, get root() { return current; }};
}

test('no task or incomplete task identity has no timer or storage writes', () => {
  for (const options of [{noRoot: true}, {order: ''}, {assignment: ''}, {assignment: 'invalid identity with spaces'}]) {
    const h = harness(options); h.boot(); h.activity(); h.advance(600000);
    assert.equal(h.timers.size, 0); assert.equal(h.storage.values.size, 0);
  }
});

test('task load alone does not start time or derive a count from existing progress', () => {
  const h = harness(); h.root.dataset.pickedLines = '30'; h.boot(); h.advance(60000);
  assert.equal(h.display('active'), '00:00'); assert.equal(h.display('net'), '0'); assert.equal(h.display('rate'), '—');
  assert.equal(h.timers.size, 0); assert.equal(h.snapshot().elapsedMs, 0);
});

test('first response event is recorded once and rate waits for meaningful active time', () => {
  const h = harness({event: 'first-confirmation', delta: '1'}); h.boot();
  assert.equal(h.display('net'), '1'); assert.equal(h.display('active'), '00:00'); assert.equal(h.display('rate'), '—');
  assert.equal(h.root.dataset.pickEvent, undefined); assert.equal(h.root.dataset.pickLineDelta, undefined);
  h.advance(1000); assert.equal(h.display('rate'), '60.0');
  h.fire('htmx:afterSwap'); h.winFire('pageshow'); assert.equal(h.display('net'), '1');
  h.advance(59000); assert.equal(h.display('active'), '01:00'); assert.equal(h.display('rate'), '1.0'); assert.equal(h.timers.size, 0);
});

test('counted and weighed confirmations both count product lines, not units or grams', () => {
  const h = harness(); h.boot(); h.fire('click', h.root.open); h.advance(10000);
  h.swap({event: 'counted-five-units', delta: '1'}); h.advance(5000);
  h.fire('input', h.root.grams); h.advance(5000); h.swap({event: 'weight-527-grams', delta: '1'});
  assert.equal(h.display('net'), '2'); assert.equal(h.display('active'), '00:20'); assert.equal(h.display('rate'), '6.0');
  assert.deepEqual(h.snapshot().seen, ['counted-five-units', 'weight-527-grams']);
});

test('partial/no-change confirmations add zero and a server-confirmed undo subtracts once', () => {
  const h = harness(); h.boot(); h.activity(); h.advance(5000);
  h.swap({event: 'partial', delta: '0'}); assert.equal(h.display('net'), '0');
  h.swap({event: 'complete', delta: '1'}); h.swap({event: 'undo', delta: '-1'});
  h.swap({event: 'undo', delta: '-1'}); assert.equal(h.display('net'), '0');
  h.swap({event: 'undo-preexisting-line', delta: '-1'}); assert.equal(h.display('net'), '-1'); assert.equal(h.display('rate'), '-12.0');
});

test('server re-render, task refresh, errors and client activity cannot fabricate completions', () => {
  const h = harness(); h.boot(); h.activity({delta: 99, net: 99, event: 'invented'}); h.advance(3000);
  for (const options of [{}, {event: 'bad', delta: '2'}, {event: 'bad', delta: '01'}, {event: 'bad', delta: '1.0'}, {event: 'bad', delta: ''}, {event: '', delta: '1'}, {event: 'a'.repeat(101), delta: '1'}]) h.swap(options);
  h.root.dataset.pickedLines = '70'; h.fire('htmx:afterSwap');
  assert.equal(h.display('net'), '0'); assert.deepEqual(h.snapshot().seen, []);
});

test('activity renewals account for all elapsed time between events without double counting', () => {
  const h = harness(); h.boot(); h.activity(); h.advance(9500, false); h.activity();
  assert.equal(h.snapshot().elapsedMs, 9500);
  h.advance(25500, false); h.activity(); assert.equal(h.snapshot().elapsedMs, 35000);
  h.advance(2000); assert.equal(h.snapshot().elapsedMs, 37000); assert.equal(h.display('active'), '00:37');
});

test('a delayed callback or new activity after idle never adds the idle gap', () => {
  const h = harness(); h.boot(); h.activity(); h.advance(10 * 60000, false); h.activity();
  assert.equal(h.snapshot().elapsedMs, 60000); h.advance(1000); assert.equal(h.snapshot().elapsedMs, 61000);
  h.advance(10 * 60000, false); h.fire('htmx:afterSwap');
  assert.equal(h.snapshot().elapsedMs, 120000); assert.equal(h.timers.size, 0);
});

test('hidden time and foreground return are excluded until a fresh picking interaction', () => {
  const h = harness(); h.boot(); h.activity(); h.advance(3500, false);
  h.document.hidden = true; h.fire('visibilitychange'); assert.equal(h.snapshot().elapsedMs, 3500); assert.equal(h.timers.size, 0);
  h.advance(15 * 60000); h.activity(); assert.equal(h.timers.size, 0);
  h.document.hidden = false; h.fire('visibilitychange'); h.advance(5000);
  assert.equal(h.snapshot().elapsedMs, 3500); h.activity(); h.advance(2500); h.winFire('pagehide');
  assert.equal(h.snapshot().elapsedMs, 6000);
});

test('a hidden first confirmation is countable but starts no hidden-time interval', () => {
  const h = harness({hidden: true, event: 'hidden-confirm', delta: '1'}); h.boot(); h.advance(60000);
  assert.equal(h.display('net'), '1'); assert.equal(h.display('rate'), '—'); assert.equal(h.snapshot().elapsedMs, 0); assert.equal(h.timers.size, 0);
});

test('reload persists active duration and seen events without counting request/reload downtime', () => {
  const storage = store(), first = harness({storage}); first.boot(); first.activity(); first.advance(7000); first.swap({event: 'one', delta: '1'}); first.winFire('pagehide');
  const next = harness({storage, event: 'one', delta: '1', navType: 'reload', start: 900000}); next.boot(); next.advance(10000);
  assert.equal(next.display('net'), '1'); assert.equal(next.display('active'), '00:07'); assert.equal(next.timers.size, 0);
  next.activity(); next.advance(1000); assert.equal(next.snapshot().elapsedMs, 8000);
});

test('BFCache restoration adopts newer same-tab metrics, never overwrites them with an older snapshot', () => {
  const storage = store(), old = harness({storage}); old.boot(); old.activity(); old.advance(10000); old.swap({event: 'first', delta: '1'}); old.winFire('pagehide');
  const newer = harness({storage}); newer.boot(); newer.activity(); newer.advance(5000); newer.swap({event: 'second', delta: '1'}); newer.winFire('pagehide');
  old.advance(500000); old.winFire('pageshow', {persisted: true});
  assert.equal(old.display('net'), '2'); assert.equal(old.snapshot().elapsedMs, 15000); assert.equal(old.timers.size, 0);
  old.fire('htmx:historyRestore'); assert.equal(old.display('net'), '2');
});

test('unseen HTML events from reload or history restoration fail closed, including after lost storage', () => {
  for (const navType of ['reload', 'back_forward']) {
    const h = harness({navType, event: 'old-confirmation', delta: '1'}); h.boot();
    assert.equal(h.display('net'), '—'); assert.equal(h.display('rate'), '—'); assert.equal(h.snapshot().net, 0);
    assert.match(h.display('status'), /restored response/); assert.equal(h.timers.size, 0);
  }
  const h = harness(); h.boot(); h.activity(); h.advance(4000); h.winFire('pagehide');
  h.root.dataset.pickEvent = 'unseen-bfcache'; h.root.dataset.pickLineDelta = '1'; h.winFire('pageshow', {persisted: true});
  assert.equal(h.display('net'), '—'); assert.equal(h.snapshot().net, 0);
});

test('storage getter/read/write failures display unknown metrics and never retry an increment', () => {
  for (const failure of ['failGetter', 'failRead', 'failWrite']) {
    const storage = store(); storage[failure] = true;
    const h = harness({storage, event: 'unsafe-first', delta: '1'}); h.boot(); h.advance(5000);
    assert.equal(h.display('net'), '—', failure); assert.equal(h.display('rate'), '—');
    storage[failure] = false; h.swap({event: 'unsafe-first', delta: '1'}); h.activity(); h.advance(1000);
    assert.equal(h.display('net'), '—'); assert.match(h.display('status'), /storage is unavailable/);
  }
});

test('a later write failure cannot inflate a stale persisted total on reload', () => {
  const storage = store(), first = harness({storage}); first.boot(); first.activity(); first.advance(2000); first.swap({event: 'first', delta: '1'});
  storage.failWrite = true; first.swap({event: 'second', delta: '1'}); assert.equal(first.display('net'), '—');
  storage.failWrite = false;
  const next = harness({storage, navType: 'reload', event: 'second', delta: '1'}); next.boot();
  assert.equal(next.display('net'), '—'); assert.equal(next.snapshot().net, 1); assert.deepEqual(next.snapshot().seen, ['first']);
});

test('corrupt, oversized and invalid persisted states are never trusted', () => {
  const base = {v: 1, elapsedMs: 1200, net: 1, seen: ['one'], pending: [], paused: false, known: true, reason: ''};
  for (const raw of ['{invalid', 'x'.repeat(64001), JSON.stringify({...base, elapsedMs: -1}), JSON.stringify({...base, net: 12}), JSON.stringify({...base, seen: ['one', 'one']}), JSON.stringify({...base, v: 2})]) {
    const storage = store(); storage.values.set(storageKey(), raw);
    const h = harness({storage, event: 'two', delta: '1'}); h.boot();
    assert.equal(h.display('net'), '—'); assert.equal(h.display('rate'), '—');
  }
});

test('bounded de-duplication never evicts old receipts and never counts overflow as fresh', () => {
  const h = harness(); h.boot(); h.activity(); h.advance(1000);
  for (let i = 0; i < 512; i++) h.swap({event: 'event-' + i, delta: '1'});
  assert.equal(h.display('net'), '512'); assert.equal(h.snapshot().seen.length, 512);
  h.swap({event: 'event-0', delta: '1'}); assert.equal(h.display('net'), '512');
  h.swap({event: 'overflow', delta: '1'}); assert.equal(h.display('net'), '—'); assert.match(h.display('status'), /event limit/);
  assert.equal(h.snapshot().seen.length, 512); assert.equal(h.snapshot().net, 512);
  h.swap({event: 'event-0', delta: '1'}); assert.equal(h.display('net'), '—');
});

test('order and assignment version isolate tasks; no secret or draft is stored', () => {
  const h = harness(); h.root.dataset.handheldToken = 'private-token'; h.root.input.value = 'private draft'; h.boot(); h.activity(); h.advance(3000); h.swap({event: 'one', delta: '1'});
  h.swap({assignment: '7:2'}); assert.equal(h.display('net'), '0'); assert.equal(h.display('active'), '00:00'); assert.equal(h.timers.size, 0);
  h.activity(); h.advance(2000); h.swap({order: '43', assignment: '7:2'}); assert.equal(h.display('net'), '0');
  h.swap(); assert.equal(h.display('net'), '1'); assert.equal(h.display('active'), '00:03'); assert.equal(h.timers.size, 0);
  assert.doesNotMatch([...h.storage.values].flat().join(' '), /private-token|private draft|handheldToken/);
});

test('manual pause/resume is explicit, persists across navigation and cannot be overridden by events', () => {
  const storage = store(), h = harness({storage}); h.boot(); h.activity(); h.advance(2300, false);
  assert.equal(h.fire('click', h.root.displays.pause).prevented, true);
  assert.equal(h.snapshot().elapsedMs, 2300); assert.equal(h.root.displays.pause.attrs['aria-pressed'], 'true');
  h.advance(600000); h.activity(); h.swap({event: 'confirmation-while-paused', delta: '1'});
  assert.equal(h.display('net'), '1'); assert.equal(h.snapshot().elapsedMs, 2300); assert.equal(h.timers.size, 0);
  h.winFire('pagehide'); const next = harness({storage}); next.boot(); assert.equal(next.snapshot().paused, true);
  next.fire('click', next.root.displays.pause); next.advance(1000);
  assert.equal(next.snapshot().elapsedMs, 3300); assert.equal(next.root.displays.pause.attrs['aria-pressed'], 'false');
});

test('only scoped picking controls start or renew the clock', () => {
  const h = harness(); h.boot();
  h.fire('click', h.root.unrelated); h.fire('click', h.root.reportSummary); h.fire('input', h.root.reportNote); h.fire('click', h.root.substitute);
  h.fire('click', h.root.item); h.fire('keydown', h.root.input, {}, {key: 'Tab'}); h.fire('click', h.root.open, {}, {isTrusted: false});
  assert.equal(h.timers.size, 0);
  for (const [type, select] of [['click', r => r.open], ['input', r => r.input], ['change', r => r.grams], ['click', r => r.camera], ['submit', r => r.confirmWeight]]) {
    h.fire(type, select(h.root)); h.advance(1000); assert.equal(h.timers.size, 1);
    h.fire('click', h.root.reportSummary); assert.equal(h.timers.size, 0);
  }
  assert.equal(h.snapshot().elapsedMs, 5000); assert.equal(h.snapshot().net, 0);
});

test('reporting and substitution interactions stop picking time and never count as picking', () => {
  for (const select of [r => r.reportNote, r => r.reportSummary, r => r.substitute]) {
    const h = harness(); h.boot(); h.activity(); h.advance(1250, false); h.fire('click', select(h.root)); h.advance(10000);
    assert.equal(h.snapshot().elapsedMs, 1250); assert.equal(h.timers.size, 0);
  }
});

test('activity integration accepts only the current task identity and never changes progress', () => {
  const h = harness(); h.boot(); h.activity({order: 'wrong'}); h.activity({assignment: '7:2'}); assert.equal(h.timers.size, 0);
  h.activity({order: 42, assignment: '7:1', delta: 10}); h.advance(1000);
  assert.equal(h.snapshot().elapsedMs, 1000); assert.equal(h.snapshot().net, 0);
});

test('task removal, pagehide and revocation clear timers without resubmitting anything', () => {
  const h = harness(); h.boot(); h.activity(); h.advance(1234, false); h.remove();
  assert.equal(h.timers.size, 0); assert.equal(h.snapshot().elapsedMs, 1234); h.advance(100000); h.activity(); assert.equal(h.timers.size, 0);
  h.swap(); h.activity(); h.advance(1000); h.root.dataset.handheldRevoked = 'true'; h.fire('handheld:revoked'); h.activity(); assert.equal(h.timers.size, 0);
  h.swap(); h.activity(); h.advance(1000); h.winFire('pagehide'); h.advance(600000); h.activity(); assert.equal(h.timers.size, 0);
  assert.doesNotMatch(source, /fetch\s*\(|XMLHttpRequest|sendBeacon|localStorage|requestSubmit\s*\(|\.submit\s*\(|Date\.now/);
});

test('late callbacks do not start or continue metrics for a detached workspace', () => {
  const h = harness(); h.boot(); h.activity(); const callback = [...h.timers.values()][0].fn;
  h.remove(); callback(); assert.equal(h.timers.size, 0);
  h.swap({order: '43'}); callback(); assert.equal(h.timers.size, 0); assert.equal(h.snapshot('43').elapsedMs, 0);
});

test('optional displays may be omitted and the script can mount after DOMContentLoaded', () => {
  const h = harness({readyState: 'complete'}); h.root.children = [h.root.item, h.root.list]; h.activity(); h.advance(1000); h.swap({event: 'one', delta: '1'});
  assert.equal(h.snapshot().net, 1); assert.equal(h.snapshot().elapsedMs, 1000);
});

test('explicit undo activity works above the item but report markers never bypass exclusions', () => {
  const h = harness(); h.boot(); h.fire('click', h.root.undoButton); h.advance(1000);
  assert.equal(h.snapshot().elapsedMs, 1000); assert.equal(h.timers.size, 1);
  h.root.report.dataset.pickingActivity = ''; h.fire('input', h.root.reportNote); h.advance(1000);
  assert.equal(h.snapshot().elapsedMs, 1000); assert.equal(h.timers.size, 0);
});

test('a zero-delta server confirmation renews active time without adding a completed line', () => {
  const h = harness(); h.boot(); h.activity(); h.advance(59000); h.swap({event: 'partial', delta: '0'}); h.advance(59000);
  assert.equal(h.snapshot().elapsedMs, 118000); assert.equal(h.snapshot().net, 0); assert.equal(h.timers.size, 1);
  h.advance(1000); assert.equal(h.timers.size, 0);
});

test('every ambiguous scoped mutation transport failure makes attribution unavailable', () => {
  for (const type of ['htmx:sendError', 'htmx:sendAbort', 'htmx:timeout', 'htmx:responseError']) {
    for (const formName of ['pick', 'confirmWeight', 'undo']) {
      const h = harness(); h.boot(); h.activity(); h.advance(1000);
      const xhr = {}; h.fire('htmx:beforeRequest', h.root[formName], {elt: h.root[formName], xhr});
      assert.equal(h.snapshot().pending.length, 1);
      h.fire(type, h.root[formName], {xhr}); assert.equal(h.display('net'), '—'); assert.equal(h.display('rate'), '—');
      assert.match(h.display('status'), /could not be matched/);
      h.advance(1000); assert.equal(h.snapshot().elapsedMs, 2000);
      h.swap(); assert.equal(h.display('net'), '—');
    }
  }
});

test('scan/preview/report errors and unrelated requests do not taint confirmed picking attribution', () => {
  const h = harness(); h.boot(); h.activity(); h.advance(1000); h.swap({event: 'one', delta: '1'});
  for (const formName of ['scan', 'weight', 'report']) {
    const xhr = {}; h.fire('htmx:beforeRequest', h.root[formName], {elt: h.root[formName], xhr}); h.fire('htmx:sendError', h.root[formName], {xhr});
  }
  h.fire('htmx:sendError', h.root, {xhr: {}});
  assert.equal(h.display('net'), '1'); assert.equal(h.snapshot().pending.length, 0);
});

test('a suppressed response from an older mutation taints its task even after navigation', () => {
  const h = harness(); h.boot(); const xhr = {};
  h.fire('htmx:beforeRequest', h.root.pick, {elt: h.root.pick, xhr}); h.swap({order: '43'});
  h.fire('htmx:beforeOnLoad', h.root, {xhr}, {defaultPrevented: true});
  assert.equal(h.display('net'), '0'); assert.equal(h.snapshot().known, false);
  h.swap(); assert.equal(h.display('net'), '—');
});

test('a confirmed command clears the matching pending receipt before a native-page reload', () => {
  const storage = store(), first = harness({storage}); first.boot(); first.activity(); first.advance(3200, false);
  first.fire('submit', first.root.pick); assert.deepEqual(first.snapshot().pending, ['pick-command']); first.winFire('pagehide');
  const next = harness({storage, event: 'pick-command', delta: '1', start: 10000}); next.boot();
  assert.equal(next.display('net'), '1'); assert.equal(next.snapshot().elapsedMs, 3200); assert.deepEqual(next.snapshot().pending, []);
  next.winFire('pagehide'); const reload = harness({storage, navType: 'reload'}); reload.boot();
  assert.equal(reload.display('net'), '1'); assert.equal(reload.snapshot().known, true);
});

test('a missing native POST confirmation or replay response does not silently become a zero rate', () => {
  for (const navType of ['navigate', 'reload', 'back_forward']) {
    const storage = store(), first = harness({storage}); first.boot(); first.fire('submit', first.root.undo); first.winFire('pagehide');
    const next = harness({storage, navType}); next.boot(); assert.equal(next.display('net'), '—'); assert.equal(next.snapshot().known, false);
    assert.deepEqual(next.snapshot().pending, []); assert.match(next.display('status'), /could not be matched/);
  }
});

test('an explicitly prevented submit creates no uncertain pending mutation', () => {
  const h = harness(); h.boot(); h.fire('submit', h.root.pick, {}, {defaultPrevented: true});
  assert.deepEqual(h.snapshot().pending, []); h.swap(); assert.equal(h.display('net'), '0');
});

test('the explicit uncertainty event is task-scoped and has no mutation side effects', () => {
  const h = harness(); h.boot(); h.activity(); h.advance(1000);
  h.fire('handheld:picking-uncertain', h.document, {order: '43', assignment: '8:1'});
  assert.equal(h.display('net'), '0'); assert.equal(h.snapshot('43', '8:1').known, false);
  h.fire('handheld:picking-uncertain', h.document); assert.equal(h.display('net'), '—');
  assert.equal(h.snapshot().elapsedMs, 1000);
});

test('late cancelled timer callbacks cannot replace the new task timer', () => {
  const h = harness(); h.boot(); h.activity(); const old = [...h.timers.values()][0].fn;
  h.swap({order: '43'}); h.activity(); const activeTimer = [...h.timers.keys()][0]; old();
  assert.deepEqual([...h.timers.keys()], [activeTimer]); h.advance(1000); assert.equal(h.snapshot('43').elapsedMs, 1000);
});

test('fresh authoritative rejection clears its pending command without changing known attribution', () => {
  for (const formName of ['pick', 'confirmWeight', 'undo']) {
    const h = harness(); h.boot(); h.activity(); h.advance(1000); h.swap({event: 'prior-save', delta: '1'});
    const form = h.root[formName], command = form.querySelector('input[name="command_key"]').value;
    h.fire('submit', form); assert.deepEqual(h.snapshot().pending, [command]);
    h.swap({rejected: command});
    assert.equal(h.display('net'), '1'); assert.equal(h.snapshot().known, true); assert.deepEqual(h.snapshot().pending, []);
    assert.deepEqual(h.snapshot().seen, ['prior-save']); assert.equal(h.root.dataset.pickRejected, undefined);
  }
});

test('native POST validation rejection stays exact, but mismatched or restored refusals cannot settle another attempt', () => {
  const storage = store(), first = harness({storage}); first.boot(); first.fire('submit', first.root.pick); first.winFire('pagehide');
  const next = harness({storage, rejected: 'pick-command'}); next.boot();
  assert.equal(next.display('net'), '0'); assert.deepEqual(next.snapshot().pending, []);
  next.fire('submit', next.root.pick); next.swap({rejected: 'other-command'}); assert.equal(next.display('net'), '—');
  const restoredStorage = store(), original = harness({storage: restoredStorage}); original.boot(); original.fire('submit', original.root.pick); original.winFire('pagehide');
  const restored = harness({storage: restoredStorage, navType: 'back_forward', rejected: 'pick-command'}); restored.boot();
  assert.equal(restored.display('net'), '—');
});

test('definite rejection does not erase uncertainty from an earlier unconfirmed mutation', () => {
  const h = harness(); h.boot(); const xhr = {}; h.fire('htmx:beforeRequest', h.root.pick, {elt: h.root.pick, xhr}); h.fire('htmx:sendError', h.root, {xhr});
  h.swap({rejected: 'pick-command'}); assert.equal(h.display('net'), '—'); assert.equal(h.snapshot().known, false);
});

test('the final confirmed product line stops active time immediately without an idle tail', () => {
  const h = harness(); h.boot(); h.activity(); h.advance(2500, false); h.swap({event: 'final-line', delta: '1', complete: true});
  assert.equal(h.snapshot().net, 1); assert.equal(h.snapshot().elapsedMs, 2500); assert.equal(h.timers.size, 0);
  h.advance(600000); assert.equal(h.snapshot().elapsedMs, 2500); assert.equal(h.display('rate'), '24.0');
  h.fire('htmx:afterSwap'); assert.equal(h.timers.size, 0);
});

test('completed task entry counts a genuine event with no fabricated time or interval', () => {
  const h = harness({event: 'final-line', delta: '1', complete: true}); h.boot(); h.advance(100000);
  assert.equal(h.display('net'), '1'); assert.equal(h.display('active'), '00:00'); assert.equal(h.display('rate'), '—'); assert.equal(h.timers.size, 0);
});

test('explicit undo/correction can start activity on a completed view, and its response governs the next interval', () => {
  const h = harness({complete: true}); h.boot(); h.fire('click', h.root.undoButton); h.advance(2000);
  assert.equal(h.snapshot().elapsedMs, 2000); assert.equal(h.timers.size, 1);
  h.swap({event: 'undo-command', delta: '-1'}); assert.equal(h.display('net'), '-1'); h.advance(1000); assert.equal(h.snapshot().elapsedMs, 3000);
  h.swap({event: 'refinish', delta: '1', complete: true}); assert.equal(h.timers.size, 0);
  h.fire('input', h.root.grams); h.advance(1000); h.swap({event: 'correct-weight', delta: '0', complete: true});
  assert.equal(h.snapshot().elapsedMs, 4000); assert.equal(h.display('net'), '0'); assert.equal(h.timers.size, 0);
});
