(function () {
  'use strict';

  var config = window.xegoPaymentConfig;

  function init() {
    if (!window.isw || !window.isw.hostedFields) {
      setMessage('The secure payment widget could not be loaded. Please refresh and try again.');
      return;
    }
    var instance;
    try {
      instance = isw.hostedFields.create(
        Object.assign({
          channel: 'WEB',
          certifyCard: true,
          hasContactless: false,
          disabled: [],
          cardNumberEl: 'cardNumber-container',
          expiryEl: 'expiry-container',
          cvvEl: 'cvv-container',
          pinEl: 'pin-container',
          otpEl: 'otp-container'
        }, config),
        hostedFieldsCallback
      );
    } catch (err) {
      setMessage('The secure payment widget failed to start. Please refresh and try again.');
      return;
    }

    var payButton = document.getElementById('pay-button');
    payButton.addEventListener('click', function () {
      payButton.disabled = true;
      setMessage('Validating your card…');
      try {
        instance.getBinConfiguration(handleBinConfig);
      } catch (err) {
        setMessage('The payment could not be started. Please refresh and try again.');
        payButton.disabled = false;
      }
    });

    function handleBinConfig(response) {
      if (!response || response.responseCode === 'T9') {
        setMessage('This card is locked. Please try another card.');
        payButton.disabled = false;
        return;
      }
      setMessage('Processing your payment… complete any OTP prompt that appears.');
      try {
        instance.makePayment(response);
      } catch (err) {
        setMessage('The payment could not be charged. Please refresh and try again.');
        payButton.disabled = false;
      }
    }
  }

  function hostedFieldsCallback(response) {
    if (!response) {
      return;
    }
    var code = response.responseCode || response.resp;
    if (code && code !== '90000' && code !== 'T0' && code !== 'S0') {
      setMessage('Payment was not completed (code ' + code + '). You can try again.');
      var payButton = document.getElementById('pay-button');
      if (payButton) {
        payButton.disabled = false;
      }
    }
  }

  function setMessage(text) {
    var el = document.getElementById('hf-message');
    if (el) {
      el.textContent = text;
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();