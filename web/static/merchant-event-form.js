// Merchant event form: ticket-tier rows, custom-field rows, and the Active
// toggle. Served from /static so the strict CSP (script-src 'self') allows
// it — the logic previously lived in an inline <script> the policy silently
// blocked, and the rows' Active checkbox used an inline onchange that was
// equally blocked (and dereferenced a null sibling).
(function() {
  document.addEventListener('DOMContentLoaded', function() {
    // ---- Ticket tiers -------------------------------------------------
    var tierAdd = document.getElementById('tier-add-btn');
    var tierList = document.getElementById('tier-list');
    if (tierAdd && tierList) {
      tierAdd.addEventListener('click', function() {
        var div = document.createElement('div');
        div.className = 'cf-row';
        div.innerHTML =
          '<input name="tier_name[]" placeholder="Tier name (e.g. VIP)">' +
          '<input type="number" name="tier_price_kobo[]" placeholder="Price (kobo)" min="1">' +
          '<input type="number" name="tier_capacity[]" placeholder="Capacity (-1 = unlimited)" value="-1">' +
          '<input type="hidden" name="tier_active[]" value="1">' +
          '<label class="checkbox-row"><input type="checkbox" checked> Active</label>' +
          '<button type="button" class="btn-sm danger cf-remove-btn">✕</button>';
        tierList.appendChild(div);
      });
      // Row remove works for server-rendered and added rows. Only the
      // custom-field list had a delegated listener before, so tier-row ✕
      // buttons did nothing.
      tierList.addEventListener('click', function(e) {
        if (e.target.classList.contains('cf-remove-btn')) {
          e.target.parentElement.remove();
        }
      });
      // The Active checkbox mirrors into the hidden tier_active[] input the
      // server reads; saveEventTiers treats a missing/other value as active.
      tierList.addEventListener('change', function(e) {
        var cb = e.target;
        if (cb.type !== 'checkbox' || !cb.closest('.cf-row')) return;
        var hidden = cb.closest('.cf-row').querySelector('input[type=hidden][name="tier_active[]"]');
        if (hidden) hidden.value = cb.checked ? '1' : '0';
      });
    }

    // ---- Custom fields (same markup contract as merchant-services.js,
    // which serves the service form; this page must load only one of the
    // two scripts or add clicks would fire twice). ----------------------
    var cfAdd = document.getElementById('cf-add-btn');
    var cfList = document.getElementById('cf-list');
    if (cfAdd && cfList) {
      cfAdd.addEventListener('click', function() {
        var div = document.createElement('div');
        div.className = 'cf-row';
        div.innerHTML =
          '<input name="cf_name[]" placeholder="Field name (e.g. Seat number)">' +
          '<select name="cf_type[]"><option value="text">Text</option><option value="number">Number</option><option value="select">Select</option></select>' +
          '<input name="cf_options[]" placeholder="Options (comma-separated, for Select)">' +
          '<label class="checkbox-row"><input type="checkbox" name="cf_required[]" value="1"> Required</label>' +
          '<button type="button" class="btn-sm danger cf-remove-btn">✕</button>';
        cfList.appendChild(div);
      });
      cfList.addEventListener('click', function(e) {
        if (e.target.classList.contains('cf-remove-btn')) {
          e.target.parentElement.remove();
        }
      });
    }
  });
})();
