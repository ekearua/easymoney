(function () {
  function submitInterswitchForm() {
    var form = document.getElementById('interswitch-form');
    if (form) {
      form.submit();
    }
  }
  if (document.readyState === 'complete') {
    submitInterswitchForm();
  } else {
    window.addEventListener('load', submitInterswitchForm);
  }
})();
