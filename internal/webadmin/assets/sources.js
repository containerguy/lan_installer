(function () {
  "use strict";

  var csrf = document.querySelector("meta[name=csrf-token]").content;
  var canAdmin = document.querySelector("meta[name=can-admin]").content === "true";
  var canTest = document.querySelector("meta[name=can-test]").content === "true";
  var editor = document.getElementById("editor");
  var form = document.getElementById("source-form");
  var result = document.getElementById("result");
  var pageMessage = document.getElementById("page-message");
  var save = document.getElementById("save-source");
  var testButton = document.getElementById("test-source");
  var deleteButton = document.getElementById("delete-source");
  var deactivateButton = document.getElementById("deactivate-source");
  var addButton = document.getElementById("add-source");
  var retryButton = document.getElementById("retry-source");
  var fields = {
    id: document.getElementById("source-id"),
    revision: document.getElementById("source-revision"),
    token: document.getElementById("test-token"),
    name: document.getElementById("name"),
    kind: document.getElementById("kind"),
    url: document.getElementById("base-url"),
    auth: document.getElementById("auth-type"),
    username: document.getElementById("username"),
    password: document.getElementById("password"),
    enabled: document.getElementById("enabled")
  };
  var editable = [fields.name, fields.kind, fields.url, fields.auth, fields.username, fields.password, fields.enabled];
  var state = { generation: 0, controller: null, probeController: null, probeGeneration: 0, opener: null, sourceName: "", saveKey: "", deactivateKey: "", dirty: false, mutationBusy: false, disabledControls: [], loadID: 0 };

  function setStatus(message, type) {
    result.textContent = message;
    result.className = "notice" + (type ? " " + type : "");
  }

  function setBusy(busy) {
    editor.setAttribute("aria-busy", busy ? "true" : "false");
  }

  function setMutationBusy(busy) {
    state.mutationBusy = busy;
    editor.setAttribute("aria-busy", busy ? "true" : "false");
    var controls = Array.from(form.querySelectorAll("input,select,button"));
    if (busy) {
      state.disabledControls = controls.map(function (control) { return { control: control, disabled: control.disabled }; });
      controls.forEach(function (control) { control.disabled = true; });
    } else {
      state.disabledControls.forEach(function (entry) { entry.control.disabled = entry.disabled; });
      state.disabledControls = [];
    }
  }

  function setDestructiveAvailable(available, enabled) {
    if (deleteButton) {
      deleteButton.hidden = !available;
      deleteButton.disabled = !enabled;
    }
    if (deactivateButton) {
      deactivateButton.hidden = !available || !enabled;
      deactivateButton.disabled = !enabled;
    }
  }

  function clearErrors() {
    ["name", "url", "username", "password"].forEach(function (key) {
      var input = fields[key];
      var output = document.getElementById(key + "-error");
      if (input) input.removeAttribute("aria-invalid");
      if (output) output.textContent = "";
    });
  }

  function showFieldErrors(fieldErrors) {
    var aliases = { baseUrl: "url", name: "name", username: "username", password: "password" };
    var first = null;
    Object.keys(fieldErrors || {}).forEach(function (key) {
      var target = aliases[key] || key;
      var input = fields[target];
      var output = document.getElementById(target + "-error");
      if (input && output) {
        input.setAttribute("aria-invalid", "true");
        output.textContent = String(fieldErrors[key]);
        if (!first) first = input;
      }
    });
    if (first) first.focus();
  }

  function errorMessage(error, fallback) {
    var message = error && error.message ? error.message : fallback;
    if (error && error.requestId) message += " (Anfrage " + error.requestId + ")";
    return message;
  }

  async function parseResponse(response) {
    var text = await response.text();
    var data = null;
    if (text && (response.headers.get("content-type") || "").toLowerCase().indexOf("application/json") >= 0) {
      try { data = JSON.parse(text); } catch (_) { data = null; }
    }
    if (!response.ok) {
      var fallback = response.status === 401 ? "Die Sitzung ist abgelaufen. Bitte erneut anmelden." :
        response.status === 412 ? "Die Quelle wurde inzwischen geändert. Details neu laden und erneut prüfen." :
        "Der Server konnte die Anfrage nicht verarbeiten.";
      var failure = data || {};
      failure.message = failure.message || fallback;
      failure.status = response.status;
      failure.requestId = failure.requestId || response.headers.get("X-Request-ID") || "";
      throw failure;
    }
    if (response.status === 204) return null;
    if (!data) throw { message: "Der Server hat ein unerwartetes Antwortformat geliefert.", requestId: response.headers.get("X-Request-ID") || "" };
    return data;
  }

  async function send(url, options) {
    options = options || {};
    options.headers = Object.assign({ "Content-Type": "application/json", "X-CSRF-Token": csrf }, options.headers || {});
    var response;
    try {
      response = await fetch(url, options);
    } catch (error) {
      if (error && error.name === "AbortError") throw error;
      throw { message: "Netzwerkfehler. Verbindung prüfen und erneut versuchen." };
    }
    return parseResponse(response);
  }

  function newIdempotencyKey() {
    if (window.crypto && window.crypto.randomUUID) return window.crypto.randomUUID();
    return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, function (character) {
      var random = Math.floor(Math.random() * 16);
      var value = character === "x" ? random : (random & 3) | 8;
      return value.toString(16);
    });
  }

  function updateAuth() {
    var isHTTPS = fields.kind.value === "https";
    if (isHTTPS) fields.auth.value = "none";
    var basic = fields.auth.value === "basic" && !isHTTPS;
    if (canAdmin) {
      editable.forEach(function (field) { field.disabled = false; });
      fields.auth.disabled = isHTTPS;
      fields.username.disabled = !basic;
      fields.password.disabled = !basic;
      fields.username.required = basic;
      fields.password.required = basic && Number(fields.id.value) === 0;
    } else {
      editable.forEach(function (field) { field.disabled = true; });
      fields.username.required = false;
      fields.password.required = false;
      if (testButton && canTest) testButton.textContent = "Gespeicherte Verbindung erneut testen";
    }
  }

  function payload() {
    var existing = Number(fields.id.value) > 0;
    var basic = fields.auth.value === "basic" && fields.kind.value !== "https";
    return {
      sourceId: Number(fields.id.value),
      revision: Number(fields.revision.value),
      name: fields.name.value.trim(),
      kind: fields.kind.value,
      baseUrl: fields.url.value.trim(),
      enabled: fields.enabled.checked,
      auth: {
        type: basic ? "basic" : "none",
        username: basic ? fields.username.value.trim() : "",
        secretMode: basic ? (fields.password.value ? "replace" : (existing ? "reuse" : "replace")) : "none",
        password: basic ? fields.password.value : ""
      },
      testToken: fields.token.value
    };
  }

  function cancelProbe() {
    state.probeGeneration += 1;
    if (state.probeController) state.probeController.abort();
    state.probeController = null;
  }

  function payloadFingerprint(value) {
    var copy = JSON.parse(JSON.stringify(value));
    copy.testToken = "";
    return JSON.stringify(copy);
  }

  function resetForLoad(opener) {
    cancelProbe();
    if (state.controller) state.controller.abort();
    state.controller = new AbortController();
    state.generation += 1;
    state.opener = opener || state.opener;
    state.loadID = 0;
    state.dirty = false;
    if (retryButton) retryButton.hidden = true;
    state.sourceName = "";
    state.saveKey = "";
    state.deactivateKey = "";
    form.reset();
    fields.id.value = "0";
    fields.revision.value = "0";
    fields.token.value = "";
    clearErrors();
    editable.forEach(function (field) { field.disabled = true; });
    if (save) save.disabled = true;
    if (testButton) testButton.disabled = true;
    setDestructiveAvailable(false, false);
    setBusy(true);
    editor.hidden = false;
    setStatus("Quelle wird geladen …", "");
    editor.focus();
    return { generation: state.generation, signal: state.controller.signal };
  }

  function openCreate() {
    if (!canAdmin) return;
    if (state.mutationBusy) { setStatus("Die laufende Aktion wird noch abgeschlossen.", "error"); return; }
    if (!editor.hidden && state.dirty && !window.confirm("Ungespeicherte Änderungen verwerfen?")) return;
    cancelProbe();
    if (state.controller) state.controller.abort();
    state.generation += 1;
    state.controller = null;
    state.opener = addButton;
    state.loadID = 0;
    state.dirty = false;
    if (retryButton) retryButton.hidden = true;
    state.sourceName = "";
    state.saveKey = "";
    state.deactivateKey = "";
    form.reset();
    fields.id.value = "0";
    fields.revision.value = "0";
    fields.token.value = "";
    fields.enabled.checked = true;
    clearErrors();
    document.getElementById("editor-title").textContent = "Quelle hinzufügen";
    document.getElementById("editor-mode").textContent = "Neu";
    setDestructiveAvailable(false, false);
    if (save) save.disabled = true;
    if (testButton) testButton.disabled = false;
    setBusy(false);
    updateAuth();
    setStatus("Name und Basis-URL ausfüllen und anschließend die Verbindung testen.", "");
    editor.hidden = false;
    fields.name.focus();
  }

  async function openEdit(id, opener) {
    if (state.mutationBusy) { setStatus("Die laufende Aktion wird noch abgeschlossen.", "error"); return; }
    if (!editor.hidden && state.dirty && !window.confirm("Ungespeicherte Änderungen verwerfen?")) return;
    var load = resetForLoad(opener);
    try {
      state.loadID = id;
      var response;
      try {
        response = await fetch("/admin/api/v1/sources/" + encodeURIComponent(id), { signal: load.signal, headers: { "Accept": "application/json" } });
      } catch (networkError) {
        if (networkError && networkError.name === "AbortError") throw networkError;
        throw { message: "Netzwerkfehler. Verbindung prüfen und erneut versuchen." };
      }
      var data = await parseResponse(response);
      if (load.generation !== state.generation) return;
      fields.id.value = data.id;
      fields.revision.value = data.revision;
      fields.name.value = data.name;
      fields.kind.value = data.kind;
      fields.url.value = data.baseUrl;
      fields.enabled.checked = data.enabled;
      fields.auth.value = data.auth.type;
      fields.username.value = data.auth.username || "";
      fields.password.value = "";
      fields.token.value = "";
      state.sourceName = data.name;
      state.loadID = data.id;
      state.dirty = false;
      if (retryButton) retryButton.hidden = true;
      document.getElementById("editor-title").textContent = data.name + " bearbeiten";
      document.getElementById("editor-mode").textContent = canAdmin ? "Bearbeiten" : (canTest ? "Prüfen" : "Ansehen");
      setBusy(false);
      updateAuth();
      if (deleteButton) {
        deleteButton.hidden = !canAdmin;
        deleteButton.disabled = !canAdmin;
      }
      if (deactivateButton) {
        deactivateButton.hidden = !canAdmin || !data.enabled;
        deactivateButton.disabled = !canAdmin;
      }
      if (testButton) testButton.disabled = !canTest;
      if (save) save.disabled = true;
      setStatus(canAdmin ? "Passwort bleibt leer, um das gebundene Secret zu behalten. Vor dem Speichern erneut testen." :
        canTest ? "Operatoren testen ausschließlich die unveränderte gespeicherte Verbindung." : "Nur-Lese-Ansicht.", "");
      editor.focus();
    } catch (error) {
      if (error && error.name === "AbortError") return;
      if (load.generation !== state.generation) return;
      fields.id.value = "0";
      fields.revision.value = "0";
      state.sourceName = "";
      setBusy(false);
      setDestructiveAvailable(false, false);
      if (testButton) testButton.disabled = true;
      if (save) save.disabled = true;
      state.loadID = id;
      if (retryButton) retryButton.hidden = false;
      setStatus(errorMessage(error, "Quelle konnte nicht geladen werden. Bitte erneut versuchen."), "error");
      editor.focus();
      if (error && error.status === 401) window.location.assign("/admin/login?next=/admin/sources");
    }
  }

  function closeEditor(options) {
    options = options || {};
    if (state.mutationBusy) {
      setStatus("Die laufende Aktion wird noch abgeschlossen.", "error");
      return false;
    }
    if (!options.force && state.dirty && !window.confirm("Ungespeicherte Änderungen verwerfen?")) return false;
    cancelProbe();
    if (state.controller) state.controller.abort();
    state.generation += 1;
    editor.hidden = true;
    state.dirty = false;
    state.loadID = 0;
    if (retryButton) retryButton.hidden = true;
    setBusy(false);
    if (state.opener && document.contains(state.opener)) state.opener.focus();
  }

  function invalidate() {
    if (!canAdmin) return;
    state.dirty = true;
    cancelProbe();
    fields.token.value = "";
    state.saveKey = "";
    clearErrors();
    if (save) save.disabled = true;
    updateAuth();
    setStatus("Konfiguration geändert – Verbindung erneut testen.", "");
  }

  if (addButton) addButton.addEventListener("click", openCreate);
  document.querySelectorAll(".edit-source").forEach(function (button) {
    button.addEventListener("click", function () { openEdit(button.dataset.id, button); });
  });
  document.getElementById("cancel").addEventListener("click", closeEditor);
  if (retryButton) retryButton.addEventListener("click", function () {
    if (state.loadID) openEdit(state.loadID, state.opener);
  });

  editable.forEach(function (field) {
    field.addEventListener("input", invalidate);
    field.addEventListener("change", invalidate);
  });

  if (testButton) testButton.addEventListener("click", async function () {
    clearErrors();
    updateAuth();
    if (!form.reportValidity()) return;
    cancelProbe();
    state.probeController = new AbortController();
    var probeGeneration = state.probeGeneration;
    var editorGeneration = state.generation;
    var testedPayload = payload();
    var testedFingerprint = payloadFingerprint(testedPayload);
    testButton.disabled = true;
    if (save) save.disabled = true;
    setStatus("Verbindung wird sicher geprüft …", "");
    try {
      var data = await send("/admin/api/v1/sources/test", { method: "POST", body: JSON.stringify(testedPayload), signal: state.probeController.signal });
      if (probeGeneration !== state.probeGeneration || editorGeneration !== state.generation || testedFingerprint !== payloadFingerprint(payload())) return;
      fields.token.value = data.testToken;
      if (save && canAdmin) save.disabled = false;
      setStatus("Erfolgreich · " + data.capabilities.join(", ") + " · " + data.latencyMs + " ms · Test 5 Minuten gültig", "success");
    } catch (error) {
      if (error && error.name === "AbortError") return;
      if (probeGeneration !== state.probeGeneration || editorGeneration !== state.generation || testedFingerprint !== payloadFingerprint(payload())) return;
      fields.token.value = "";
      showFieldErrors(error.fieldErrors || {});
      setStatus(errorMessage(error, "Verbindungstest fehlgeschlagen."), "error");
      if (error.status === 401) window.location.assign("/admin/login?next=/admin/sources");
    } finally {
      if (probeGeneration === state.probeGeneration && editorGeneration === state.generation) {
        state.probeController = null;
        testButton.disabled = !canTest || Number(fields.id.value) === 0 && !canAdmin;
      }
    }
  });

  form.addEventListener("submit", async function (event) {
    event.preventDefault();
    if (!canAdmin) return;
    if (!fields.token.value) {
      setStatus("Bitte die aktuelle Konfiguration zuerst erfolgreich testen.", "error");
      return;
    }
    var data = payload();
    var editing = data.sourceId > 0;
    var url = editing ? "/admin/api/v1/sources/" + data.sourceId : "/admin/api/v1/sources";
    state.saveKey = state.saveKey || newIdempotencyKey();
    setMutationBusy(true);
    clearErrors();
    try {
      await send(url, {
        method: editing ? "PUT" : "POST",
        headers: Object.assign({ "Idempotency-Key": state.saveKey }, editing ? { "If-Match": "\"" + data.revision + "\"" } : {}),
        body: JSON.stringify(data)
      });
      state.dirty = false;
      sessionStorage.setItem("lanready-source-flash", editing ? "Quelle wurde gespeichert." : "Quelle wurde angelegt.");
      window.location.assign("/admin/sources");
    } catch (error) {
      showFieldErrors(error.fieldErrors || {});
      setStatus(errorMessage(error, error.status === 412 ? "Details neu laden, erneut testen und speichern." : "Quelle konnte nicht gespeichert werden."), "error");
      if (error.status === 401) window.location.assign("/admin/login?next=/admin/sources");
    } finally {
      setMutationBusy(false);
    }
  });

  if (deactivateButton) deactivateButton.addEventListener("click", async function () {
    var id = Number(fields.id.value);
    if (!id || !state.sourceName) return;
    if (!window.confirm("Quelle ‘" + state.sourceName + "’ deaktivieren? Bestehende Referenzen bleiben erhalten.")) return;
    state.deactivateKey = state.deactivateKey || newIdempotencyKey();
    setMutationBusy(true);
    try {
      await send("/admin/api/v1/sources/" + id + "/deactivate", {
        method: "POST",
        headers: { "If-Match": "\"" + fields.revision.value + "\"", "Idempotency-Key": state.deactivateKey },
        body: "{}"
      });
      state.dirty = false;
      sessionStorage.setItem("lanready-source-flash", "Quelle ‘" + state.sourceName + "’ wurde deaktiviert.");
      window.location.assign("/admin/sources");
    } catch (error) {
      setStatus(errorMessage(error, "Deaktivierung fehlgeschlagen."), "error");
    } finally {
      setMutationBusy(false);
    }
  });

  if (deleteButton) deleteButton.addEventListener("click", async function () {
    var id = Number(fields.id.value);
    if (!id || !state.sourceName) return;
    if (!window.confirm("Quelle ‘" + state.sourceName + "’ endgültig löschen?")) return;
    setMutationBusy(true);
    try {
      await send("/admin/api/v1/sources/" + id, { method: "DELETE", headers: { "If-Match": "\"" + fields.revision.value + "\"" } });
      state.dirty = false;
      sessionStorage.setItem("lanready-source-flash", "Quelle ‘" + state.sourceName + "’ wurde gelöscht.");
      window.location.assign("/admin/sources");
    } catch (error) {
      var refs = error.fieldErrors || {};
      var details = refs.launcherVersions || refs.gameVersions ? " Launcher-Versionen: " + (refs.launcherVersions || 0) + ", Spielversionen: " + (refs.gameVersions || 0) : "";
      setStatus(errorMessage(error, "Löschen fehlgeschlagen.") + details, "error");
    } finally {
      setMutationBusy(false);
    }
  });

  var search = document.getElementById("search");
  var kindFilter = document.getElementById("kind-filter");
  var enabledFilter = document.getElementById("enabled-filter");
  var sort = document.getElementById("sort");
  var rows = Array.from(document.querySelectorAll("#source-rows tr"));

  function applyListState() {
    var query = search ? search.value.trim().toLowerCase() : "";
    var visible = 0;
    rows.forEach(function (row) {
      var match = (!query || row.dataset.search.toLowerCase().indexOf(query) >= 0) &&
        (!kindFilter.value || row.dataset.kind === kindFilter.value) &&
        (!enabledFilter.value || row.dataset.enabled === enabledFilter.value);
      row.hidden = !match;
      if (match) visible += 1;
    });
    rows.sort(function (left, right) {
      var key = sort.value === "type" ? "kind" : sort.value === "status" ? "status" : "name";
      return left.dataset[key].localeCompare(right.dataset[key], "de", { sensitivity: "base" });
    }).forEach(function (row) { row.parentNode.appendChild(row); });
    var count = document.getElementById("result-count");
    if (count) count.textContent = visible + " von " + rows.length + " Quelle(n)";
    var noResults = document.getElementById("no-results");
    if (noResults) noResults.hidden = visible !== 0;
    var params = new URLSearchParams(window.location.search);
    [["q", query], ["kind", kindFilter.value], ["enabled", enabledFilter.value], ["sort", sort.value === "name" ? "" : sort.value]].forEach(function (entry) {
      if (entry[1]) params.set(entry[0], entry[1]); else params.delete(entry[0]);
    });
    history.replaceState(null, "", window.location.pathname + (params.toString() ? "?" + params.toString() : ""));
  }

  if (search && kindFilter && enabledFilter && sort) {
    var initial = new URLSearchParams(window.location.search);
    search.value = initial.get("q") || "";
    kindFilter.value = initial.get("kind") || "";
    enabledFilter.value = initial.get("enabled") || "";
    sort.value = initial.get("sort") || "name";
    [search, kindFilter, enabledFilter, sort].forEach(function (control) {
      control.addEventListener(control === search ? "input" : "change", applyListState);
    });
    applyListState();
  }

  window.addEventListener("beforeunload", function (event) {
    if (!state.dirty) return;
    event.preventDefault();
    event.returnValue = "";
  });

  var flash = sessionStorage.getItem("lanready-source-flash");
  if (flash) {
    sessionStorage.removeItem("lanready-source-flash");
    pageMessage.textContent = flash;
    pageMessage.className = "notice success";
    pageMessage.hidden = false;
  }
  updateAuth();
})();
