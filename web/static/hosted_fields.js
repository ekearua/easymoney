(function () {
  'use strict';

  // isw-hosted-fields contract (verified against sdk.js V1.0.0 and the
  // published Hosted Fields integration guide):
  //   create(config, callback)      → callback(null, instance) once every
  //                                   configured field has mounted
  //   instance.getBinConfiguration(cb) → cb(error, binConfig); best-effort
  //                                   lock precheck ONLY — a bin lookup the
  //                                   merchant has not been provisioned for
  //                                   (Z1/Z81/Z82) never blocks the flow; the
  //                                   charge is the authority
  //   instance.makePayment(cb)         → cb(error, paymentResponse); sends OTP
  //   instance.validatePayment(cb)     → cb(error, response); charges the card
  //   instance.on('cardinal-response', cb) → cb(error, response) when the
  //                                   charge needed a 3-D Secure challenge
  // The callback passed to makePayment/validatePayment IS the response
  // handler — it is not a bin-config argument.
  // fields keys must be exactly: cardNumber, expirationDate, cvv, pin, otp,
  // and each MUST carry a styles object: the frame's field builder calls
  // Object.keys(styles) and silently drops the field (empty container, no
  // input) when it is missing.
  var config = null;
  try {
    var raw = document.getElementById('xego-payment-config');
    config = raw ? JSON.parse(raw.getAttribute('data-config')) : null;
  } catch (err) {
    config = null;
  }
  if (!config || !config.paymentParameters || !config.fields || !config.cardinal) {
    setMessage('The payment configuration is missing. Please refresh and try again.');
    return;
  }

  // Codes the gateway returns for an approved charge. Anything else after
  // makePayment means the customer still has to finish the OTP hop.
  var approvedCodes = ['00', '90000', '10'];
  var instance = null;
  var payButton = document.getElementById('pay-button');
  var continueButton = document.getElementById('continue-button');
  var validateButton = document.getElementById('validate-button');

  function showStep(name) {
    ['details', 'pin', 'otp'].forEach(function (step) {
      var el = document.getElementById(step + '-page');
      if (!el) {
        return;
      }
      if (step === name) {
        el.removeAttribute('hidden');
      } else {
        el.setAttribute('hidden', 'hidden');
      }
    });
  }

  function setMessage(text) {
    var el = document.getElementById('hf-message');
    if (el) {
      el.textContent = text;
    }
  }

  function setBusy(busy) {
    if (payButton) {
      payButton.disabled = busy;
    }
  }

  function approved(response) {
    var code = response && (response.responseCode || response.resp);
    return approvedCodes.indexOf(String(code)) !== -1;
  }

  // The SDK hands back a service error object, not the gateway payload, so the
  // copy has to distinguish a customer mistake from a gateway refusal —
  // blaming the card for a merchant-side rejection sends the customer in
  // circles.
  function describeServiceError(error, fallback) {
    if (!error) {
      return fallback;
    }
    if (error.networkError) {
      return 'We could not reach the payment gateway. Please check your connection and try again.';
    }
    if (String(error.responseCode) === 'T9') {
      return 'This card is locked. Please try another card.';
    }
    if (error.validationError) {
      return 'Your card details look incomplete or invalid. Please check them and try again.';
    }
    var code = String(error.responseCode || '');
    if (code) {
      if (code === 'Z81') {
        return 'This card type is not accepted. Please try another card.';
      }
      if (code === 'Z82') {
        return 'Card payments are not enabled for this merchant yet. Please try another payment method.';
      }
      if (code === 'Z5') {
        return 'This payment was already processed. Go back and check your receipt.';
      }
      if (code === 'XS1') {
        return 'The payment window has expired. Please refresh and try again.';
      }
      if (code === 'Z1') {
        return 'The gateway could not process this transaction (code Z1). Please try again or use another payment method.';
      }
      return 'The payment was not completed (code ' + error.responseCode + '). Please try again or choose another payment method.';
    }
    return fallback;
  }

  function backToDetails(text) {
    setMessage(text);
    setBusy(false);
    showStep('details');
  }

  // The terminal hop: nothing is released here — the return page verifies the
  // transaction server-side before any value moves.
  function finish(error, response) {
    if (error) {
      backToDetails(describeServiceError(error, 'Payment was not completed. Please check your card details and try again.'));
      return;
    }
    if (response && !approved(response) && (response.responseCode || response.resp)) {
      backToDetails('Payment was not completed (code ' + (response.responseCode || response.resp) + '). You can try again.');
      return;
    }
    setMessage('Payment approved. Confirming on our side…');
    var back = (response && (response.redirectURL || response.redirectUrl)) || config.paymentParameters.redirectURL;
    if (back) {
      window.location.href = back;
    }
  }

  function onBinConfiguration(error, binConfig) {
    console.info('[hosted fields] bin config', error || binConfig);
    // A definitive lock verdict stops the flow; everything else — including a
    // bin lookup the merchant has not been provisioned for (Z1/Z81/Z82) — must
    // NOT block the charge attempt. The BIN lookup and the charge are separate
    // gateway calls, so the customer still gets to enter their PIN and the
    // makePayment response is the single authority on whether the charge went
    // through.
    if (binConfig && String(binConfig.responseCode) === 'T9') {
      backToDetails('This card is locked. Please try another card.');
      return;
    }
    if (error && String(error.responseCode || '') === 'T9') {
      backToDetails('This card is locked. Please try another card.');
      return;
    }
    // Cards that do not support a PIN skip the PIN step entirely — the charge
    // is attempted straight away and the makePayment response decides whether
    // an OTP or a 3-D Secure challenge is required.
    if (binConfig && binConfig.supportsPin === false) {
      setMessage('Charging your card…');
      try {
        instance.makePayment(onPayment);
      } catch (e) {
        backToDetails(describeServiceError(e, 'The charge could not be started. Please try again.'));
      }
      return;
    }
    // The PIN travels inside the secure payload makePayment sends, so it has
    // to be entered before the charge is attempted.
    setMessage('Enter your card PIN, then continue.');
    showStep('pin');
  }

  function onPayment(error, response) {
    if (error) {
      console.warn('[hosted fields] makePayment error', error);
      backToDetails(describeServiceError(error, 'The charge was not completed. Please try again.'));
      return;
    }
    console.info('[hosted fields] makePayment response', response);
    if (response && response.requiresCentinelAuthorization === true) {
      // The 3-D Secure challenge renders inside #cardinal-container; its
      // resolution arrives as a cardinal-response event and calls finish().
      setMessage('Authenticating your card…');
      return;
    }
    if (approved(response)) {
      finish(null, response);
      return;
    }
    if (response && String(response.responseCode) === 'T0') {
      setMessage('Enter the OTP sent to your phone, then validate.');
      showStep('otp');
      return;
    }
    if (response && (response.responseCode || response.resp)) {
      backToDetails('Payment was not completed (code ' + (response.responseCode || response.resp) + '). You can try again.');
      return;
    }
    setMessage('Enter the OTP sent to your phone, then validate.');
    showStep('otp');
  }

  function onCreated(createError, hostedFieldsInstance) {
    if (createError || !hostedFieldsInstance) {
      setMessage('The secure payment widget failed to start. Please refresh and try again.');
      return;
    }
    instance = hostedFieldsInstance;
    instance.on('cardinal-response', finish);
    showStep('details');
    setBusy(false);
    setMessage('Enter your card details to pay.');
  }

  function init() {
    if (!window.isw || !window.isw.hostedFields) {
      setMessage('The secure payment widget could not be loaded. Please refresh and try again.');
      return;
    }
    try {
      isw.hostedFields.create(config, onCreated);
    } catch (err) {
      setMessage('The secure payment widget failed to start. Please refresh and try again.');
    }

    if (payButton) {
      payButton.addEventListener('click', function () {
        if (!instance) {
          setMessage('The secure fields are still loading. Please wait a moment and try again.');
          return;
        }
        setBusy(true);
        setMessage('Checking your card…');
        try {
          instance.getBinConfiguration(onBinConfiguration);
        } catch (err) {
          backToDetails('The payment could not be started. Please refresh and try again.');
        }
      });
    }
    if (continueButton) {
      continueButton.addEventListener('click', function () {
        if (!instance) {
          return;
        }
        setMessage('Sending OTP to your phone…');
        try {
          instance.makePayment(onPayment);
        } catch (err) {
          backToDetails('The payment could not be started. Please refresh and try again.');
        }
      });
    }
    if (validateButton) {
      validateButton.addEventListener('click', function () {
        if (!instance) {
          return;
        }
        setMessage('Charging your card…');
        try {
          instance.validatePayment(finish);
        } catch (err) {
          backToDetails('The payment could not be completed. Please try again.');
        }
      });
    }
    ['pin-back-button', 'otp-back-button'].forEach(function (id) {
      var back = document.getElementById(id);
      if (back) {
        back.addEventListener('click', function () {
          backToDetails('You can adjust your card details and try again.');
        });
      }
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
