const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('web/static/app.js', 'utf8');

function harness() {
  const listeners = new Map(), ids = new Map();
  const document = {
    body: {}, activeElement: null, basket: null,
    addEventListener(name, fn) { if (!listeners.has(name)) listeners.set(name, []); listeners.get(name).push(fn); },
    getElementById(id) { return ids.get(id) || null; },
    querySelector(selector) { return selector === 'form[data-customer-basket]' ? this.basket : null; },
    querySelectorAll() { return []; },
    createElement() { return node(); }
  };
  function node(id = '') {
    return { id, dataset: {}, children: [], attrs: {}, hidden: false, disabled: false, textContent: '',
      setAttribute(k, v) { this.attrs[k] = v; }, getAttribute(k) { return this.attrs[k]; },
      appendChild(n) { this.children.push(n); },
      querySelector(q) { return q === ':scope > .action-feedback' ? this.children.find(n => n.className?.includes('action-feedback')) : null; },
      closest() { return null; }, focus() { document.activeElement = this; }, scrollIntoView() {},
      matches(selector) { return selector === '[data-cart-quantity]' && this.quantity; }
    };
  }
  function install({key = 'basket-a', revision = '3', note = '', first = '1', second = '2', savedFirst = first, savedSecond = second} = {}) {
    ids.clear();
    const form = node('customer-basket'); form.tagName = 'FORM';
    form.dataset = {customerBasket: key, basketRevision: revision};
    const instructions = node('instructions'); instructions.value = note;
    instructions.setSelectionRange = function(start, end) { this.selectionStart = start; this.selectionEnd = end; };
    const inputs = [first, second].map((value, i) => {
      const input = node(`quantity-${i + 1}`); input.quantity = true; input.value = value;
      input.dataset.savedQuantity = [savedFirst, savedSecond][i]; input.attrs['aria-describedby'] = `quantity-draft-${i + 1}`;
      ids.set(input.id, input); ids.set(input.attrs['aria-describedby'], node(input.attrs['aria-describedby'])); return input;
    });
    const button = node('update-1'); button.clicks = 0; button.click = () => button.clicks++;
    button.closest = selector => selector === 'form' ? form : null;
    form.contains = element => element === instructions || element === button || inputs.includes(element);
    form.querySelectorAll = selector => selector === '[data-cart-quantity]' ? inputs : [];
    const originalQuery = form.querySelector.bind(form);
    form.querySelector = selector => selector === '[name="instructions"]' ? instructions : selector === 'button[id]' ? button : originalQuery(selector);
    ids.set(form.id, form); ids.set(button.id, button); ids.set(instructions.id, instructions);
    document.basket = form; document.activeElement = document.body;
    return {form, inputs, instructions, button};
  }
  function fire(name, detail = {}, target, key) {
    const event = {detail, target, key, prevented: false, preventDefault() { this.prevented = true; }};
    for (const fn of listeners.get(name) || []) fn(event);
    return event;
  }
  vm.runInNewContext(source, {document, WeakMap, Number});
  function begin(view) {
    const xhr = {getResponseHeader() { return null; }};
    fire('htmx:beforeRequest', {xhr, elt: view.form, requestConfig: {triggeringEvent: {submitter: view.button}}});
    return xhr;
  }
  function beforeSwap(xhr) { return fire('htmx:beforeSwap', {xhr, target: {id:'workspace'}, shouldSwap:true}).detail; }
  function afterSwap(xhr) { fire('htmx:afterSwap', {xhr, target:{id:'workspace'}}); }
  return {document, ids, install, fire, begin, beforeSwap, afterSwap};
}

test('quantity response keeps a typed note and other unsaved quantities without submitting again', () => {
  const h = harness(), old = h.install({note:'Demo bread on top', first:'3', savedFirst:'1', second:'4', savedSecond:'2'});
  const xhr = h.begin(old); h.beforeSwap(xhr);
  const next = h.install({revision:'4', note:'Demo bread on top', first:'3', second:'4', savedSecond:'2'}); h.afterSwap(xhr);
  assert.equal(next.instructions.value, 'Demo bread on top'); assert.equal(next.inputs[1].value, '4');
  assert.equal(h.ids.get('quantity-draft-1').hidden, true); assert.equal(h.ids.get('quantity-draft-2').hidden, false);
  assert.equal(next.button.clicks, 0);
});

