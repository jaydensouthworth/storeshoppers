// Dependency-free behavior checks for the early appearance script. This fake
// DOM exercises preference/lifecycle behavior; it is not a browser layout test.
const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const rootPath = path.join(__dirname, "..");
const source = fs.readFileSync(path.join(rootPath, "web/static/theme.js"), "utf8");
const key = "neighborhood-market-theme";

function run({ dark = false, saved = null, blockedRead = false, blockedWrite = false,
  ready = "loading", legacyMedia = false, noMedia = false, storage = new Map() } = {}) {
  if (saved !== null) storage.set(key, saved);
  const documentEvents = new Map();
  const windowEvents = new Map();
  const attrs = new Map();
  let mediaListener;
  let controls;
  const media = { matches: dark };
  media[legacyMedia ? "addListener" : "addEventListener"] = (...args) => {
    mediaListener = args.at(-1);
  };
  const document = {
    readyState: ready,
    documentElement: { setAttribute: (name, value) => attrs.set(name, value) },
    addEventListener: (name, listener) => documentEvents.set(name, listener),
    querySelectorAll: (selector) => controls ? [controls[selector]] : [],
  };
  const window = {
    get localStorage() {
      if (blockedRead) throw Error("storage denied");
      return {
        getItem: (name) => storage.get(name) ?? null,
        setItem: (name, value) => {
          if (blockedWrite) throw Error("quota exhausted");
          storage.set(name, value);
        },
      };
    },
    addEventListener: (name, listener) => windowEvents.set(name, listener),
    matchMedia: noMedia ? undefined : () => media,
  };
  function replaceControls() {
    controls = {
      "[data-theme-select]": { value: "system", matches: (selector) => selector === "[data-theme-select]" },
      "[data-theme-control]": { hidden: true },
      "[data-theme-status]": { textContent: "" },
    };
  }
  if (ready !== "loading") replaceControls();
  vm.runInNewContext(source, { document, window });
  return {
    attrs, storage,
    get control() { return controls["[data-theme-control]"]; },
    get choice() { return controls["[data-theme-select]"].value; },
    get status() { return controls["[data-theme-status]"].textContent; },
    ready() { replaceControls(); documentEvents.get("DOMContentLoaded")?.(); },
    choose(value) {
      controls["[data-theme-select]"].value = value;
      documentEvents.get("change")({ target: controls["[data-theme-select]"] });
    },
    setSystem(value) { media.matches = value; mediaListener?.(); },
    swap() { replaceControls(); documentEvents.get("htmx:afterSwap")(); },
    historyRestore() { replaceControls(); documentEvents.get("htmx:historyRestore")(); },
    pageshow() { windowEvents.get("pageshow")(); },
    storageEvent(event) { windowEvents.get("storage")(event); },
    unrelatedChange() { documentEvents.get("change")({ target: { value: "dark" } }); },
  };
}

test("system default is resolved before DOM content and tracks live OS changes", () => {
  const page = run({ dark: true });
  assert.equal(page.attrs.get("data-theme"), "dark");
  page.ready();
  assert.equal(page.choice, "system");
  assert.equal(page.control.hidden, false);
  page.setSystem(false);
  assert.equal(page.attrs.get("data-theme"), "light");
});

test("explicit light and dark persist reloads and ignore system changes", () => {
  for (const choice of ["light", "dark"]) {
    const page = run({ dark: choice === "light" });
    page.ready();
    page.choose(choice);
    assert.equal(page.storage.get(key), choice);
    page.setSystem(choice === "light");
    assert.equal(page.attrs.get("data-theme"), choice);
    const reloaded = run({ storage: page.storage, dark: choice === "light" });
    assert.equal(reloaded.attrs.get("data-theme"), choice);
    reloaded.ready();
    assert.equal(reloaded.choice, choice);
  }
});

test("returning to System persists and follows the operating system again", () => {
  const page = run({ dark: true, saved: "light" });
  page.ready();
  page.choose("system");
  assert.equal(page.attrs.get("data-theme"), "dark");
  assert.equal(page.storage.get(key), "system");
  page.setSystem(false);
  assert.equal(page.attrs.get("data-theme"), "light");
});

test("workspace replacement and history restore synchronize newly rendered controls", () => {
  const page = run();
  page.ready();
  page.choose("dark");
  page.swap();
  assert.equal(page.choice, "dark");
  assert.equal(page.control.hidden, false);
  page.choose("light");
  page.historyRestore();
  assert.equal(page.choice, "light");
  assert.equal(page.attrs.get("data-theme"), "light");
});

