(() => {
  "use strict";
  const requests = new WeakMap();

  // Basket drafts remain in the current document and in-flight request only.
  // The checkout generation scopes recovery; no storage or mutation replay.
  function customerBasket() {
    return document.querySelector("form[data-customer-basket]");
  }

  function basketValues(form) {
    const quantities = {};
    form.querySelectorAll("[data-cart-quantity]").forEach((input) => {
      quantities[input.id] = input.value;
    });
    return { instructions: form.querySelector('[name="instructions"]').value, quantities };
  }

  function syncCustomerQuantities() {
    const form = customerBasket();
    if (!form) return;
    form.querySelectorAll("[data-cart-quantity]").forEach((input) => {
      const notice = document.getElementById(input.getAttribute("aria-describedby"));
      if (notice) notice.hidden = input.value === input.dataset.savedQuantity;
    });
  }

  function restoreBasketDraft(context) {
    const draft = context?.customer?.latest;
    const form = customerBasket();
    if (!draft || !form || form.dataset.customerBasket !== context.customer.key) return;
    if (draft.instructions !== context.customer.sent.instructions) {
      const value = draft.instructions.replace(/\r\n/g, "\n").replace(/[\x00-\x08\x0b-\x1f\x7f-\x9f]/g, "�");
      form.querySelector('[name="instructions"]').value = Array.from(value).slice(0, 500).join("");
    }
    form.querySelectorAll("[data-cart-quantity]").forEach((input) => {
      // Unchanged submitted values use the server's current saved/draft value.
      // Only edits made during the request override the response.
      if (Object.prototype.hasOwnProperty.call(draft.quantities, input.id) &&
          draft.quantities[input.id] !== context.customer.sent.quantities[input.id]) {
        input.value = draft.quantities[input.id];
      }
    });
    syncCustomerQuantities();
  }

  function restoreBasketFocus(context) {
    const draft = context?.customer?.latest;
    if (!draft?.focusID) return;
    const form = customerBasket();
    if (!form || form.dataset.customerBasket !== context.customer.key) return;
    const input = document.getElementById(draft.focusID);
    if (!input || !form.contains(input) || input.disabled) return;
    input.focus({ preventScroll: true });
    if (draft.selectionStart != null && input.setSelectionRange) {
      input.setSelectionRange(draft.selectionStart, draft.selectionEnd);
    }
  }

  function formContext(event) {
    const detail = event.detail || {};
    const element = detail.elt;
    const form = element?.tagName === "FORM" ? element : element?.closest?.("form");
    if (!form) return null;
    const submitted = detail.requestConfig?.triggeringEvent?.submitter;
    const active = document.activeElement;
    const button = submitted || (form.contains(active) ? active : null) || form.querySelector("button[id]");
    const scopes = [];
    let scope = form.closest("[data-feedback-scope]");
    while (scope) {
      if (scope.id) scopes.push(scope.id);
      scope = scope.parentElement?.closest("[data-feedback-scope]");
    }
    const selected = form.querySelector('input[name="replacement_id"]:checked, input[name="product_id"]:checked');
    const selectedProduct = selected && /^\d+$/.test(selected.value) ? selected.value : "";
    const context = { formID: form.id, buttonID: button?.id || "", scopes, selectedProduct };
    if (form.dataset?.customerBasket) {
      context.preferredID = button?.closest?.("[data-feedback-scope]")?.id;
      context.customer = { key: form.dataset.customerBasket, revision: form.dataset.basketRevision, sent: basketValues(form) };
    }
    return context;
  }

  function targetFor(context) {
    if (!context) return null;
    if (context.preferredID) {
      const preferred = document.getElementById(context.preferredID);
      if (preferred) return preferred;
    }
    if (context.formID) {
      const form = document.getElementById(context.formID);
      if (form) return form;
    }
    if (context.buttonID) {
      const button = document.getElementById(context.buttonID);
      if (button) return button.closest("form") || button.parentElement;
    }
    for (const id of context.scopes) {
      const scope = document.getElementById(id);
      if (scope) return scope;
    }
    return null;
  }

  function showFeedback(context, message, error, moveFocus = true) {
    const target = targetFor(context);
    let notice;
    if (target) {
      let disclosure = target.closest("details");
      while (disclosure) {
        disclosure.open = true;
        disclosure = disclosure.parentElement?.closest("details");
      }
      notice = target.querySelector(":scope > .action-feedback");
      if (!notice) {
        notice = document.createElement("div");
        if (target.tagName === "SECTION") target.insertBefore(notice, target.firstElementChild);
        else target.appendChild(notice);
      }
      notice.className = `notice action-feedback ${error ? "error" : "success"}`;
    } else {
      notice = document.getElementById("network-error");
      if (notice) notice.className = error ? "notice error" : "notice success";
    }
    if (!notice) return;
    const wasHidden = notice.hidden || !notice.textContent;
    notice.textContent = message;
    notice.setAttribute("role", error ? "alert" : "status");
    notice.tabIndex = -1;
    notice.hidden = false;
    if (moveFocus && (target || wasHidden)) {
      if (error) notice.focus({ preventScroll: true });
      notice.scrollIntoView({ block: "nearest", behavior: "auto" });
    }
    return notice;
  }

  document.addEventListener("htmx:beforeRequest", (event) => {
    if (!event.detail?.xhr) return;
    const context = formContext(event);
    if (context) requests.set(event.detail.xhr, context);
  });

  document.addEventListener("htmx:beforeSwap", (event) => {
    const context = requests.get(event.detail?.xhr);
    if (!context?.customer || event.detail.target?.id !== "workspace") return;
    const form = customerBasket();
    if (!form || form.dataset.customerBasket !== context.customer.key ||
        form.dataset.basketRevision !== context.customer.revision) {
      // A newer response/navigation has already replaced the initiating basket.
      event.detail.shouldSwap = false;
      return;
    }
    const latest = basketValues(form);
    const active = document.activeElement;
    if (active && form.contains(active) && (active.matches?.("[data-cart-quantity]") || active.id === "instructions")) {
      latest.focusID = active.id;
      latest.selectionStart = active.selectionStart;
      latest.selectionEnd = active.selectionEnd;
    }
    context.customer.latest = latest;
  });

  document.addEventListener("htmx:sendError", (event) => {
    showFeedback(requests.get(event.detail?.xhr),
      "We couldn’t confirm this change because the connection failed. Your entered values are kept. Check the current saved state before trying again.", true,
      !!requests.get(event.detail?.xhr));
  });

  document.addEventListener("htmx:responseError", (event) => {
    const xhr = event.detail?.xhr;
    const context = requests.get(xhr);
    const code = xhr?.getResponseHeader("X-Shop-Error");
    let message = "This change couldn’t be confirmed. Your entered values are kept. Refresh the current state before trying again.";
    if (code === "csrf-expired") {
      const token = xhr.getResponseHeader("X-Shop-CSRF");
      if (token && token.length <= 256) {
        document.querySelectorAll('input[name="csrf"]').forEach((input) => { input.value = token; });
        message = "This form expired after manager access changed. Your values are kept and the form is ready again. Review them, then submit this change again.";
      } else {
        message = "This session changed or expired. Your values are still shown, but the saved state may be different. Refresh this page before submitting again.";
      }
    } else if (code === "manager-expired") {
      message = "Manager access expired. Your values are kept. Sign in using the link below, then return to this form and review your change.";
    }
    const notice = showFeedback(context, message, true, !!context);
    if (code === "manager-expired" && notice) {
      const link = document.createElement("a");
      link.href = "/manager/login";
      link.target = "_blank";
      link.rel = "noopener";
      link.className = "form-recovery-link";
      link.textContent = "Sign in in a new tab";
      notice.appendChild(link);
    }
  });

  function syncQuantityControls() {
    document.querySelectorAll("form[data-quantity-editor]").forEach((form) => {
      const panel = form.querySelector("[data-picked-disposition]");
      if (!panel) return;
      const input = form.querySelector("[data-requested-quantity]");
      const quantity = Number(input.value);
      const needed = input.value !== "" && Number.isInteger(quantity) && quantity < Number(form.dataset.picked);
      panel.hidden = !needed;
      const choice = panel.querySelector("select");
      choice.disabled = !needed;
      choice.required = needed;
    });
  }
  function syncPickerForms() {
    document.querySelectorAll("form[data-picker-form]").forEach((form) => {
      const selected = form.querySelector('.product-picker-results input[type="radio"]:checked');
      const submit = form.querySelector("[data-picker-submit]");
      if (submit) submit.disabled = !selected;
      form.querySelectorAll?.("[data-picker-quantity]").forEach((input) => {
        input.disabled = !selected || input.dataset.pickerQuantity !== selected.value;
      });
    });
  }
  document.addEventListener("change", (event) => {
    if (event.target?.closest?.(".product-picker-results")) syncPickerForms();
    if (event.target?.matches?.("[data-requested-quantity]")) syncQuantityControls();
  });
  document.addEventListener("input", (event) => {
    if (event.target?.matches?.("[data-requested-quantity]")) syncQuantityControls();
    if (event.target?.matches?.("[data-cart-quantity]")) syncCustomerQuantities();
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" || !event.target?.matches?.("[data-cart-quantity]")) return;
    // Enter updates that row only; it must never renew or place an order.
    event.preventDefault();
    const button = document.getElementById(event.target.id.replace("quantity-", "update-"));
    if (button && !button.disabled) button.click();
  });
  document.addEventListener("DOMContentLoaded", () => { syncPickerForms(); syncQuantityControls(); syncCustomerQuantities(); });
  document.addEventListener("htmx:afterSwap", (event) => {
    syncPickerForms();
    syncQuantityControls();
    if (event.detail.target?.id !== "workspace") return;
    const context = requests.get(event.detail?.xhr);
    restoreBasketDraft(context);
    syncCustomerQuantities();
    const weightReview = document.querySelector(".weight-review");
    if (weightReview && context?.formID?.startsWith("measure-line-")) {
      weightReview.tabIndex = -1;
      weightReview.focus({ preventScroll: true });
      weightReview.scrollIntoView({ block: "start", behavior: "auto" });
      return;
    }
    const error = document.querySelector("#workspace .notice.error:not([hidden])");
    const success = document.querySelector("#workspace .notice.success:not([hidden])");
    const notice = error || success;
    if (!error && context?.selectedProduct) {
      const changedItem = document.querySelector(`[data-workflow-product="${context.selectedProduct}"]`);
      if (changedItem?.id) context.preferredID = changedItem.id;
    }
    if (notice && context) {
      const message = notice.textContent;
      if (targetFor(context)) notice.hidden = true;
      showFeedback(context, message, !!error);
    } else if (error) {
      error.focus({ preventScroll: true });
      error.scrollIntoView({ block: "nearest", behavior: "auto" });
    }
    if (!error) restoreBasketFocus(context);
    if (!error && context?.buttonID && document.activeElement === document.body) {
      const button = document.getElementById(context.buttonID);
      if (button && !button.disabled) button.focus({ preventScroll: true });
    }
  });
})();
