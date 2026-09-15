(function () {
  'use strict';

  var MONTHS = [
    'January', 'February', 'March', 'April', 'May', 'June',
    'July', 'August', 'September', 'October', 'November', 'December'
  ];

  function pad(n) { return n < 10 ? '0' + n : '' + n; }

  function addOption(sel, value, label) {
    var opt = document.createElement('option');
    opt.value = value;
    opt.textContent = label;
    sel.appendChild(opt);
    return opt;
  }

  function buildSelect(label) {
    var sel = document.createElement('select');
    sel.setAttribute('aria-label', label);
    addOption(sel, '', label);
    return sel;
  }

  function enhance(input) {
    if (input.dataset.dpBuilt) return;
    input.dataset.dpBuilt = '1';

    var isDateTime = input.type === 'datetime-local';

    var daySel = buildSelect('Day');
    var monthSel = buildSelect('Month');
    var yearSel = buildSelect('Year');
    var hourSel = null;
    var minuteSel = null;

    var now = new Date();
    var currentYear = now.getFullYear();
    var minYear = parseInt(input.dataset.dpMinYear || '', 10) || currentYear - 100;
    var maxYear = parseInt(input.dataset.dpMaxYear || '', 10) || currentYear + 10;
    for (var y = maxYear; y >= minYear; y--) {
      addOption(yearSel, '' + y, '' + y);
    }

    for (var m = 0; m < 12; m++) {
      addOption(monthSel, '' + (m + 1), MONTHS[m] + ' (' + (m + 1) + ')');
    }

    if (isDateTime) {
      hourSel = buildSelect('Hour');
      minuteSel = buildSelect('Minute');
      for (var h = 0; h < 24; h++) { addOption(hourSel, pad(h), pad(h)); }
      for (var mi = 0; mi < 60; mi++) { addOption(minuteSel, pad(mi), pad(mi)); }
    }

    function daysInMonth() {
      var y = +yearSel.value;
      var m = +monthSel.value;
      if (!y || !m) return 31;
      return new Date(y, m, 0).getDate();
    }

    function renderDays() {
      var prev = daySel.value;
      daySel.textContent = '';
      addOption(daySel, '', 'Day');
      var dim = daysInMonth();
      for (var d = 1; d <= dim; d++) {
        addOption(daySel, '' + d, '' + d);
      }
      if (prev && +prev <= dim) daySel.value = prev;
    }

    function sync() {
      var y = yearSel.value;
      var m = monthSel.value;
      var d = daySel.value;
      var value = '';
      if (isDateTime) {
        var h = hourSel.value;
        var minute = minuteSel.value;
        if (y && m && d && h && minute) {
          value = y + '-' + pad(m) + '-' + pad(d) + 'T' + h + ':' + minute;
        }
      } else if (y && m && d) {
        value = y + '-' + pad(m) + '-' + pad(d);
      }
      input.value = value;
    }

    var parts = (input.value || '').match(/^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2}):(\d{2}))?$/);
    var presetDay = '';
    if (parts) {
      yearSel.value = parts[1];
      monthSel.value = String(+parts[2]);
      presetDay = String(+parts[3]);
      if (isDateTime) {
        hourSel.value = parts[4];
        minuteSel.value = parts[5];
      }
    }

    renderDays();
    if (presetDay) {
      daySel.value = presetDay;
    }
    sync();

    var picks = [daySel, monthSel, yearSel];
    if (isDateTime) picks.push(hourSel, minuteSel);
    if (input.required) {
      input.removeAttribute('required');
      for (var p = 0; p < picks.length; p++) { picks[p].required = true; }
    }
    for (var i = 0; i < picks.length; i++) {
      picks[i].addEventListener('change', onChange);
      picks[i].addEventListener('input', onChange);
    }

    function onChange() {
      renderDays();
      sync();
    }

    var wrapper = document.createElement('span');
    wrapper.className = 'date-picker';
    wrapper.appendChild(daySel);
    wrapper.appendChild(monthSel);
    wrapper.appendChild(yearSel);
    if (isDateTime) {
      wrapper.appendChild(hourSel);
      wrapper.appendChild(minuteSel);
    }

    input.className += ' dp-orig';
    input.parentNode.insertBefore(wrapper, input.nextSibling);
  }

  document.addEventListener('DOMContentLoaded', function () {
    document.querySelectorAll('input[type="date"], input[type="datetime-local"]').forEach(enhance);
  });
})();