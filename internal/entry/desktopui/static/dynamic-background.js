/* Original procedural scenes. No images, network requests, or third-party runtime. */
(function (browser, factory) {
  "use strict";
  const create = factory();
  if (typeof module === "object" && module.exports) module.exports = { createDynamicBackground: create };
  if (!browser?.document) return;
  const mount = () => {
    const canvas = browser.document.getElementById("dynamic-background");
    if (canvas) browser.NovelDynamicBackground = create(canvas, browser);
  };
  if (browser.document.readyState === "loading") browser.document.addEventListener("DOMContentLoaded", mount, { once: true });
  else mount();
})(typeof window === "undefined" ? null : window, () => {
  "use strict";
  const themes = new Set(["starry", "aurora", "silk"]);
  const TAU = Math.PI * 2;
  const wrap = (n, max) => ((n % max) + max) % max;

  function random(seed) {
    return () => {
      seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
      return seed / 4294967296;
    };
  }

  function glow(ctx, x, y, radius, color, alpha) {
    const light = ctx.createRadialGradient(x, y, 0, x, y, radius);
    light.addColorStop(0, `rgba(${color},${alpha})`);
    light.addColorStop(.42, `rgba(${color},${alpha * .42})`);
    light.addColorStop(1, `rgba(${color},0)`);
    ctx.fillStyle = light;
    ctx.fillRect(x - radius, y - radius, radius * 2, radius * 2);
  }

  function stars(ctx, points, w, h, time, sparse = false, quality = 1) {
    const count = Math.floor(points.length * quality * (sparse ? .35 : 1));
    for (let i = 0; i < count; i++) {
      const star = points[i];
      const x = wrap(star.x * w + time * (1.8 + star.depth * 5), w + 8) - 4;
      const y = wrap(star.y * h - time * (.35 + star.depth * 1.4), h + 8) - 4;
      const shimmer = .65 + .3 * Math.sin(time * (.45 + star.depth * .4) + star.phase);
      const radius = .45 + star.depth * 1.25;
      ctx.globalAlpha = shimmer * (sparse ? .36 : .9);
      ctx.fillStyle = i % 5 === 0 ? "#bcb6ff" : "#d4e7ff";
      ctx.beginPath(); ctx.arc(x, y, radius, 0, TAU); ctx.fill();
      if (!sparse && i % 23 === 0) {
        glow(ctx, x, y, radius * 6, "129,178,255", .28 * shimmer);
        ctx.fillStyle = "#d9eaff";
        ctx.fillRect(x - radius * 3, y - .35, radius * 6, .7);
        ctx.fillRect(x - .35, y - radius * 3, .7, radius * 6);
      }
    }
    ctx.globalAlpha = 1;
  }

  function starry(ctx, points, w, h, time, quality) {
    ctx.fillStyle = "#070d21"; ctx.fillRect(0, 0, w, h);
    glow(ctx, w * (.7 + .07 * Math.sin(time * .06)), h * .24, w * .5, "62,62,156", .52);
    glow(ctx, w * (.39 + .06 * Math.cos(time * .08)), h * .66, w * .37, "25,100,157", .36);
    glow(ctx, w * .94, h * (.62 + .09 * Math.sin(time * .09)), w * .32, "126,55,137", .25);
    stars(ctx, points, w, h, time, false, quality);
    // A quiet, short meteor once every eighteen seconds, never a full-screen flash.
    const meteor = wrap(time + 3, 18);
    if (meteor < 1.3) {
      const phase = meteor / 1.3;
      const x = w * (.75 - phase * .36), y = h * (.13 + phase * .28);
      const tail = Math.min(w * .14, 170);
      const streak = ctx.createLinearGradient(x, y, x + tail, y - tail * .5);
      streak.addColorStop(0, `rgba(214,229,255,${Math.sin(phase * Math.PI) * .8})`);
      streak.addColorStop(1, "rgba(171,195,255,0)");
      ctx.strokeStyle = streak; ctx.lineWidth = 1.5;
      ctx.beginPath(); ctx.moveTo(x, y); ctx.lineTo(x + tail, y - tail * .5); ctx.stroke();
    }
  }

  function aurora(ctx, points, w, h, time, quality) {
    ctx.fillStyle = "#061a22"; ctx.fillRect(0, 0, w, h);
    glow(ctx, w * .75, h * .28, w * .7, "25,96,112", .42);
    stars(ctx, points, w, h, time, true, quality);
    const colors = ["93,239,197", "97,164,244", "170,116,239"];
    const segments = Math.round(88 * quality);
    ctx.globalCompositeOperation = "screen";
    for (let layer = 0; layer < 3; layer++) {
      const wave = (x) => h * (.18 + layer * .12 + .12 * Math.sin(x * 5.4 + time * .22 + layer * 1.9)
        + .06 * Math.sin(x * 10.2 - time * .13 + layer));
      for (let i = 0; i < segments; i++) {
        const x = i / segments, next = (i + 1.02) / segments;
        const height = h * (.25 + .1 * Math.sin(x * 6 + time * .16 + layer));
        const top = wave(x) - h * .04;
        const curtain = ctx.createLinearGradient(0, top, 0, top + height);
        curtain.addColorStop(0, `rgba(${colors[layer]},0)`);
        curtain.addColorStop(.13, `rgba(${colors[layer]},.78)`);
        curtain.addColorStop(.4, `rgba(${colors[layer]},.35)`);
        curtain.addColorStop(1, `rgba(${colors[layer]},0)`);
        ctx.fillStyle = curtain;
        ctx.globalAlpha = (.23 + .17 * (.5 + .5 * Math.sin(x * 53 + time * .4 + layer))) * (1 - layer * .12);
        ctx.beginPath();
        ctx.moveTo(x * w, top); ctx.lineTo(next * w, wave(next) - h * .04);
        ctx.lineTo(next * w, wave(next) - h * .04 + height); ctx.lineTo(x * w, top + height); ctx.closePath(); ctx.fill();
      }
      // Fine luminous folds keep the shape legible without a costly blur pass.
      for (let fold = 0; fold < 8; fold++) {
        ctx.globalAlpha = .055 * (1 - fold / 9);
        ctx.strokeStyle = `rgb(${colors[layer]})`; ctx.lineWidth = 2 + fold * 1.6;
        ctx.beginPath();
        for (let i = 0; i <= segments; i++) {
          const x = i / segments, y = wave(x) + fold * 2;
          if (i === 0) ctx.moveTo(0, y); else ctx.lineTo(x * w, y);
        }
        ctx.stroke();
      }
    }
    ctx.globalAlpha = 1; ctx.globalCompositeOperation = "source-over";
  }

  function silk(ctx, _points, w, h, time, quality) {
    ctx.fillStyle = "#1a1327"; ctx.fillRect(0, 0, w, h);
    glow(ctx, w * .68, h * .2, w * .65, "107,66,128", .48);
    const folds = Math.round(112 * quality), steps = Math.round(60 * quality);
    for (let fold = 0; fold < folds; fold++) {
      const position = fold / folds;
      const highlight = Math.pow(.5 + .5 * Math.sin(position * 28 - time * .18), 5);
      const sheen = ctx.createLinearGradient(0, h, w, 0);
      sheen.addColorStop(0, "rgba(86,65,125,0)");
      sheen.addColorStop(.24, `rgba(150,112,191,${.06 + highlight * .1})`);
      sheen.addColorStop(.58, `rgba(235,172,178,${.07 + highlight * .19})`);
      sheen.addColorStop(.8, `rgba(240,205,187,${.04 + highlight * .12})`);
      sheen.addColorStop(1, "rgba(130,110,180,0)");
      ctx.strokeStyle = sheen;
      ctx.lineWidth = h / folds * (3.8 + highlight * .8);
      ctx.beginPath();
      for (let step = 0; step <= steps; step++) {
        const x = step / steps;
        const y = h * (-.34 + position * 1.55 + x * .25
          + .16 * Math.sin(x * 4.8 + position * 3.8 + time * .19)
          + .08 * Math.sin(x * 9.5 - position * 4.7 - time * .13));
        if (step === 0) ctx.moveTo(0, y); else ctx.lineTo(x * w, y);
      }
      ctx.stroke();
    }
  }

  const painters = { starry, aurora, silk };
  return function createDynamicBackground(canvas, env) {
    const doc = env.document, root = doc.documentElement;
    const reduced = env.matchMedia("(prefers-reduced-motion: reduce)");
    const forced = env.matchMedia("(forced-colors: active)");
    let ctx;
    try { ctx = canvas.getContext("2d", { alpha: false, desynchronized: true }); } catch (_) { /* Static CSS fallback. */ }
    let theme = "", running = false, destroyed = false, handle = 0, lastTick = null, lastPaint = -Infinity;
    let elapsed = 0, frameCount = 0, width = 0, height = 0, pixelRatio = 1, dirty = true, slowFrames = 0;
    let quality = env.navigator?.hardwareConcurrency > 0 && env.navigator.hardwareConcurrency <= 4 ? .75 : 1;
    const rng = random(0x4e6f7665);
    const points = Array.from({ length: 205 }, () => ({ x: rng(), y: rng(), depth: rng(), phase: rng() * TAU }));
    const now = () => env.performance.now();
    const stop = () => {
      running = false;
      if (handle) env.cancelAnimationFrame(handle);
      handle = 0; lastTick = null;
    };
    function resize() {
      if (destroyed) return;
      const w = Math.max(1, Math.round(env.innerWidth)), h = Math.max(1, Math.round(env.innerHeight));
      // Bound backing-store cost on high-DPI and 4K displays as well as DPR.
      const ratio = Math.min(env.devicePixelRatio || 1, 1.5, Math.sqrt(2400000 / (w * h))) * quality;
      if (w !== width || h !== height || ratio !== pixelRatio) {
        width = w; height = h; pixelRatio = ratio;
        canvas.width = Math.max(1, Math.round(w * ratio)); canvas.height = Math.max(1, Math.round(h * ratio));
        dirty = true;
      }
    }
    function paint() {
      if (!ctx || !themes.has(theme) || doc.hidden || forced.matches) return;
      // Resizing clears a canvas. Apply a slower device's budget before the
      // next draw, never after it: a paused scene has no RAF to refill it.
      if (slowFrames >= 8 && quality > .6) { quality = .6; slowFrames = 0; resize(); }
      const started = now();
      ctx.setTransform(pixelRatio, 0, 0, pixelRatio, 0, 0);
      ctx.globalAlpha = 1; ctx.globalCompositeOperation = "source-over";
      painters[theme](ctx, points, width, height, elapsed, quality);
      frameCount++; dirty = false;
      if (now() - started > 24) slowFrames++; else slowFrames = Math.max(0, slowFrames - 1);
    }
    function tick(timestamp) {
      handle = 0;
      if (!running || destroyed) return;
      if (doc.hidden || reduced.matches || forced.matches || root.dataset.motion !== "on") { sync(); return; }
      if (lastTick !== null) elapsed += Math.min(100, Math.max(0, timestamp - lastTick)) / 1000;
      lastTick = timestamp;
      const interval = quality < .7 ? 50 : 1000 / 30;
      if (dirty || timestamp - lastPaint >= interval - .5) { paint(); lastPaint = timestamp; }
      handle = env.requestAnimationFrame(tick);
    }
    function sync() {
      if (destroyed) return;
      const next = root.dataset.theme;
      if (theme !== next) { theme = next; elapsed = 0; lastTick = null; lastPaint = -Infinity; dirty = true; }
      const active = Boolean(ctx) && themes.has(theme) && !forced.matches;
      canvas.hidden = !active;
      if (!active) { stop(); return; }
      resize();
      const animate = root.dataset.motion === "on" && !reduced.matches && !doc.hidden;
      if (!animate) stop();
      if (dirty && !doc.hidden) { paint(); lastPaint = now(); }
      if (animate && !running) { running = true; handle = env.requestAnimationFrame(tick); }
    }
    const resized = () => { resize(); sync(); };
    env.addEventListener("resize", resized);
    doc.addEventListener("visibilitychange", sync);
    reduced.addEventListener("change", sync);
    forced.addEventListener("change", sync);
    sync();
    return {
      sync,
      getStatus: () => ({ theme, running, frameCount, elapsed, width, height, pixelRatio, quality }),
      destroy() {
        if (destroyed) return;
        stop(); destroyed = true; canvas.hidden = true;
        env.removeEventListener("resize", resized); doc.removeEventListener("visibilitychange", sync);
        reduced.removeEventListener("change", sync); forced.removeEventListener("change", sync);
        canvas.width = 1; canvas.height = 1;
      },
    };
  };
});
