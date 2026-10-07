// Records a bulk-payment recipient's current position from a browser link.
//
// This is the fallback path for recipients whose channel cannot send a native
// location reply. Nothing is requested on load; the position is only captured
// after an explicit tap, and only the coordinates and their accuracy are sent.
(function () {
  "use strict";

  var root = document.querySelector("[data-bulk-location]");
  if (!root) {
    return;
  }
  var button = root.querySelector("[data-bulk-share]");
  var status = root.querySelector("[data-bulk-status]");
  var itemID = root.getAttribute("data-item");

  function setStatus(message, isError) {
    if (!status) {
      return;
    }
    status.textContent = message;
    status.hidden = false;
    status.className = isError ? "error" : "muted";
  }

  button.addEventListener("click", function () {
    if (!navigator.geolocation) {
      setStatus("This browser cannot share a location.", true);
      return;
    }
    button.disabled = true;
    button.textContent = "Checking…";
    navigator.geolocation.getCurrentPosition(
      function (position) {
        fetch("/bulk/loc", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            id: itemID,
            latitude: position.coords.latitude,
            longitude: position.coords.longitude,
            accuracy: position.coords.accuracy || 0
          })
        })
          .then(function (response) {
            if (response.ok) {
              setStatus("Location received. You can close this page.", false);
              return;
            }
            return response.json().catch(function () {
              return {};
            }).then(function (body) {
              setStatus(body.error || "This location request is no longer open.", true);
            });
          })
          .catch(function () {
            setStatus("Could not send your location. Please try again.", true);
          })
          .then(function () {
            button.disabled = false;
            button.textContent = "Share my location";
          });
      },
      function () {
        button.disabled = false;
        button.textContent = "Share my location";
        setStatus("Location permission was not granted.", true);
      },
      { enableHighAccuracy: true, timeout: 15000, maximumAge: 0 }
    );
  });
})();