test("blocked reads and exhausted writes retain usable choices through swaps", () => {
  for (const failure of [{ blockedRead: true }, { blockedWrite: true }]) {
    const page = run({ ...failure, dark: true });
    page.ready();
    assert.equal(page.attrs.get("data-theme"), "dark");
    page.choose("light");
    assert.equal(page.attrs.get("data-theme"), "light");
    assert.match(page.status, /this page only/);
    page.swap();
    page.pageshow();
    assert.equal(page.choice, "light");
    assert.equal(page.attrs.get("data-theme"), "light");
    assert.match(page.status, /this page only/);
  }
});

test("invalid stored preferences and unrelated controls are harmless", () => {
  const page = run({ saved: "invalid", dark: true });
  page.ready();
  assert.equal(page.choice, "system");
  page.unrelatedChange();
  assert.equal(page.attrs.get("data-theme"), "dark");
  page.choose("unsupported");
  assert.equal(page.storage.get(key), "invalid");
  page.setSystem(false);
  assert.equal(page.attrs.get("data-theme"), "light");
});

test("other-tab preference changes and storage clearing synchronize without reload", () => {
  const page = run({ dark: false });
  page.ready();
  page.storageEvent({ key: "unrelated", newValue: "dark" });
  assert.equal(page.choice, "system");
  page.storageEvent({ key, newValue: "dark" });
  assert.equal(page.attrs.get("data-theme"), "dark");
  assert.equal(page.choice, "dark");
  page.storageEvent({ key: null, newValue: null });
  assert.equal(page.attrs.get("data-theme"), "light");
  assert.equal(page.choice, "system");
});

test("back-forward page restoration refreshes the saved preference", () => {
  const page = run({ saved: "light" });
  page.ready();
  page.storage.set(key, "dark");
  page.pageshow();
  assert.equal(page.attrs.get("data-theme"), "dark");
  assert.equal(page.choice, "dark");
});

test("legacy media listeners and missing matchMedia retain a usable control", () => {
  const legacy = run({ legacyMedia: true, ready: "complete" });
  legacy.setSystem(true);
  assert.equal(legacy.attrs.get("data-theme"), "dark");
  const fallback = run({ noMedia: true, ready: "complete" });
  assert.equal(fallback.attrs.get("data-theme"), "light");
  fallback.choose("dark");
  assert.equal(fallback.attrs.get("data-theme"), "dark");
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


test("ordinary and reset layouts initialize appearance before styles and reuse one labelled control", () => {
  const app = fs.readFileSync(path.join(rootPath, "web/templates/app.html"), "utf8");
  const reset = fs.readFileSync(path.join(rootPath, "web/templates/reset.html"), "utf8");
  for (const markup of [app, reset]) {
    const head = markup.slice(markup.indexOf("<head>"), markup.indexOf("</head>"));
    const scripts = [...head.matchAll(/<script\b([^>]*)src="([^"]+)"([^>]*)>/g)];
    const theme = scripts.find((match) => match[2] === "/static/theme.js");
    assert.ok(theme, "head loads the theme script");
    assert.doesNotMatch(theme[1] + theme[3], /\b(?:defer|async)\b/,
      "theme preference must apply before first paint");
    assert.ok(head.indexOf('/static/theme.js') < head.indexOf('rel="stylesheet"'));
    const styles = [...head.matchAll(/<link rel="stylesheet" href="([^"]+)"/g)];
    assert.equal(styles.at(-1)[1], "/static/theme.css", "theme overrides every feature stylesheet");
    const header = markup.slice(markup.indexOf('<header class="site-header">'), markup.indexOf("</header>"));
    assert.equal([...header.matchAll(/{{template "theme-control" \.}}/g)].length, 1);
    assert.doesNotMatch(header, /<form/);
    assert.match(header, /Employees/);
  }
  assert.match(app, /<label for="theme-choice">Theme<\/label>/);
  assert.match(app, /<select id="theme-choice" data-theme-select aria-describedby="theme-status">/);
  assert.match(app, /data-theme-status role="status" aria-live="polite"/);
  assert.equal([...app.matchAll(/data-theme-control hidden/g)].length, 1);
  assert.match(app, /href="\/static\/manager_workflows.css"/);
});

test("restoring a page after clearing or corrupting storage resumes the system preference", () => {
  for (const stored of [null, "unsupported"]) {
    const page = run({ dark: true, saved: "light" });
    page.ready();
    if (stored === null) page.storage.delete(key);
    else page.storage.set(key, stored);
    page.pageshow();
    assert.equal(page.choice, "system");
    assert.equal(page.attrs.get("data-theme"), "dark");
    page.setSystem(false);
    assert.equal(page.attrs.get("data-theme"), "light");
  }
});
