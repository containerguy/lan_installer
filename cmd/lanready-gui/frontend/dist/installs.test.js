const test = require("node:test");
const assert = require("node:assert/strict");
const installs = require("./installs.js");

test("sizes are shown so a multi-gigabyte download is never a surprise", () => {
  assert.equal(installs.formatSize(512), "512 B");
  assert.equal(installs.formatSize(2805653130), "2,6 GB");
  assert.equal(installs.formatSize(1288891233), "1,2 GB");
  assert.equal(installs.formatSize(undefined), "unbekannte Größe");
  assert.equal(installs.formatSize(-1), "unbekannte Größe");
});

test("a failed or missing list never looks like everything is installed", () => {
  const failed = installs.viewModel(null, "Server nicht erreichbar");
  assert.equal(failed.state, "error");
  assert.equal(failed.pendingCount, 0);
  const missing = installs.viewModel(undefined);
  assert.equal(missing.state, "unavailable");
  assert.notEqual(missing.state, "complete");
});

test("pending games are counted and named", () => {
  const model = installs.viewModel([
    { gameId: "flatout2", name: "FlatOut 2", sizeBytes: 2805653130, targetDir: "C:\\Users\\x\\LANReady Games\\flatout2", installed: false },
    { gameId: "wc3-tft", name: "WC3 TFT", sizeBytes: 1288891233, targetDir: "C:\\Users\\x\\LANReady Games\\wc3-tft", installed: true }
  ]);
  assert.equal(model.state, "pending");
  assert.equal(model.pendingCount, 1);
  assert.equal(model.items[0].sizeLabel, "2,6 GB");
  assert.match(model.message, /1 Spiel wartet/);
});

test("a fully installed event is reported as complete", () => {
  const model = installs.viewModel([{ gameId: "a", name: "A", sizeBytes: 10, targetDir: "C:\\a", installed: true }]);
  assert.equal(model.state, "complete");
  assert.equal(model.pendingCount, 0);
});

test("entries without a game id are dropped instead of rendered blank", () => {
  const model = installs.viewModel([{ name: "kaputt", sizeBytes: 1, installed: false }]);
  assert.equal(model.items.length, 0);
  assert.equal(model.state, "none");
});

test("the confirmation names size and target directory", () => {
  const text = installs.confirmText({ name: "FlatOut 2", sizeLabel: "2,6 GB", targetDir: "C:\\Users\\x\\LANReady Games\\flatout2" });
  assert.match(text, /FlatOut 2/);
  assert.match(text, /2,6 GB/);
  assert.match(text, /LANReady Games/);
});

test("the recommended executable is marked as a suggestion, not a decision", () => {
  assert.match(installs.candidateLabel({ relativePath: "FlatOut2.exe", sizeBytes: 12582912, recommended: true }), /Vorschlag/);
  assert.doesNotMatch(installs.candidateLabel({ relativePath: "unins000.exe", sizeBytes: 100, recommended: false }), /Vorschlag/);
});
