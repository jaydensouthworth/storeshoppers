(() => {
  "use strict";

  const storageKey = "neighborhood-market-theme";
  const root = document.documentElement;
  const choices = ["system", "light", "dark"];
  let preference = "system";
  let unsaved = false;
  let systemTheme;

  function valid(value) {
    return choices.includes(value);
  }

  function readPreference() {
    try {
      const saved = window.localStorage.getItem(storageKey);
      return valid(saved) ? saved : "system";
    } catch {
      return null;
    }
  }

  try {
    systemTheme = window.matchMedia("(prefers-color-scheme: dark)");
  } catch {
    // A browser without matchMedia still has a usable explicit theme choice.
  }

  preference = readPreference() || "system";

  function applyTheme() {
    const dark = preference === "dark" ||
      (preference === "system" && systemTheme?.matches);
    root.setAttribute("data-theme", dark ? "dark" : "light");
  }

  function syncControls() {
    document.querySelectorAll("[data-theme-select]").forEach((select) => {
      select.value = preference;
    });
    document.querySelectorAll("[data-theme-control]").forEach((control) => {
      control.hidden = false;
    });
    document.querySelectorAll("[data-theme-status]").forEach((status) => {
      status.textContent = unsaved ? "Couldn’t save · this page only" : "";
    });
  }

  // This small local script runs in the head, before styles and first paint.
  applyTheme();

  document.addEventListener("change", (event) => {
    const select = event.target;
    if (!select?.matches?.("[data-theme-select]") || !valid(select.value)) return;
    preference = select.value;
    applyTheme();
    try {
      window.localStorage.setItem(storageKey, preference);
      unsaved = false;
    } catch {
      // Keep the choice for this document, including subsequent HTMX swaps.
      unsaved = true;
    }
    syncControls();
  });

  function followSystem() {
    if (preference === "system") applyTheme();
  }
  if (systemTheme?.addEventListener) {
    systemTheme.addEventListener("change", followSystem);
  } else if (systemTheme?.addListener) {
    systemTheme.addListener(followSystem);
  }

  window.addEventListener("storage", (event) => {
    if (event.key !== storageKey && event.key !== null) return;
    preference = valid(event.newValue) ? event.newValue : "system";
    unsaved = false;
    applyTheme();
    syncControls();
  });

  // The preference lives above #workspace; new fragments get fresh controls.
  document.addEventListener("htmx:afterSwap", syncControls);
  document.addEventListener("htmx:historyRestore", syncControls);
  window.addEventListener("pageshow", () => {
    if (!unsaved) preference = readPreference() || preference;
    applyTheme();
    syncControls();
  });
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", syncControls, { once: true });
  } else {
    syncControls();
  }
})();
