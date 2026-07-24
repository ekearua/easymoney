(function(){
  var scanner = null;
  var scanning = false;

  function extractToken(decodedText) {
    var text = decodedText.trim();
    var match = text.match(/\/scan\/([^\/\s?#]+)/);
    if (match) return decodeURIComponent(match[1]);
    return text;
  }

  function startScan() {
    if (scanning) return;
    var el = document.getElementById('qr-reader');
    el.style.display = 'block';
    scanner = new Html5Qrcode('qr-reader');
    scanner.start(
      { facingMode: 'environment' },
      { fps: 10, qrbox: { width: 250, height: 250 } },
      function onScanSuccess(decodedText) {
        stopScan();
        var token = extractToken(decodedText);
        document.getElementById('token-input').value = token;
        document.getElementById('scan-form').submit();
      },
      function() {}
    ).then(function() {
      scanning = true;
      document.getElementById('btn-scan').classList.add('hidden');
      document.getElementById('btn-stop').classList.remove('hidden');
      document.getElementById('scan-status').textContent = 'Scanning... point camera at QR code.';
    }).catch(function(err) {
      document.getElementById('scan-status').textContent = 'Camera error: ' + (err.message || err);
    });
  }

  function stopScan() {
    if (scanner && scanning) {
      scanner.stop().then(function() { scanner.clear(); }).catch(function() {});
    }
    scanning = false;
    document.getElementById('btn-scan').classList.remove('hidden');
    document.getElementById('btn-stop').classList.add('hidden');
    document.getElementById('scan-status').textContent = 'Position a receipt QR code inside the frame. The scan validates automatically.';
  }

  document.getElementById('btn-scan').addEventListener('click', startScan);
  document.getElementById('btn-stop').addEventListener('click', stopScan);
})();
