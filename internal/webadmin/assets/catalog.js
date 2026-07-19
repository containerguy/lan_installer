(() => {
  "use strict";

  const byId = (id) => document.getElementById(id);
  const csrf = document.querySelector('meta[name="csrf-token"]').content;
  const canEdit = document.querySelector('meta[name="can-edit"]').content === "true";
  const canDelete = document.querySelector('meta[name="can-delete"]').content === "true";
  const canPublish = document.querySelector('meta[name="can-publish"]').content === "true";
  const initialTab = document.querySelector('meta[name="initial-tab"]').content;
	const cacheHelpers = window.LANReadyCatalogCache;
  const validTabs = ["games", "launchers", "launcher-versions", "game-versions", "events"];
  const state = { data: null, tab: validTabs.includes(initialTab) ? initialTab : "games", selected: null, opener: null, dirty: false, busy: false, entityKey: "", deactivateKey: "", assignmentKey: "", releaseKey: "", releaseEnvelope: null, releasePayload: null, releaseStatus: null, updateKey: "", updateEnvelope: null, updatePayload: null, updateArtifactReady: false, cacheStatus: null, cacheMutationKeys: {}, cachePollTimer: 0, disabledControls: [] };

  const config = {
    games: {
      kind: "game", singular: "Spiel", title: "Spiele", subtitle: "Spiele und ihre Launcher-Zuordnung verwalten.",
      empty: "Lege das erste Spiel an.", columns: ["Name", "Launcher", "Externe ID", "Status", ""],
    },
    launchers: {
      kind: "launcher", singular: "Launcher", title: "Launcher", subtitle: "Steam, EA App und Ubisoft Connect samt Adapter verwalten.",
      empty: "Die drei initialen Launcher werden normalerweise automatisch angelegt.", columns: ["Name", "Adapter", "Slug", "Status", ""],
    },
    "launcher-versions": {
      kind: "launcher-version", singular: "Launcher-Version", title: "Launcher-Versionen", subtitle: "Installer, Quelle und verifizierte Installationsparameter zuordnen.",
      empty: "Lege eine Launcher-Version an, sobald eine geprüfte Quelle vorhanden ist.", columns: ["Launcher", "Version", "Quelle und Pfad", "Größe", "Cache", "Status", ""],
    },
    "game-versions": {
      kind: "game-version", singular: "Spiel-Version", title: "Spiel-Versionen", subtitle: "Spielpakete versionieren und einer externen Quelle zuordnen.",
      empty: "Lege eine Spiel-Version an, sobald Spiel und Quelle vorhanden sind.", columns: ["Spiel", "Version", "Quelle und Pfad", "Größe", "Cache", "Status", ""],
    },
    events: {
      kind: "event", singular: "Event", title: "Events", subtitle: "Event-Entwürfe und ihre benötigten Spielversionen verwalten.",
      empty: "Lege den ersten Event-Entwurf an.", columns: ["Name", "Zeitraum", "Status", "Spielversionen", ""],
    },
  };

  class APIError extends Error {
    constructor(message, status, requestId, fieldErrors) {
      super(message);
      this.status = status;
      this.requestId = requestId || "";
      this.fieldErrors = fieldErrors || {};
    }
  }

  async function api(path, options = {}) {
    const headers = Object.assign({ Accept: "application/json" }, options.headers || {});
    if (options.body !== undefined) {
      headers["Content-Type"] = "application/json";
      headers["X-CSRF-Token"] = csrf;
      options.body = JSON.stringify(options.body);
    } else if (options.method && options.method !== "GET") {
      headers["X-CSRF-Token"] = csrf;
    }
    let response;
    try {
      response = await fetch(path, Object.assign({ credentials: "same-origin" }, options, { headers }));
    } catch (error) {
      if (error && error.name === "AbortError") throw error;
      throw new APIError("Netzwerkfehler: Der Managementserver ist derzeit nicht erreichbar.", 0, "", {});
    }
    const text = await response.text();
    let payload = {};
    if (text) {
      try { payload = JSON.parse(text); } catch (_) {
        throw new APIError("Der Server hat keine lesbare Antwort geliefert.", response.status, response.headers.get("X-Request-ID"));
      }
    }
    if (!response.ok) {
      if (response.status === 401) {
        window.location.assign("/admin/login?next=" + encodeURIComponent(location.pathname + location.search));
      }
      throw new APIError(payload.message || "Die Anfrage ist fehlgeschlagen.", response.status, payload.requestId || response.headers.get("X-Request-ID"), payload.fieldErrors);
    }
    return payload;
  }

  function showPageMessage(message, type = "success") {
    const target = byId("page-message");
    target.textContent = message;
    target.className = "notice page-notice " + type;
    target.hidden = false;
    target.focus?.();
  }

  function clearPageMessage() {
    byId("page-message").hidden = true;
  }

  function errorText(error) {
    const details = [];
    const references = Number(error?.fieldErrors?.references || 0);
    if (references > 0) details.push(references + (references === 1 ? " abhängiger Eintrag" : " abhängige Einträge"));
    if (error?.requestId) details.push("Request-ID: " + error.requestId);
    return (error?.message || "Unbekannter Fehler.") + (details.length ? " · " + details.join(" · ") : "");
  }

  function newIdempotencyKey() {
    if (window.crypto && window.crypto.randomUUID) return window.crypto.randomUUID();
    return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (character) => {
      const random = Math.floor(Math.random() * 16);
      const value = character === "x" ? random : (random & 3) | 8;
      return value.toString(16);
    });
  }

  function revisionHeader(item) {
    return { "If-Match": "\"" + Number(item?.Revision || 0) + "\"" };
  }

  function normalizeSnapshot(value) {
    const snapshot = value && typeof value === "object" ? value : {};
    ["launchers", "sources", "games", "launcherVersions", "gameVersions", "events", "eventGames", "cacheJobs"].forEach((key) => {
      snapshot[key] = Array.isArray(snapshot[key]) ? snapshot[key] : [];
    });
    return snapshot;
  }

  function listForTab() {
    if (!state.data) return [];
    return {
      games: state.data.games,
      launchers: state.data.launchers,
      "launcher-versions": state.data.launcherVersions,
      "game-versions": state.data.gameVersions,
      events: state.data.events,
    }[state.tab] || [];
  }

  function isActive(item) {
    return state.tab === "events" ? item.Status !== "archived" : Boolean(item.Enabled);
  }

  function itemName(item) {
    switch (state.tab) {
      case "games":
      case "launchers":
      case "events": return item.Name;
      case "launcher-versions": return item.LauncherName + " " + item.Version;
      case "game-versions": return item.GameName + " " + item.Version;
      default: return "Eintrag";
    }
  }

  function searchText(item) {
    return Object.values(item).filter((value) => typeof value === "string" || typeof value === "number").join(" ").toLocaleLowerCase("de");
  }

  function filteredItems() {
    const query = byId("catalog-search").value.trim().toLocaleLowerCase("de");
    const status = byId("catalog-status").value;
    const sort = byId("catalog-sort").value;
    let items = listForTab().filter((item) => {
      if (query && !searchText(item).includes(query)) return false;
      if (state.tab === "events" && status && item.Status !== status) return false;
      if (state.tab !== "events" && status === "active" && !isActive(item)) return false;
      if (state.tab !== "events" && status === "inactive" && isActive(item)) return false;
      return true;
    });
    items = items.slice().sort((left, right) => {
      if (sort === "status") {
        const result = Number(isActive(right)) - Number(isActive(left));
        if (result) return result;
      }
      return itemName(left).localeCompare(itemName(right), "de", { sensitivity: "base", numeric: true });
    });
    return items;
  }

  function cell(row, label, value, className = "") {
    const td = document.createElement("td");
    td.dataset.label = label;
    if (className) td.className = className;
    if (value instanceof Node) td.append(value);
    else td.textContent = value == null || value === "" ? "–" : String(value);
    row.append(td);
    return td;
  }

  function badge(active, label) {
    const span = document.createElement("span");
    span.className = "badge " + (active ? "ok" : "inactive");
    const dot = document.createElement("span");
    dot.className = "dot";
    span.append(dot, document.createTextNode(label || (active ? "Aktiv" : "Deaktiviert")));
    return span;
  }

  function detail(primary, secondary) {
    const wrapper = document.createElement("div");
    const strong = document.createElement("div");
    strong.className = "source-name";
    strong.textContent = primary || "–";
    const small = document.createElement("div");
    small.className = "subtle";
    small.textContent = secondary || "";
    wrapper.append(strong, small);
    return wrapper;
  }

  function formatBytes(value) {
    const bytes = Number(value || 0);
    if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
    const units = ["B", "KB", "MB", "GB", "TB"];
    const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return new Intl.NumberFormat("de-DE", { maximumFractionDigits: index ? 1 : 0 }).format(bytes / 1024 ** index) + " " + units[index];
  }

  function formatDate(value) {
    if (!value) return "nicht festgelegt";
    const date = new Date(value);
    if (Number.isNaN(date.valueOf())) return value;
    return new Intl.DateTimeFormat("de-DE", { dateStyle: "medium", timeStyle: "short" }).format(date);
  }

  function statusForEvent(item) {
    const labels = { draft: "Entwurf", published: "Veröffentlicht", archived: "Archiviert" };
    return badge(item.Status !== "archived", labels[item.Status] || item.Status);
  }

  function actionButton(item) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "button";
    button.textContent = canEdit ? "Bearbeiten" : "Details";
    button.addEventListener("click", () => openEditor(item, button));
    return button;
  }

  function latestCacheJob(item) {
    if (!state.data || (state.tab !== "launcher-versions" && state.tab !== "game-versions")) return null;
    const targetType = state.tab === "launcher-versions" ? "launcher_version" : "game_version";
    return state.data.cacheJobs.find((job) => job.TargetType === targetType && Number(job.TargetID) === Number(item.ID)) || null;
  }

  function cacheStatus(item) {
    const job = latestCacheJob(item);
    if (!job) return detail("Noch nicht im Cache", item.SHA256 ? "Prüfsumme hinterlegt" : "Prüfsumme wird beim Import ermittelt");
    const labels = { queued: "Wartet", running: "Wird geladen", succeeded: "Verifiziert", failed: "Fehlgeschlagen", cancelled: "Abgebrochen" };
    let secondary = "Versuch " + Number(job.Attempts || 0);
    if (job.Status === "running") secondary = formatBytes(job.ProgressBytes) + (job.ExpectedSize > 0 ? " von " + formatBytes(job.ExpectedSize) : " übertragen");
	if (job.Status === "running" && job.CancelRequested) secondary = "Abbruch angefordert · " + formatBytes(job.ProgressBytes) + " übertragen";
    if (job.Status === "failed") secondary = job.ErrorMessage || job.ErrorCode || "Download konnte nicht abgeschlossen werden";
	const currentArtifact = job.Status === "succeeded" && item.SHA256 && item.SHA256 === job.ArtifactDigest && Number(item.SizeBytes) === Number(job.ArtifactSize);
    if (job.Status === "succeeded") secondary = currentArtifact ? formatBytes(job.ArtifactSize) + " · SHA-256 verifiziert" : "Katalogversion wurde seit diesem Import geändert";
    const displayStatus = job.Status === "succeeded" && !currentArtifact ? "stale" : job.Status;
	if (displayStatus === "stale") labels.stale = "Veraltet";
    const wrapper = detail(labels[displayStatus] || displayStatus, secondary);
    wrapper.classList.add("cache-state", "cache-" + displayStatus);
    return wrapper;
  }

  function cacheActionButton(item) {
    const job = latestCacheJob(item);
	if (!cacheHelpers.canShowAction(canEdit, item, job)) return null;
    const button = document.createElement("button");
    button.type = "button";
    button.className = "button";
    button.textContent = job?.Status === "queued" || job?.Status === "running" ? "Abbrechen" : job?.Status === "failed" || job?.Status === "cancelled" ? "Erneut versuchen" : job?.Status === "succeeded" ? "Neu prüfen" : "In Cache laden";
    button.addEventListener("click", () => mutateCacheJob(item, job, button));
    return button;
  }

  function versionActions(item) {
    const group = document.createElement("div");
    group.className = "table-actions";
    const cacheButton = cacheActionButton(item);
    if (cacheButton) group.append(cacheButton);
    group.append(actionButton(item));
    return group;
  }

  async function mutateCacheJob(item, job, button) {
    if (state.busy) return;
    const active = job?.Status === "queued" || job?.Status === "running";
    const retry = job?.Status === "failed" || job?.Status === "cancelled";
    const operation = active ? "cancel" : retry ? "retry" : "enqueue";
    const keyName = operation + ":" + (job?.ID || state.tab + ":" + item.ID);
    state.busy = true;
    button.disabled = true;
    clearPageMessage();
    try {
      state.cacheMutationKeys[keyName] = state.cacheMutationKeys[keyName] || newIdempotencyKey();
      if (operation === "enqueue") {
        await api("/admin/api/v1/cache-jobs", { method: "POST", headers: { "Idempotency-Key": state.cacheMutationKeys[keyName] }, body: { targetType: config[state.tab].kind, targetId: item.ID } });
      } else {
        await api(`/admin/api/v1/cache-jobs/${encodeURIComponent(job.ID)}/${operation}`, { method: "POST", headers: { "Idempotency-Key": state.cacheMutationKeys[keyName] } });
      }
      delete state.cacheMutationKeys[keyName];
      await refreshCacheJobs();
      showPageMessage(operation === "cancel" ? "Abbruch des Cache-Auftrags wurde angefordert." : operation === "retry" ? "Cache-Auftrag wurde erneut eingeplant." : "Cache-Auftrag wurde eingeplant.");
    } catch (error) {
      showPageMessage(errorText(error), "error");
    } finally {
      state.busy = false;
      button.disabled = false;
    }
  }

  function rowFor(item) {
    const row = document.createElement("tr");
    switch (state.tab) {
      case "games":
        cell(row, "Name", detail(item.Name, item.Slug));
        cell(row, "Launcher", item.LauncherName);
        cell(row, "Externe ID", item.ExternalGameID);
        cell(row, "Status", badge(item.Enabled));
        break;
      case "launchers":
        cell(row, "Name", detail(item.Name, item.Slug));
        cell(row, "Adapter", item.Adapter);
        cell(row, "Slug", item.Slug, "mono");
        cell(row, "Status", badge(item.Enabled));
        break;
      case "launcher-versions":
        cell(row, "Launcher", item.LauncherName);
        cell(row, "Version", detail(item.Version, item.SHA256 ? "SHA-256 hinterlegt" : "Prüfsumme fehlt"));
        cell(row, "Quelle und Pfad", detail(item.SourceName, item.SourcePath));
        cell(row, "Größe", formatBytes(item.SizeBytes));
		cell(row, "Cache", cacheStatus(item));
        cell(row, "Status", badge(item.Enabled));
        break;
      case "game-versions":
        cell(row, "Spiel", item.GameName);
        cell(row, "Version", detail(item.Version, item.SHA256 ? "SHA-256 hinterlegt" : "Prüfsumme fehlt"));
        cell(row, "Quelle und Pfad", detail(item.SourceName, item.SourcePath));
        cell(row, "Größe", formatBytes(item.SizeBytes));
		cell(row, "Cache", cacheStatus(item));
        cell(row, "Status", badge(item.Enabled));
        break;
      case "events": {
        const assignments = state.data.eventGames.filter((entry) => entry.EventID === item.ID).length;
        cell(row, "Name", detail(item.Name, item.Slug));
        cell(row, "Zeitraum", detail(formatDate(item.StartsAt), item.EndsAt ? "bis " + formatDate(item.EndsAt) : "ohne Endzeit"));
        cell(row, "Status", statusForEvent(item));
        cell(row, "Spielversionen", String(assignments));
        break;
      }
    }
    const versionTab = state.tab === "launcher-versions" || state.tab === "game-versions";
    cell(row, "Aktion", versionTab ? versionActions(item) : actionButton(item));
    return row;
  }

  function updateStatusFilter() {
    const select = byId("catalog-status");
    const mode = state.tab === "events" ? "events" : "enabled";
    if (select.dataset.mode === mode) return;
    const values = mode === "events"
      ? [["", "Alle Event-Status"], ["draft", "Entwurf"], ["published", "Veröffentlicht"], ["archived", "Archiviert"]]
      : [["", "Alle Status"], ["active", "Aktiv"], ["inactive", "Deaktiviert"]];
    select.replaceChildren(...values.map(([value, label]) => {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = label;
      return option;
    }));
    select.dataset.mode = mode;
  }

  function renderChrome(scrollTab = false) {
    const current = config[state.tab];
    byId("page-title").textContent = current.title;
    byId("page-subtitle").textContent = current.subtitle;
    byId("event-assignment").hidden = state.tab !== "events";
    byId("event-release").hidden = state.tab !== "events";
    byId("client-update-release").hidden = state.tab !== "events";
    byId("cache-management").hidden = state.tab !== "launcher-versions" && state.tab !== "game-versions";
    byId("add-entity") && (byId("add-entity").textContent = current.singular + " hinzufügen");
    updateStatusFilter();

    let activeTab = null;
    document.querySelectorAll("[data-tab]").forEach((button) => {
      const active = button.dataset.tab === state.tab;
      button.setAttribute("aria-selected", String(active));
      button.classList.toggle("active", active);
      button.tabIndex = active ? 0 : -1;
      if (active) activeTab = button;
    });
    byId("catalog-panel").setAttribute("aria-labelledby", "tab-" + state.tab);
    if (scrollTab && activeTab) {
      requestAnimationFrame(() => activeTab.scrollIntoView({ behavior: "auto", block: "nearest", inline: "center" }));
    }
  }

  function renderCacheManagement(message = "") {
    const usage = Number(state.cacheStatus?.usageBytes || 0);
    const quota = Number(state.cacheStatus?.quotaBytes || 0);
    const percentage = quota > 0 ? Math.min(100, Math.max(0, Math.round(usage * 1000 / quota) / 10)) : 0;
    byId("cache-usage").textContent = quota > 0 ? formatBytes(usage) + " von " + formatBytes(quota) + " registriert" : "Cacheverbrauch ist nicht verfügbar";
    byId("cache-usage-detail").textContent = quota > 0 ? percentage.toLocaleString("de-DE") + " % der konfigurierten Quota. Temporäre oder verwaiste Dateien außerhalb der Metadaten sind nicht enthalten." : "Prüfe Server- und Cachekonfiguration.";
    const progress = byId("cache-usage-progress");
    progress.value = percentage;
    progress.textContent = percentage.toLocaleString("de-DE") + " %";
    if (message) byId("cache-gc-result").textContent = message;
  }

  async function loadCacheStatus() {
    try {
      state.cacheStatus = await api("/admin/api/v1/cache-status");
      renderCacheManagement();
    } catch (error) {
      state.cacheStatus = null;
      renderCacheManagement("Cacheverbrauch konnte nicht geladen werden: " + errorText(error));
    }
  }

  async function runCacheGarbageCollection() {
    if (state.busy || !canDelete) return;
    const age = Number(byId("cache-gc-age").value);
    const label = byId("cache-gc-age").selectedOptions[0]?.textContent || "der gewählten Frist";
    if (!window.confirm("Alle unreferenzierten Cacheartefakte älter als „" + label + "“ werden dauerhaft entfernt. Katalog- und Releaseartefakte bleiben geschützt. Fortfahren?")) return;
    const button = byId("run-cache-gc");
    state.busy = true;
    button.disabled = true;
    byId("cache-gc-age").disabled = true;
    byId("cache-management").setAttribute("aria-busy", "true");
    byId("cache-gc-result").textContent = "Unreferenzierte Cacheartefakte werden geprüft …";
    try {
      const result = await api("/admin/api/v1/cache-gc", { method: "POST", headers: { "Idempotency-Key": newIdempotencyKey() }, body: { minimumAgeHours: age, limit: 1000 } });
      state.cacheStatus = { usageBytes: result.usageBytes, quotaBytes: result.quotaBytes };
      const message = result.removed > 0 ? result.removed + (result.removed === 1 ? " Artefakt" : " Artefakte") + " entfernt, " + formatBytes(result.removedBytes) + " registrierten Cache freigegeben." : "Keine passenden unreferenzierten Artefakte gefunden.";
      renderCacheManagement(message + " Geprüft: " + result.examined + "." + (result.partial ? " Der Lauf wurde nach einem Fehler sicher beendet; starte ihn erneut." : result.examined >= 1000 ? " Das Prüflimit wurde erreicht; möglicherweise ist ein weiterer Lauf nötig." : ""));
    } catch (error) {
      byId("cache-gc-result").textContent = errorText(error);
    } finally {
      state.busy = false;
      button.disabled = false;
      byId("cache-gc-age").disabled = false;
      byId("cache-management").setAttribute("aria-busy", "false");
    }
  }

  function render() {
    renderChrome();
    if (!state.data) return;
    const current = config[state.tab];
    const head = byId("catalog-head");
    head.replaceChildren(...current.columns.map((label) => {
      const th = document.createElement("th");
      th.textContent = label;
      return th;
    }));

    const allItems = listForTab();
    const items = filteredItems();
    const rows = byId("catalog-rows");
    rows.replaceChildren(...items.map(rowFor));
    byId("catalog-table-wrap").hidden = items.length === 0;
    byId("catalog-empty").hidden = items.length !== 0;
    const filtered = Boolean(byId("catalog-search").value.trim() || byId("catalog-status").value);
    byId("catalog-empty-title").textContent = filtered && allItems.length ? "Keine Treffer" : "Noch keine Einträge";
    byId("catalog-empty-text").textContent = filtered && allItems.length ? "Passe Suche oder Statusfilter an." : current.empty;
    const count = items.length + (items.length === 1 ? " Eintrag" : " Einträge");
    byId("catalog-count").textContent = items.length === allItems.length ? count : count + " von " + allItems.length;
    renderAssignments();
  }

  function populateSelect(select, items, label, selected) {
    if (!select) return;
    const values = Array.isArray(items) ? items : [];
    select.replaceChildren();
    if (!values.length) {
      const option = document.createElement("option");
      option.value = "";
      option.textContent = "Keine Auswahl verfügbar";
      select.append(option);
      select.disabled = true;
      return;
    }
    select.disabled = false;
    for (const item of values) {
      const option = document.createElement("option");
      option.value = String(item.ID);
      option.textContent = label(item);
      option.selected = Number(selected) === Number(item.ID);
      select.append(option);
    }
  }

  function selectableParents(items, selected, available) {
    const selectedID = Number(selected || 0);
    return (Array.isArray(items) ? items : []).filter((entry) => available(entry) || Number(entry.ID) === selectedID);
  }

  function parentLabel(item, text, available) {
    return text + (available(item) ? "" : " · deaktiviert");
  }

  function refreshSelects(item = {}) {
    if (!state.data) return;
    const enabled = (entry) => Boolean(entry.Enabled);
    populateSelect(byId("entity-launcher"), selectableParents(state.data.launchers, item.LauncherID, enabled), (entry) => parentLabel(entry, entry.Name, enabled), item.LauncherID);
    populateSelect(byId("version-launcher"), selectableParents(state.data.launchers, item.LauncherID, enabled), (entry) => parentLabel(entry, entry.Name, enabled), item.LauncherID);
    populateSelect(byId("version-game"), selectableParents(state.data.games, item.GameID, enabled), (entry) => parentLabel(entry, entry.Name, enabled), item.GameID);
    populateSelect(byId("entity-source"), selectableParents(state.data.sources, item.SourceID, enabled), (entry) => parentLabel(entry, entry.Name + " · " + entry.Kind, enabled), item.SourceID);
    populateSelect(byId("assignment-event"), state.data.events.filter((entry) => entry.Status === "draft"), (entry) => entry.Name, 0);
    populateSelect(byId("assignment-version"), state.data.gameVersions.filter(enabled), (entry) => entry.GameName + " · " + entry.Version, 0);
  }

  function visibleKinds(kind) {
    document.querySelectorAll("[data-kinds]").forEach((element) => {
      const visible = element.dataset.kinds.split(" ").includes(kind);
      element.hidden = !visible;
      element.querySelectorAll("input,select").forEach((control) => { control.disabled = !visible; });
    });
  }

  function clearErrors() {
    document.querySelectorAll(".field-error").forEach((element) => { element.textContent = ""; });
    document.querySelectorAll('[aria-invalid="true"]').forEach((element) => element.removeAttribute("aria-invalid"));
    byId("editor-message").textContent = "";
    byId("editor-message").className = "notice";
  }

  function setError(id, message) {
    const control = byId(id);
    const target = byId(id + "-error");
    if (target) target.textContent = message;
    control?.setAttribute("aria-invalid", "true");
  }

  function resetEditorValues() {
    byId("entity-id").value = "0";
    ["entity-name", "entity-slug", "entity-external-id", "entity-version", "entity-path", "entity-sha", "entity-silent-args", "event-start", "event-end"].forEach((id) => { byId(id).value = ""; });
    byId("entity-size").value = "0";
    byId("entity-enabled").checked = true;
    byId("event-status").value = "draft";
  }

  function toLocalDateTime(value) {
    if (!value) return "";
    const date = new Date(value);
    if (Number.isNaN(date.valueOf())) return value.slice(0, 16);
    const local = new Date(date.valueOf() - date.getTimezoneOffset() * 60000);
    return local.toISOString().slice(0, 16);
  }

  function toAPIDateTime(value) {
    if (!value) return "";
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? value : date.toISOString();
  }

  function openEditor(item = null, opener = null) {
    if (state.busy) {
      showPageMessage("Die laufende Aktion wird noch abgeschlossen.", "error");
      return;
    }
    if (!state.data) {
      showPageMessage("Die Katalogdaten sind noch nicht verfügbar. Bitte lade die Übersicht erneut.", "error");
      return;
    }
    clearErrors();
    resetEditorValues();
    state.selected = item;
    state.opener = opener || document.activeElement;
    state.dirty = false;
    state.entityKey = "";
    state.deactivateKey = "";
    const current = config[state.tab];
    visibleKinds(current.kind);
    refreshSelects(item || {});
    byId("editor-title").textContent = item ? current.singular + " bearbeiten" : current.singular + " hinzufügen";
    byId("editor-mode").textContent = item ? "Bearbeiten" : "Neu";
    byId("entity-id").value = item ? item.ID : 0;

    if (item) {
      byId("entity-name").value = item.Name || "";
      byId("entity-slug").value = item.Slug || "";
      byId("entity-adapter").value = item.Adapter || "steam";
      byId("entity-external-id").value = item.ExternalGameID || "";
      byId("entity-version").value = item.Version || "";
      byId("entity-path").value = item.SourcePath || "";
      byId("entity-sha").value = item.SHA256 || "";
      byId("entity-size").value = item.SizeBytes || 0;
      byId("entity-silent-args").value = item.SilentArgsVerified && Array.isArray(item.SilentArgs) ? item.SilentArgs.join(" · ") : "Nicht verifiziert";
      byId("entity-enabled").checked = item.Enabled !== false;
      byId("event-start").value = toLocalDateTime(item.StartsAt);
      byId("event-end").value = toLocalDateTime(item.EndsAt);
      if (item.Status) byId("event-status").value = item.Status;
    }

    const immutable = state.tab === "events" && item?.Status === "published";
    if (immutable) {
      byId("editor-message").textContent = "Dieses Event ist veröffentlicht und deshalb unveränderlich.";
      byId("editor-message").className = "notice error";
    }
    byId("catalog-form").querySelectorAll("input,select").forEach((control) => {
      if (!control.closest("[hidden]")) control.disabled = immutable || !canEdit;
    });
    if (byId("save-entity")) byId("save-entity").hidden = immutable;
    if (byId("delete-entity")) byId("delete-entity").hidden = !item || immutable;
    if (byId("deactivate-entity")) {
      byId("deactivate-entity").hidden = !item || immutable || (state.tab !== "events" && item.Enabled === false) || (state.tab === "events" && item.Status === "archived");
      byId("deactivate-entity").textContent = state.tab === "events" ? "Archivieren" : "Deaktivieren";
    }
    byId("catalog-editor").hidden = false;
    byId("catalog-editor").focus();
  }

  function closeEditor(options = {}) {
    const force = Boolean(options.force);
    const restoreFocus = options.restoreFocus !== false;
    if (state.busy && !force) {
      showEditorMessage("Die laufende Aktion wird noch abgeschlossen.", "error");
      return false;
    }
    if (!force && state.dirty && !window.confirm("Ungespeicherte Änderungen verwerfen?")) return false;
    const opener = state.opener;
    state.selected = null;
    state.opener = null;
    state.dirty = false;
    byId("catalog-editor").hidden = true;
    if (restoreFocus && opener && document.contains(opener)) opener.focus();
    return true;
  }

  function setEditorBusy(busy) {
    state.busy = busy;
    byId("catalog-editor").setAttribute("aria-busy", String(busy));
    const controls = Array.from(byId("catalog-form").querySelectorAll("input,select,button"));
    if (busy) {
      state.disabledControls = controls.map((control) => ({ control, disabled: control.disabled }));
      controls.forEach((control) => { control.disabled = true; });
    } else {
      state.disabledControls.forEach((entry) => { entry.control.disabled = entry.disabled; });
      state.disabledControls = [];
    }
  }

  function intValue(id) {
    return Number.parseInt(byId(id).value, 10) || 0;
  }

  function validate(kind) {
    clearErrors();
    let valid = true;
    if (["launcher", "game", "event"].includes(kind)) {
      const name = byId("entity-name").value.trim();
      const slug = byId("entity-slug").value.trim();
      if (!name) { setError("entity-name", "Bitte gib einen Namen ein."); valid = false; }
      if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(slug)) { setError("entity-slug", "Nur Kleinbuchstaben, Zahlen und einzelne Bindestriche."); valid = false; }
    }
    if (kind === "game" && !intValue("entity-launcher")) { setError("entity-launcher", "Bitte wähle einen Launcher."); valid = false; }
    if (kind === "launcher-version" || kind === "game-version") {
      if (kind === "launcher-version" && !intValue("version-launcher")) { setError("version-launcher", "Bitte wähle einen Launcher."); valid = false; }
      if (kind === "game-version" && !intValue("version-game")) { setError("version-game", "Bitte wähle ein Spiel."); valid = false; }
      if (!byId("entity-version").value.trim()) { setError("entity-version", "Bitte gib eine Version ein."); valid = false; }
      if (!intValue("entity-source")) { setError("entity-source", "Bitte wähle eine Quelle."); valid = false; }
      const sourcePath = byId("entity-path").value.trim();
      if (!sourcePath || sourcePath.startsWith("/") || sourcePath.includes("\\") || sourcePath.split("/").includes("..")) {
        setError("entity-path", "Nutze einen relativen Pfad ohne .. oder Backslashes."); valid = false;
      }
      const sha = byId("entity-sha").value.trim();
      if (sha && !/^[a-fA-F0-9]{64}$/.test(sha)) { setError("entity-sha", "Erwartet werden genau 64 Hex-Zeichen."); valid = false; }
      if (Number(byId("entity-size").value) < 0) { setError("entity-size", "Die Größe darf nicht negativ sein."); valid = false; }
    }
    if (kind === "event" && byId("event-start").value && byId("event-end").value && byId("event-end").value < byId("event-start").value) {
      setError("event-end", "Das Ende muss nach dem Beginn liegen."); valid = false;
    }
    return valid;
  }

  function payload(kind) {
    const id = intValue("entity-id");
    const base = { id, enabled: byId("entity-enabled").checked };
    if (kind === "launcher") return Object.assign(base, { name: byId("entity-name").value.trim(), slug: byId("entity-slug").value.trim(), adapter: byId("entity-adapter").value });
    if (kind === "game") return Object.assign(base, { name: byId("entity-name").value.trim(), slug: byId("entity-slug").value.trim(), launcherId: intValue("entity-launcher"), externalGameId: byId("entity-external-id").value.trim() });
    if (kind === "launcher-version") return Object.assign(base, { launcherId: intValue("version-launcher"), version: byId("entity-version").value.trim(), sourceId: intValue("entity-source"), sourcePath: byId("entity-path").value.trim(), sha256: byId("entity-sha").value.trim(), sizeBytes: intValue("entity-size") });
    if (kind === "game-version") return Object.assign(base, { gameId: intValue("version-game"), version: byId("entity-version").value.trim(), sourceId: intValue("entity-source"), sourcePath: byId("entity-path").value.trim(), sha256: byId("entity-sha").value.trim(), sizeBytes: intValue("entity-size") });
    return { id, name: byId("entity-name").value.trim(), slug: byId("entity-slug").value.trim(), startsAt: toAPIDateTime(byId("event-start").value), endsAt: toAPIDateTime(byId("event-end").value), status: byId("event-status").value, enabled: true };
  }

  async function saveEntity(event) {
    event.preventDefault();
    const current = config[state.tab];
    if (!validate(current.kind)) {
      byId("editor-message").textContent = "Bitte korrigiere die markierten Felder.";
      byId("editor-message").className = "notice error";
      byId("catalog-form").querySelector('[aria-invalid="true"]')?.focus();
      return;
    }
    const body = payload(current.kind);
    const id = body.id;
    const path = "/admin/api/v1/catalog/" + current.kind + (id ? "/" + id : "");
    setEditorBusy(true);
    try {
      const headers = id ? revisionHeader(state.selected) : { "Idempotency-Key": state.entityKey || (state.entityKey = newIdempotencyKey()) };
      const result = await api(path, { method: id ? "PUT" : "POST", headers, body });
      state.entityKey = "";
      state.dirty = false;
      closeEditor({ force: true, restoreFocus: false });
      const refreshed = await load("Die Änderung wurde gespeichert, aber die Übersicht konnte nicht aktualisiert werden.");
      if (refreshed) showPageMessage(result.message || "Änderungen wurden gespeichert.");
    } catch (error) {
      showEditorMessage(errorText(error), "error");
    } finally {
      setEditorBusy(false);
    }
  }

  async function deactivateEntity() {
    if (!state.selected) return;
    const current = config[state.tab];
    const selected = state.selected;
    const expected = itemName(selected);
    if (window.prompt('Zum Bestätigen "' + expected + '" eingeben:') !== expected) {
      showEditorMessage("Deaktivierung wurde nicht bestätigt.", "error");
      return;
    }
    setEditorBusy(true);
    try {
      let result;
      if (current.kind === "event") {
        const body = payload("event");
        body.status = "archived";
        result = await api("/admin/api/v1/catalog/event/" + selected.ID, { method: "PUT", headers: revisionHeader(selected), body });
      } else {
        state.deactivateKey = state.deactivateKey || newIdempotencyKey();
        result = await api("/admin/api/v1/catalog/" + current.kind + "/" + selected.ID + "/deactivate", { method: "POST", headers: Object.assign(revisionHeader(selected), { "Idempotency-Key": state.deactivateKey }) });
      }
      state.deactivateKey = "";
      state.dirty = false;
      closeEditor({ force: true, restoreFocus: false });
      const refreshed = await load("Der Eintrag wurde geändert, aber die Übersicht konnte nicht aktualisiert werden.");
      if (refreshed) showPageMessage(result.message || "Eintrag wurde deaktiviert.");
    } catch (error) {
      showEditorMessage(errorText(error), "error");
    } finally {
      setEditorBusy(false);
    }
  }

  async function deleteEntity() {
    if (!state.selected) return;
    const current = config[state.tab];
    const selected = state.selected;
    const expected = itemName(selected);
    if (window.prompt('Zum endgültigen Löschen "' + expected + '" eingeben:') !== expected) {
      showEditorMessage("Löschen wurde nicht bestätigt.", "error");
      return;
    }
    setEditorBusy(true);
    try {
      const result = await api("/admin/api/v1/catalog/" + current.kind + "/" + selected.ID, { method: "DELETE", headers: revisionHeader(selected) });
      state.dirty = false;
      closeEditor({ force: true, restoreFocus: false });
      const refreshed = await load("Der Eintrag wurde gelöscht, aber die Übersicht konnte nicht aktualisiert werden.");
      if (refreshed) showPageMessage(result.message || "Eintrag wurde gelöscht.");
    } catch (error) {
      showEditorMessage(errorText(error), "error");
    } finally {
      setEditorBusy(false);
    }
  }

  function showEditorMessage(message, type) {
    byId("editor-message").textContent = message;
    byId("editor-message").className = "notice " + type;
  }

  function renderAssignments() {
    if (!state.data || state.tab !== "events") return;
    refreshSelects();
    const list = byId("assignment-list");
    list.replaceChildren();
    if (!state.data.eventGames.length) {
      const empty = document.createElement("div");
      empty.className = "empty compact-empty";
      empty.textContent = "Noch keine Spielversion zugeordnet.";
      list.append(empty);
      return;
    }
    for (const item of state.data.eventGames) {
      const row = document.createElement("div");
      row.className = "assignment-row";
      const description = detail(item.EventName, item.GameName + " · " + item.Version);
      const required = badge(item.Required, item.Required ? "Erforderlich" : "Optional");
      row.append(description, required);
      const event = state.data.events.find((entry) => entry.ID === item.EventID);
      if (canEdit && event?.Status === "draft") {
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "button";
        remove.textContent = "Entfernen";
        remove.addEventListener("click", () => removeAssignment(item, remove));
        row.append(remove);
      }
      list.append(row);
    }
  }

  async function saveAssignment(event) {
    event.preventDefault();
    const body = { eventId: intValue("assignment-event"), gameVersionId: intValue("assignment-version"), required: byId("assignment-required").checked };
    if (!body.eventId || !body.gameVersionId) {
      showPageMessage("Für die Zuordnung werden ein Event-Entwurf und eine aktive Spielversion benötigt.", "error");
      return;
    }
    const button = event.submitter;
    if (button) button.disabled = true;
    byId("event-assignment").setAttribute("aria-busy", "true");
    try {
      state.assignmentKey = state.assignmentKey || newIdempotencyKey();
      const result = await api("/admin/api/v1/event-games", { method: "POST", headers: { "Idempotency-Key": state.assignmentKey }, body });
      state.assignmentKey = "";
      const refreshed = await load("Die Zuordnung wurde gespeichert, aber die Übersicht konnte nicht aktualisiert werden.");
      if (refreshed) showPageMessage(result.message);
    } catch (error) {
      showPageMessage(errorText(error), "error");
    } finally {
      if (button) button.disabled = false;
      byId("event-assignment").setAttribute("aria-busy", "false");
    }
  }

  async function removeAssignment(item, button) {
    const expected = item.GameName + " " + item.Version;
    if (window.confirm(expected + " aus " + item.EventName + " entfernen?") !== true) return;
    button.disabled = true;
    byId("event-assignment").setAttribute("aria-busy", "true");
    try {
      const result = await api("/admin/api/v1/event-games/" + item.EventID + "/" + item.GameVersionID, { method: "DELETE", headers: revisionHeader(item) });
      const refreshed = await load("Die Zuordnung wurde entfernt, aber die Übersicht konnte nicht aktualisiert werden.");
      if (refreshed) showPageMessage(result.message);
    } catch (error) {
      showPageMessage(errorText(error), "error");
    } finally {
      button.disabled = false;
      byId("event-assignment").setAttribute("aria-busy", "false");
    }
  }

  function showReleaseError(message) {
    const target = byId("release-error");
    if (!target) return;
    target.textContent = message || "";
    target.className = message ? "notice error" : "notice";
  }

  function decodeBase64URL(value) {
    if (typeof value !== "string" || !/^[A-Za-z0-9_-]+$/.test(value)) throw new Error("Der signierte Payload ist nicht korrekt base64url-codiert.");
    const base64 = value.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - value.length % 4) % 4);
    const binary = window.atob(base64);
    const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  }

  function resetReleasePreview() {
    state.releaseEnvelope = null;
    state.releasePayload = null;
    state.releaseKey = "";
    byId("release-preview") && (byId("release-preview").hidden = true);
    byId("publish-release") && (byId("publish-release").disabled = true);
  }

  function renderReleaseStatus() {
    const active = state.releaseStatus?.activeEvent;
    if (active?.delivering) {
      byId("release-current").textContent = "Aktuell an Clients ausgeliefert: „" + active.eventId + "“, Sequenz " + active.sequence + ", gültig bis " + formatDate(active.validUntil) + ".";
    } else if (active?.deliveryState === "expired") {
      byId("release-current").textContent = "Die ausgewählte Sequenz „" + active.eventId + "“ #" + active.sequence + " ist abgelaufen und wird nicht mehr an Clients ausgeliefert.";
    } else if (active?.deliveryState === "scheduled") {
      byId("release-current").textContent = "Die ausgewählte Sequenz „" + active.eventId + "“ #" + active.sequence + " ist noch nicht gültig und wird derzeit nicht ausgeliefert.";
    } else {
      byId("release-current").textContent = "Aktuell wird kein Event an Clients ausgeliefert.";
    }
    renderReleaseHistory();
    renderClientUpdateStatus();
    if (!canPublish || !byId("release-activate")) return;
    const nextEvent = state.releasePayload?.eventId;
    if (byId("release-activate").checked && nextEvent) {
      byId("release-activation-impact").textContent = active
        ? "Aktivierung ersetzt für alle Clients „" + active.eventId + "“ Sequenz " + active.sequence + " durch „" + nextEvent + "“ Sequenz " + state.releasePayload.sequence + "."
        : "Aktivierung liefert „" + nextEvent + "“ Sequenz " + state.releasePayload.sequence + " erstmals an alle Clients aus.";
    } else {
      byId("release-activation-impact").textContent = "Ohne Aktivierung wird die Sequenz gespeichert, aber nicht an Clients ausgeliefert.";
    }
  }

  function renderClientUpdateStatus() {
    const target = byId("client-update-current");
    if (!target) return;
    const update = state.releaseStatus?.clientUpdate;
    target.textContent = update
      ? "Stable: Version " + update.version + ", Sequenz " + update.sequence + ", unterstützt Clients ab " + update.minimumVersion + "."
      : state.releaseStatus ? "Im Stable-Kanal ist noch kein Clientupdate veröffentlicht." : "Clientupdate-Stand derzeit nicht verfügbar.";
  }

  function resetClientUpdatePreview() {
    state.updateEnvelope = null;
    state.updatePayload = null;
	state.updateArtifactReady = false;
    state.updateKey = "";
    if (byId("client-update-preview")) byId("client-update-preview").hidden = true;
    if (byId("publish-client-update")) byId("publish-client-update").disabled = true;
	if (byId("upload-client-update-artifact")) byId("upload-client-update-artifact").disabled = true;
	if (byId("client-update-artifact-status")) byId("client-update-artifact-status").textContent = "";
  }

  function showClientUpdateError(message) {
    const target = byId("client-update-error");
    if (!target) return;
    target.textContent = message || "";
    target.className = message ? "notice error" : "notice";
  }

  async function readClientUpdateFile(event) {
    resetClientUpdatePreview();
    showClientUpdateError("");
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > 950 * 1024) { showClientUpdateError("Das Envelope überschreitet das Management-Limit von 950 KiB."); return; }
    try {
      const envelope = JSON.parse(await file.text());
      if (envelope?.formatVersion !== 1 || envelope?.algorithm !== "Ed25519" || typeof envelope?.keyId !== "string" || typeof envelope?.signature !== "string") throw new Error("Die Datei ist kein vollständiges LANReady-Ed25519-Envelope.");
      const payload = JSON.parse(decodeBase64URL(envelope.payload));
      if (payload?.formatVersion !== 1 || payload?.channel !== "stable" || !Number.isSafeInteger(payload?.sequence) || payload.sequence < 1 || typeof payload?.version !== "string" || typeof payload?.minimumVersion !== "string" || payload?.artifactKind !== "portable_exe" || payload?.updaterProtocol !== 1 || !/^[0-9a-f]{64}$/.test(payload?.publisherCertificateSHA256 || "") || !/^[0-9a-f]{64}$/.test(payload?.sha256 || "") || !Number.isSafeInteger(payload?.size) || payload.size < 1 || payload.size > 1024 * 1024 * 1024) throw new Error("Der enthaltene Clientupdate-Payload besitzt nicht die erwartete portable Updater-Struktur.");
      state.updateEnvelope = envelope;
      state.updatePayload = payload;
      byId("client-update-channel").textContent = payload.channel;
      byId("client-update-version").textContent = payload.version;
      byId("client-update-sequence").textContent = String(payload.sequence);
      byId("client-update-minimum").textContent = payload.minimumVersion;
	  byId("client-update-kind").textContent = payload.artifactKind + " · v" + payload.updaterProtocol;
      byId("client-update-size").textContent = formatBytes(payload.size);
      byId("client-update-digest").textContent = payload.sha256;
	  byId("client-update-publisher").textContent = payload.publisherCertificateSHA256;
      byId("client-update-published").textContent = formatDate(payload.publishedAt);
      byId("client-update-key-id").textContent = envelope.keyId;
      byId("client-update-preview").hidden = false;
	  await checkClientUpdateArtifact();
    } catch (error) {
      showClientUpdateError(error?.message || "Die Update-Datei konnte nicht gelesen werden.");
    }
  }

  async function checkClientUpdateArtifact() {
    state.updateArtifactReady = false;
    const payload = state.updatePayload;
    if (!payload) return;
    const status = byId("client-update-artifact-status");
    status.textContent = "Cache-Status der signierten EXE wird geprüft …";
    try {
      const result = await api(`/admin/api/v1/artifacts/client-update/${payload.sha256}?size=${payload.size}`);
	  if (state.updatePayload !== payload) return;
      state.updateArtifactReady = Boolean(result.ready);
      status.textContent = result.ready ? "Die exakt passende EXE liegt verifiziert im Cache." : "Die EXE fehlt noch im Cache. Wähle das fertig signierte portable Artefakt und lade es hoch.";
    } catch (error) {
	  if (state.updatePayload !== payload) return;
      status.textContent = errorText(error);
    }
	if (state.updatePayload !== payload) return;
    byId("upload-client-update-artifact").disabled = state.updateArtifactReady || !byId("client-update-artifact-file").files?.[0];
    byId("publish-client-update").disabled = !state.updateArtifactReady;
  }

  async function uploadClientUpdateArtifact() {
    const payload = state.updatePayload;
    const file = byId("client-update-artifact-file").files?.[0];
    if (!payload || !file) { showClientUpdateError("Wähle zuerst Envelope und zugehörige portable EXE."); return; }
    if (file.size !== payload.size) { showClientUpdateError(`Die gewählte EXE hat ${formatBytes(file.size)}, signiert erwartet sind ${formatBytes(payload.size)}.`); return; }
    state.busy = true;
    setClientUpdateBusy(true);
    showClientUpdateError("");
    byId("client-update-artifact-status").textContent = "EXE wird gestreamt, gehasht und atomar in den Cache übernommen …";
    try {
      const response = await fetch(`/admin/api/v1/artifacts/client-update/${payload.sha256}?size=${payload.size}`, {method: "POST", credentials: "same-origin", headers: {Accept: "application/json", "Content-Type": "application/vnd.microsoft.portable-executable", "X-CSRF-Token": csrf}, body: file});
      const result = await response.json().catch(() => ({}));
      if (!response.ok) throw new APIError(result.message || "Die EXE konnte nicht hochgeladen werden.", response.status, result.requestId || response.headers.get("X-Request-ID"));
      state.updateArtifactReady = Boolean(result.ready);
      byId("client-update-artifact-status").textContent = "Die EXE wurde mit passender Größe und SHA-256 atomar im Cache gespeichert.";
      byId("publish-client-update").disabled = !state.updateArtifactReady;
    } catch (error) {
      state.updateArtifactReady = false;
      showClientUpdateError(errorText(error));
      byId("client-update-artifact-status").textContent = "Der Cache-Upload wurde nicht abgeschlossen.";
    } finally {
      setClientUpdateBusy(false);
      state.busy = false;
    }
  }

  async function publishClientUpdate(event) {
    event.preventDefault();
    if (!state.updateEnvelope || !state.updatePayload) { showClientUpdateError("Bitte wähle zuerst ein gültig aufgebautes Update-Envelope."); return; }
    if (!state.updateArtifactReady) { showClientUpdateError("Die signierte portable EXE muss vor der Veröffentlichung verifiziert im Cache liegen."); return; }
    if (!state.releaseStatus) { showClientUpdateError("Der aktuelle Stable-Stand ist unbekannt. Lade die Seite erneut."); return; }
    const publishedAt = Date.parse(state.updatePayload.publishedAt || "");
    if (!Number.isFinite(publishedAt) || publishedAt > Date.now()) { showClientUpdateError("Der Veröffentlichungszeitpunkt ist ungültig oder liegt in der Zukunft."); return; }
    const current = state.releaseStatus.clientUpdate;
    const impact = current ? "Stable " + current.version + " (#" + current.sequence + ") wird durch " + state.updatePayload.version + " (#" + state.updatePayload.sequence + ") ersetzt." : "Stable " + state.updatePayload.version + " (#" + state.updatePayload.sequence + ") wird erstmals veröffentlicht.";
    if (!window.confirm(impact + " Erwartetes Authenticode-Zertifikat: " + state.updatePayload.publisherCertificateSHA256 + ". Clients prüfen das signierte Release beim nächsten Statusabruf. Wirklich veröffentlichen?")) return;
    state.busy = true;
    setClientUpdateBusy(true);
    showClientUpdateError("");
    try {
      state.updateKey = state.updateKey || newIdempotencyKey();
      const result = await api("/admin/api/v1/releases/client-updates", { method: "POST", headers: { "Idempotency-Key": state.updateKey }, body: { envelope: state.updateEnvelope } });
      state.updateKey = "";
      byId("client-update-form").reset();
      resetClientUpdatePreview();
      await loadReleaseStatus();
      showPageMessage("Clientupdate " + result.version + " wurde als unveränderliche Stable-Sequenz " + result.sequence + " veröffentlicht.");
    } catch (error) {
      showClientUpdateError(errorText(error));
    } finally {
      setClientUpdateBusy(false);
      state.busy = false;
    }
  }

  function setClientUpdateBusy(busy) {
    byId("client-update-release")?.setAttribute("aria-busy", String(busy));
    if (byId("client-update-file")) byId("client-update-file").disabled = busy;
	if (byId("client-update-artifact-file")) byId("client-update-artifact-file").disabled = busy;
	if (byId("upload-client-update-artifact")) byId("upload-client-update-artifact").disabled = busy || state.updateArtifactReady || !state.updatePayload || !byId("client-update-artifact-file").files?.[0];
    if (byId("publish-client-update")) byId("publish-client-update").disabled = busy || !state.updateEnvelope || !state.updateArtifactReady;
    if (byId("client-update-progress")) byId("client-update-progress").textContent = busy ? "Signatur, Version, Sequenz und CAS-Artefakt werden geprüft …" : "";
  }

  function renderReleaseHistory() {
    const target = byId("release-history");
    if (!target) return;
    target.replaceChildren();
    if (!state.releaseStatus) {
      target.append(detail("Veröffentlichte Sequenzen", "Release-Historie derzeit nicht verfügbar."));
      return;
    }
    const releases = Array.isArray(state.releaseStatus?.eventReleases) ? state.releaseStatus.eventReleases : [];
    if (!releases.length) {
      target.append(detail("Veröffentlichte Sequenzen", "Noch keine Event-Releases vorhanden."));
      return;
    }
    const heading = document.createElement("strong");
    heading.textContent = "Veröffentlichte Sequenzen";
    target.append(heading);
    const latestByEvent = new Map();
    for (const item of releases) latestByEvent.set(item.eventId, Math.max(latestByEvent.get(item.eventId) || 0, item.sequence));
    for (const item of releases) {
      const row = document.createElement("div");
      row.className = "assignment-row";
      const stateLabel = item.selected && item.deliveryState === "active" ? "Aktiv" : item.deliveryState === "expired" ? "Abgelaufen" : item.deliveryState === "scheduled" ? "Noch nicht gültig" : "Bereit";
      row.append(detail("„" + item.eventId + "“ · Sequenz " + item.sequence, "Release " + item.releaseId + " · " + stateLabel + " · gültig bis " + formatDate(item.validUntil)));
      if (canPublish && !item.selected && item.deliveryState === "active" && latestByEvent.get(item.eventId) === item.sequence) {
        const activate = document.createElement("button");
        activate.type = "button";
        activate.className = "button";
        activate.textContent = "Aktivieren";
        activate.addEventListener("click", () => activateStoredRelease(item, activate));
        row.append(activate);
      }
      if (canPublish && item.sequence < latestByEvent.get(item.eventId)) {
        const rollback = document.createElement("button");
        rollback.type = "button";
        rollback.className = "button";
        rollback.textContent = "Rollback vorbereiten";
        rollback.addEventListener("click", () => downloadRollbackCandidate(item, rollback));
        row.append(rollback);
      }
      target.append(row);
    }
  }

  async function downloadRollbackCandidate(item, button) {
    if (!state.releaseStatus) {
      showReleaseError("Der aktuelle Release-Status ist unbekannt. Lade ihn erneut, bevor du einen Rollback vorbereitest.");
      return;
    }
    const validityValue = byId("rollback-valid-until")?.value;
    const validUntil = Date.parse(validityValue || "");
    if (!Number.isFinite(validUntil) || validUntil <= Date.now()) {
      showReleaseError("Wähle zuerst ein zukünftiges Gültigkeitsende für den Rollback-Kandidaten.");
      byId("rollback-valid-until")?.focus();
      return;
    }
    if (!window.confirm("Der Inhalt von „" + item.eventId + "“ #" + item.sequence + " wird als neue höhere Sequenz vorbereitet. Danach muss die JSON-Datei im Offline-Signer signiert und hier wieder hochgeladen werden. Fortfahren?")) return;
    button.disabled = true;
    state.busy = true;
    showReleaseError("");
    try {
      setReleaseControlsBusy(true);
      const result = await api("/admin/api/v1/releases/events/" + encodeURIComponent(item.eventId) + "/" + item.sequence + "/rollback-candidate", { method: "POST", body: { validUntil: new Date(validUntil).toISOString() } });
      const blob = new Blob([JSON.stringify(result.payload, null, 2) + "\n"], { type: "application/json" });
      const link = document.createElement("a");
      link.href = URL.createObjectURL(blob);
      link.download = item.eventId + "-rollback-seq-" + result.sequence + ".json";
      document.body.append(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(link.href);
      showPageMessage("Rollback-Kandidat für Sequenz " + result.sequence + " wurde erstellt. Signiere ihn offline mit lanready-release sign-event und lade das Envelope anschließend hier hoch.");
    } catch (error) {
      showReleaseError(errorText(error));
    } finally {
      button.disabled = false;
      setReleaseControlsBusy(false);
      state.busy = false;
    }
  }

  async function activateStoredRelease(item, button) {
    if (!state.releaseStatus) {
      showReleaseError("Der aktuelle Release-Status ist unbekannt. Lade ihn erneut, bevor du global aktivierst.");
      return;
    }
    const current = state.releaseStatus?.activeEvent;
    const impact = current?.delivering ? "„" + current.eventId + "“ #" + current.sequence + " wird für alle Clients ersetzt. " : "";
    if (!window.confirm(impact + "„" + item.eventId + "“ #" + item.sequence + " jetzt an alle Clients ausliefern?")) return;
    button.disabled = true;
    state.busy = true;
    setReleaseControlsBusy(true);
    showReleaseError("");
    try {
      const result = await api("/admin/api/v1/releases/events/" + encodeURIComponent(item.eventId) + "/" + item.sequence + "/activate", { method: "POST", headers: { "Idempotency-Key": newIdempotencyKey() }, body: {} });
      await loadReleaseStatus();
      showPageMessage("Event „" + result.eventId + "“ Sequenz " + result.sequence + " wird jetzt an Clients ausgeliefert.");
    } catch (error) {
      showReleaseError(errorText(error));
      button.disabled = false;
    } finally {
      setReleaseControlsBusy(false);
      state.busy = false;
    }
  }

  async function loadReleaseStatus() {
    try {
      state.releaseStatus = await api("/admin/api/v1/releases/status");
      renderReleaseStatus();
    } catch (error) {
      state.releaseStatus = null;
      renderReleaseStatus();
      byId("release-current").textContent = "Aktiver Clientstand konnte nicht geladen werden: " + errorText(error);
    }
  }

  function setReleaseControlsBusy(busy) {
    byId("event-release")?.setAttribute("aria-busy", String(busy));
    ["release-file", "release-activate", "rollback-valid-until"].forEach((id) => {
      const element = byId(id);
      if (element) element.disabled = busy;
    });
    if (byId("publish-release")) byId("publish-release").disabled = busy || !state.releaseEnvelope;
    byId("release-history")?.querySelectorAll("button").forEach((button) => { button.disabled = busy; });
    if (byId("release-progress")) byId("release-progress").textContent = busy ? "Release-Aktion wird serverseitig geprüft …" : "";
  }

  async function readReleaseFile(event) {
    resetReleasePreview();
    showReleaseError("");
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > 950 * 1024) {
      showReleaseError("Das Envelope überschreitet das Management-Limit von 950 KiB.");
      return;
    }
    try {
      const envelope = JSON.parse(await file.text());
      if (envelope?.formatVersion !== 1 || envelope?.algorithm !== "Ed25519" || typeof envelope?.keyId !== "string" || typeof envelope?.signature !== "string") {
        throw new Error("Die Datei ist kein vollständiges LANReady-Ed25519-Envelope.");
      }
      const payload = JSON.parse(decodeBase64URL(envelope.payload));
      if (payload?.formatVersion !== 2 || typeof payload?.eventId !== "string" || !Number.isSafeInteger(payload?.sequence) || payload.sequence < 1 || !Array.isArray(payload?.games) || !Array.isArray(payload?.artifacts)) {
        throw new Error("Der enthaltene Event-Payload besitzt nicht die erwartete Struktur.");
      }
      const knownEvent = state.data?.events?.find((item) => item.Slug === payload.eventId);
      if (!knownEvent) throw new Error("Das signierte Event „" + payload.eventId + "“ existiert nicht im aktuellen Katalog.");
      if (knownEvent.Status === "archived") throw new Error("Ein archiviertes Event kann nicht veröffentlicht werden.");
      state.releaseEnvelope = envelope;
      state.releasePayload = payload;
      byId("release-event-id").textContent = payload.eventId;
      byId("release-id").textContent = payload.releaseId || "—";
      byId("release-sequence").textContent = String(payload.sequence);
      byId("release-issued").textContent = formatDate(payload.issuedAt);
      byId("release-valid-until").textContent = formatDate(payload.validUntil);
      byId("release-minimum-client").textContent = payload.minimumClientVersion || "—";
      byId("release-launchers").textContent = String(Array.isArray(payload.launchers) ? payload.launchers.length : 0);
      byId("release-games").textContent = String(payload.games.length);
      byId("release-artifacts").textContent = String(payload.artifacts.length);
      byId("release-key-id").textContent = envelope.keyId;
      byId("release-preview").hidden = false;
      byId("publish-release").disabled = false;
      renderReleaseStatus();
    } catch (error) {
      showReleaseError(error?.message || "Die Release-Datei konnte nicht gelesen werden.");
    }
  }

  async function publishRelease(event) {
    event.preventDefault();
    if (!state.releaseEnvelope) {
      showReleaseError("Bitte wähle zuerst ein gültig aufgebautes Release-Envelope.");
      return;
    }
    const activate = byId("release-activate").checked;
    if (activate && !state.releaseStatus) {
      showReleaseError("Der aktuell ausgelieferte Clientstand ist unbekannt. Lade die Seite erneut, bevor du global aktivierst.");
      return;
    }
    if (activate) {
      const issuedAt = Date.parse(state.releasePayload?.issuedAt || "");
      const validUntil = Date.parse(state.releasePayload?.validUntil || "");
      const now = Date.now();
      if (!Number.isFinite(issuedAt) || !Number.isFinite(validUntil) || issuedAt > now || validUntil <= now) {
        showReleaseError("Dieses Release ist derzeit nicht gültig und kann nicht aktiviert werden.");
        return;
      }
      const active = state.releaseStatus.activeEvent;
      const change = active?.delivering
        ? "„" + active.eventId + "“ Sequenz " + active.sequence + " wird für alle Clients durch „" + state.releasePayload.eventId + "“ Sequenz " + state.releasePayload.sequence + " ersetzt."
        : "„" + state.releasePayload.eventId + "“ Sequenz " + state.releasePayload.sequence + " wird erstmals an alle Clients ausgeliefert.";
      if (!window.confirm(change + " Wirklich veröffentlichen und aktivieren?")) return;
    }
    const button = byId("publish-release");
    state.busy = true;
    button.disabled = true;
    button.textContent = "Release wird geprüft …";
    byId("release-file").disabled = true;
    byId("release-activate").disabled = true;
    byId("event-release").setAttribute("aria-busy", "true");
    byId("release-progress").textContent = "Signatur, Sequenz und Artefakte werden serverseitig geprüft …";
    showReleaseError("");
    try {
      state.releaseKey = state.releaseKey || newIdempotencyKey();
      const result = await api("/admin/api/v1/releases/events", { method: "POST", headers: { "Idempotency-Key": state.releaseKey }, body: { envelope: state.releaseEnvelope, activate } });
      state.releaseKey = "";
      state.releaseEnvelope = null;
      byId("release-form").reset();
      resetReleasePreview();
      const refreshed = await load("Das Release wurde veröffentlicht, aber die Übersicht konnte nicht aktualisiert werden.");
      if (refreshed) showPageMessage("Event „" + result.eventId + "“ wurde als unveränderliche Sequenz " + result.sequence + (result.active ? " veröffentlicht und aktiviert." : " veröffentlicht."));
    } catch (error) {
      showReleaseError(errorText(error));
    } finally {
      byId("release-file").disabled = false;
      byId("release-activate").disabled = false;
      byId("event-release").setAttribute("aria-busy", "false");
      byId("release-progress").textContent = "";
      button.textContent = "Prüfen und veröffentlichen";
      button.disabled = !state.releaseEnvelope;
      state.busy = false;
    }
  }

  function changeTab(tab, focusTab = false) {
    if (!validTabs.includes(tab)) return;
    if (state.busy) {
      showPageMessage("Die laufende Aktion wird noch abgeschlossen.", "error");
      return;
    }
    if (tab !== state.tab && !byId("catalog-editor").hidden && !closeEditor({ restoreFocus: false })) {
      document.querySelector('[data-tab="' + state.tab + '"]')?.focus();
      return;
    }
    state.tab = tab;
    clearPageMessage();
    renderChrome(true);
    byId("catalog-search").value = "";
    byId("catalog-status").value = "";
    const url = tab === "events" ? "/admin/events" : "/admin/catalog?tab=" + encodeURIComponent(tab);
    history.replaceState(null, "", url);
    render();
    if (focusTab) document.querySelector('[data-tab="' + tab + '"]')?.focus();
  }

  function setListControlsEnabled(enabled) {
    ["catalog-search", "catalog-status", "catalog-sort"].forEach((id) => { byId(id).disabled = !enabled; });
    if (byId("add-entity")) byId("add-entity").disabled = !enabled;
  }

  async function load(failureContext = "") {
    const hadData = Boolean(state.data);
    clearPageMessage();
    byId("retry-catalog").hidden = true;
    byId("catalog-panel").setAttribute("aria-busy", "true");
    byId("catalog-loading").hidden = false;
    setListControlsEnabled(false);
    if (!hadData) {
      byId("catalog-table-wrap").hidden = true;
      byId("catalog-empty").hidden = true;
    }
    try {
      state.data = normalizeSnapshot(await api("/admin/api/v1/catalog"));
      await Promise.all([loadReleaseStatus(), loadCacheStatus()]);
      refreshSelects(state.selected || {});
      render();
	  scheduleCachePoll();
      return true;
    } catch (error) {
      const message = (failureContext ? failureContext + " " : "") + errorText(error);
      showPageMessage(message, "error");
      byId("retry-catalog").hidden = false;
      if (state.data) render();
      else byId("catalog-count").textContent = "Laden fehlgeschlagen";
      return false;
    } finally {
      byId("catalog-loading").hidden = true;
      byId("catalog-panel").setAttribute("aria-busy", "false");
      setListControlsEnabled(Boolean(state.data));
    }
  }

  function scheduleCachePoll() {
    window.clearTimeout(state.cachePollTimer);
    state.cachePollTimer = 0;
    if (!state.data?.cacheJobs.some((job) => job.Status === "queued" || job.Status === "running")) return;
    state.cachePollTimer = window.setTimeout(() => refreshCacheJobs(true), 2000);
  }

  async function refreshCacheJobs(silent = false) {
    if (!state.data) return false;
    try {
	  const previous = state.data.cacheJobs;
      const result = await api("/admin/api/v1/cache-jobs");
	  const next = Array.isArray(result.jobs) ? result.jobs : [];
	  const becameTerminal = cacheHelpers.becameTerminal(previous, next);
	  if (becameTerminal) return load(silent ? "" : "Der Cache-Auftrag wurde abgeschlossen, aber der aktualisierte Katalog konnte nicht geladen werden.");
	  state.data.cacheJobs = next;
      render();
      scheduleCachePoll();
      return true;
    } catch (error) {
      if (!silent) showPageMessage(errorText(error), "error");
      scheduleCachePoll();
      return false;
    }
  }

  document.querySelectorAll("[data-tab]").forEach((button) => {
    button.addEventListener("click", () => changeTab(button.dataset.tab, true));
    button.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      const index = validTabs.indexOf(state.tab);
      const next = event.key === "ArrowRight" ? (index + 1) % validTabs.length : (index - 1 + validTabs.length) % validTabs.length;
      changeTab(validTabs[next], true);
    });
  });
  ["catalog-search", "catalog-status", "catalog-sort"].forEach((id) => byId(id).addEventListener("input", render));
  byId("add-entity")?.addEventListener("click", (event) => openEditor(null, event.currentTarget));
  byId("cancel-editor").addEventListener("click", () => closeEditor());
  byId("catalog-form").addEventListener("submit", saveEntity);
  byId("catalog-form").addEventListener("input", () => { if (canEdit && !byId("catalog-editor").hidden) { state.dirty = true; state.entityKey = ""; } });
  byId("catalog-form").addEventListener("change", () => { if (canEdit && !byId("catalog-editor").hidden) { state.dirty = true; state.entityKey = ""; } });
  byId("deactivate-entity")?.addEventListener("click", deactivateEntity);
  byId("delete-entity")?.addEventListener("click", deleteEntity);
  byId("run-cache-gc")?.addEventListener("click", runCacheGarbageCollection);
  byId("assignment-form")?.addEventListener("submit", saveAssignment);
  byId("assignment-form")?.addEventListener("input", () => { state.assignmentKey = ""; });
  byId("assignment-form")?.addEventListener("change", () => { state.assignmentKey = ""; });
  if (canPublish) {
    byId("release-file")?.addEventListener("change", readReleaseFile);
    byId("release-form")?.addEventListener("submit", publishRelease);
    byId("release-activate")?.addEventListener("change", renderReleaseStatus);
    byId("client-update-file")?.addEventListener("change", readClientUpdateFile);
	byId("client-update-artifact-file")?.addEventListener("change", () => { byId("upload-client-update-artifact").disabled = !state.updatePayload || !byId("client-update-artifact-file").files?.[0] || state.updateArtifactReady; });
	byId("upload-client-update-artifact")?.addEventListener("click", uploadClientUpdateArtifact);
    byId("client-update-form")?.addEventListener("submit", publishClientUpdate);
  }
  byId("retry-catalog").addEventListener("click", () => load());
  window.addEventListener("beforeunload", (event) => {
    if (!state.dirty && !state.busy) return;
    event.preventDefault();
    event.returnValue = "";
  });

  renderChrome(true);
  load();
})();
