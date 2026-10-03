(() => {
  "use strict";
  const requests = new WeakMap();
  let requestSequence = 0, lifetimeTimer = null;
  function workspace() { return document.getElementById("handheld-workspace"); }
  function notice(message, focus = true) {
    const target = document.getElementById("handheld-network");
    if (!target) return;
    target.textContent = message;
    target.hidden = false;
    if (focus) { target.focus({preventScroll:true}); target.scrollIntoView({block:"nearest"}); }
  }
  function importPairingLink() {
    const input = document.getElementById("pairing-code");
    if (!window.location.hash.startsWith("#pair=")) return;
    const value = new URLSearchParams(window.location.hash.slice(1)).get("pair") || "";
    // Remove one-use invitations from the address bar before any navigation.
    // Never persist the code, request it from another origin or redeem on load.
    try { window.history.replaceState(null, "", window.location.pathname + window.location.search); }
    catch (_) { notice("This browser could not clear the pairing link. The code still needs your confirmation and expires shortly.", false); }
    if (value.length > 80 || !/^[A-Za-z2-7 -]{20,80}$/.test(value)) {
      notice("This pairing link is not valid. Enter the code shown by the desktop manager.", false);
      return;
    }
    if (!input) {
      document.dispatchEvent(new CustomEvent("handheld:pause"));
      notice("You’re already connected to a task, and that assignment has not changed. Disconnect this phone, then open the new connection link again to review it.", false);
      return;
    }
    input.value = value;
    const note = document.querySelector("[data-pairing-link-note]");
    if (note) note.hidden = false;
  }
  function revoke() {
    const root = workspace(); if (root) root.dataset.handheldRevoked = "true";
    document.dispatchEvent(new CustomEvent("handheld:revoked"));
    document.querySelectorAll("form[data-handheld-authorized]").forEach(form => {
      form.querySelectorAll("button,input,select,textarea").forEach(input => { input.disabled = true; });
    });
  }
  document.addEventListener("submit", event => {
    const form = event.target;
    if (!form?.matches?.("[data-handheld-form]")) return;
    if (form.matches("[data-handheld-authorized]") && workspace()?.dataset.handheldRevoked === "true") {
      event.preventDefault();
      event.stopImmediatePropagation();
      notice("This phone connection is no longer active. Refresh or reconnect before reviewing this item again. Your entered values are kept here.");
      return;
    }
    if (navigator.onLine === false) {
      event.preventDefault();
      event.stopImmediatePropagation();
      notice("You’re offline. Nothing was sent. Your entered values are kept; reconnect and refresh the task before confirming any changes.");
    }
  }, true);
  document.addEventListener("htmx:beforeRequest", event => {
    const root = workspace();
    const element = event.detail?.elt;
    if (!root || !element || !root.contains(element) || !event.detail?.xhr) return;
    const form = element.matches?.("form") ? element : element.closest?.("form");
    if (form?.matches?.("[data-handheld-authorized]") && root.dataset.handheldRevoked === "true") {
      event.preventDefault();
      notice("This phone connection is no longer active. Refresh or reconnect before reviewing this item again. Your entered values are kept here.");
      return;
    }
    if (form?.matches?.("[data-handheld-form]") && navigator.onLine === false) {
      event.preventDefault();
      notice("You’re offline. Nothing was sent. Your entered values are kept; reconnect and refresh the task before confirming any changes.");
      return;
    }
    const context = {root, disabled:[], sequence:++requestSequence};
    // HTMX has already serialized the request. Lock this form during its one
    // request so new edits cannot be mistaken for the submitted confirmation.
    if (form?.matches?.("[data-handheld-form]")) {
      form.querySelectorAll("input,select,textarea,button").forEach(input => {
        if (!input.disabled) { context.disabled.push(input); input.dataset.handheldBusy = String(context.sequence); input.disabled = true; }
      });
    }
    requests.set(event.detail.xhr, context);
    const pending = document.getElementById("handheld-pending");
    if (pending) pending.hidden = false;
  });
  document.addEventListener("htmx:beforeOnLoad", event => {
    const context = requests.get(event.detail?.xhr);
    if (context && (context.sequence !== requestSequence || context.root !== workspace())) {
      // Cancel processing before HX-Redirect or other response headers can let
      // an obsolete connection/navigation replace the latest user intent.
      event.preventDefault();
    }
  });
  document.addEventListener("htmx:beforeSwap", event => {
    const context = requests.get(event.detail?.xhr);
    if (!context) return;
    if (context.sequence !== requestSequence || context.root !== workspace()) { event.detail.shouldSwap = false; return; }
    if (event.detail.xhr.getResponseHeader("X-Handheld-Error") === "revoked") revoke();
  });
  document.addEventListener("htmx:afterRequest", event => {
    const context = requests.get(event.detail?.xhr);
    if (!context) return;
    context.disabled.forEach(input => {
      // A restored form may already belong to a newer request. Its controls
      // must stay locked when cleanup from the old request arrives late.
      if (input.dataset.handheldBusy !== String(context.sequence)) return;
      delete input.dataset.handheldBusy;
      if (input.isConnected && context.root.dataset.handheldRevoked !== "true") input.disabled = false;
    });
    if (context.sequence !== requestSequence || context.root !== workspace()) return;
    const pending = document.getElementById("handheld-pending");
    if (pending) pending.hidden = true;
  });
  function requestFailed(event) {
    const context = requests.get(event.detail?.xhr);
    if (!context || context.sequence !== requestSequence || context.root !== workspace()) return;
    if (event.detail.xhr?.getResponseHeader?.("X-Handheld-Error") === "revoked") revoke();
    notice("We couldn’t confirm the result. Your values are still shown. Refresh the task to check the saved state before trying again; this page will not resend anything automatically.");
  }
  document.addEventListener("htmx:sendError", requestFailed);
  document.addEventListener("htmx:responseError", requestFailed);
  document.addEventListener("htmx:afterSwap", event => {
    if (event.detail?.target?.id !== "handheld-workspace") return;
    importPairingLink();
    mountLifetime();
    const error = document.querySelector("#handheld-workspace .handheld-feedback.error");
    const focusTarget = error || document.querySelector("#handheld-workspace [data-handheld-flow-focus]") || document.querySelector("#handheld-workspace [data-handheld-weight-review]") || document.querySelector("#handheld-workspace #handheld-picked");
    if (focusTarget) { focusTarget.focus({preventScroll:true}); if (focusTarget.id === "handheld-picked") focusTarget.select?.(); focusTarget.scrollIntoView({block:"nearest"}); }
  });
  function mountLifetime() {
    if (lifetimeTimer !== null) { window.clearTimeout(lifetimeTimer); lifetimeTimer = null; }
    const root = workspace();
    const expiry = Number(root?.dataset.handheldExpires);
    if (!root || !Number.isFinite(expiry) || expiry <= 0) return;
    const expire = () => {
      if (workspace() !== root) return;
      revoke();
      notice("This phone connection expired. Refresh or reconnect to continue; your recorded picks are kept.", false);
    };
    const remaining = expiry * 1000 - Date.now();
    if (remaining <= 0) expire();
    else lifetimeTimer = window.setTimeout(expire, Math.min(remaining, 2147483647));
  }
  function resume() {
    // A restored page is a new navigation intent. Ignore any response from a
    // request that began before Back/Forward or a history restoration.
    requestSequence++;
    const root = workspace();
    document.querySelectorAll("[data-handheld-busy]").forEach(input => {
      delete input.dataset.handheldBusy;
      if (root?.dataset.handheldRevoked !== "true") input.disabled = false;
    });
    const pending = document.getElementById("handheld-pending");
    if (pending) pending.hidden = true;
    importPairingLink(); mountLifetime();
  }
  document.addEventListener("click", async event => {
    const button = event.target?.closest?.("[data-copy-pairing]");
    if (!button) return;
    const input = document.getElementById("phone-pairing-code");
    const status = document.querySelector("[data-copy-pairing-status]");
    if (!input || !status) return;
    try {
      await navigator.clipboard.writeText(input.value);
      status.textContent = "Code copied. Paste it only into your demo phone connection form.";
    } catch (_) {
      input.focus(); input.select();
      status.textContent = "Copy this selected code, then enter it on your phone.";
    }
  });
  window.addEventListener("pageshow", resume);
  document.addEventListener("htmx:historyRestore", resume);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) mountLifetime(); });
  window.addEventListener("hashchange", importPairingLink);
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", resume);
  else resume();
})();
