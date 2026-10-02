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

  function isDark() {
    return preference === "dark" ||
      (preference === "system" && !!systemTheme?.matches);
  }

  function applyTheme() {
    root.setAttribute("data-theme", isDark() ? "dark" : "light");
  }

  function syncControls() {
    const dark = isDark();
    document.querySelectorAll("[data-theme-toggle]").forEach((button) => {
      // Keep the accessible name stable; pressed describes the current theme.
      button.setAttribute("aria-pressed", String(dark));
      button.title = dark ? "Switch to light mode" : "Switch to dark mode";
    });
    document.querySelectorAll("[data-theme-system]").forEach((button) => {
      // aria-disabled keeps keyboard focus in place when this action is used.
      button.setAttribute("aria-disabled", String(preference === "system" && !unsaved));
    });
    document.querySelectorAll("[data-theme-preference]").forEach((status) => {
      const appearance = dark ? "Dark" : "Light";
      status.textContent = preference === "system" ?
        `Appearance: System (${appearance.toLowerCase()})` : `Appearance: ${appearance}`;
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

  function choose(value) {
    preference = value;
    applyTheme();
    try {
      window.localStorage.setItem(storageKey, preference);
      unsaved = false;
    } catch {
      // Keep the choice for this document, including subsequent HTMX swaps.
      unsaved = true;
    }
    syncControls();
  }

  // Native buttons provide Enter/Space activation. Delegation also catches
  // clicks on the SVG paths and buttons introduced by an HTMX replacement.
  document.addEventListener("click", (event) => {
    if (event.target?.closest?.("[data-theme-toggle]")) {
      choose(isDark() ? "light" : "dark");
    } else if (event.target?.closest?.("[data-theme-system]")) {
      if (preference !== "system" || unsaved) choose("system");
    }
  });

  function followSystem() {
    if (preference === "system") {
      applyTheme();
      syncControls();
    }
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
