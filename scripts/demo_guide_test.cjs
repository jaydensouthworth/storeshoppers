// Dependency-free lifecycle and placement checks. The fake DOM supplies sizes;
// rendered-browser checks remain necessary for actual type and CSS geometry.
const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const source = fs.readFileSync(path.join(__dirname, "../web/static/demo_guide.js"), "utf8");
const key = "neighborhood-market-demo-guide";

function rect(left, top, width = 90, height = 44) {
  return { left, top, width, height, right: left + width, bottom: top + height };
}

function run({ width = 1280, height = 800, anchor = rect(1030, 65), boxHeight = 70,
  ready = "complete", eligible = true, local = new Map(), session = new Map(),
  blockedLocalRead = false, blockedLocalWrite = false,
  blockedSessionRead = false, blockedSessionWrite = false, visual, basket } = {}) {
  const documentEvents = new Map();
  const windowEvents = new Map();
  const visualEvents = new Map();
  const frames = [];
  const focuses = [];
  const observers = [];
  const failures = { blockedLocalRead, blockedLocalWrite, blockedSessionRead, blockedSessionWrite };
  const reads = { local: 0, session: 0 };
  let measurements = 0;
  let targetRect = anchor;
  let controls;
  let scrollCalls = 0;
  let prevented = 0;
  function listen(events, name, callback) {
    if (!events.has(name)) events.set(name, []);
    events.get(name).push(callback);
  }
  function emit(events, name, event = {}) {
    events.get(name)?.forEach((callback) => callback(event));
  }
  function node(selector) {
    return {
      hidden: true,
      style: { setProperty(name, value) { this[name] = value; } },
      attrs: new Map(),
      closest: (query) => query === selector ? controls[selector] : null,
      focus: (options) => {
        focuses.push({ selector, options });
        document.activeElement = controls[selector];
      },
      setAttribute(name, value) { this.attrs.set(name, value); },
    };
  }
  function replace(isEligible = true) {
    controls = {};
    const target = node("[data-demo-guide-target]");
    target.getBoundingClientRect = () => targetRect;
    target.scrollIntoView = () => {
      scrollCalls++;
      targetRect = rect(targetRect.left, (window.innerHeight - targetRect.height) / 2,
        targetRect.width, targetRect.height);
    };
    controls["[data-demo-guide-target]"] = target;
    if (basket) controls[".phone-basket"] = { getBoundingClientRect: () => basket };
    if (!isEligible) return;
    const guide = node("[data-demo-guide]");
    const close = node("[data-demo-guide-close]");
    guide.contains = (child) => child === close;
    guide.querySelector = (selector) => selector === "[data-demo-guide-close]" ? close : null;
    guide.getBoundingClientRect = () => {
      measurements++;
      return rect(parseFloat(guide.style.left) || 0, parseFloat(guide.style.top) || 0,
        Math.min(250, parseFloat(guide.style.maxWidth) || 250), boxHeight);
    };
    controls["[data-demo-guide]"] = guide;
    controls["[data-demo-guide-close]"] = close;
    controls["[data-demo-guide-reopen]"] = node("[data-demo-guide-reopen]");
  }
  const document = {
    readyState: ready,
    documentElement: { clientWidth: width, clientHeight: height },
    querySelector: (selector) => controls[selector] || null,
    querySelectorAll: (selector) => controls[selector] ? [controls[selector]] : [],
    addEventListener: (name, listener) => listen(documentEvents, name, listener),
  };
  const window = {
    innerWidth: width,
    innerHeight: height,
    addEventListener: (name, listener) => listen(windowEvents, name, listener),
    requestAnimationFrame: (callback) => frames.push(callback),
    ResizeObserver: class {
      constructor(callback) { this.callback = callback; this.elements = []; observers.push(this); }
      observe(element) { this.elements.push(element); }
      disconnect() { this.elements = []; }
    },
  };
  for (const [name, data, prefix] of [["localStorage", local, "Local"], ["sessionStorage", session, "Session"]]) {
    Object.defineProperty(window, name, { get() {
      if (failures[`blocked${prefix}Read`]) throw Error("storage inaccessible");
      return {
        getItem: (entry) => { reads[prefix.toLowerCase()]++; return data.get(entry) ?? null; },
        setItem: (entry, value) => {
          if (failures[`blocked${prefix}Write`]) throw Error("storage full");
          data.set(entry, value);
        },
      };
    } });
  }
  if (visual) window.visualViewport = {
    ...visual, addEventListener: (name, listener) => listen(visualEvents, name, listener),
  };
  replace(eligible);
  const context = vm.createContext({ window, document });
  vm.runInContext(source, context);
  function flush() {
    while (frames.length) frames.shift()();
  }
  flush();
  return {
    window, document, local, session, focuses, failures, observers, reads,
    get guide() { return controls["[data-demo-guide]"]; },
    get reopen() { return controls["[data-demo-guide-reopen]"]; },
    get target() { return controls["[data-demo-guide-target]"]; },
    get frameCount() { return frames.length; },
    get measurements() { return measurements; },
    get scrollCalls() { return scrollCalls; },
    get prevented() { return prevented; },
    get anchor() { return targetRect; },
    setAnchor(value) { targetRect = value; },
    flush,
    ready() { emit(documentEvents, "DOMContentLoaded"); flush(); },
    click(kind) {
      emit(documentEvents, "click", { preventDefault: () => prevented++, target: { closest: (selector) =>
        selector === `[data-demo-guide-${kind}]` ? controls[selector] : null } });
      flush();
    },
    replace,
    swap(isEligible = true) { replace(isEligible); emit(documentEvents, "htmx:afterSwap"); flush(); },
    restore() { replace(true); emit(documentEvents, "htmx:historyRestore"); flush(); },
    pageshow() { emit(windowEvents, "pageshow"); flush(); },
    emitDocument(name, event) { emit(documentEvents, name, event); },
    emitWindow(name, event) { emit(windowEvents, name, event); },
    emitVisual(name) { emit(visualEvents, name); },
    evaluateAgain() { vm.runInContext(source, context); flush(); },
    listenerCounts() {
      return [...documentEvents, ...windowEvents, ...visualEvents]
        .map(([name, callbacks]) => [name, callbacks.length]);
    },
  };
}

