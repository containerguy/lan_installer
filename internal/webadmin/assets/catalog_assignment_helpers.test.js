"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const helpers = require("./catalog_assignment_helpers.js");

const versions = [
  { ID: 11, GameName: "Age of Empires", Version: "1", Enabled: true },
  { ID: 22, GameName: "Counter-Strike 2", Version: "2", Enabled: true },
  { ID: 33, GameName: "Deaktiviert", Version: "3", Enabled: false },
];

test("keeps an explicitly selected version instead of resetting to the first item", () => {
  assert.equal(helpers.retainedID(versions, 22), 22);
  assert.equal(helpers.retainedID(versions, 999), 11);
  assert.equal(helpers.retainedID([], 22), 0);
});

test("removes versions already assigned to the selected event only", () => {
  const assignments = [
    { EventID: 7, GameVersionID: 11 },
    { EventID: 8, GameVersionID: 22 },
  ];
  assert.deepEqual(helpers.availableVersions(versions, assignments, 7).map((item) => item.ID), [22]);
  assert.deepEqual(helpers.availableVersions(versions, assignments, 8).map((item) => item.ID), [11]);
  assert.deepEqual(helpers.availableVersions(versions, assignments, 0), []);
});

test("disables submit after the last available version was assigned", () => {
  const available = helpers.availableVersions([versions[1]], [{ EventID: 7, GameVersionID: 22 }], 7);
  const versionID = helpers.retainedID(available, 22);
  assert.equal(versionID, 0);
  assert.equal(helpers.canSubmit(7, versionID), false);
  assert.equal(helpers.canSubmit(7, 22), true);
});
