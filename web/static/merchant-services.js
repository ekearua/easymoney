(function() {
  document.addEventListener('DOMContentLoaded', function() {
    var addBtn = document.getElementById('cf-add-btn');
    var list = document.getElementById('cf-list');
    if (addBtn && list) {
      addBtn.addEventListener('click', function() {
        var div = document.createElement('div');
        div.className = 'cf-row';
        div.innerHTML =
          '<input name="cf_name[]" placeholder="Field name (e.g. Seat number)">' +
          '<select name="cf_type[]"><option value="text">Text</option><option value="number">Number</option><option value="select">Select</option></select>' +
          '<input name="cf_options[]" placeholder="Options (comma-separated, for Select)">' +
          '<label class="checkbox-row"><input type="checkbox" name="cf_required[]" value="1"> Required</label>' +
          '<button type="button" class="btn-sm danger cf-remove-btn">✕</button>';
        list.appendChild(div);
      });
      list.addEventListener('click', function(e) {
        if (e.target.classList.contains('cf-remove-btn')) {
          e.target.parentElement.remove();
        }
      });
    }
  });
})();
