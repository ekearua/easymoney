// Passkey ceremonies: the server hands over public-key options, the browser
// runs navigator.credentials, and the result goes back verbatim. Kept as a
// served file so the strict script-src 'self' policy applies.
(function () {
  "use strict";

  function base64URL(buffer) {
    var bytes = new Uint8Array(buffer);
    var binary = "";
    for (var i = 0; i < bytes.length; i += 1) {
      binary += String.fromCharCode(bytes[i]);
    }
    return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function toBuffer(value) {
    var base64 = String(value).replace(/-/g, "+").replace(/_/g, "/");
    var padded = base64 + "=".repeat((4 - (base64.length % 4)) % 4);
    var binary = atob(padded);
    var bytes = new Uint8Array(binary.length);
    for (var i = 0; i < binary.length; i += 1) {
      bytes[i] = binary.charCodeAt(i);
    }
    return bytes.buffer;
  }

  function creationOptions(publicKey) {
    var options = Object.assign({}, publicKey);
    options.challenge = toBuffer(publicKey.challenge);
    options.excludeCredentials = (publicKey.excludeCredentials || []).map(function (descriptor) {
      return Object.assign({}, descriptor, { id: toBuffer(descriptor.id) });
    });
    if (publicKey.user) {
      options.user = Object.assign({}, publicKey.user, { id: toBuffer(publicKey.user.id) });
    }
    return options;
  }

  function requestOptions(publicKey) {
    var options = Object.assign({}, publicKey);
    options.challenge = toBuffer(publicKey.challenge);
    options.allowCredentials = (publicKey.allowCredentials || []).map(function (descriptor) {
      return Object.assign({}, descriptor, { id: toBuffer(descriptor.id) });
    });
    return options;
  }

  // The JSON shape go-webauthn parses. Browsers that expose toJSON() build it
  // for us; the manual path covers the rest.
  function credentialJSON(credential) {
    if (typeof credential.toJSON === "function") {
      return credential.toJSON();
    }
    var response = credential.response;
    var payload = {
      id: credential.id,
      rawId: base64URL(credential.rawId),
      type: credential.type,
      response: { clientDataJSON: base64URL(response.clientDataJSON) },
      clientExtensionResults:
        typeof credential.getClientExtensionResults === "function"
          ? credential.getClientExtensionResults()
          : {},
    };
    if (response.attestationObject) {
      payload.response.attestationObject = base64URL(response.attestationObject);
      if (typeof response.getTransports === "function") {
        payload.response.transports = response.getTransports();
      }
      return payload;
    }
    payload.response.authenticatorData = base64URL(response.authenticatorData);
    payload.response.signature = base64URL(response.signature);
    payload.response.userHandle = response.userHandle ? base64URL(response.userHandle) : null;
    return payload;
  }

  function post(url, body, csrf) {
    var headers = { "Content-Type": "application/json" };
    if (csrf) {
      headers["X-CSRF-Token"] = csrf;
    }
    return fetch(url, {
      method: "POST",
      headers: headers,
      body: JSON.stringify(body),
      credentials: "same-origin",
    }).then(function (response) {
      return response.text().then(function (text) {
        var parsed = {};
        if (text) {
          try {
            parsed = JSON.parse(text);
          } catch (error) {
            parsed = { error: text.slice(0, 200) };
          }
        }
        if (!response.ok) {
          throw new Error(parsed.error || "That request failed.");
        }
        return parsed;
      });
    });
  }

  function setStatus(element, message, isError) {
    if (!element) {
      return;
    }
    element.textContent = message;
    if (isError) {
      element.classList.add("alert");
    } else {
      element.classList.remove("alert");
    }
  }

  function deviceLabel() {
    var platform = "";
    if (window.navigator.userAgentData && window.navigator.userAgentData.platform) {
      platform = window.navigator.userAgentData.platform;
    } else if (window.navigator.platform) {
      platform = window.navigator.platform;
    }
    platform = String(platform).trim();
    return platform ? "Passkey · " + platform : "Passkey";
  }

  // Enrollment on the sign-in security page.
  var enroll = document.getElementById("passkey-enroll");
  var enrollButton = document.getElementById("passkey-enroll-button");
  if (enroll && enrollButton) {
    enrollButton.addEventListener("click", function () {
      var status = document.getElementById("passkey-enroll-status");
      enrollButton.disabled = true;
      setStatus(status, "Waiting for your device…", false);
      post(enroll.dataset.begin, {}, enroll.dataset.csrf)
        .then(function (started) {
          return navigator.credentials
            .create({ publicKey: creationOptions(started.options.publicKey) })
            .then(function (credential) {
              return post(
                enroll.dataset.finish,
                { token: started.token, credential: credentialJSON(credential), label: deviceLabel() },
                enroll.dataset.csrf
              );
            });
        })
        .then(function () {
          setStatus(status, "Passkey added.", false);
          window.location.reload();
        })
        .catch(function (error) {
          setStatus(status, error && error.message ? error.message : "That passkey could not be added.", true);
          enrollButton.disabled = false;
        });
    });
  }

  // The passkey half of a sign-in step.
  var signin = document.getElementById("passkey-signin");
  if (signin) {
    signin.addEventListener("click", function (event) {
      event.preventDefault();
      var status = document.getElementById("passkey-status");
      var endpoint = signin.dataset.endpoint;
      var payload = { token: signin.dataset.token, account: signin.dataset.account };
      signin.disabled = true;
      setStatus(status, "Waiting for your device…", false);
      post(endpoint, Object.assign({ action: "begin" }, payload))
        .then(function (started) {
          return navigator.credentials
            .get({ publicKey: requestOptions(started.options.publicKey) })
            .then(function (credential) {
              return post(
                endpoint,
                Object.assign({ action: "finish", credential: credentialJSON(credential) }, payload)
              );
            });
        })
        .then(function (result) {
          if (result.redirect) {
            window.location.href = result.redirect;
            return;
          }
          window.location.reload();
        })
        .catch(function (error) {
          setStatus(status, error && error.message ? error.message : "That passkey could not be used.", true);
          signin.disabled = false;
        });
    });
  }
})();