function verifyVisiblePlacement(page, view = { left: 0, top: 0, right: page.window.innerWidth,
  bottom: page.window.innerHeight }) {
  const guide = page.guide;
  assert.equal(guide.hidden, false);
  assert.equal(guide.style.visibility, "");
  const box = guide.getBoundingClientRect();
  const anchor = page.anchor;
  assert.ok(box.left >= view.left + 12 && box.right <= view.right - 12, "horizontal bounds");
  assert.ok(box.top >= view.top + 12 && box.bottom <= view.bottom - 12, "vertical bounds");
  assert.ok(box.bottom <= anchor.top - 20 || box.top >= anchor.bottom + 20, "target stays unobscured");
  const arrowX = box.left + parseFloat(guide.style["--demo-guide-arrow-x"]);
  assert.ok(arrowX >= anchor.left && arrowX <= anchor.right, "arrow points inside Employees");
}

test("initial display waits for markup and never takes focus or scrolls", () => {
  const page = run({ ready: "loading" });
  assert.equal(page.guide.hidden, true);
  page.ready();
  verifyVisiblePlacement(page);
  assert.equal(page.guide.attrs.get("data-placement"), "below");
  assert.equal(page.reopen.hidden, false);
  assert.equal(page.focuses.length, 0);
  assert.equal(page.scrollCalls, 0);
});

test("desktop and narrow mobile bounds preserve an arrow pointing at the uncovered target", () => {
  for (const width of [280, 320, 375, 768, 1440]) {
    for (const left of [0, 18, width - 110, width - 90]) {
      const page = run({ width, anchor: rect(left, 120) });
      verifyVisiblePlacement(page);
    }
  }
});

test("placement flips above near the viewport bottom and hides when neither side fits", () => {
  const above = run({ height: 300, anchor: rect(1030, 235) });
  verifyVisiblePlacement(above);
  assert.equal(above.guide.attrs.get("data-placement"), "above");
  const short = run({ height: 130, anchor: rect(1030, 43) });
  assert.equal(short.guide.hidden, true);
  assert.equal(short.reopen.hidden, false);
});

test("mobile placement avoids the fixed basket bar and ignores its hidden desktop rect", () => {
  const page = run({ width: 375, height: 360, anchor: rect(260, 195), basket: rect(0, 298, 375, 62) });
  verifyVisiblePlacement(page, { left: 0, top: 0, right: 375, bottom: 298 });
  assert.equal(page.guide.attrs.get("data-placement"), "above");
  const noRoom = run({ width: 320, height: 250, anchor: rect(220, 90), basket: rect(0, 188, 320, 62) });
  assert.equal(noRoom.guide.hidden, true);
  const desktop = run({ basket: rect(0, 0, 0, 0) });
  verifyVisiblePlacement(desktop);
});

