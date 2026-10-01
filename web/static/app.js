(() => {
  let focusBeforeSwap = "";

  function showRequestError(message) {
    const notice = document.getElementById("network-error");
    if (!notice) return;
    const firstFailure = notice.hidden;
    notice.textContent = message;
    notice.hidden = false;
    // A failed background tracker poll should not keep pulling keyboard focus.
    if (firstFailure) notice.focus({ preventScroll: true });
  }

  document.addEventListener("htmx:sendError", () => {
    showRequestError(
      "We couldn’t reach the market. Try again when the connection returns.",
    );
  });

  document.addEventListener("htmx:responseError", () => {
    showRequestError(
      "This request couldn’t be completed. Refresh the page and try again.",
    );
  });

  document.addEventListener("htmx:beforeSwap", (event) => {
    if (event.detail.target?.id !== "workspace") return;
    const focused = document.activeElement;
    focusBeforeSwap = focused?.id || "";
  });

  document.addEventListener("htmx:afterSwap", (event) => {
    // Polling replaces only the tracker; it must not refocus old form errors.
    if (event.detail.target?.id !== "workspace") return;
    const error = document.querySelector(".notice.error:not([hidden])");
    if (error) {
      error.focus({ preventScroll: true });
    } else if (focusBeforeSwap && document.activeElement === document.body) {
      const replacement = document.getElementById(focusBeforeSwap);
      if (replacement && !replacement.disabled) {
        replacement.focus({ preventScroll: true });
      }
    }
    focusBeforeSwap = "";
  });
})();
