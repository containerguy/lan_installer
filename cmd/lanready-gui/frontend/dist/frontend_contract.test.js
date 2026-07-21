"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");

const html = fs.readFileSync(__dirname + "/index.html", "utf8");
const app = fs.readFileSync(__dirname + "/app.js", "utf8");

test("event readiness UI ships all required accessible targets", () => {
  for (const id of ["ready-number", "ready-label", "event-readiness-panel", "event-readiness-title", "event-readiness-badge", "event-readiness-message", "event-readiness-components"]) {
    assert.match(html, new RegExp(`id=["']${id}["']`), `missing ${id}`);
    assert.match(app, new RegExp(`\\$\\(["']${id}["']\\)`), `app does not render ${id}`);
  }
  assert.ok(html.indexOf('src="readiness.js"') < html.indexOf('src="app.js"'), "readiness helper must load before app.js");
  assert.match(html, /aria-labelledby="event-readiness-title"/);
});

test("event readiness rendering uses text nodes instead of HTML injection", () => {
  const start = app.indexOf("function renderEventReadiness");
  const end = app.indexOf("\nfunction primaryAction", start);
  assert.ok(start >= 0 && end > start, "readiness renderer missing");
  const renderer = app.slice(start, end);
  assert.doesNotMatch(renderer, /innerHTML|insertAdjacentHTML/);
  assert.match(renderer, /textContent/);
});

test("standalone game flow is catalog-bound and accessible", () => {
  for (const id of ["manual-game-button", "manual-game-modal", "manual-game-form", "manual-game-catalog", "manual-game-path", "manual-game-browse", "manual-game-save", "manual-game-error"]) {
    assert.match(html, new RegExp(`id=["']${id}["']`), `missing ${id}`);
    assert.match(app, new RegExp(`["']${id}["']`), `app does not reference ${id}`);
  }
  assert.match(html, /Unbekannte Spiele können hier nicht frei eingetragen werden/);
  assert.doesNotMatch(app.slice(app.indexOf("async function openManualGame"), app.indexOf("async function removeManualGame")), /innerHTML|insertAdjacentHTML/);
});

test("install UI ships all required accessible targets", () => {
  // Static labels only have to exist in the markup.
  for (const id of ["install-panel", "install-title", "install-pick-modal", "install-pick-title", "install-pick-form"]) {
    assert.match(html, new RegExp(`id=["']${id}["']`), `missing ${id}`);
  }
  // Everything the app fills at runtime must exist in both places, otherwise a
  // renamed id silently stops updating.
  for (const id of ["install-badge", "install-message", "install-list", "install-pick-intro", "install-pick-select", "install-pick-save"]) {
    assert.match(html, new RegExp(`id=["']${id}["']`), `missing ${id}`);
    assert.match(app, new RegExp(`\\$\\(["']${id}["']\\)`), `app does not render ${id}`);
  }
  // Error slots are driven through setError rather than a direct lookup.
  for (const id of ["install-error", "install-pick-error"]) {
    assert.match(html, new RegExp(`id=["']${id}["'][^>]*role="alert"`), `${id} must be an alert region`);
    assert.match(app, new RegExp(`setError\\(["']${id}["']`), `app never reports errors into ${id}`);
  }
  assert.ok(html.indexOf('src="installs.js"') < html.indexOf('src="app.js"'), "installs helper must load before app.js");
  assert.match(html, /aria-labelledby="install-title"/);
  assert.match(html, /id="install-pick-modal"[^>]*role="dialog"[^>]*aria-modal="true"/);
});

test("install rendering builds nodes instead of injecting HTML", () => {
  const start = app.indexOf("function renderInstalls");
  const end = app.indexOf("async function installGame");
  assert.ok(start > -1 && end > start, "renderInstalls must exist");
  const body = app.slice(start, end);
  assert.doesNotMatch(body, /innerHTML|insertAdjacentHTML|outerHTML/, "install list must not inject HTML");
  assert.match(body, /textContent/);
});

test("a large install is never started without an explicit confirmation", () => {
  const start = app.indexOf("async function installGame");
  const end = app.indexOf("function openInstallPicker");
  assert.ok(start > -1 && end > start, "installGame must exist");
  const body = app.slice(start, end);
  const confirmAt = body.indexOf("window.confirm");
  const callAt = body.indexOf('api("InstallGame"');
  assert.ok(confirmAt > -1, "install must ask for confirmation");
  assert.ok(callAt > confirmAt, "confirmation must happen before the download starts");
});

test("the main executable is never chosen automatically", () => {
  const start = app.indexOf("async function saveInstalledExecutable");
  assert.ok(start > -1, "saveInstalledExecutable must exist");
  const body = app.slice(start);
  assert.match(body, /install-pick-select["']\)\.value/, "the registered path must come from the user's selection");
});
