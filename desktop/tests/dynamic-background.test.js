"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { createDynamicBackground } = require("../../internal/entry/desktopui/static/dynamic-background.js");

function surface({ theme = "original", motion = "off", reduced = false, forced = false, context = true, cores = 8 } = {}) {
  const frames = new Map(), events = new Map(), docEvents = new Map();
  let time = 0, next = 0, cost = 0, painted = false;
  const drawing = [];
  const ctx = new Proxy({}, {
    set(target, key, value) { target[key] = value; drawing.push([key, value]); return true; },
    get(target, key) {
      if (key in target) return target[key];
      return (...args) => {
        drawing.push([key, ...args]);
        if (key === "fillRect") painted = true;
        if (key === "createLinearGradient" || key === "createRadialGradient") return {
          addColorStop(...stop) { drawing.push(["color", ...stop]); },
        };
      };
    },
  });
  const query = value => {
    const listeners = new Set();
    return { matches: value, addEventListener(_name, fn) { listeners.add(fn); }, removeEventListener(_name, fn) { listeners.delete(fn); },
      change(value) { this.matches = value; for (const fn of listeners) fn(); }, listeners };
  };
  const reducedQuery = query(reduced), forcedQuery = query(forced);
  const doc = { documentElement: { dataset: { theme, motion } }, hidden: false,
    addEventListener: (name, fn) => docEvents.set(name, fn), removeEventListener: name => docEvents.delete(name) };
  const env = { document: doc, innerWidth: 1280, innerHeight: 800, devicePixelRatio: 2, navigator: { hardwareConcurrency: cores },
    performance: { now() { const value = time; time += cost; return value; } },
    matchMedia: name => name.includes("reduced-motion") ? reducedQuery : forcedQuery,
    addEventListener: (name, fn) => events.set(name, fn), removeEventListener: name => events.delete(name),
    requestAnimationFrame(fn) { const id = ++next; frames.set(id, fn); return id; }, cancelAnimationFrame: id => frames.delete(id),
  };
  let width = 0, height = 0;
  const canvas = {
    get width() { return width; }, set width(value) { width = value; painted = false; },
    get height() { return height; }, set height(value) { height = value; painted = false; },
    hidden: false, getContext: () => context ? ctx : null,
  };
  const engine = createDynamicBackground(canvas, env);
  return { engine, env, canvas, frames, events, docEvents, drawing, reducedQuery, forcedQuery,
    tick(value) { time = value; const callbacks = [...frames.values()]; frames.clear(); for (const callback of callbacks) callback(value); },
    select(theme, motion = "on") { Object.assign(doc.documentElement.dataset, { theme, motion }); engine.sync(); },
    hide(value) { doc.hidden = value; docEvents.get("visibilitychange")?.(); },
    resize(w, h, dpr) { env.innerWidth = w; env.innerHeight = h; env.devicePixelRatio = dpr; events.get("resize")?.(); },
    slow(milliseconds) { cost = milliseconds; },
    hasPixels: () => painted,
  };
}

test("static original and legacy skins never allocate an animation loop", () => {
  const h = surface();
  for (const theme of ["original", "ink", "fresh", "romance", "genshin", "unknown"]) {
    h.select(theme);
    assert.equal(h.frames.size, 0);
    assert.equal(h.canvas.hidden, true);
    assert.equal(h.engine.getStatus().frameCount, 0);
  }
  h.engine.destroy();
});

test("each procedural scene changes its actual drawing geometry over time, capped at 30 fps", () => {
  for (const theme of ["starry", "aurora", "silk"]) {
    const h = surface({ theme, motion: "on" });
    const geometry = () => h.drawing.filter(entry => ["arc", "moveTo", "lineTo"].includes(entry[0]));
    const first = geometry();
    assert.ok(first.length > 50, `${theme} must paint a scene`);
    h.drawing.length = 0;
    for (let time = 0; time <= 1000; time += 10) h.tick(time);
    assert.notDeepEqual(geometry().slice(-first.length), first, `${theme} stayed static`);
    assert.ok(h.engine.getStatus().frameCount <= 31, `${theme} exceeded 30 fps`);
    assert.equal(h.frames.size, 1, "exactly one RAF producer");
    assert.ok(h.engine.getStatus().elapsed > .9);
    h.engine.destroy();
  }
});

