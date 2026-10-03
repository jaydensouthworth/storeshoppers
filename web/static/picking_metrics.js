(() => {
  "use strict";

  // This is a tab-local estimate, never a saved staff-performance record.
  // Only server-confirmed command deltas can change the numerator.
  const PREFIX = "handheld:picking:v1:";
  const IDLE_MS = 60000, MAX_EVENTS = 512, MIN_RATE_MS = 1000;
  const PICK_AREA = ".handheld-item, .handheld-item-detail, .handheld-pick-list";
  const PICK_FORMS = new Set(["/handheld/scan", "/handheld/pick", "/handheld/pick/undo", "/handheld/weight/preview", "/handheld/weight/confirm"]);
  const MUTATIONS = new Set(["/handheld/pick", "/handheld/pick/undo", "/handheld/weight/confirm"]);
  const requests = new WeakMap();
  const identifier = value => typeof value === "string" && /^[A-Za-z0-9:_-]{1,100}$/.test(value);
  let root = null, key = "", state = null, timer = null;
  let sampledAt = null, activeUntil = 0, suspended = false, storageFailed = false;

  function workspace() { return document.getElementById("handheld-workspace"); }
  function now() { return window.performance.now(); }
  function identity(element) {
    const order = element?.dataset.handheldOrder, assignment = element?.dataset.handheldAssignment;
    return identifier(order) && identifier(assignment) ? PREFIX + JSON.stringify([order, assignment]) : "";
  }
  function blank() { return {v: 1, elapsedMs: 0, net: 0, seen: [], pending: [], paused: false, known: true, reason: ""}; }
  function unavailable(reason) {
    state.known = false;
    state.reason = reason;
  }
  function storageError() {
    storageFailed = true;
    unavailable("Browser storage is unavailable. Line count and rate are unknown for this task session.");
    // Quota failures may still allow replacing an existing record with a small
    // tombstone. Never recover stale totals as if an unrecorded event were new.
    try { window.sessionStorage.setItem(key, JSON.stringify({...blank(), known: false, reason: state.reason})); }
    catch (_) { /* Reload/history restoration also refuses unseen HTML events. */ }
  }
  function valid(saved) {
    return saved && saved.v === 1 && Number.isFinite(saved.elapsedMs) && saved.elapsedMs >= 0 && saved.elapsedMs <= Number.MAX_SAFE_INTEGER &&
      Number.isSafeInteger(saved.net) && Math.abs(saved.net) <= MAX_EVENTS && typeof saved.paused === "boolean" && typeof saved.known === "boolean" &&
      typeof saved.reason === "string" && saved.reason.length <= 240 && Array.isArray(saved.seen) && saved.seen.length <= MAX_EVENTS &&
      saved.seen.every(identifier) && new Set(saved.seen).size === saved.seen.length && Math.abs(saved.net) <= saved.seen.length &&
      Array.isArray(saved.pending) && saved.pending.length <= 16 && saved.pending.every(identifier) && new Set(saved.pending).size === saved.pending.length;
  }
  function load() {
    state = blank();
    if (storageFailed) { unavailable("Browser storage is unavailable. Line count and rate are unknown for this task session."); return; }
    let raw;
    try {
      raw = window.sessionStorage.getItem(key);
    } catch (_) { storageError(); }
    if (storageFailed || raw === null) return;
    let saved = null;
    try { if (raw.length <= 64000) saved = JSON.parse(raw); }
    catch (_) { /* Corrupt local data is not evidence of picking work. */ }
    if (valid(saved)) state = saved;
    else unavailable("Stored picking metrics could not be verified. Line count and rate are unknown for this task session.");
  }
  function persist() {
    if (!key || !state || storageFailed) return;
    try { window.sessionStorage.setItem(key, JSON.stringify(state)); }
    catch (_) { storageError(); }
  }
  function clearTimer() {
    if (timer !== null) window.clearTimeout(timer);
    timer = null;
  }
  function settle(at) {
    if (sampledAt === null) return;
    const end = Math.min(at, activeUntil);
    state.elapsedMs += Math.max(0, end - sampledAt);
    sampledAt = end;
    if (at >= activeUntil) sampledAt = null;
  }
  function stop() {
    if (state) settle(now());
    sampledAt = null;
    clearTimer();
    persist();
  }
  function usable() {
    return root && state && !suspended && !document.hidden && workspace() === root && identity(root) === key && root.dataset.handheldRevoked !== "true";
  }
  function elapsedLabel(ms) {
    const seconds = Math.floor(ms / 1000);
    return String(Math.floor(seconds / 60)).padStart(2, "0") + ":" + String(seconds % 60).padStart(2, "0");
  }
  function render() {
    if (!root || !state) return;
    const reason = state.known ? "" : state.reason;
    const rate = state.known && state.elapsedMs >= MIN_RATE_MS ? (state.net * 60000 / state.elapsedMs).toFixed(1) : "—";
    root.querySelectorAll("[data-picking-active]").forEach(node => { node.textContent = elapsedLabel(state.elapsedMs); });
    root.querySelectorAll("[data-picking-net]").forEach(node => { node.textContent = state.known ? String(state.net) : "—"; node.title = reason; });
    root.querySelectorAll("[data-picking-rate]").forEach(node => { node.textContent = rate; node.title = reason || (state.elapsedMs < MIN_RATE_MS ? "Rate appears after one active picking second." : "Net completed product lines per active minute in this browser task session."); });
    root.querySelectorAll("[data-picking-pause]").forEach(node => {
      node.textContent = state.paused ? "Resume picking timer" : "Pause picking timer";
      node.setAttribute("aria-pressed", String(state.paused));
    });
    root.querySelectorAll("[data-picking-status]").forEach(node => {
      node.textContent = reason || (state.paused ? "Timer paused by you." : sampledAt !== null ? "Active picking · pauses after 60 seconds without picking activity." : "Timer paused · use a picking control to start or resume.");
    });
  }
  function schedule() {
    clearTimer();
    if (!usable() || sampledAt === null) return;
    const handle = window.setTimeout(() => {
      if (timer !== handle) return;
      timer = null;
      if (!usable()) { stop(); render(); return; }
      settle(now()); persist(); render(); schedule();
    }, Math.max(1, Math.min(1000, activeUntil - now())));
    timer = handle;
  }
  function activity() {
    if (!usable() || state.paused) return;
    const at = now();
    settle(at); // Settle the old idle window before renewing it.
    sampledAt = at;
    activeUntil = at + IDLE_MS;
    persist(); render(); schedule();
  }
  function restoredNavigation() {
    const type = window.performance.getEntriesByType?.("navigation")?.[0]?.type;
    return type === "reload" || type === "back_forward";
  }
  function uncertain(taskKey = key) {
    if (!taskKey) return;
    const reason = "A picking save could not be matched to a confirmed result. Line count and rate are unknown for this task session.";
    if (taskKey === key && state) {
      unavailable(reason); state.pending = []; persist(); render(); return;
    }
    // A late response may belong to a task that is no longer on screen. Mark
    // that task only, so returning to it cannot show a silently incomplete rate.
    try {
      const raw = window.sessionStorage.getItem(taskKey);
      let saved = null;
      try { if (raw && raw.length <= 64000) saved = JSON.parse(raw); }
      catch (_) { /* Preserve unknown status even when the old record is corrupt. */ }
      const value = valid(saved) ? saved : blank();
      value.known = false; value.reason = reason; value.pending = [];
      window.sessionStorage.setItem(taskKey, JSON.stringify(value));
    } catch (_) {
      if (state) storageError();
      else storageFailed = true;
      try { window.sessionStorage.setItem(taskKey, JSON.stringify({...blank(), known: false, reason})); }
      catch (_) { /* Existing pending receipts also fail closed on restoration. */ }
    }
  }
  function mutationContext(element) {
    const form = element?.closest?.("form"), current = workspace();
    if (!form || !current?.contains(form) || !MUTATIONS.has(form.getAttribute("action"))) return null;
    const taskKey = identity(current);
    if (!taskKey) return null;
    return {key: taskKey, command: form.querySelector('input[name="command_key"]')?.value};
  }
  function pending(context) {
    if (!context || context.key !== key || !state?.known) return;
    if (!identifier(context.command)) { uncertain(); return; }
    if (state.seen.includes(context.command) || state.pending.includes(context.command)) return;
    if (state.pending.length >= 16) { uncertain(); return; }
    state.pending.push(context.command); persist();
  }
  function consumeEvent(restored) {
    const event = root.dataset.pickEvent, delta = root.dataset.pickLineDelta, rejected = root.dataset.pickRejected;
    // Strip consumed response evidence from the live DOM before BFCache can
    // snapshot it. No command fields or server state are altered.
    delete root.dataset.pickEvent;
    delete root.dataset.pickLineDelta;
    delete root.dataset.pickRejected;
    // A fresh handled refusal settles this request without changing progress.
    // An old restored refusal cannot settle a newer retry using the same key.
    if (!restored && identifier(rejected)) state.pending = state.pending.filter(command => command !== rejected);
    if (!identifier(event) || !/^(?:-1|0|1)$/.test(delta || "") || !state.known) return;
    if (state.seen.includes(event)) { state.pending = state.pending.filter(command => command !== event); return; }
    if (restored) {
      unavailable("A restored response could not be matched to saved metrics. Line count and rate are unknown for this task session.");
      persist(); return;
    }
    if (state.seen.length >= MAX_EVENTS) {
      unavailable("This task session reached its local event limit. Line count and rate are unknown; saved order progress is unchanged.");
      persist(); return;
    }
    state.seen.push(event);
    state.pending = state.pending.filter(command => command !== event);
    state.net += Number(delta);
    // Store the receipt and delta together, before displaying a known total.
    // A failure makes the metric unknown rather than retrying the increment.
    persist();
    // A successful first-entry confirmation establishes activity now. We
    // cannot reconstruct time on a previous page or while JavaScript was off.
    activity();
  }
  function mount(restored = false, reconcilePending = false) {
    const next = workspace(), nextKey = identity(next);
    if (!nextKey || next?.dataset.handheldRevoked === "true") {
      stop(); root = null; key = ""; state = null; return;
    }
    if (key !== nextKey) {
      stop(); root = next; key = nextKey; load();
    } else if (restored) {
      // Another document in this tab may have advanced the same session while
      // this document was in BFCache. Its persisted snapshot wins.
      sampledAt = null; clearTimer(); root = next; load();
    } else {
      settle(now()); root = next;
    }
    consumeEvent(restored);
    if (reconcilePending && state.pending.length) uncertain();
    // Finishing the last line ends this picking interval immediately. An
    // explicit correction/undo can still start a new interval on this view.
    if (root.dataset.pickingComplete === "true") stop();
    persist(); render(); schedule();
  }
  function elementTarget(event) { return event.target?.closest ? event.target : event.target?.parentElement; }
  function interaction(event) {
    if (!usable() || event.isTrusted === false) return;
    const target = elementTarget(event);
    if (!target || !root.contains(target)) return;
    const pause = target.closest("[data-picking-pause]");
    if (pause && event.type === "click") {
      event.preventDefault();
      stop(); state.paused = !state.paused;
      if (!state.paused) activity();
      persist(); render(); return;
    }
    if (!target.closest(PICK_AREA) && !target.closest("[data-picking-activity]")) return;
    const form = target.closest("form"), link = target.closest("a[href]");
    const formAction = form?.getAttribute("action");
    const isPickLink = link && /^\/handheld\/(?:\?[^#]*)?(?:#.*)?$/.test(link.getAttribute("href") || "");
    if (target.closest(".handheld-report, [data-picking-ignore]") || (form && !PICK_FORMS.has(formAction)) || (link && !isPickLink)) {
      stop(); render(); return;
    }
    if (event.type === "keydown" && ["Tab", "Shift", "Control", "Alt", "Meta", "Escape"].includes(event.key)) return;
    const control = target.closest("input, textarea, select, button, a, summary, [data-picking-activity]");
    if (!control && event.type !== "submit") return;
    if ((form && PICK_FORMS.has(formAction)) || isPickLink || target.closest("[data-picking-activity]")) activity();
  }
  // Native HTML POSTs do not emit HTMX request events. Persist the command ID
  // before leaving the page so a missing/replayed response becomes unknown.
  document.addEventListener("submit", event => {
    if (!event.defaultPrevented) pending(mutationContext(event.target));
  }, true);
  ["click", "input", "change", "keydown", "submit"].forEach(type => document.addEventListener(type, interaction));
  document.addEventListener("htmx:beforeRequest", event => {
    if (event.defaultPrevented || !event.detail?.xhr) return;
    const context = mutationContext(event.detail.elt);
    if (context) { requests.set(event.detail.xhr, context); pending(context); }
  });
  ["htmx:sendError", "htmx:sendAbort", "htmx:timeout", "htmx:responseError"].forEach(type => document.addEventListener(type, event => {
    const context = requests.get(event.detail?.xhr);
    if (context) uncertain(context.key);
  }));
  document.addEventListener("htmx:beforeOnLoad", event => {
    // handheld.js is loaded first; its latest-navigation guard cancels stale
    // responses before this listener runs. A cancelled save may still exist.
    const context = requests.get(event.detail?.xhr);
    if (context && event.defaultPrevented) uncertain(context.key);
  });
  document.addEventListener("handheld:picking-activity", event => {
    // This explicit integration hook starts time only. It never accepts a
    // client-supplied count, delta, command key, or persisted clock value.
    const detail = event.detail;
    if (detail?.order !== undefined && String(detail.order) !== root?.dataset.handheldOrder) return;
    if (detail?.assignment !== undefined && String(detail.assignment) !== root?.dataset.handheldAssignment) return;
    activity();
  });
  document.addEventListener("handheld:picking-uncertain", event => {
    const order = event.detail?.order, assignment = event.detail?.assignment;
    if (order === undefined && assignment === undefined) uncertain();
    else if (identifier(String(order)) && identifier(String(assignment))) uncertain(PREFIX + JSON.stringify([String(order), String(assignment)]));
  });
  document.addEventListener("htmx:afterSwap", event => mount(false, workspace() !== root || event.detail?.target?.id === "handheld-workspace"));
  document.addEventListener("htmx:historyRestore", () => mount(true, true));
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) { stop(); render(); }
    else mount(); // Foreground alone does not restart the activity clock.
  });
  ["handheld:pause", "handheld:revoked"].forEach(type => document.addEventListener(type, () => { stop(); render(); }));
  window.addEventListener("pagehide", () => { stop(); suspended = true; render(); });
  window.addEventListener("pageshow", event => {
    const restoring = suspended || event.persisted === true;
    suspended = false;
    mount(restoring, restoring);
  });
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", () => mount(restoredNavigation(), true));
  else mount(restoredNavigation(), true);
})();
