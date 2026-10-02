/* This controller owns only order messages. Scanner/picker request state is separate. */
(function () {
  "use strict";
  const LIMITS = Object.freeze({characters:500, bytes:2000, activePoll:5000, quietPoll:15000, requestTimeout:15000});
  function utf8Length(value) {
    let bytes = 0;
    for (const point of value) { const n = point.codePointAt(0); bytes += n <= 0x7f ? 1 : n <= 0x7ff ? 2 : n <= 0xffff ? 3 : 4; }
    return bytes;
  }
  function boundedDraft(value) {
    let text = "", count = 0, bytes = 0;
    for (const point of String(value)) {
      const size = utf8Length(point);
      if (count === LIMITS.characters || bytes + size > LIMITS.bytes) break;
      text += point; count++; bytes += size;
    }
    return text;
  }
  function install(env) {
    const doc = env.document, requests = new WeakMap(), activeDeadlines = new Set();
    const lanes = {feed:0, navigation:0, send:0};
    let root = null, generation = 0, navigation = 0, barrier = 0;
    let state = "closed", context = "", version = -1, revision = -1, expires = 0;
    let fresh = false, revoked = false, review = false, pending = null, uncertain = null;
    let planned = null, pollTimer = null, expiryTimer = null, feedPending = false, quiet = 0, deferredLatest = false, showLatest = false;
    const id = name => doc.getElementById(name);
    const field = name => id("order-message-form")?.querySelector('[name="' + name + '"]');
    const online = () => env.navigator.onLine !== false;
    const visible = () => !doc.hidden;
    const live = () => root && root === id("order-messages-workspace") && root.isConnected !== false;
    const older = () => id("order-message-feed")?.dataset.messagesOlder === "true";
    const canWrite = () => live() && !revoked && fresh && state === "open" && !review && online() && visible();
    function notice(text, error = false, focus = false) {
      const element = id("messages-notice"); if (!element) return;
      element.textContent = text; element.hidden = !text;
      element.classList.toggle("is-error", error);
      if (focus) { element.focus({preventScroll:true}); element.scrollIntoView({block:"nearest"}); }
    }
    function payload() {
      const values = {};
      for (const key of ["csrf","conversation_key","assignment_id","assignment_version","command_key","body"]) values[key] = field(key)?.value || "";
      return values;
    }
    function setField(name, value) { const input = field(name); if (input) input.value = String(value); }
    function metadata(element) {
      const d = element?.dataset || {};
      return {context:d.messagesContext || "", version:Number(d.messagesContextVersion), revision:Number(d.messagesRevision), expires:Number(d.messagesExpires),
        state:d.messagesState || (d.messagesCanSend === "true" ? "open" : "closed"), reason:d.messagesReason || "", assignment:d.messagesAssignmentName || ""};
    }
    function render() {
      const input = field("body"), sending = Boolean(pending), locked = sending || Boolean(uncertain);
      if (input) {
        input.readOnly = locked || revoked || !fresh || state !== "open" || review || !online() || !visible();
        const count = [...input.value].length, bytes = utf8Length(input.value), valid = count <= LIMITS.characters && bytes <= LIMITS.bytes;
        const counter = id("message-limit");
        if (counter) { counter.textContent = count + " / 500 characters · " + bytes + " / 2,000 UTF-8 bytes"; counter.classList.toggle("is-invalid", !valid); }
        const send = id("message-send");
        if (send) { send.disabled = !canWrite() || locked || !valid || !input.value.trim(); send.textContent = sending ? "Sending…" : "Send message →"; }
      }
      const check = id("message-check"), retry = id("message-retry"), controls = id("message-uncertain"), reviewButton = id("message-review");
      if (controls) controls.hidden = !uncertain;
      if (check) check.disabled = sending || !online() || !visible() || revoked || !fresh;
      if (retry) retry.disabled = sending || !uncertain || !canWrite();
      if (reviewButton) { reviewButton.hidden = !review || Boolean(uncertain) || revoked; reviewButton.disabled = sending || !online() || !visible() || !fresh; }
      const readOnly = id("message-readonly");
      if (readOnly) { readOnly.hidden = state === "open" && !revoked; if (!readOnly.hidden && !readOnly.textContent) readOnly.textContent = "This conversation is currently read-only."; }
      if (root) root.dataset.messagesBusy = sending ? "true" : "false";
    }
    function clearDeadline(request) {
      if (request?.deadline !== undefined) { env.clearTimeout(request.deadline); delete request.deadline; }
      activeDeadlines.delete(request);
    }
    function clearDeadlines() { for (const request of activeDeadlines) clearDeadline(request); }
    function clearTimer() { if (pollTimer !== null) env.clearTimeout(pollTimer); pollTimer = null; }
    function schedule() {
      clearTimer();
      if (!live() || revoked || !visible() || !online() || older() || state === "closed") return;
      pollTimer = env.setTimeout(() => { pollTimer = null; refresh(); }, quiet >= 3 ? LIMITS.quietPoll : LIMITS.activePoll);
    }
    function expireAt(seconds) {
      if (expiryTimer !== null) env.clearTimeout(expiryTimer); expiryTimer = null;
      if (!Number.isFinite(seconds) || seconds <= 0) return;
      const expiryMessage = root?.dataset.messagesPhone === "true" ? "This phone connection expired. Reconnect from the pick list to view messages again." : "This customer session expired. Return to the order before viewing messages again.";
      const remaining = seconds * 1000 - Date.now();
      if (remaining <= 0) { revoke(expiryMessage); return; }
      const owner = root;
      expiryTimer = env.setTimeout(() => { if (owner === root && live()) revoke(expiryMessage); }, Math.min(remaining,2147483647));
    }
    function revoke(text) {
      revoked = true; fresh = false; state = "revoked"; barrier++; generation++;
      pending = null; uncertain = null; clearTimer(); clearDeadlines();
      if (expiryTimer !== null) env.clearTimeout(expiryTimer); expiryTimer = null;
      // A disconnected phone must not retain an authorized transcript or draft.
      const feed = id("order-message-feed"); if (feed) feed.replaceChildren();
      setField("body", ""); setField("command_key", "");
      const hint = id("messages-new"); if (hint) hint.hidden = true;
      notice(text || "Access to this conversation ended. Return to your order or reconnect your phone.",true,true);
      const status = id("messages-authority"); if (status) status.textContent = "Conversation access ended";
      render();
    }
    function applyAuthority(meta, request, explicit) {
      if (revoked || !Number.isFinite(meta.version) || meta.version < version) return false;
      if (request && request.barrier !== barrier && (!explicit || (meta.version <= version && (!Number.isFinite(meta.revision) || meta.revision <= revision)))) return false;
      const changed = context && meta.context && meta.context !== context;
      const restrictive = meta.state !== "open";
      if (changed || restrictive) {
        barrier++;
        if (meta.state === "closed" || meta.state === "unassigned") review = false;
        else if (changed) review = true;
      }
      // A feed cannot lift a refusal/context barrier, even when server revisions tie.
      if (!explicit && !changed && state !== "open" && meta.state === "open") { fresh = true; review = true; render(); return false; }
      if (meta.context) context = meta.context;
      version = meta.version; state = meta.state; expires = meta.expires || 0;
      if (Number.isFinite(meta.revision)) revision = Math.max(revision, meta.revision);
      fresh = true;
      if (state === "closed" && uncertain) notice("This conversation is closed and read-only. The earlier send is still uncertain; use Check send result to find out whether it was saved.");
      if (changed && !pending && !uncertain) {
        if (state === "closed") notice("This conversation is closed and read-only. Saved notes are still available." + (field("body")?.value.trim() ? " Your unsent draft is kept but cannot be sent." : ""));
        else if (state === "unassigned") notice("This conversation is read-only until a shopper is assigned. Your saved notes and any unsent draft are kept.");
        else notice("The assigned shopper changed. Check / review this draft before deciding whether to send it.",true);
      }
      const status = id("messages-authority");
      if (status) status.textContent = meta.reason || (state === "open" ? "Conversation open · Sent means saved, not read." : "Read-only conversation");
      const readOnly = id("message-readonly"); if (readOnly) readOnly.textContent = meta.reason || "This conversation is currently read-only.";
      const label = root?.querySelector("[data-message-assignment]");
      if (label) { const small = doc.createElement("small"); small.textContent = "Fictional shopper"; label.replaceChildren(doc.createTextNode(meta.assignment || "No shopper assigned"),small); }
      expireAt(expires); render(); return !revoked;
    }
    function headerMeta(xhr, fallback) {
      const get = name => xhr.getResponseHeader(name);
      const cv = get("X-Messages-Context-Version"), rev = get("X-Messages-Revision"), exp = get("X-Messages-Expires");
      return {...fallback, context:get("X-Messages-Context") || fallback.context, version:cv === null ? fallback.version : Number(cv),
        revision:rev === null ? fallback.revision : Number(rev), expires:exp === null ? fallback.expires : Number(exp), state:get("X-Messages-State") || fallback.state};
    }
    function obsolete(request) {
      return !request || request.finished || !live() || request.root !== root || request.generation !== generation || request.sequence !== lanes[request.lane] || (request.lane !== "send" && request.navigation !== navigation);
    }
    function parseRegion(xhr, region) {
      try { return new env.DOMParser().parseFromString(xhr.responseText || "", "text/html").getElementById(region); }
      catch (_) { return null; }
    }
    function call(method, url, options) {
      if (!env.htmx) return false;
      try { const result = env.htmx.ajax(method,url,options); result?.catch?.(() => {}); return true; }
      catch (_) { return false; }
    }
    function refresh(force = false) {
      if (!live() || revoked || !visible() || !online() || feedPending || (older() && !force)) { schedule(); return; }
      feedPending = true;
      const url = root.dataset.messagesFeedUrl;
      if (!call("GET",url,{source:id("order-message-feed"),target:"#order-message-feed",swap:older() ? "none" : "outerHTML"})) { feedPending = false; schedule(); }
    }
    function ambiguous(request) {
      if (obsolete(request) || request.lane !== "send") return;
      if (request.payload) uncertain = {...request.payload}; pending = null;
      const recovery = state !== "open" ? "Check send result. This conversation is read-only, so another send is not available." : review ? "Check send result before reviewing the current assignment." : "Check send result, or explicitly retry the same message.";
      notice("We couldn’t confirm whether this exact note was saved. " + recovery + " It will not be sent automatically.",true,true);
      render();
      // Establish the uncertain payload first, then retire this transport. An
      // abort or a late load must never acknowledge over a subsequent lookup.
      clearDeadline(request); request.finished = true; schedule();
    }
    function submit(kind) {
      if (!live() || revoked || pending) return;
      if (!online() || !visible() || !fresh) { notice("Refresh the conversation after reconnecting before sending. Nothing was sent.",true,true); render(); return; }
      const isCheck = kind === "check" || kind === "review";
      if (!isCheck && !canWrite()) { notice("This draft needs a fresh review, or the conversation is read-only. Nothing was sent.",true,true); return; }
      if (kind === "send" && uncertain) return;
      let body = field("body")?.value || "";
      if (!isCheck && (!body.trim() || boundedDraft(body) !== body)) {
        setField("body",boundedDraft(body)); notice("Use 1–500 characters and at most 2,000 UTF-8 bytes. Review the retained text, then send again.",true,true); render(); return;
      }
      const values = uncertain ? {...uncertain} : payload();
      if (!values.command_key) { notice("Reload this conversation to get a fresh send form.",true,true); return; }
      planned = {kind, payload:values};
      const success = call("POST",root.dataset.messagesPath + (isCheck ? "/check" : ""),{source:id("order-message-form"),target:"#order-message-composer",swap:"none",values});
      if (!success) { planned = null; notice("The message could not be started. Your draft is still here.",true,true); }
    }
    function rememberScroll() {
      const feed = id("order-message-feed"), scroll = feed?.querySelector(".messages-scroll");
      if (!scroll) return null;
      const viewport = scroll.getBoundingClientRect();
      const anchor = [...feed.querySelectorAll("[data-message-revision]")].find(item => item.getBoundingClientRect().bottom >= viewport.top);
      return {top:scroll.scrollTop, bottom:scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 48, anchor:anchor?.id, offset:anchor ? anchor.getBoundingClientRect().top - viewport.top : 0, revision:Number(feed.dataset.messagesRevision)};
    }
    function restoreScroll(saved, isNavigation) {
      const feed = id("order-message-feed"), scroll = feed?.querySelector(".messages-scroll"); if (!scroll || !saved) return;
      if (isNavigation || showLatest) { scroll.scrollTop = older() ? 0 : scroll.scrollHeight; scroll.focus({preventScroll:true}); showLatest = false; deferredLatest = false; const hint = id("messages-new"); if (hint) hint.hidden = true; return; }
      const anchor = saved.anchor && id(saved.anchor);
      scroll.scrollTop = saved.bottom ? scroll.scrollHeight : anchor ? scroll.scrollTop + anchor.getBoundingClientRect().top - scroll.getBoundingClientRect().top - saved.offset : saved.top;
      if (!saved.bottom && Number(feed.dataset.messagesRevision) > saved.revision) { const hint = id("messages-new"); if (hint) hint.hidden = false; }
    }
    function reconcile(request, xhr, composer) {
      if (obsolete(request) || !composer) return;
      const result = xhr.getResponseHeader("X-Messages-Result"), ack = xhr.getResponseHeader("X-Messages-Key");
      const meta = headerMeta(xhr,metadata(composer));
      const sent = result === "sent" && ack === request.payload?.command_key;
      const rejected = result === "invalid" || result === "stale";
      const incoming = name => composer.querySelector('[name="' + name + '"]')?.value;
      if (!sent && !rejected) {
        if (result === "absent") {
          const original = request.payload;
          const sameContext = meta.context === [original.conversation_key,original.assignment_id,original.assignment_version].join("/");
          const sameIdentity = ["conversation_key","assignment_id","assignment_version","command_key"].every(name => incoming(name) === original[name]);
          // HTML parsing normalizes CRLF; the server trims outer whitespace.
          // Compare that normalization only, retaining the original body bytes.
          const normalized = body => body.replace(/\r\n/g,"\n").trim();
          const sameBody = typeof incoming("body") === "string" && normalized(incoming("body")) === normalized(original.body);
          if (sameContext && sameIdentity && sameBody && applyAuthority(meta,request,true)) {
            if (revoked || obsolete(request)) return;
            review = false;
            const refreshedCSRF = incoming("csrf");
            if (refreshedCSRF === original.csrf || /^[a-f0-9]{64}$/.test(refreshedCSRF || "")) {
              // Rotate authentication only. Absence is not a terminal result:
              // never replace its key, body or assignment with a new intent.
              request.payload = {...original,csrf:refreshedCSRF};
              setField("csrf",refreshedCSRF);
            }
          }
        }
        ambiguous(request); return;
      }
      const accepted = applyAuthority(meta,request,true);
      if (revoked || obsolete(request)) return;
      const current = field("body")?.value || "", owned = current === request.payload?.body;
      if (accepted) {
        for (const name of ["csrf","conversation_key","assignment_id","assignment_version"]) if (incoming(name) !== undefined) setField(name,incoming(name));
        review = false;
      }
      const nextKey = incoming("command_key");
      if (nextKey && nextKey !== request.payload?.command_key) setField("command_key",nextKey);
      else if (sent) { fresh = false; notice("The note was saved, but this form needs to be reloaded before another send.",true); }
      if (owned) setField("body",sent ? "" : boundedDraft(incoming("body") ?? current));
      uncertain = null; pending = null;
      root.querySelectorAll("[data-messages-server-feedback]").forEach(element => element.remove());
      if (sent) { notice("Sent · your note was saved with this order."); quiet = 0; if (!older()) refresh(); }
      else notice(composer.dataset.messagesServerError || composer.dataset.messagesServerMessage || "This note was not sent. Review the retained draft before sending again.",true,true);
      render();
      if (owned && !revoked) field("body")?.focus({preventScroll:true});
    }
    doc.addEventListener("submit", event => {
      if (event.target?.id !== "order-message-form") return;
      const button = event.submitter;
      const kind = button?.matches?.("[data-messages-check]") ? "check" : button?.matches?.("[data-messages-retry]") ? "retry" : "send";
      if (env.htmx) { event.preventDefault(); event.stopImmediatePropagation(); submit(kind); }
      else if (!canWrite() && kind !== "check") { event.preventDefault(); notice("Refresh the conversation before sending this draft.",true,true); }
    },true);
    doc.addEventListener("click",event => {
      if (event.target?.closest?.("#message-review")) { event.preventDefault(); submit("review"); }
      if (event.target?.closest?.("#messages-new")) {
        if (deferredLatest) { showLatest = true; refresh(true); }
        else { const scroll = id("order-message-feed")?.querySelector(".messages-scroll"); if (scroll) { scroll.scrollTop = scroll.scrollHeight; scroll.focus({preventScroll:true}); } id("messages-new").hidden = true; }
      }
    });
    doc.addEventListener("input",event => {
      if (event.target?.id !== "order-message-body") return;
      quiet = 0; render();
    });
    doc.addEventListener("htmx:beforeRequest",event => {
      const element = event.detail?.elt, xhr = event.detail?.xhr;
      if (!live() || !element || !root.contains(element) || !xhr) return;
      const send = element.id === "order-message-form";
      const link = element.closest?.("[data-messages-navigation]");
      const lane = send ? "send" : link ? "navigation" : "feed";
      if (revoked || !online() || !visible() || (send && !planned)) { event.preventDefault(); return; }
      if (lane === "navigation") { navigation++; clearTimer(); feedPending = false; }
      const request = {root,generation,navigation,barrier,lane,sequence:++lanes[lane],payload:send ? planned?.payload : null,kind:send ? planned?.kind : null,history:link?.dataset.messagesNavigation === "older",scroll:send ? null : rememberScroll()};
      if (send) { pending = request.payload; planned = null; }
      else feedPending = true;
      requests.set(xhr,request);
      if (send) {
        activeDeadlines.add(request);
        request.deadline = env.setTimeout(() => {
          if (!activeDeadlines.has(request)) return;
          clearDeadline(request);
          if (obsolete(request)) return;
          ambiguous(request);
          // Abort releases HTMX's form lock. Persistence remains uncertain:
          // aborting the transport cannot roll back an in-flight server write.
          xhr.abort?.();
        },LIMITS.requestTimeout);
      }
      render();
    });
    doc.addEventListener("htmx:beforeOnLoad",event => {
      const xhr = event.detail?.xhr, request = requests.get(xhr); if (!request) return;
      if (obsolete(request)) { event.preventDefault(); return; }
      clearDeadline(request);
      if (xhr.getResponseHeader("X-Messages-Access") === "ended" || xhr.getResponseHeader("X-Messages-State") === "revoked") { event.preventDefault(); revoke(); return; }
      if (request.lane !== "send" && request.barrier !== barrier && !request.history) { event.preventDefault(); return; }
      // This dedicated UI has no redirect response contract. Never let a late
      // response header navigate away from a newer draft or authorization state.
      for (const header of ["HX-Redirect","HX-Location","HX-Refresh"]) if (xhr.getResponseHeader(header)) { event.preventDefault(); if (request.lane === "send") ambiguous(request); return; }
      if (xhr.status !== 200) { event.preventDefault(); if (request.lane === "send") ambiguous(request); return; }
      const region = parseRegion(xhr,request.lane === "send" ? "order-message-composer" : "order-message-feed");
      if (!region) { event.preventDefault(); if (request.lane === "send") ambiguous(request); return; }
      if (request.lane === "send") {
        reconcile(request,xhr,region); request.reconciled = true;
      } else {
        const meta = headerMeta(xhr,metadata(region));
        const immutableHistory = request.history && meta.version === version && meta.context === context && Number.isFinite(meta.revision) && meta.revision < revision;
        if (meta.version < version || (meta.version === version && Number.isFinite(meta.revision) && meta.revision < revision && !immutableHistory)) { event.preventDefault(); return; }
        const beforeRevision = revision;
        // An immutable older page may predate a concurrent saved message.
        // Render that history without rolling back the newer composer authority.
        if (!immutableHistory && !applyAuthority(meta,request,request.lane === "navigation")) { event.preventDefault(); return; }
        quiet = meta.revision > beforeRevision ? 0 : quiet + 1;
        // A forced authorization refresh while reading history must not swap
        // the history page for latest messages or resume its polling.
        request.noSwap = older() && request.lane === "feed";
        if (request.lane === "feed" && !request.noSwap && !showLatest && request.scroll && !request.scroll.bottom && meta.revision > request.scroll.revision) {
          // Keep the visible page intact, including an anchor that may fall out
          // of the bounded latest-50 window. Only a deliberate click replaces it.
          request.noSwap = true; deferredLatest = true; const hint = id("messages-new"); if (hint) hint.hidden = false;
        }
      }
    });
    doc.addEventListener("htmx:beforeSwap",event => {
      const request = requests.get(event.detail?.xhr); if (!request) return;
      if (obsolete(request) || request.lane === "send" || request.noSwap) event.detail.shouldSwap = false;
    });
    doc.addEventListener("htmx:afterSwap",event => {
      const request = requests.get(event.detail?.xhr);
      if (!request || obsolete(request) || request.lane === "send") return;
      restoreScroll(request.scroll,request.lane === "navigation"); render();
    });
    doc.addEventListener("htmx:afterRequest",event => {
      const xhr = event.detail?.xhr, request = requests.get(xhr); clearDeadline(request); if (!request || obsolete(request)) return;
      if (request.lane === "send" && !request.reconciled && pending) ambiguous(request);
      if (request.lane !== "send") feedPending = false;
      render(); schedule();
    });
    for (const name of ["htmx:sendError","htmx:timeout","htmx:responseError"]) doc.addEventListener(name,event => {
      const request = requests.get(event.detail?.xhr); if (!request || obsolete(request)) return;
      if (request.lane === "send") ambiguous(request);
      else { feedPending = false; notice("Messages could not refresh. The saved notes shown here may be out of date.",true); schedule(); }
    });
    function suspend() {
      generation++; navigation++; clearTimer(); clearDeadlines(); feedPending = false; fresh = false;
      if (pending) { uncertain = {...pending}; pending = null; }
      render();
    }
    function resume() {
      if (!live()) { mount(); return; }
      suspend();
      if (online() && visible() && !revoked) { notice("Checking conversation access before sending…"); refresh(true); }
    }
    function mount() {
      const next = id("order-messages-workspace"); if (!next) return;
      if (root === next) return;
      clearDeadlines();
      root = next; generation++; navigation++; barrier++; revoked = false; pending = null; uncertain = null; review = false; deferredLatest = false; showLatest = false;
      context = ""; version = -1; revision = -1; state = "closed"; fresh = false;
      const composer = id("order-message-composer"), feed = id("order-message-feed");
      applyAuthority(metadata(feed || composer),null,true);
      if (composer?.dataset.messagesSendState === "unconfirmed") uncertain = payload();
      if (!visible() || !online()) fresh = false;
      const scroll = feed?.querySelector(".messages-scroll"); if (scroll && !older()) scroll.scrollTop = scroll.scrollHeight;
      render(); schedule();
    }
    doc.addEventListener("visibilitychange",() => { if (!visible()) suspend(); else resume(); });
    // Enhanced Older/Latest links stay in this document. Native hrefs remain
    // the no-script fallback, but this chat never starts HTMX's separate raw
    // history transport or replaces the composer from an old page snapshot.
    for (const name of ["htmx:historyCacheMiss","htmx:historyCacheHit"]) doc.addEventListener(name,event => {
      if (live()) event.preventDefault();
    });
    doc.addEventListener("htmx:historyRestore",resume);
    env.addEventListener("offline",() => { suspend(); notice("You’re offline. Sending is paused; reconnect to check conversation access.",true); });
    env.addEventListener("online",resume);
    env.addEventListener("pagehide",suspend);
    env.addEventListener("pageshow",event => { if (event.persisted) resume(); else mount(); });
    env.addEventListener("popstate",resume);
    if (doc.readyState === "loading") doc.addEventListener("DOMContentLoaded",mount); else mount();
    return {submit,refresh,resume,suspend,mount,getState:() => ({state,context,version,revision,fresh,revoked,review,pending,uncertain,barrier,lanes:{...lanes}})};
  }
  if (typeof module === "object" && module.exports) module.exports = {LIMITS,utf8Length,boundedDraft,install};
  else install(window);
})();
