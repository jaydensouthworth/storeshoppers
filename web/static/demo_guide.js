(() => {
  "use strict";

  // HTMX may evaluate a script again while restoring history. Keep one set of
  // listeners, and keep an unsaved dismissal for the lifetime of this document.
  if (window.neighborhoodDemoGuide) {
    window.neighborhoodDemoGuide.refresh();
    return;
  }

  const storageKey = "neighborhood-market-demo-guide";
  const guideSelector = "[data-demo-guide]";
  const targetSelector = "[data-demo-guide-target]";
  let initialized = false;
  let dismissed = false;
  let explicitlyOpened = false;
  let scheduled = false;
  let observedGuide;
  let observedTarget;
  const observer = window.ResizeObserver ? new window.ResizeObserver(schedule) : null;

  function eachStorage(action) {
    ["localStorage", "sessionStorage"].forEach((name) => {
      try { action(window[name]); } catch {
        // Private browsing, storage policies and quota failures are harmless.
      }
    });
  }

  function initializePreference() {
    if (initialized) return;
    initialized = true;
    let canRemember = false;
    const readableStores = [];
    eachStorage((storage) => {
      const value = storage.getItem(storageKey);
      if (value === "dismissed") dismissed = true;
      readableStores.push(storage);
    });
    // Read both before writing: a prior session-only dismissal must migrate to
    // local storage when it becomes available again, never turn back into seen.
    readableStores.forEach((storage) => {
      // Probe writes too: readable but full storage cannot retain a dismissal.
      try {
        storage.setItem(storageKey, dismissed ? "dismissed" : "seen");
        canRemember = true;
      } catch {
        // Another readable store may still be writable.
      }
    });
    // If both stores are unusable, opt into the guide with the footer control
    // instead of nagging on every full navigation. No server preference needed.
    if (!canRemember) dismissed = true;
  }

  function viewport() {
    const visual = window.visualViewport;
    const root = document.documentElement;
    const left = visual?.offsetLeft || 0;
    const top = visual?.offsetTop || 0;
    const width = visual?.width || root.clientWidth || window.innerWidth;
    const height = visual?.height || window.innerHeight || root.clientHeight;
    return { left, top, width, height, right: left + width, bottom: top + height };
  }

  function clamp(value, min, max) {
    return Math.max(min, Math.min(value, max));
  }

  function refresh() {
    const guide = document.querySelector(guideSelector);
    const target = document.querySelector(targetSelector);
    document.querySelectorAll("[data-demo-guide-reopen]").forEach((button) => {
      button.hidden = !guide || !target;
    });
    if (guide !== observedGuide || target !== observedTarget) {
      observer?.disconnect();
      if (guide) observer?.observe(guide);
      if (target) observer?.observe(target);
      observedGuide = guide;
      observedTarget = target;
    }
    if (!guide) return;
    guide.hidden = true;
    if (!target) return;
    initializePreference();
    if (dismissed) return;

    const view = viewport();
    const basket = document.querySelector(".phone-basket")?.getBoundingClientRect();
    // The mobile basket is a fixed action bar. Treat its top edge as the bottom
    // of the available viewport instead of obscuring a purchase/navigation step.
    if (basket?.width && basket.height && basket.top < view.bottom &&
        basket.bottom > view.top && basket.left < view.right && basket.right > view.left) {
      view.bottom = Math.min(view.bottom, basket.top);
    }
    const anchor = target.getBoundingClientRect();
    // An off-screen or clipped target should never leave a floating pointer.
    if (!anchor.width || !anchor.height || anchor.top < view.top ||
        anchor.bottom > view.bottom || anchor.left < view.left || anchor.right > view.right) return;

    const margin = 12;
    const gap = 20;
    guide.style.maxWidth = `${Math.max(0, view.width - margin * 2)}px`;
    guide.style.visibility = "hidden";
    guide.hidden = false;
    const box = guide.getBoundingClientRect();
    let top;
    let placement;
    if (anchor.bottom + gap + box.height <= view.bottom - margin) {
      top = anchor.bottom + gap;
      placement = "below";
    } else if (anchor.top - gap - box.height >= view.top + margin) {
      top = anchor.top - gap - box.height;
      placement = "above";
    } else {
      // On extremely short/zoomed viewports, keep the target usable. The footer
      // can reopen the guide after scrolling it into a position with more room.
      guide.hidden = true;
      guide.style.visibility = "";
      return;
    }
    const center = anchor.left + anchor.width / 2;
    const left = clamp(center - box.width + 36,
      view.left + margin, view.right - box.width - margin);
    const arrow = clamp(center - left, 14, box.width - 14);
    // The arrow may shift within a wide link near an edge, but always points
    // inside the target, and neither its stem nor the callout covers that link.
    if (left + arrow < anchor.left || left + arrow > anchor.right) {
      guide.hidden = true;
      guide.style.visibility = "";
      return;
    }
    guide.style.left = `${left}px`;
    guide.style.top = `${top}px`;
    guide.style.setProperty("--demo-guide-arrow-x", `${arrow}px`);
    guide.setAttribute("data-placement", placement);
    guide.style.visibility = "";
  }

  function schedule() {
    if (scheduled) return;
    scheduled = true;
    window.requestAnimationFrame(() => {
      scheduled = false;
      refresh();
    });
  }

  function syncSavedDismissal() {
    if (!explicitlyOpened) {
      eachStorage((storage) => {
        if (storage.getItem(storageKey) === "dismissed") dismissed = true;
      });
    }
    schedule();
  }

  function dismiss() {
    dismissed = true;
    explicitlyOpened = false;
    eachStorage((storage) => storage.setItem(storageKey, "dismissed"));
    refresh();
  }

  document.addEventListener("click", (event) => {
    const close = event.target?.closest?.("[data-demo-guide-close]");
    const reopen = event.target?.closest?.("[data-demo-guide-reopen]");
    const completed = event.target?.closest?.(targetSelector);
    const guide = document.querySelector(guideSelector);
    const target = document.querySelector(targetSelector);
    if (!guide || !target) return;
    if ((close && guide.contains(close)) || completed === target) {
      dismiss();
      // Following Employees completes the guide, including modifier-clicks.
      // Let the native link navigate normally without changing its focus.
      if (close) target.focus({ preventScroll: true });
    } else if (reopen) {
      initializePreference();
      dismissed = false;
      explicitlyOpened = true;
      // Reopening is an explicit, document-local exception to a saved dismissal.
      target.scrollIntoView({ block: "center", inline: "nearest", behavior: "instant" });
      refresh();
      if (!guide.hidden) guide.querySelector("[data-demo-guide-close]")?.focus({ preventScroll: true });
      else target.focus({ preventScroll: true });
    }
  });

  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" || event.defaultPrevented) return;
    const guide = document.querySelector(guideSelector);
    const target = document.querySelector(targetSelector);
    if (!guide || guide.hidden || !target) return;
    const focusInside = guide.contains(document.activeElement);
    dismiss();
    // Escape is useful anywhere, but must not steal focus from product search,
    // quantity fields or other unrelated controls.
    if (focusInside) target.focus({ preventScroll: true });
  });

  ["htmx:afterSwap", "htmx:afterSettle", "htmx:historyRestore"].forEach((name) => {
    document.addEventListener(name, schedule);
  });
  window.addEventListener("resize", schedule);
  window.addEventListener("scroll", schedule, { capture: true, passive: true });
  window.visualViewport?.addEventListener("resize", schedule);
  window.visualViewport?.addEventListener("scroll", schedule);
  window.addEventListener("pageshow", syncSavedDismissal);
  window.addEventListener("storage", (event) => {
    if (event.key !== storageKey || event.newValue !== "dismissed") return;
    dismissed = true;
    explicitlyOpened = false;
    schedule();
  });
  document.fonts?.ready?.then(schedule);
  window.neighborhoodDemoGuide = { refresh: schedule };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", schedule, { once: true });
  } else {
    schedule();
  }
})();
