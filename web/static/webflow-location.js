// Confirms the visitor's current position for a location-verification step.
//
// The button is inert until the browser grants permission, and nothing is sent
// without an explicit tap: the page never asks for location on load. Only the
// position and its accuracy leave the page — no device identifier, no history.
// If the permission is refused the customer can still upload a utility bill,
// which the step accepts as the alternative proof.
(function () {
  "use strict";

  var form = document.querySelector("[data-location-form]");
  if (!form) {
    return;
  }
  var button = form.querySelector("[data-location]");
  var error = document.querySelector("[data-location-error]");
  if (!button) {
    return;
  }

  function fail(message) {
    if (error) {
      error.textContent = message;
      error.hidden = false;
    }
  }

  function setField(name, value) {
    var input = form.querySelector('input[name="' + name + '"]');
    if (input) {
      input.value = value;
    }
  }

  button.addEventListener("click", function () {
    if (!navigator.geolocation) {
      fail("This browser cannot share a location. Upload a utility bill instead.");
      return;
    }
    button.disabled = true;
    button.textContent = "📍 Checking…";
    navigator.geolocation.getCurrentPosition(
      function (position) {
        setField("latitude", position.coords.latitude);
        setField("longitude", position.coords.longitude);
        setField("accuracy_m", position.coords.accuracy || 0);
        form.submit();
      },
      function () {
        button.disabled = false;
        button.textContent = "📍 Confirm my current location";
        fail("Location permission was not granted. Upload a utility bill instead.");
      },
      { enableHighAccuracy: true, timeout: 15000, maximumAge: 0 }
    );
  });
})();