test("motion-off freezes the scene, hidden and reduced-motion windows stop without changing preference", () => {
  const h = surface({ theme: "aurora", motion: "on" });
  h.tick(0); h.tick(100);
  h.hide(true);
  const paused = h.engine.getStatus();
  h.tick(100000);
  assert.equal(h.frames.size, 0);
  assert.equal(h.engine.getStatus().frameCount, paused.frameCount);
  h.hide(false); h.tick(100010);
  assert.equal(h.engine.getStatus().elapsed, paused.elapsed, "hidden time must not jump the animation");
  h.reducedQuery.change(true);
  assert.equal(h.frames.size, 0);
  assert.equal(h.env.document.documentElement.dataset.motion, "on");
  h.reducedQuery.change(false);
  assert.equal(h.frames.size, 1);
  h.select("aurora", "off");
  const frozen = h.engine.getStatus().frameCount;
  h.tick(100100); h.tick(100200);
  assert.equal(h.engine.getStatus().frameCount, frozen);
  assert.equal(h.canvas.hidden, false, "motion off retains a static scene");
  h.engine.destroy();
});

test("restored motion-off stays still and high contrast disables the canvas completely", () => {
  const h = surface({ theme: "silk", motion: "off" });
  assert.equal(h.engine.getStatus().frameCount, 1);
  assert.equal(h.frames.size, 0);
  h.forcedQuery.change(true);
  assert.equal(h.canvas.hidden, true);
  h.select("silk", "on");
  assert.equal(h.frames.size, 0);
  h.forcedQuery.change(false);
  assert.equal(h.canvas.hidden, false);
  assert.equal(h.frames.size, 1);
  h.engine.destroy();
});

test("resize bounds pixel allocation on high-DPI displays and slow painters reduce their budget", () => {
  const h = surface({ theme: "starry", motion: "on" });
  assert.ok(h.engine.getStatus().pixelRatio <= 1.5);
  h.resize(3840, 2160, 3);
  assert.ok(h.canvas.width * h.canvas.height <= 2410000);
  assert.equal(h.engine.getStatus().width, 3840);
  h.slow(25);
  for (let time = 100; time <= 1500; time += 100) h.tick(time);
  assert.equal(h.engine.getStatus().quality, .6);
  assert.ok(h.canvas.width * h.canvas.height < 1000000);
  h.engine.destroy();
});

test("slow static resize preserves its last picture and applies a lower budget only before a new draw", () => {
  const h = surface({ theme: "silk", motion: "off" });
  h.slow(25);
  for (let step = 1; step <= 8; step++) h.resize(1280 + step, 800, 1);
  assert.equal(h.hasPixels(), true, "the last slow draw was cleared by a backing-store resize");
  assert.equal(h.engine.getStatus().quality, 1, "lower quality must wait for the next actual draw");
  assert.equal(h.frames.size, 0, "a static scene must not schedule a refill loop");
  const count = h.engine.getStatus().frameCount;
  h.hide(true);
  assert.equal(h.hasPixels(), true);
  assert.equal(h.engine.getStatus().frameCount, count);
  h.hide(false);
  assert.equal(h.engine.getStatus().frameCount, count, "showing an unchanged static scene must not repaint");
  h.resize(1300, 800, 1);
  assert.equal(h.engine.getStatus().quality, .6);
  assert.equal(h.hasPixels(), true, "the resized lower-budget surface must be painted before return");
  assert.equal(h.frames.size, 0);
  h.engine.destroy();
});

test("a missing canvas context fails closed, and destroy releases callbacks and backing pixels", () => {
  const unavailable = surface({ theme: "starry", motion: "on", context: false });
  assert.equal(unavailable.frames.size, 0);
  assert.equal(unavailable.canvas.hidden, true);
  unavailable.engine.destroy();
  const h = surface({ theme: "silk", motion: "on" });
  h.engine.destroy(); h.engine.destroy();
  assert.equal(h.frames.size, 0);
  assert.equal(h.events.size + h.docEvents.size + h.reducedQuery.listeners.size + h.forcedQuery.listeners.size, 0);
  assert.equal(h.canvas.width * h.canvas.height, 1);
  h.select("aurora");
  assert.equal(h.frames.size, 0);
});
