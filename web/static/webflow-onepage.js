// One-page payment flows: live charge labels on the method buttons.
//
// The server renders each payment button's label with the charge computed at
// render time ("Pay ₦2,550 with card"). On a one-page flow the amount input is
// on the same page, so as the customer edits it this enhancement recomputes
// each button's label from that button's data-fee-* attributes. The recompute
// is display-only: the server always recalculates the charge from the posted
// amount, never from these labels.
//
// Progressive: without JS the buttons keep their render-time labels and the
// WhatsApp confirmation carries the exact charge — no correctness dependency.
// Buttons without data-fee-bps (e.g. "Bank transfer", whose DVA fee is quoted
// on the transfer instructions page) are left untouched.
(function () {
  "use strict";

  var amountInput = document.querySelector('input[name="amount_kobo"]');
  var buttons = Array.prototype.slice.call(
    document.querySelectorAll('.wf .actions button[data-fee-bps]')
  );
  if (!amountInput || buttons.length === 0) return;

  // Fee mirrors service.XegoCollectionFee: min(bps*amount/10000 + fixed, cap),
  // floored at the amount itself. Returns null when there is no amount yet.
  function charge(amount, bps, fixed, cap) {
    if (!(amount > 0)) return null;
    var fee = Math.floor((amount * bps) / 10000) + fixed;
    if (cap > 0 && fee > cap) fee = cap;
    if (fee > amount) fee = amount;
    return amount + fee;
  }

  // NGN formatting mirrors domain.FormatNGN: thousands separators, "₦".
  function ngn(kobo) {
    var whole = Math.round(kobo / 100);
    return "₦" + whole.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  }

  buttons.forEach(function (btn) {
    btn.dataset.labelBase = btn.textContent;
  });

  function update() {
    var raw = (amountInput.value || "").replace(/[^0-9.]/g, "");
    var kobo = Math.round(parseFloat(raw || "0") * 100);
    buttons.forEach(function (btn) {
      var bps = parseInt(btn.dataset.feeBps, 10);
      var fixed = parseInt(btn.dataset.feeFixed, 10);
      var cap = parseInt(btn.dataset.feeCap, 10);
      var c = charge(kobo, bps, fixed, cap);
      // Only rewrite labels that lead with "Pay ₦…"; a static base label
      // ("Bank transfer") has nothing live to show.
      if (c && /^Pay /.test(btn.dataset.labelBase)) {
        btn.textContent = btn.dataset.labelBase.replace(/^Pay [^ ]+/, "Pay " + ngn(c));
      } else {
        btn.textContent = btn.dataset.labelBase;
      }
    });
  }

  amountInput.addEventListener("input", update);
  update();
})();