test("scrolling a target off-screen hides the guide and scrolling back reanchors it", () => {
  const page = run();
  for (const anchor of [rect(1030, -1), rect(1030, 780), rect(-1, 70), rect(1220, 70), rect(1030, 70, 0)]) {
    page.setAnchor(anchor);
    page.emitWindow("scroll");
    page.flush();
    assert.equal(page.guide.hidden, true);
  }
  page.setAnchor(rect(1100, 120));
  page.emitWindow("scroll");
  page.flush();
  verifyVisiblePlacement(page);
  assert.equal(page.focuses.length, 0);
});

test("resize, settle and element-size events coalesce and update anchor measurements", () => {
  const page = run();
  const before = page.measurements;
  page.document.documentElement.clientWidth = page.window.innerWidth = 320;
  page.setAnchor(rect(212, 154));
  page.emitWindow("resize");
  page.emitWindow("scroll");
  page.emitDocument("htmx:afterSettle");
  page.observers[0].callback();
  assert.equal(page.frameCount, 1);
  page.flush();
  assert.equal(page.measurements, before + 1);
  verifyVisiblePlacement(page);
});

test("visual viewport zoom and pan offsets are used for clamping and target visibility", () => {
  const page = run({ anchor: rect(180, 110),
    visual: { offsetLeft: 100, offsetTop: 80, width: 320, height: 400 } });
  verifyVisiblePlacement(page, { left: 100, top: 80, right: 420, bottom: 480 });
  page.window.visualViewport.offsetTop = 180;
  page.emitVisual("scroll");
  page.flush();
  assert.equal(page.guide.hidden, true);
});

test("closing saves dismissal across full navigation, swaps and history restoration", () => {
  const page = run();
  page.click("close");
  assert.equal(page.guide.hidden, true);
  assert.equal(page.local.get(key), "dismissed");
  assert.equal(page.session.get(key), "dismissed");
  assert.equal(page.focuses.at(-1).selector, "[data-demo-guide-target]");
  page.swap();
  page.restore();
  page.pageshow();
  assert.equal(page.guide.hidden, true);
  assert.equal(run({ local: page.local }).guide.hidden, true);
});

test("following Employees completes the guide without intercepting link navigation or focus", () => {
  const page = run();
  page.click("target");
  assert.equal(page.local.get(key), "dismissed");
  assert.equal(page.guide.hidden, true);
  assert.equal(page.prevented, 0);
  assert.equal(page.focuses.length, 0);
  page.restore();
  page.pageshow();
  assert.equal(page.guide.hidden, true);
  assert.equal(run({ local: page.local }).guide.hidden, true);
});

test("session storage retains dismissal on full navigation if local storage is blocked or full", () => {
  for (const failure of [{ blockedLocalRead: true }, { blockedLocalWrite: true }]) {
    const page = run(failure);
    verifyVisiblePlacement(page);
    page.click("close");
    const reloaded = run({ ...failure, session: page.session });
    assert.equal(reloaded.guide.hidden, true);
    assert.equal(reloaded.reopen.hidden, false);
  }
});

test("a session-only dismissal migrates into local storage when it becomes writable again", () => {
  const session = new Map([[key, "dismissed"]]);
  const local = new Map([[key, "seen"]]);
  const recovered = run({ local, session });
  assert.equal(recovered.guide.hidden, true);
  assert.equal(local.get(key), "dismissed");
  assert.equal(run({ local, session: new Map() }).guide.hidden, true,
    "dismissal remains after the fallback session ends");
});

test("Escape closes the visible guide without taking focus from an unrelated input", () => {
  const page = run();
  const input = { value: "apples" };
  page.document.activeElement = input;
  page.emitDocument("keydown", { key: "Escape" });
  assert.equal(page.guide.hidden, true);
  assert.equal(page.local.get(key), "dismissed");
  assert.equal(page.document.activeElement, input);
  assert.equal(page.focuses.length, 0);
  assert.equal(page.prevented, 0);
});

