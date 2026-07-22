"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { clampPercentage, componentBadge, viewModel } = require("./readiness.js");

test("no active event remains explicit and never shows a false percentage", () => {
  const value = viewModel({state: "none", message: "Kein aktives Event."});
  assert.equal(value.label, "Kein aktives Event");
  assert.equal(value.percentage, null);
  assert.equal(value.ringText, "—");
  assert.match(value.eventTitle, /kein signiertes Event/i);
});

test("verified readiness exposes bounded progress and actionable component labels", () => {
  const value = viewModel({
    state: "warning", eventId: "lan-2026", percentage: 140,
    games: [{status: "version_mismatch"}],
    launchers: [{status: "detected_version_unverified"}]
  });
  assert.equal(value.percentage, 100);
  assert.equal(value.tone, "warn");
  assert.equal(value.games[0].statusLabel, "Andere Version installiert");
  assert.match(value.launchers[0].statusLabel, /Version ungeprüft/);
  assert.equal(clampPercentage(-10), 0);
});

test("security failures never look ready", () => {
  const value = viewModel({state: "security_error", percentage: 100});
  assert.equal(value.tone, "danger");
  assert.equal(value.percentage, null);
  assert.equal(value.ringText, "—");
});

test("optional components never demand action", () => {
  assert.equal(componentBadge("missing", false), "Optional offen");
  assert.equal(componentBadge("missing", true), "Handlung nötig");
  assert.equal(componentBadge("detected_version_unverified", true), "Version offen");
});

test("an installed launcher counts as ready even without any of its games", () => {
  assert.equal(componentBadge("detected", true), "Bereit");
});

test("a launcher only inferred from a game find keeps its version caveat", () => {
  assert.equal(componentBadge("detected_version_unverified", true), "Version offen");
});
