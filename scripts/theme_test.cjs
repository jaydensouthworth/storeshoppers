// Dependency-free interaction checks for the early appearance script. Native
// button keyboard activation and visual layout are also checked in browser QA.
const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const rootPath = path.join(__dirname, "..");
const source = fs.readFileSync(path.join(rootPath, "web/static/theme.js"), "utf8");
const appTemplate = fs.readFileSync(path.join(rootPath, "web/templates/app.html"), "utf8");
const key = "neighborhood-market-theme";

function run({ dark = false, saved = null, blockedRead = false, blockedWrite = false,
  ready = "loading", legacyMedia = false, noMedia = false, storage = new Map() } = {}) {
  if (saved !== null) storage.set(key, saved);
  const documentEvents = new Map();
  const windowEvents = new Map();
  const attrs = new Map();
  let mediaListener;
  let controls;
  let writeBlocked = blockedWrite;
  let writes = 0;
  const media = { matches: dark };
  media[legacyMedia ? "addListener" : "addEventListener"] = (...args) => {
    mediaListener = args.at(-1);
  };
  const document = {
    readyState: ready,
    activeElement: { value: "Unsaved product draft" },
    documentElement: { setAttribute: (name, value) => attrs.set(name, value) },
    addEventListener: (name, listener) => documentEvents.set(name, listener),
    querySelectorAll: (selector) => controls?.[selector] || [],
  };
  const window = {
    get localStorage() {
      if (blockedRead) throw Error("storage denied");
      return {
        getItem: (name) => storage.get(name) ?? null,
        setItem: (name, value) => {
          writes++;
          if (writeBlocked) throw Error("quota exhausted");
          storage.set(name, value);
        },
      };
    },
    addEventListener: (name, listener) => windowEvents.set(name, listener),
    matchMedia: noMedia ? undefined : () => media,
  };
  function button(attribute) {
    const markup = appTemplate.match(new RegExp(`<button[^>]* ${attribute}[^>]*>`))[0];
    const attrs = new Map([...markup.matchAll(/([\w-]+)="([^"]*)"/g)].map((m) => [m[1], m[2]]));
    return {
      attrs,
      title: attrs.get("title") || "",
      setAttribute: (name, value) => attrs.set(name, value),
      closest: (selector) => selector === `[${attribute}]` ? controls[selector][0] : null,
    };
  }
  function replaceControls() {
    controls = {
      "[data-theme-toggle]": [button("data-theme-toggle")],
      "[data-theme-system]": [button("data-theme-system")],
      "[data-theme-preference]": [{ textContent: "Appearance: System" }],
      "[data-theme-control]": [{ hidden: true }, { hidden: true }],
      "[data-theme-status]": [{ textContent: "" }],
    };
  }
  if (ready !== "loading") replaceControls();
  vm.runInNewContext(source, { document, window });
  function activate(selector, nested = false, detail = 1) {
    const control = controls[selector][0];
    document.activeElement = control;
    const target = nested ? { closest: (name) => control.closest(name) } : control;
    documentEvents.get("click")({ target, detail });
    assert.equal(document.activeElement, control, "theme changes must not move keyboard focus");
  }
  return {
    attrs, storage, documentEvents,
    get toggle() { return controls["[data-theme-toggle]"][0]; },
    get system() { return controls["[data-theme-system]"][0]; },
    get controls() { return controls["[data-theme-control]"]; },
    get preference() { return controls["[data-theme-preference]"][0].textContent; },
    get status() { return controls["[data-theme-status]"][0].textContent; },
    get writes() { return writes; },
    get activeElement() { return document.activeElement; },
    ready() { replaceControls(); documentEvents.get("DOMContentLoaded")?.(); },
    toggleTheme({ nested = false, keyboard = false } = {}) { activate("[data-theme-toggle]", nested, keyboard ? 0 : 1); },
    useSystem() { activate("[data-theme-system]"); },
    setSystem(value) { media.matches = value; mediaListener?.(); },
    allowWrites() { writeBlocked = false; },
    swap() { replaceControls(); documentEvents.get("htmx:afterSwap")(); },
    historyRestore() { replaceControls(); documentEvents.get("htmx:historyRestore")(); },
    pageshow() { windowEvents.get("pageshow")(); },
    storageEvent(event) { windowEvents.get("storage")(event); },
    unrelatedClick(target = {}) { documentEvents.get("click")({ target }); },
  };
}

function assertAppearance(page, dark, system) {
  assert.equal(page.attrs.get("data-theme"), dark ? "dark" : "light");
  assert.equal(page.toggle.attrs.get("aria-pressed"), String(dark));
  assert.equal(page.toggle.attrs.get("aria-label"), "Dark mode", "toggle name must stay stable");
  assert.equal(page.toggle.title, dark ? "Switch to light mode" : "Switch to dark mode");
  assert.equal(page.preference, system ? `Appearance: System (${dark ? "dark" : "light"})` : `Appearance: ${dark ? "Dark" : "Light"}`);
  for (const control of page.controls) assert.equal(control.hidden, false);
}

test("system default resolves before paint and synchronizes toggle state on OS changes", () => {
  const page = run({ dark: true });
  assert.equal(page.attrs.get("data-theme"), "dark");
  page.ready();
  assertAppearance(page, true, true);
  assert.equal(page.system.attrs.get("aria-disabled"), "true");
  page.setSystem(false);
  assertAppearance(page, false, true);
  assert.equal(page.writes, 0, "following system must not save an explicit preference");
});

test("a toggle click chooses the opposite resolved theme and persists across reloads", () => {
  for (const dark of [false, true]) {
    const page = run({ dark });
    page.ready();
    page.toggleTheme();
    assertAppearance(page, !dark, false);
    assert.equal(page.storage.get(key), dark ? "light" : "dark");
    assert.equal(page.system.attrs.get("aria-disabled"), "false");
    page.setSystem(dark);
    assertAppearance(page, !dark, false);
    const reloaded = run({ storage: page.storage, dark });
    reloaded.ready();
    assertAppearance(reloaded, !dark, false);
  }
});

test("SVG descendants and native keyboard-generated clicks activate exactly once", () => {
  const page = run();
  page.ready();
  page.toggleTheme({ nested: true });
  assertAppearance(page, true, false);
  page.toggleTheme({ keyboard: true });
  assertAppearance(page, false, false);
  assert.equal(page.writes, 2);
  assert.equal(page.documentEvents.has("keydown"), false, "native buttons must not be double-activated by custom keyboard handlers");
  assert.equal(page.toggle.attrs.get("type"), "button", "theme activation must never submit a nearby form");
});

test("footer system action restores live OS behavior and is idempotent without losing focus", () => {
  const page = run({ dark: true, saved: "light" });
  page.ready();
  page.useSystem();
  assertAppearance(page, true, true);
  assert.equal(page.storage.get(key), "system");
  assert.equal(page.system.attrs.get("aria-disabled"), "true");
  assert.equal(page.system.attrs.has("disabled"), false, "system action stays focusable after use");
  page.useSystem();
  assert.equal(page.writes, 1);
  page.setSystem(false);
  assertAppearance(page, false, true);
  const reloaded = run({ storage: page.storage, dark: true });
  reloaded.ready();
  assertAppearance(reloaded, true, true);
});

test("repeated workspace and history replacements rebind fresh controls without duplicate actions", () => {
  const page = run();
  page.ready();
  page.toggleTheme();
  const original = page.toggle;
  for (let i = 0; i < 3; i++) {
    page.swap();
    assert.notEqual(page.toggle, original);
    assertAppearance(page, true, false);
    page.historyRestore();
    assertAppearance(page, true, false);
  }
  page.toggleTheme({ nested: true });
  assertAppearance(page, false, false);
  assert.equal(page.writes, 2);
  page.useSystem();
  page.swap();
  assertAppearance(page, false, true);
});

test("storage read/write failure retains a usable theme and disclosure through swaps", () => {
  for (const failure of [{ blockedRead: true }, { blockedWrite: true }]) {
    const page = run({ ...failure, dark: true });
    page.ready();
    page.toggleTheme();
    assertAppearance(page, false, false);
    assert.match(page.status, /this page only/);
    page.swap();
    page.pageshow();
    assertAppearance(page, false, false);
    assert.match(page.status, /this page only/);
    page.useSystem();
    assertAppearance(page, true, true);
    assert.equal(page.system.attrs.get("aria-disabled"), "false", "unsaved system preference can be retried");
    page.setSystem(false);
    assertAppearance(page, false, true);
  }
});

test("retrying system appearance after storage recovers persists it and clears the warning", () => {
  const page = run({ saved: "light", dark: true, blockedWrite: true });
  page.ready();
  page.useSystem();
  assert.match(page.status, /this page only/);
  page.allowWrites();
  page.useSystem();
  assertAppearance(page, true, true);
  assert.equal(page.storage.get(key), "system");
  assert.equal(page.status, "");
  assert.equal(page.system.attrs.get("aria-disabled"), "true");
});

test("unrelated input clicks and malformed saved values do not mutate form context", () => {
  const page = run({ saved: "invalid", dark: true });
  page.ready();
  const draft = page.activeElement;
  page.unrelatedClick(draft);
  page.unrelatedClick(null);
  assert.equal(page.activeElement, draft);
  assert.equal(draft.value, "Unsaved product draft");
  assertAppearance(page, true, true);
  assert.equal(page.writes, 0);
});

test("other-tab preferences and storage clearing synchronize controls without reloading", () => {
  const page = run();
  page.ready();
  page.storageEvent({ key: "unrelated", newValue: "dark" });
  assertAppearance(page, false, true);
  page.storageEvent({ key, newValue: "dark" });
  assertAppearance(page, true, false);
  page.storageEvent({ key: null, newValue: null });
  assertAppearance(page, false, true);
});

test("back-forward page restoration refreshes stored, cleared, or corrupt preferences", () => {
  for (const stored of [null, "unsupported", "dark"]) {
    const page = run({ dark: true, saved: "light" });
    page.ready();
    if (stored === null) page.storage.delete(key);
    else page.storage.set(key, stored);
    page.pageshow();
    assertAppearance(page, true, stored !== "dark");
    page.setSystem(false);
    assertAppearance(page, stored === "dark", stored !== "dark");
  }
});

test("legacy and absent matchMedia retain functional explicit and system controls", () => {
  const legacy = run({ legacyMedia: true, ready: "complete" });
  legacy.setSystem(true);
  assertAppearance(legacy, true, true);
  const fallback = run({ noMedia: true, ready: "complete" });
  assertAppearance(fallback, false, true);
  fallback.toggleTheme();
  assertAppearance(fallback, true, false);
  fallback.useSystem();
  assertAppearance(fallback, false, true);
});

test("text color roles meet 4.5:1 contrast in both palettes and the no-JS fallback", () => {
  const css = fs.readFileSync(path.join(rootPath, "web/static/theme.css"), "utf8");
  const blocks = [...css.matchAll(/:root(?:\[data-theme="dark"\]|:not\(\[data-theme\]\))?\s*\{([^}]+)\}/g)];
  assert.equal(blocks.length, 3);
  const palettes = blocks.map((block) => Object.fromEntries(
    [...block[1].matchAll(/(--[\w-]+):\s*(#[0-9a-f]{6})/g)].map((match) => [match[1], match[2]]),
  ));
  assert.deepEqual(palettes[1], palettes[2], "system and explicit dark palettes differ");
  function luminance(hex) {
    const rgb = [1, 3, 5].map((start) => parseInt(hex.slice(start, start + 2), 16) / 255)
      .map((v) => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
    return 0.2126 * rgb[0] + 0.7152 * rgb[1] + 0.0722 * rgb[2];
  }
  function contrast(a, b) {
    const [low, high] = [luminance(a), luminance(b)].sort((x, y) => x - y);
    return (high + 0.05) / (low + 0.05);
  }
  for (const palette of palettes) {
    for (const [text, background] of [
      ["text", "page"], ["text", "surface"], ["text", "input"], ["text", "highlight"],
      ["muted", "page"], ["muted", "surface"], ["muted", "wash"], ["muted", "highlight"],
      ["accent", "page"], ["accent", "surface"], ["error-text", "error-surface"],
      ["warning-text", "surface"], ["warning-text", "highlight"],
      ["placeholder", "input"], ["disabled-text", "disabled"],
      ["complete-text", "complete-surface"], ["highlight-heading", "highlight"],
    ]) {
      assert.ok(contrast(palette[`--theme-${text}`], palette[`--theme-${background}`]) >= 4.5,
        `${text} on ${background} needs readable contrast`);
    }
  }
  for (const palette of palettes) {
    assert.ok(contrast(palette["--theme-input-border"], palette["--theme-input"]) >= 3,
      "form control boundaries need 3:1 contrast");
    for (const surface of ["surface", "input", "page"]) {
      assert.ok(contrast(palette["--theme-focus"], palette[`--theme-${surface}`]) >= 3,
        `focus outline on ${surface} needs 3:1 contrast`);
    }
  }
  assert.ok(contrast(palettes[0]["--selection-muted"], "#ffe449") >= 4.5,
    "selected product metadata on yellow");
  assert.ok(contrast("#142b59", "#ffe449") >= 4.5, "navy on yellow labels");
  assert.ok(contrast("#ffffff", "#c72b21") >= 4.5, "white on red prices");
  assert.ok(contrast("#ffffff", "#142b59") >= 4.5, "white on navy navigation");
});


test("ordinary and reset layouts initialize appearance before styles with one compact header control", () => {
  const reset = fs.readFileSync(path.join(rootPath, "web/templates/reset.html"), "utf8");
  for (const markup of [appTemplate, reset]) {
    const head = markup.slice(markup.indexOf("<head>"), markup.indexOf("</head>"));
    const scripts = [...head.matchAll(/<script\b([^>]*)src="([^"]+)"([^>]*)>/g)];
    const theme = scripts.find((match) => match[2] === "/static/theme.js");
    assert.ok(theme, "head loads the theme script");
    assert.doesNotMatch(theme[1] + theme[3], /\b(?:defer|async)\b/);
    assert.ok(head.indexOf('/static/theme.js') < head.indexOf('rel="stylesheet"'));
    const styles = [...head.matchAll(/<link rel="stylesheet" href="([^"]+)"/g)];
    assert.equal(styles.at(-1)[1], "/static/theme.css");
    const header = markup.slice(markup.indexOf('<header class="site-header">'), markup.indexOf("</header>"));
    assert.equal([...header.matchAll(/{{template "theme-control" \.}}/g)].length, 1);
    assert.doesNotMatch(header, /<form|<select|theme-footer/);
    assert.match(header, /Employees/);
    for (const footer of markup.matchAll(/<footer>(.*?)<\/footer>/g)) {
      assert.match(footer[1], /{{template "theme-footer" \.}}/);
    }
  }
  const headerControl = appTemplate.match(/{{define "theme-control"}}(.*?){{end}}/)[1];
  assert.equal([...headerControl.matchAll(/<button/g)].length, 1);
  assert.doesNotMatch(headerControl, /<select|<label/);
  assert.match(headerControl, /data-theme-toggle aria-label="Dark mode" aria-pressed="false"/);
  assert.equal([...headerControl.matchAll(/aria-hidden="true" focusable="false"/g)].length, 2);
  assert.match(appTemplate, /data-theme-status role="status" aria-live="polite"/);
  assert.match(appTemplate, /data-theme-system[^>]*>Use system appearance<\/button>/);
  assert.equal([...appTemplate.matchAll(/data-theme-control hidden/g)].length, 2);
});


test("handheld and phone-pairing layouts retain prepaint theme and final color layer", () => {
  for (const name of ["handheld.html", "handheld_pairing.html"]) {
    const markup = fs.readFileSync(path.join(rootPath, "web/templates", name), "utf8");
    const head = markup.slice(markup.indexOf("<head>"), markup.indexOf("</head>"));
    assert.ok(head.indexOf('/static/theme.js') < head.indexOf('rel="stylesheet"'));
    assert.match(head, /<script src="\/static\/theme.js"><\/script>/);
    const styles = [...head.matchAll(/<link rel="stylesheet" href="([^"]+)"/g)];
    assert.equal(styles.at(-1)[1], "/static/theme.css");
    const header = markup.slice(markup.indexOf("<header"), markup.indexOf("</header>"));
    assert.equal([...header.matchAll(/{{template "theme-control" \.}}/g)].length, 1);
    assert.match(markup, /{{template "theme-footer" \.}}/);
  }
});