test("Escape returns guide focus to Employees and ignores hidden guides or handled keystrokes", () => {
  const page = run();
  page.click("reopen");
  page.emitDocument("keydown", { key: "Escape", defaultPrevented: true });
  assert.equal(page.guide.hidden, false);
  page.emitDocument("keydown", { key: "Enter" });
  assert.equal(page.guide.hidden, false);
  page.emitDocument("keydown", { key: "Escape" });
  assert.equal(page.guide.hidden, true);
  assert.equal(page.focuses.at(-1).selector, "[data-demo-guide-target]");
  assert.equal(page.focuses.length, 2);
  page.emitDocument("keydown", { key: "Escape" });
  assert.equal(page.focuses.length, 2, "Escape on a hidden guide does nothing");
});

test("when both stores fail, repeated documents stay quiet and explicit reopen remains useful", () => {
  for (const failure of [
    { blockedLocalRead: true, blockedSessionRead: true },
    { blockedLocalWrite: true, blockedSessionWrite: true },
  ]) {
    const page = run(failure);
    assert.equal(page.guide.hidden, true);
    assert.equal(page.reopen.hidden, false);
    page.click("reopen");
    verifyVisiblePlacement(page);
    assert.equal(page.focuses.at(-1).selector, "[data-demo-guide-close]");
    page.swap();
    page.pageshow();
    assert.equal(page.guide.hidden, false);
    page.click("close");
    page.restore();
    page.pageshow();
    assert.equal(page.guide.hidden, true);
    assert.equal(run(failure).guide.hidden, true);
  }
});

test("storage becoming unwritable after opening still retains dismissal in the document", () => {
  const page = run();
  page.failures.blockedLocalWrite = page.failures.blockedSessionWrite = true;
  page.click("close");
  page.swap();
  page.restore();
  page.pageshow();
  assert.equal(page.guide.hidden, true);
  assert.equal(page.focuses.length, 1);
});

test("explicit reopen scrolls to the target without clearing a previous saved dismissal", () => {
  const page = run({ local: new Map([[key, "dismissed"]]), anchor: rect(1030, -500) });
  page.click("reopen");
  verifyVisiblePlacement(page);
  assert.equal(page.scrollCalls, 1);
  assert.equal(page.focuses.at(-1).selector, "[data-demo-guide-close]");
  page.pageshow();
  page.restore();
  assert.equal(page.guide.hidden, false);
  assert.equal(run({ local: page.local }).guide.hidden, true);
});

test("eligible fragments regain the guide; unrelated surfaces remove controls and observation", () => {
  const page = run({ eligible: false });
  assert.equal(page.reads.local + page.reads.session, 0, "non-demo surfaces do not initialize a preference");
  page.swap(true);
  verifyVisiblePlacement(page);
  const previousGuide = page.guide;
  page.swap(false);
  assert.equal(page.guide, undefined);
  assert.ok(!page.observers[0].elements.includes(previousGuide));
  page.restore();
  verifyVisiblePlacement(page);
});

test("missing target hides the guide and footer safely", () => {
  const page = run();
  const actualQuery = page.document.querySelector;
  page.document.querySelector = (selector) => selector === "[data-demo-guide-target]" ? null : actualQuery(selector);
  page.emitDocument("htmx:afterSwap");
  page.flush();
  assert.equal(page.guide.hidden, true);
  assert.equal(page.reopen.hidden, true);
});

test("history script re-evaluation installs no duplicate event handlers or observers", () => {
  const page = run();
  const listeners = page.listenerCounts();
  page.click("close");
  page.evaluateAgain();
  page.restore();
  assert.deepEqual(page.listenerCounts(), listeners);
  assert.ok(listeners.every(([, count]) => count === 1));
  assert.equal(page.observers.length, 1);
  assert.equal(page.guide.hidden, true);
});

test("another tab or bfcache restoration picks up dismissal without clearing it on unrelated events", () => {
  const page = run();
  page.emitWindow("storage", { key: "other", newValue: "dismissed" });
  page.flush();
  assert.equal(page.guide.hidden, false);
  page.emitWindow("storage", { key, newValue: "dismissed" });
  page.flush();
  assert.equal(page.guide.hidden, true);
  page.emitWindow("storage", { key: null, newValue: null });
  page.flush();
  assert.equal(page.guide.hidden, true);
  const restored = run();
  restored.local.set(key, "dismissed");
  restored.pageshow();
  assert.equal(restored.guide.hidden, true);
});
