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
