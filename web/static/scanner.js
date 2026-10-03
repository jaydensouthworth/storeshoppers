/* StoreShoppers owns camera lifecycle. ZXing is used only for local pixel decoding. */
(function (global) {
  "use strict";
  const LIMITS = Object.freeze({ fileBytes: 8 * 1024 * 1024, photoPixels: 24 * 1024 * 1024, frameSide: 1920, framePixels: 1920 * 1440, interval: 300, sessionMS: 120000 });
  const validCode = value => typeof value === "string" && /^SHOPDEMO-[!-~]{1,55}$/.test(value);
  function fail(message) { const e = new Error(message); e.scannerMessage = true; return e; }
  function expectedMiss(e) { return /NotFound|Checksum|Format/.test(e?.getKind?.() || e?.constructor?.kind || e?.name || ""); }
  function grayPixels(pixels) {
    const gray = new Uint8ClampedArray(pixels.width * pixels.height);
    for (let i = 0; i < gray.length; i++) gray[i] = (pixels.data[i * 4] + 2 * pixels.data[i * 4 + 1] + pixels.data[i * 4 + 2]) / 4;
    return gray;
  }
  // Sample multiple rows and the remainder of each recognized row so two labels
  // do not silently select an arbitrary product. Both image orientations work.
  function decodePixels(pixels, ZXing) {
    if (!ZXing) throw fail("The local decoder did not load. Reload the page or type the printed code.");
    if (!pixels.width || !pixels.height || pixels.width * pixels.height > LIMITS.framePixels) throw fail("This image is too large to scan safely.");
    const found = new Set();
    let ambiguous = false;
    const hints = new Map([[ZXing.DecodeHintType.TRY_HARDER, true]]);
    const reader = new ZXing.Code128Reader();
    function scan(gray, width, height) {
      const bitmap = new ZXing.BinaryBitmap(new ZXing.HybridBinarizer(new ZXing.RGBLuminanceSource(gray, width, height)));
      let previousRow = -1;
      for (let i = 0; i < 24 && found.size < 2 && !ambiguous; i++) {
        let rowRecognized = false;
        const y = Math.min(height - 1, Math.floor((i + 0.5) * height / 24));
        let row;
        try { row = bitmap.getBlackRow(y, null); } catch (e) { if (expectedMiss(e)) continue; throw e; }
        for (let direction = 0; direction < 2 && found.size < 2; direction++) {
          let offset = 0;
          for (let region = 0; region < 3 && width - offset > 80; region++) {
            const part = new ZXing.BitArray(width - offset);
            for (let x = offset; x < width; x++) if (row.get(x)) part.set(x - offset);
            try {
              const result = reader.decodeRow(y, part, hints);
              found.add(result.getText());
              rowRecognized = true;
              if (region > 0) ambiguous = true;
              const points = result.getResultPoints().map(point => point.getX());
              const end = Math.max(...points), begin = Math.min(...points);
              const module = (end - begin) / (11 * (result.getRawBytes().length - 1));
              // Result points are symbol centers. Step past the full stop,
              // leaving the next label’s quiet zone intact.
              offset += Math.ceil(end + module * 8);
            } catch (e) { if (!expectedMiss(e)) throw e; break; }
          }
          row.reverse();
        }
        if (rowRecognized) { if (previousRow >= 0 && i - previousRow > 2) ambiguous = true; previousRow = i; }
      }
    }
    const gray = grayPixels(pixels);
    scan(gray, pixels.width, pixels.height);
    if (found.size < 2 && !ambiguous) {
      const rotated = new Uint8ClampedArray(gray.length);
      for (let y = 0; y < pixels.height; y++) for (let x = 0; x < pixels.width; x++) rotated[x * pixels.height + pixels.height - y - 1] = gray[y * pixels.width + x];
      scan(rotated, pixels.height, pixels.width);
    }
    const results = [...found].map(rawValue => ({ rawValue, format: "code_128" }));
    if (ambiguous && results.length === 1) results.push({ ...results[0] });
    return results;
  }
  // Inspect encoded dimensions before asking the browser to allocate a bitmap.
  // JPEG, PNG and WebP are deliberately the only supported photo containers.
  function photoSize(bytes, type) {
    const b = new Uint8Array(bytes), v = new DataView(bytes);
    const u16 = n => v.getUint16(n), u32 = n => v.getUint32(n);
    const ascii = (n, count) => String.fromCharCode(...b.slice(n, n + count));
    let width = 0, height = 0;
    if (type === "image/png" && b.length >= 24 && b[0] === 137 && ascii(1, 3) === "PNG" && ascii(12, 4) === "IHDR") {
      width = u32(16); height = u32(20);
    } else if (type === "image/jpeg" && b.length > 4 && u16(0) === 0xffd8) {
      let p = 2;
      while (p + 4 < b.length && p < 1024 * 1024) {
        if (b[p++] !== 255) break;
        while (b[p] === 255) p++;
        const marker = b[p++];
        if (marker === 0xda || marker === 0xd9) break;
        if (marker === 0x01 || (marker >= 0xd0 && marker <= 0xd7)) continue;
        if (p + 2 > b.length) break;
        const length = u16(p);
        if (length < 2 || p + length > b.length) break;
        if ([0xc0, 0xc1, 0xc2].includes(marker) && length >= 8) { height = u16(p + 3); width = u16(p + 5); break; }
        p += length;
      }
    } else if (type === "image/webp" && b.length >= 30 && ascii(0, 4) === "RIFF" && ascii(8, 4) === "WEBP") {
      const kind = ascii(12, 4), le24 = n => b[n] | b[n + 1] << 8 | b[n + 2] << 16;
      if (kind === "VP8X" && !(b[20] & 2)) { width = le24(24) + 1; height = le24(27) + 1; }
      else if (kind === "VP8 " && b[23] === 0x9d && b[24] === 1 && b[25] === 0x2a) { width = v.getUint16(26, true) & 0x3fff; height = v.getUint16(28, true) & 0x3fff; }
      else if (kind === "VP8L" && b[20] === 0x2f) { width = 1 + ((b[21] | b[22] << 8) & 0x3fff); height = 1 + ((b[22] >> 6 | b[23] << 2 | b[24] << 10) & 0x3fff); }
    }
    if (!width || !height || width * height > LIMITS.photoPixels || width > 16000 || height > 16000) throw fail("Choose a JPEG, PNG or still WebP photo under 24 megapixels and 8 MB, or type the code.");
    return { width, height };
  }
  async function createDecoder(env) {
    let native = null;
    if (env.BarcodeDetector?.getSupportedFormats) {
      try {
        const formats = await env.BarcodeDetector.getSupportedFormats();
        if (formats.includes("code_128")) native = new env.BarcodeDetector({ formats });
      } catch (_) { /* The bundled pixel decoder remains available. */ }
    }
    return async canvas => {
      if (native) {
        try { const results = await native.detect(canvas); if (results.length) return results; }
        catch (_) { native = null; }
      }
      return decodePixels(canvas.getContext("2d", { willReadFrequently: true }).getImageData(0, 0, canvas.width, canvas.height), env.ZXing);
    };
  }
  function createScanner(root, env) {
    const doc = env.document, form = root.closest("form"), input = form?.querySelector('[name="code"]');
    if (!input) return null;
    const query = name => root.querySelector(`[data-scan-${name}]`);
    const start = query("start"), stopButton = query("stop"), photo = query("photo"), video = query("video"), status = query("status");
    const canvas = doc.createElement("canvas");
    let generation = 0, active = false, destroyed = false, timer = null, deadline = null, stream = null, objectURL = null, cancelImage = null;
    let permissionPending = false, operationPending = false, scanned = false;
    const statusText = (text, error = false) => { if (status.textContent !== text) status.textContent = text; status.dataset.error = String(error); };
    const connected = () => !destroyed && root.isConnected !== false && !input.disabled && !doc.hidden && root.dataset.scanRevoked !== "true";
    const current = token => token === generation && active && connected();
    const release = media => { if (media) for (const track of media.getTracks()) { track.onended = null; track.stop(); } };
    function stop(message) {
      generation++; active = false;
      if (timer !== null) env.clearTimeout(timer); timer = null;
      if (deadline !== null) env.clearTimeout(deadline); deadline = null;
      release(stream); stream = null;
      video.pause(); video.srcObject = null; video.hidden = true;
      if (cancelImage) { const cancel = cancelImage; cancelImage = null; cancel(); }
      if (objectURL) env.URL.revokeObjectURL(objectURL); objectURL = null;
      canvas.width = 0; canvas.height = 0;
      start.disabled = permissionPending || operationPending || !!start.dataset.handheldBusy; stopButton.hidden = true;
      if (message) statusText(message);
    }
    function frame(source, width, height) {
      if (!width || !height) return false;
      const scale = Math.min(1, LIMITS.frameSide / Math.max(width, height), Math.sqrt(LIMITS.framePixels / (width * height)));
      canvas.width = Math.max(1, Math.floor(width * scale)); canvas.height = Math.max(1, Math.floor(height * scale));
      const context = canvas.getContext("2d", { willReadFrequently: true });
      context.fillStyle = "#fff"; context.fillRect(0, 0, canvas.width, canvas.height);
      context.drawImage(source, 0, 0, canvas.width, canvas.height);
      return true;
    }
    function recognize(results, source, token) {
      if (!current(token) || !results.length) return false;
      if (results.length !== 1) { stop(); statusText("More than one barcode was found. Isolate one product label and try again.", true); return true; }
      const code = results[0].rawValue;
      if (!/^(code_128|Code128)$/.test(results[0].format) || !validCode(code)) { stop(); statusText("This is not a supported demo Code 128 label. Use the product page’s scan label or type its SHOPDEMO code.", true); return true; }
      stop();
      scanned = true; input.value = code;
      for (const [name, value] of [["source", source], ["format", "Code128"]]) { const field = form.querySelector(`[name="${name}"]`); if (field) field.value = value; }
      input.dispatchEvent(new env.Event("input", { bubbles: true })); input.dispatchEvent(new env.Event("change", { bubbles: true })); scanned = false;
      statusText(`Recognized ${code}. Review this code, then use Review code. Nothing has been picked yet.`);
      input.focus();
      // Employee fast path submits only the read-only identity review. The
      // absolute count or scale reading still requires an explicit confirmation.
      if (root.dataset.scanAutoReview === "true" && form.getAttribute?.("action") === "/handheld/scan" && typeof form.requestSubmit === "function") {
        statusText(`Recognized ${code}. Checking this item. Nothing has been picked yet.`);
        form.requestSubmit();
      }
      return true;
    }
    function errorMessage(error, camera) {
      if (error.scannerMessage) return error.message;
      if (["NotAllowedError", "SecurityError"].includes(error.name)) return "Camera permission was not granted. Allow it in your browser and try again, choose a photo, or type the code.";
      if (["NotFoundError", "OverconstrainedError"].includes(error.name)) return "No usable camera was found. Choose a photo or type the printed code.";
      return camera ? "The camera could not continue. Stop other camera apps and try again, choose a photo, or type the code." : "That photo could not be decoded. Choose a clear photo of one label or type the code.";
    }
    async function startCamera() {
      if (!connected() || active || permissionPending || operationPending) return;
      if (!env.navigator.mediaDevices?.getUserMedia || env.isSecureContext === false) { statusText("Camera access requires a supported browser over HTTPS. Choose a photo or type the code.", true); return; }
      stop(); active = true; const token = generation; start.disabled = true; stopButton.hidden = false;
      statusText("Waiting for camera permission. You can stop here and type the code instead.");
      permissionPending = true;
      try {
        const media = await env.navigator.mediaDevices.getUserMedia({ audio: false, video: { facingMode: { ideal: "environment" }, width: { ideal: 1280 }, height: { ideal: 720 } } });
        permissionPending = false;
        if (!current(token)) { release(media); start.disabled = operationPending || !!start.dataset.handheldBusy; return; }
        stream = media;
        for (const track of stream.getTracks()) track.onended = () => { if (current(token)) stop("Camera ended. Start it again or type the code."); };
        deadline = env.setTimeout(() => { if (current(token)) stop("Camera paused after two minutes. Start it again or type the code."); }, LIMITS.sessionMS);
        video.srcObject = media; video.hidden = false;
        await video.play();
        if (!current(token)) return;
        const decode = await createDecoder(env);
        if (!current(token)) return;
        statusText("Point at one product label. Keep the full barcode and white border visible.");
        async function tick() {
          if (!current(token)) { if (active) stop(); return; }
          try {
            if (frame(video, video.videoWidth, video.videoHeight)) {
              operationPending = true;
              const results = await decode(canvas);
              operationPending = false;
              if (recognize(results, "camera", token)) return;
            }
          } catch (error) { operationPending = false; if (current(token)) { stop(); statusText(errorMessage(error, true), true); } return; }
          if (current(token)) timer = env.setTimeout(tick, LIMITS.interval);
          else start.disabled = permissionPending || !!start.dataset.handheldBusy;
        }
        timer = env.setTimeout(tick, 0);
      } catch (error) {
        permissionPending = false;
        if (current(token)) { stop(); statusText(errorMessage(error, true), true); }
        else start.disabled = operationPending || !!start.dataset.handheldBusy;
      }
    }
    async function choosePhoto() {
      const file = photo.files?.[0]; photo.value = "";
      if (!file || !connected()) return;
      stop();
      if (permissionPending || operationPending) { statusText("The previous camera request is still closing. Try the photo again shortly, or type the code.", true); return; }
      if (file.size > LIMITS.fileBytes || !["image/jpeg", "image/png", "image/webp"].includes(file.type)) { statusText("Choose a JPEG, PNG or WebP photo under 8 MB, or type the code.", true); return; }
      active = true; const token = generation; start.disabled = true; stopButton.hidden = false; operationPending = true;
      deadline = env.setTimeout(() => { if (current(token)) stop("Photo scanning took too long. Choose another photo or type the code."); }, 20000);
      statusText("Reading this photo on your device…");
      try {
        photoSize(await file.arrayBuffer(), file.type);
        if (!current(token)) return;
        objectURL = env.URL.createObjectURL(file);
        const img = await new Promise((resolve, reject) => {
          const image = new env.Image();
          const cleanup = () => { image.onload = null; image.onerror = null; cancelImage = null; };
          cancelImage = () => { cleanup(); image.src = ""; reject(fail("Photo scan stopped.")); };
          image.onload = () => { cleanup(); resolve(image); };
          image.onerror = () => { cleanup(); reject(fail("This photo could not be opened. Choose another photo or type the code.")); };
          image.src = objectURL;
        });
        if (!current(token)) return;
        if (img.naturalWidth * img.naturalHeight > LIMITS.photoPixels || !frame(img, img.naturalWidth, img.naturalHeight)) throw fail("This photo is too large. Choose a smaller photo or type the code.");
        const decode = await createDecoder(env);
        if (!current(token)) return;
        const results = await decode(canvas);
        operationPending = false;
        if (current(token) && !recognize(results, "photo", token)) { stop(); statusText("No readable demo Code 128 label was found. Include its full white border, improve the light, or type the code.", true); }
      } catch (error) { if (current(token)) { stop(); statusText(errorMessage(error, false), true); } }
      finally { operationPending = false; if (!active) start.disabled = permissionPending || !!start.dataset.handheldBusy; if (objectURL) env.URL.revokeObjectURL(objectURL); objectURL = null; }
    }
    const manual = () => { if (!scanned) { const field = form.querySelector('[name="source"]'); if (field) field.value = "manual"; if (active) stop("Camera stopped while you edit the code. Use Review code when ready."); } };
    const stopClick = () => stop("Scanner stopped. Type the code or start again.");
    const submitted = () => stop();
    start.addEventListener("click", startCamera); stopButton.addEventListener("click", stopClick); photo.addEventListener("change", choosePhoto);
    input.addEventListener("input", manual); form.addEventListener("submit", submitted);
    root.hidden = false;
    function destroy() { stop(); destroyed = true; start.removeEventListener("click", startCamera); stopButton.removeEventListener("click", stopClick); photo.removeEventListener("change", choosePhoto); input.removeEventListener("input", manual); form.removeEventListener("submit", submitted); }
    return { root, stop, destroy, startCamera, choosePhoto };
  }
  function install(env) {
    const scanners = new Map(), doc = env.document;
    function mount() {
      for (const [root, scanner] of scanners) if (!root.isConnected) { scanner.destroy(); scanners.delete(root); }
      for (const root of doc.querySelectorAll("[data-handheld-scanner]")) if (!scanners.has(root)) { const scanner = createScanner(root, env); if (scanner) scanners.set(root, scanner); }
    }
    const stopAll = () => { for (const scanner of scanners.values()) scanner.stop("Scanner paused. Start again when you’re ready."); };
    doc.addEventListener("visibilitychange", () => { if (doc.hidden) stopAll(); });
    env.addEventListener("pagehide", stopAll); env.addEventListener("popstate", stopAll);
    doc.addEventListener("handheld:pause", stopAll);
    doc.addEventListener("handheld:revoked", () => { for (const scanner of scanners.values()) { scanner.root.dataset.scanRevoked = "true"; scanner.destroy(); } });
    doc.addEventListener("htmx:beforeCleanupElement", event => { const target = event.detail?.elt; for (const [root, scanner] of scanners) if (target === root || target?.contains(root)) { scanner.destroy(); scanners.delete(root); } });
    doc.addEventListener("htmx:beforeRequest", event => { if (event.detail?.elt?.closest?.("form")?.querySelector("[data-handheld-scanner]")) stopAll(); });
    doc.addEventListener("htmx:afterSwap", mount); doc.addEventListener("htmx:historyRestore", mount); env.addEventListener("pageshow", mount);
    if (env.MutationObserver) new env.MutationObserver(mount).observe(doc.documentElement, { childList: true, subtree: true });
    if (doc.readyState === "loading") doc.addEventListener("DOMContentLoaded", mount); else mount();
    return { mount, stopAll, scanners };
  }
  const api = { LIMITS, decodePixels, photoSize, createDecoder, createScanner, install };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  if (global?.document) install(global);
})(typeof window !== "undefined" ? window : null);