test('edits and textarea caret made during a request win over its older snapshot', () => {
  const h = harness(), old = h.install({note:'Before', first:'3', savedFirst:'1'}), xhr = h.begin(old);
  old.instructions.value = 'Edited while waiting'; old.instructions.selectionStart = 7; old.instructions.selectionEnd = 11;
  old.inputs[0].value = '5'; old.inputs[1].value = '';
  h.document.activeElement = old.instructions; h.beforeSwap(xhr);
  const next = h.install({revision:'4', note:'Before', first:'3'}); h.afterSwap(xhr);
  assert.equal(next.instructions.value, 'Edited while waiting'); assert.equal(next.inputs[0].value, '5'); assert.equal(next.inputs[1].value, '');
  assert.equal(h.document.activeElement, next.instructions); assert.equal(next.instructions.selectionStart, 7); assert.equal(next.instructions.selectionEnd, 11);
  assert.equal(h.ids.get('quantity-draft-1').hidden, false); assert.equal(next.button.clicks, 0);
});

test('explicitly clearing notes while waiting is preserved', () => {
  const h = harness(), old = h.install({note:'Discard this note'}), xhr = h.begin(old);
  old.instructions.value = ''; h.beforeSwap(xhr);
  const next = h.install({revision:'4', note:'Discard this note'}); h.afterSwap(xhr);
  assert.equal(next.instructions.value, '');
});

test('failed or expired requests keep entered values without mutation replay', () => {
  for (const event of ['htmx:sendError','htmx:responseError']) {
    const h = harness(), old = h.install({note:'Still here', first:'6', savedFirst:'1'}), xhr = h.begin(old);
    h.fire(event, {xhr});
    assert.equal(old.instructions.value, 'Still here'); assert.equal(old.inputs[0].value, '6'); assert.equal(old.button.clicks, 0);
  }
});

test('renewal and review swaps recover new input, but checkout and reset generations cannot inherit it', () => {
  for (const key of ['basket-b', 'reset-session']) {
    const h = harness(), old = h.install({note:'Old draft'}), xhr = h.begin(old); h.beforeSwap(xhr);
    const next = h.install({key, revision:'1'}); h.afterSwap(xhr);
    assert.equal(next.instructions.value, ''); assert.equal(next.inputs[0].value, '1');
  }
  const h = harness(), old = h.install({note:'Placed note'}), xhr = h.begin(old); h.beforeSwap(xhr);
  h.document.basket = null; h.afterSwap(xhr);
  const next = h.install({key:'after-checkout'}); h.fire('DOMContentLoaded'); assert.equal(next.instructions.value, '');
});

test('late responses cannot replace a newer basket revision, session or navigation', () => {
  for (const replacement of [{revision:'4'}, {key:'new-session'}, null]) {
    const h = harness(), old = h.install({note:'Stale'}), xhr = h.begin(old);
    if (replacement) h.install(replacement); else h.document.basket = null;
    assert.equal(h.beforeSwap(xhr).shouldSwap, false);
  }
});

test('rapid responses preserve latest typing and ignore an obsolete response', () => {
  const h = harness(), old = h.install({note:'First'}), first = h.begin(old), second = h.begin(old);
  old.instructions.value = 'Latest'; h.beforeSwap(second);
  const next = h.install({revision:'4', note:'First'}); h.afterSwap(second);
  assert.equal(next.instructions.value, 'Latest'); assert.equal(h.beforeSwap(first).shouldSwap, false);
});

test('Enter in a quantity updates only its row and never submits disabled controls', () => {
  const h = harness(), old = h.install();
  assert.equal(h.fire('keydown', {}, old.inputs[0], 'Enter').prevented, true); assert.equal(old.button.clicks, 1);
  old.button.disabled = true; h.fire('keydown', {}, old.inputs[0], 'Enter'); assert.equal(old.button.clicks, 1);
  assert.equal(h.fire('keydown', {}, old.instructions, 'Enter').prevented, false);
});

test('server-bounded instructions are not overwritten with an unchanged oversized draft', () => {
  const h = harness(), old = h.install({note:'x'.repeat(501)}), xhr = h.begin(old); h.beforeSwap(xhr);
  const next = h.install({revision:'4', note:'x'.repeat(500)}); h.afterSwap(xhr);
  assert.equal(next.instructions.value.length, 500);
});

test('notes edited during a request stay bounded and plaintext', () => {
  const h = harness(), old = h.install({note:'Before'}), xhr = h.begin(old);
  old.instructions.value = '\u0000<script>fake</script>\n' + '界'.repeat(510); h.beforeSwap(xhr);
  const next = h.install({revision:'4', note:'Before'}); h.afterSwap(xhr);
  assert.equal(Array.from(next.instructions.value).length, 500);
  assert.ok(next.instructions.value.startsWith('�<script>fake</script>\n'));
  assert.equal(next.instructions.children.length, 0);
});
