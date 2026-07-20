"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const helpers = require("./catalog_cache_helpers.js");

test("terminal transition triggers a full catalog refresh", () => {
  const previous = [{ ID: "job-1", Status: "running" }];
  assert.equal(helpers.becameTerminal(previous, [{ ID: "job-1", Status: "succeeded" }]), true);
  assert.equal(helpers.becameTerminal(previous, [{ ID: "job-1", Status: "running" }]), false);
});

test("active cache job remains cancellable for a disabled version", () => {
  assert.equal(helpers.canShowAction(true, { Enabled: false }, { Status: "running" }), true);
  assert.equal(helpers.canShowAction(true, { Enabled: false }, { Status: "cancelled" }), false);
  assert.equal(helpers.canShowAction(false, { Enabled: true }, { Status: "running" }), false);
});

test("launcher-managed game version does not offer a cache import", () => {
  assert.equal(helpers.canShowAction(true, { Enabled: true, SourceID: 0 }, null), false);
  assert.equal(helpers.canShowAction(true, { Enabled: true, SourceID: 7 }, null), true);
});
