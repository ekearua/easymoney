/* webflow-scan.js — optional in-browser document scanning for web-flow upload
 * fields. Enhances the plain "choose a file" uploader with a camera flow:
 *
 *   1. Open the rear camera (getUserMedia) in an overlay.
 *   2. On capture, downscale and run a lightweight edge detector (Sobel on a
 *      luminance buffer) to find the document's bounding quad.
 *   3. Crop the photo to the detected document and re-encode as JPEG.
 *   4. Post the cropped file to the flow's /media endpoint exactly like the
 *      ordinary file upload would, then reload to show what was read.
 *
 * It is strictly progressive enhancement: if the camera or canvas APIs are
 * unavailable, the original file input keeps working untouched.
 */
(function () {
  'use strict';
  if (!window.document || !navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    return; // plain uploads remain available
  }

  var MAX_EDGE = 1400; // longest edge used for the detection buffer
  var OVERLAY_HTML =
    '<div class="wf-cam-holder" role="dialog" aria-modal="true" aria-label="Scan with camera">' +
    '  <div class="wf-cam-card">' +
    '    <video autoplay playsinline muted></video>' +
    '    <canvas width="0" height="0"></canvas>' +
    '    <div class="wf-cam-actions">' +
    '      <button type="button" class="ghost" data-cam-cancel>Cancel</button>' +
    '      <button type="button" class="primary" data-cam-capture>Capture</button>' +
    '      <button type="button" class="primary" data-cam-retake style="display:none">Retake</button>' +
    '      <button type="button" class="danger" data-cam-crop style="display:none">Use without crop</button>' +
    '      <button type="button" class="primary" data-cam-done style="display:none">Use photo</button>' +
    '    </div>' +
    '    <p class="wf-cam-note" data-cam-note></p>' +
    '  </div>' +
    '</div>';

  function setup() {
    var overlay = null;
    var stream = null;
    var form = null;
    var video = null;
    var canvas = null;
    var note = null;
    var latestShot = null; // last captured frame (canvas)
    var cropped = null; // canvas with the edge-cropped photo, when accepted

    function noteText(txt) {
      if (note) { note.textContent = txt; }
    }

    function stopCamera() {
      if (stream) {
        stream.getTracks().forEach(function (t) { t.stop(); });
        stream = null;
      }
    }

    function hide() {
      stopCamera();
      if (overlay) { overlay.classList.remove('open'); }
    }

    function showButtons(phase) {
      var map = {
        capture: { cancel: true, capture: true, retake: false, crop: false, done: false },
        preview: { cancel: true, capture: false, retake: true, crop: true, done: true }
      };
      var state = map[phase] || map.capture;
      var btn = overlay.querySelector('[data-cam-cancel]');
      btn.style.display = state.cancel ? '' : 'none';
      btn = overlay.querySelector('[data-cam-capture]');
      btn.style.display = state.capture ? '' : 'none';
      btn = overlay.querySelector('[data-cam-retake]');
      btn.style.display = state.retake ? '' : 'none';
      btn = overlay.querySelector('[data-cam-crop]');
      btn.style.display = state.crop ? '' : 'none';
      btn = overlay.querySelector('[data-cam-done]');
      btn.style.display = state.done ? '' : 'none';
      if (video) { video.style.display = state.capture ? '' : 'none'; }
      if (canvas) { canvas.style.display = state.preview ? '' : 'none'; }
    }

    function openCamera() {
      var constraints = { video: { facingMode: 'environment', width: { ideal: 1920 }, height: { ideal: 1080 } }, audio: false };
      navigator.mediaDevices.getUserMedia(constraints).then(function (s) {
        stream = s;
        if (!video || !overlay || !overlay.classList.contains('open')) { stopCamera(); return; }
        video.srcObject = s;
        noteText('Position the slip inside the frame and tap Capture.');
        showButtons('capture');
      }).catch(function (err) {
        noteText('Camera unavailable (' + (err && err.name ? err.name : 'error') + '). Choose a photo from your gallery instead.');
        hide();
      });
    }

    // Lightweight document detection. Returns the axis-aligned crop rect of
    // the strongest edge cluster, or null when the frame has no clear
    // document edges (callers then use the full frame).
    function detectCropRect(source) {
      var w = source.width, h = source.height;
      if (w === 0 || h === 0) { return null; }
      var scale = Math.min(1, MAX_EDGE / Math.max(w, h));
      var bw = Math.max(2, Math.round(w * scale));
      var bh = Math.max(2, Math.round(h * scale));
      var buf = document.createElement('canvas');
      buf.width = bw; buf.height = bh;
      var bctx = buf.getContext('2d', { willReadFrequently: true });
      bctx.drawImage(source, 0, 0, bw, bh);
      var data = bctx.getImageData(0, 0, bw, bh).data;
      var lum = new Float32Array(bw * bh);
      var i, p = 0;
      for (i = 0; i < bw * bh; i++) {
        lum[i] = 0.299 * data[p] + 0.587 * data[p + 1] + 0.114 * data[p + 2];
        p += 4;
      }
      // Sobel magnitude on the luminance buffer (skip a 1px border).
      var mag = new Float32Array(bw * bh);
      var x, y, gx, gy, idx, sum = 0, max = 0;
      var m = 0;
      for (y = 1; y < bh - 1; y++) {
        for (x = 1; x < bw - 1; x++) {
          idx = y * bw + x;
          gx = (-lum[idx - bw - 1] - 2 * lum[idx - 1] - lum[idx + bw - 1])
             + (lum[idx - bw + 1] + 2 * lum[idx + 1] + lum[idx + bw + 1]);
          gy = (-lum[idx - bw - 1] - 2 * lum[idx - bw] - lum[idx - bw + 1])
             + (lum[idx + bw - 1] + 2 * lum[idx + bw] + lum[idx + bw + 1]);
          var v = Math.abs(gx) + Math.abs(gy);
          mag[idx] = v;
          sum += v;
          if (v > max) { max = v; }
        }
      }
      if (max <= 0) { return null; }
      var mean = sum / (bw * bh);
      var thr = Math.max(mean * 1.2, max * 0.18); // strong edges only
      var minX = bw, minY = bh, maxX = 0, maxY = 0, hits = 0;
      for (y = 1; y < bh - 1; y++) {
        for (x = 1; x < bw - 1; x++) {
          if (mag[y * bw + x] >= thr) {
            hits++;
            if (x < minX) { minX = x; }
            if (x > maxX) { maxX = x; }
            if (y < minY) { minY = y; }
            if (y > maxY) { maxY = y; }
          }
        }
      }
      var minHits = bw * bh * 0.004; // reject photographic noise
      if (hits < minHits) { return null; }
      // Trim near-border noise: keep the innermost dense band.
      var marginX = Math.floor(bw * 0.03), marginY = Math.floor(bh * 0.03);
      if (minX <= marginX) { minX = marginX; }
      if (minY <= marginY) { minY = marginY; }
      if (maxX >= bw - marginX) { maxX = bw - 1 - marginX; }
      if (maxY >= bh - marginY) { maxY = bh - 1 - marginY; }
      var cw = maxX - minX, ch = maxY - minY;
      if (cw < bw * 0.18 || ch < bh * 0.18) { return null; } // no doc-sized object
      // Nearly-full frames have no useful crop.
      if (cw > bw * 0.92 || ch > bh * 0.92) { return null; }
      var padX = Math.round(cw * 0.03), padY = Math.round(ch * 0.03);
      return {
        x: Math.round((minX - padX) / scale),
        y: Math.round((minY - padY) / scale),
        w: Math.round((cw + 2 * padX) / scale),
        h: Math.round((ch + 2 * padY) / scale)
      };
    }

    function frameToCanvas() {
      if (!video || !video.videoWidth) { return null; }
      var shot = document.createElement('canvas');
      shot.width = video.videoWidth;
      shot.height = video.videoHeight;
      var ctx = shot.getContext('2d');
      ctx.drawImage(video, 0, 0, shot.width, shot.height);
      return shot;
    }

    function showPreview() {
      if (!latestShot) { return; }
      var rect = detectCropRect(latestShot);
      cropped = null;
      var out = document.createElement('canvas');
      var ctx = out.getContext('2d');
      if (rect && rect.w > 40 && rect.h > 40) {
        out.width = Math.min(rect.w, MAX_EDGE);
        out.height = Math.round(out.width * (rect.h / rect.w));
        // White backing, then the cropped region scaled to fit.
        ctx.fillStyle = '#fff';
        ctx.fillRect(0, 0, out.width, out.height);
        var sx = rect.x, sy = rect.y, sw = rect.w, sh = rect.h;
        // Guard against over-large source reads.
        var maxRead = 6000;
        if (sw > maxRead || sh > maxRead) {
          var k = Math.min(maxRead / sw, maxRead / sh);
          sx = sx + sw * (1 - k) / 2; sy = sy + sh * (1 - k) / 2;
          sw *= k; sh *= k;
        }
        ctx.drawImage(latestShot, sx, sy, sw, sh, 0, 0, out.width, out.height);
        cropped = out;
        noteText('Crop detected — review it, retake, or use the photo without cropping.');
      } else {
        out.width = latestShot.width;
        out.height = latestShot.height;
        ctx.drawImage(latestShot, 0, 0);
        noteText('No clear document edges found — you can still use this photo.');
      }
      canvas.width = out.width;
      canvas.height = out.height;
      ctx = canvas.getContext('2d');
      ctx.drawImage(out, 0, 0);
      canvas.style.display = 'block';
      if (video) { video.style.display = 'none'; }
      showButtons('preview');
    }

    function usePhoto(applyCrop) {
      var chosen = applyCrop && cropped ? cropped : latestShot;
      if (!chosen || !form) { hide(); return; }
      var name = 'scan.jpg';
      canvas.toBlob(function (blob) {
        if (!blob) { noteText('Could not encode the photo. Try the file upload instead.'); return; }
        var fd = new FormData();
        fd.append('file', blob, name);
        var fieldInput = form.querySelector('input[name="field"]');
        var promptInput = form.querySelector('input[name="prompt"]');
        if (fieldInput) { fd.append('field', fieldInput.value); }
        if (promptInput) { fd.append('prompt', promptInput.value); }
        noteText('Uploading the scanned photo…');
        fetch(form.getAttribute('action'), { method: 'POST', body: fd, credentials: 'same-origin' })
          .then(function (resp) {
            if (!resp.ok && resp.status !== 303) {
              throw new Error('upload failed (' + resp.status + ')');
            }
            window.location.reload();
          })
          .catch(function (err) {
            noteText('Upload failed (' + (err && err.message ? err.message : 'error') + '). Use the file upload instead.');
          });
      }, 'image/jpeg', 0.9);
    }

    function bind() {
      var cancel = overlay.querySelector('[data-cam-cancel]');
      var capture = overlay.querySelector('[data-cam-capture]');
      var retake = overlay.querySelector('[data-cam-retake]');
      var cropBtn = overlay.querySelector('[data-cam-crop]');
      var done = overlay.querySelector('[data-cam-done]');
      cancel.addEventListener('click', function () {
        latestShot = null; cropped = null; hide();
      });
      capture.addEventListener('click', function () {
        latestShot = frameToCanvas();
        if (!latestShot) { noteText('Camera is not ready yet — wait for the preview, then capture.'); return; }
        stopCamera(); // freeze the frame; the canvas already holds the pixels
        showPreview();
      });
      retake.addEventListener('click', function () {
        latestShot = null; cropped = null;
        if (canvas) { canvas.style.display = 'none'; }
        if (video) { video.style.display = ''; }
        showButtons('capture');
        openCamera();
      });
      cropBtn.addEventListener('click', function () { usePhoto(false); });
      done.addEventListener('click', function () { usePhoto(true); });
    }

    function enhance(formEl) {
      formEl = formEl || form;
      var openBtn = formEl.querySelector('.wf-scan-open');
      if (!openBtn) { return; }
      openBtn.addEventListener('click', function () {
        if (!overlay) {
          overlay = document.createElement('div');
          overlay.innerHTML = OVERLAY_HTML;
          document.body.appendChild(overlay);
          video = overlay.querySelector('video');
          canvas = overlay.querySelector('canvas');
          note = overlay.querySelector('[data-cam-note]');
          bind();
        }
        form = formEl;
        if (canvas) { canvas.style.display = 'none'; }
        if (video) { video.style.display = ''; }
        overlay.classList.add('open');
        openCamera();
      });
    }

    var forms = document.querySelectorAll('.media-upload form');
    Array.prototype.forEach.call(forms, function (f) {
      var fileInput = f.querySelector('input[type="file"][accept^="image/"]');
      var hasScan = f.querySelector('.wf-scan-open');
      if (fileInput && hasScan) { enhance(f); }
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', setup);
  } else {
    setup();
  }
})();
