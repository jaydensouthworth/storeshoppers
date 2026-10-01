document.addEventListener("htmx:sendError", () => {
  const notice = document.getElementById("network-error");
  if (notice) {
    notice.hidden = false;
    notice.focus();
  }
});
document.addEventListener("htmx:responseError", () => {
  const notice = document.getElementById("network-error");
  if (notice) {
    notice.textContent =
      "This request couldn’t be completed. Refresh the page and try again.";
    notice.hidden = false;
  }
});
document.addEventListener("htmx:afterSwap", () => {
  const error = document.querySelector(".notice.error:not([hidden])");
  if (error) error.focus({ preventScroll: true });
});
