"use strict";

const state = { connection: null, discovery: null, selected: new Set(), authorizedIndices: [], activities: [], auth: null, authAttempt: 0, pollTimer: null };
let modalReturnFocus = null;
const $ = (id) => document.getElementById(id);
const api = (name, ...args) => {
  const app = window.go?.windowsapp?.App;
  if (!app || typeof app[name] !== "function") return Promise.reject(new Error("LANReady-Backend ist noch nicht bereit."));
  return app[name](...args);
};
const errorText = (error) => String(error?.message || error || "Unbekannter Fehler").replace(/^Error:\s*/, "");

document.addEventListener("DOMContentLoaded", async () => {
  bindEvents();
  if (await loadState()) await api("ConfirmUIReady");
});

function bindEvents() {
  document.querySelectorAll(".nav-item").forEach((button) => button.addEventListener("click", () => showPage(button.dataset.page)));
  [$("connect-button"), $("hero-action"), $("next-action")].forEach((button) => button.addEventListener("click", primaryAction));
  $("hero-secondary").addEventListener("click", () => { showPage("games"); discover(); });
  [$("discover-button"), $("empty-discover")].forEach((button) => button.addEventListener("click", discover));
  $("select-all-button").addEventListener("click", () => { state.selected = new Set((state.discovery?.installations || []).map((_, index) => index)); renderGames(); });
  $("clear-selection-button").addEventListener("click", () => { state.selected.clear(); renderGames(); });
  $("games-retry").addEventListener("click", discover);
  $("global-retry").addEventListener("click", loadState);
  $("global-reset").addEventListener("click", resetLocalProfile);
  $("enroll-form").addEventListener("submit", enroll);
  $("sync-button").addEventListener("click", startAuthorization);
  $("confirm-sync").addEventListener("click", syncInventory);
  $("auth-link").addEventListener("click", (event) => { event.preventDefault(); if (state.auth?.verificationUrl) window.runtime.BrowserOpenURL(state.auth.verificationUrl); });
  $("auth-retry").addEventListener("click", restartAuthorization);
  $("disconnect-button").addEventListener("click", disconnect);
  $("install-update").addEventListener("click", installUpdate);
  $("cancel-update").addEventListener("click", cancelUpdate);
  $("quit-agent").addEventListener("click", () => api("QuitApplication"));
  document.querySelectorAll("[data-close-modal]").forEach((button) => button.addEventListener("click", () => closeModal()));
  $("modal-backdrop").addEventListener("click", (event) => { if (event.target === $("modal-backdrop")) closeModal(); });
  document.addEventListener("keydown", handleModalKeydown);
}

async function loadState() {
  setPersistentError("global", "");
  $("global-reset").classList.add("hidden");
  setStateLoading(true);
  try {
    state.connection = await api("State");
    if (!connectionUsable()) invalidateClientWorkflow();
    renderConnection();
    renderGames();
    renderStats();
    if (state.connection?.serverWarning) setPersistentError("global", state.connection.serverWarning);
  }
  catch (error) { state.connection = null; setPersistentError("global", errorText(error)); $("global-reset").classList.remove("hidden"); }
  finally { setStateLoading(false); }
	return state.connection !== null;
}

function connectionUsable() {
  return Boolean(state.connection?.connected && state.connection?.serverReachable && !state.connection?.updateRequired);
}

function invalidateClientWorkflow() {
  if (state.auth?.authorizationId) api("CancelAuthorization", state.auth.authorizationId).catch(() => {});
  clearTimeout(state.pollTimer);
  state.authAttempt++;
  state.auth = null;
  state.authorizedIndices = [];
  state.discovery = null;
  state.selected.clear();
  closeModal(true);
}

async function resetLocalProfile() {
  if (!window.confirm("Das lokale LANReady-Geräteprofil dieses Windows-Benutzers löschen? Anschließend ist ein neuer Enrollment-Code erforderlich.")) return;
  $("global-reset").disabled = true;
  try { await api("Disconnect"); await loadState(); }
  catch (error) { setPersistentError("global", errorText(error)); }
  finally { $("global-reset").disabled = false; }
}

function setStateLoading(loading) {
  const actions = [$("connect-button"), $("hero-action"), $("next-action"), $("global-retry")];
  actions.forEach((button) => { button.disabled = loading || (!loading && !state.connection && button.id !== "global-retry"); });
  $("connect-button").textContent = loading ? "Status wird geladen …" : (state.connection?.connected ? "Verbunden" : "PC verbinden");
}

function showPage(page) {
  document.querySelectorAll(".nav-item").forEach((item) => {
    const active = item.dataset.page === page;
    item.classList.toggle("active", active);
    if (active) item.setAttribute("aria-current", "page"); else item.removeAttribute("aria-current");
  });
  document.querySelectorAll(".page").forEach((item) => item.classList.toggle("active", item.id === `page-${page}`));
  const titles = { overview: ["DEIN LAN-STATUS", "Übersicht"], games: ["LOKALES INVENTAR", "Meine Spiele"], activity: ["SITZUNGSPROTOKOLL", "Aktivität"], settings: ["LANREADY CLIENT", "Einstellungen"] };
  [$("page-eyebrow").textContent, $("page-title").textContent] = titles[page];
}

function renderConnection() {
  const connected = Boolean(state.connection?.connected);
  const updateRequired = Boolean(state.connection?.updateRequired);
	const updateAvailable = Boolean(state.connection?.updateAvailable);
  const usable = connected && Boolean(state.connection?.serverReachable) && !updateRequired;
  const eventBlocked = usable && ["security_error", "error"].includes(state.connection?.eventReadiness?.state);
  $("connect-button").textContent = updateRequired ? "Update erforderlich" : usable ? "Verbunden" : connected ? "Status prüfen" : "PC verbinden";
  $("connect-button").classList.toggle("button-ghost", connected);
  $("connect-button").classList.toggle("button-primary", !connected);
  $("sidebar-dot").classList.toggle("online", usable);
  $("sidebar-status").textContent = updateRequired ? "Update erforderlich" : eventBlocked ? "Verbunden · Event gesperrt" : usable ? "Sicher verbunden" : connected ? "Status ungeprüft" : "Nicht verbunden";
  $("sidebar-device").textContent = connected ? state.connection.deviceName : "Managementserver";
  $("hero-pill").classList.toggle("online", usable);
  $("hero-pill").lastChild.textContent = updateRequired ? " Update erforderlich" : eventBlocked ? " Verbunden · Event gesperrt" : usable ? " Sicher verbunden" : connected ? " Status ungeprüft" : " Nicht verbunden";
  $("hero-title").textContent = updateRequired ? "LANReady muss aktualisiert werden." : connected ? `${state.connection.deviceName} ist verbunden.` : "Mach deinen PC bereit für die nächste LAN.";
  $("hero-text").textContent = updateRequired ? "Der Server hat ein signiertes Pflichtupdate gemeldet. Spiele- und Synchronisationsfunktionen bleiben bis zur sicheren Aktualisierung gesperrt." : usable ? "Suche jetzt installierte Spiele. Vor jeder Übertragung siehst du die vollständige Auswahl und meldest dich persönlich an." : connected ? "Prüfe die Verbindung zum Managementserver erneut, bevor du fortfährst." : "Verbinde LANReady mit dem Managementserver. Danach findest du installierte Spiele und entscheidest selbst, was synchronisiert wird.";
  $("hero-action").textContent = updateRequired ? "Update-Status öffnen" : usable ? "Meine Spiele öffnen" : connected ? "Status erneut prüfen" : "PC verbinden";
  $("hero-secondary").classList.toggle("hidden", !usable);
  $("next-title").textContent = connected ? "Installierte Spiele finden" : "Managementserver verbinden";
  $("next-text").textContent = connected ? "LANReady erkennt Installationen von Steam, EA App und Ubisoft Connect lokal. Du entscheidest anschließend einzeln, welche Funde synchronisiert werden." : "Ein Admin erzeugt in der Weboberfläche einen einmaligen Enrollment-Code. Der private Geräteschlüssel bleibt geschützt in deinem Windows-Benutzerprofil.";
  $("next-action").innerHTML = connected ? "Spiele suchen <span>→</span>" : "Jetzt verbinden <span>→</span>";
  $("step-number").textContent = connected ? "02" : "01";
  $("settings-device").textContent = connected ? state.connection.deviceName : "Nicht verbunden";
  $("settings-server").textContent = state.connection?.serverUrl || "—";
  $("settings-id").textContent = state.connection?.deviceId || "—";
  $("settings-version").textContent = state.connection?.clientVersion || "—";
  $("disconnect-button").classList.toggle("hidden", !connected);
  $("quit-agent").classList.toggle("hidden", !state.connection?.agentMode);
  $("update-status").textContent = updateRequired ? "Pflichtupdate" : updateAvailable ? `Version ${state.connection.updateVersion} verfügbar` : "Aktuell";
  $("install-update").classList.toggle("hidden", !updateAvailable);
  $("install-update").textContent = updateRequired ? "Pflichtupdate installieren" : "Signiertes Update installieren";
  $("update-copy").textContent = updateAvailable ? "LANReady lädt ausschließlich das signierte Stable-Release, setzt den Download fort, prüft Ed25519, Sequenz, Version, Größe, SHA-256 und Windows-Authenticode und stellt bei fehlendem Gesundheitscheck automatisch die vorige Version wieder her." : "LANReady hat kein neueres signiertes Stable-Release gefunden. Die letzte funktionierende Version wird bei einem fehlgeschlagenen Neustart automatisch wiederhergestellt.";
  renderEventReadiness();
}

function renderEventReadiness() {
  const model = window.LANReadyReadiness.viewModel(state.connection?.eventReadiness);
  const panel = $("event-readiness-panel");
  panel.className = `panel event-readiness-panel tone-${model.tone}`;
  $("event-readiness-title").textContent = model.eventTitle;
  $("event-readiness-badge").textContent = model.label;
  $("event-readiness-message").textContent = model.message;
  $("ready-number").textContent = model.ringText;
  $("ready-label").textContent = model.label;
  const ring = document.querySelector(".readiness-ring");
  const degrees = model.percentage === null ? 0 : model.percentage * 3.6;
  const color = model.tone === "ok" ? "var(--cyan)" : model.tone === "warn" ? "var(--amber)" : model.tone === "danger" ? "var(--danger)" : "#697792";
  ring.style.background = `conic-gradient(${color} ${degrees}deg, rgba(255,255,255,.1) ${degrees}deg)`;
  ring.setAttribute("aria-label", model.percentage === null ? model.label : `${model.label}: ${model.percentage} Prozent`);

  const rows = [];
  model.launchers.forEach((launcher) => rows.push(readinessComponent(
    "Launcher", launcherName(launcher.launcher === "ea-app" ? "ea_app" : launcher.launcher === "ubisoft-connect" ? "ubisoft_connect" : launcher.launcher),
    `Soll ${launcher.requiredVersion} · ${launcher.statusLabel}`, launcher.required, launcher.status
  )));
  model.games.forEach((game) => {
    const detected = game.detectedVersion ? `Lokal ${game.detectedVersion}` : "Keine lokale Version";
    rows.push(readinessComponent("Spiel", game.name || game.gameId, `Soll ${game.requiredVersion} · ${detected} · ${game.statusLabel}`, game.required, game.status));
  });
  const components = $("event-readiness-components");
  components.replaceChildren(...rows);
  components.classList.toggle("hidden", rows.length === 0);
}

function readinessComponent(kind, name, details, required, status) {
  const row = document.createElement("div");
  row.className = "event-component";
  const icon = document.createElement("span");
  icon.className = "event-component-icon";
  icon.textContent = kind === "Spiel" ? "◈" : "◇";
  const copy = document.createElement("div");
  const title = document.createElement("strong");
  title.textContent = name;
  const meta = document.createElement("small");
  meta.textContent = `${kind} · ${required ? "erforderlich" : "optional"} · ${details}`;
  copy.append(title, meta);
  const badge = document.createElement("span");
  badge.className = `component-state state-${status}`;
  badge.textContent = window.LANReadyReadiness.componentBadge(status, required);
  row.append(icon, copy, badge);
  return row;
}

function primaryAction() {
  if (state.connection?.updateRequired) showPage("settings");
  else if (state.connection?.connected && !state.connection?.serverReachable) loadState();
  else if (state.connection?.connected) showPage("games");
  else openModal("enroll-modal");
}

async function enroll(event) {
  event.preventDefault();
  const button = $("enroll-submit");
  setBusy(button, true, "Wird verbunden …");
  setError("enroll-error", "");
  try {
    state.connection = await api("Enroll", { serverUrl: $("server-url").value, code: $("enrollment-code").value, deviceName: $("device-name").value });
    addActivity("PC verbunden", `${state.connection.deviceName} wurde sicher registriert.`);
    closeModal(); await loadState(); showToast("Dieser PC ist jetzt mit LANReady verbunden."); if (!state.connection?.updateRequired) showPage("games");
  } catch (error) { setError("enroll-error", errorText(error)); }
  finally { setBusy(button, false, "PC sicher verbinden"); }
}

async function discover() {
  if (!state.connection?.connected) { openModal("enroll-modal"); return; }
  if (state.connection?.updateRequired || !state.connection?.serverReachable) { showPage(state.connection?.updateRequired ? "settings" : "overview"); setPersistentError("global", state.connection?.serverWarning || "Prüfe zuerst den Managementserver-Status erneut."); return; }
  const buttons = [$("discover-button"), $("empty-discover")];
  buttons.forEach((button) => setBusy(button, true, "Suche läuft …"));
  setPersistentError("games", "");
  try {
    const result = await api("Discover");
    state.discovery = result; state.selected = new Set();
    renderGames(); renderStats(); renderConnection();
    $("games-status").textContent = `${result.installations?.length || 0} unterstützte Installation(en) gefunden. Keine Installation ist vorausgewählt.`;
    addActivity("Spiele gesucht", `${result.installations?.length || 0} Installation(en) wurden lokal erkannt.`);
  } catch (error) { setPersistentError("games", errorText(error)); }
  finally { buttons.forEach((button) => setBusy(button, false, button.id === "discover-button" ? "Erneut suchen" : "Suche starten")); }
}

function renderGames() {
  const installations = state.discovery?.installations || [];
  const searched = state.discovery !== null;
  $("games-empty").classList.toggle("hidden", installations.length > 0);
  $("games-list").classList.toggle("hidden", installations.length === 0);
  $("selection-bar").classList.toggle("hidden", installations.length === 0);
  $("game-count-badge").classList.toggle("hidden", installations.length === 0);
  $("select-all-button").classList.toggle("hidden", installations.length === 0);
  $("clear-selection-button").classList.toggle("hidden", installations.length === 0);
  $("game-count-badge").textContent = installations.length;
  $("games-empty-title").textContent = searched ? "Keine unterstützten Installationen gefunden" : "Noch keine Suche durchgeführt";
  $("games-empty-copy").textContent = searched ? "Prüfe, ob Steam, EA App oder Ubisoft Connect für diesen Windows-Benutzer installiert sind, und starte die Suche erneut." : "LANReady durchsucht ausschließlich bekannte Launcher-Verzeichnisse und Registry-Einträge.";
  $("games-list").replaceChildren(...installations.map((game, index) => gameCard(game, index)));
  const warnings = state.discovery?.warnings || [];
  $("warning-box").classList.toggle("hidden", warnings.length === 0);
  $("warning-box").textContent = warnings.join(" · ");
  updateSelection();
}

function gameCard(game, index) {
  const label = document.createElement("label"); label.className = "game-card";
  const checkbox = document.createElement("input"); checkbox.type = "checkbox"; checkbox.checked = state.selected.has(index);
  checkbox.addEventListener("change", () => { checkbox.checked ? state.selected.add(index) : state.selected.delete(index); updateSelection(); });
  const icon = document.createElement("div"); icon.className = "game-icon"; icon.textContent = (game.launcher || "?").slice(0, 2);
  const info = document.createElement("div"); info.className = "game-info";
  const name = document.createElement("strong"); name.textContent = game.displayName || game.externalGameId;
  const meta = document.createElement("div"); meta.className = "game-meta";
  const launcher = document.createElement("span"); launcher.className = "launcher-tag"; launcher.textContent = launcherName(game.launcher);
  const id = document.createElement("span"); id.textContent = `ID ${game.externalGameId}`;
  const path = document.createElement("span"); path.title = game.installPath; path.textContent = game.installPath;
  meta.append(launcher, id, path); info.append(name, meta);
  const version = document.createElement("div"); version.className = "game-version"; version.textContent = game.detectedVersion || "Version unbekannt";
  const source = document.createElement("small"); source.textContent = game.versionSource || "Keine Versionsquelle"; version.append(source);
  label.append(checkbox, icon, info, version); return label;
}

function updateSelection() {
  const count = state.selected.size;
  $("selection-count").textContent = `${count} ${count === 1 ? "Spiel" : "Spiele"} ausgewählt`;
  $("sync-button").disabled = count === 0 || !connectionUsable();
  $("stat-selected").textContent = state.discovery ? count : "—";
}

async function startAuthorization() {
  if (state.selected.size === 0 || !connectionUsable()) { setPersistentError("games", state.connection?.serverWarning || "LANReady ist bis zur erfolgreichen Statusprüfung gesperrt."); return; }
  const button = $("sync-button"); setBusy(button, true, "Anmeldung startet …");
  setPersistentError("games", "");
  const selectedIndices = Array.from(state.selected).sort((a, b) => a - b);
  const attempt = ++state.authAttempt;
  try {
    state.auth = await api("StartAuthorization", selectedIndices);
    if (attempt !== state.authAttempt) { api("CancelAuthorization", state.auth.authorizationId).catch(() => {}); return; }
    state.authorizedIndices = selectedIndices;
    $("auth-code").textContent = state.auth.userCode;
    $("auth-link").href = state.auth.verificationUrl;
    $("auth-status").textContent = state.auth.browserWarning || "Warte auf Bestätigung …";
    setError("auth-error", ""); $("auth-retry").classList.add("hidden"); $("auth-modal").querySelector(".waiting").classList.remove("hidden"); openModal("auth-modal"); schedulePoll(Math.max(1, state.auth.pollIntervalSeconds) * 1000, attempt);
  } catch (error) { setPersistentError("games", errorText(error)); }
  finally { setBusy(button, false, "Auswahl synchronisieren"); }
}

function schedulePoll(delay, attempt) {
  clearTimeout(state.pollTimer);
  state.pollTimer = setTimeout(async () => {
    if (attempt !== state.authAttempt) return;
    if (!state.auth || Date.now() >= Date.parse(state.auth.expiresAt)) { showAuthRetry("Die Anmeldung ist abgelaufen. Bitte neu starten."); return; }
    try {
      const result = await api("PollAuthorization", state.auth.authorizationId);
      if (attempt !== state.authAttempt) return;
      if (result.status === "authorized") {
        closeModal(true); $("confirm-copy").textContent = `Du überträgst ${state.authorizedIndices.length} ausgewählte Installation(en) an ${state.connection.serverUrl}.`;
        renderConfirmation();
        openModal("confirm-modal"); return;
      }
      $("auth-status").textContent = result.status === "slow_down" ? "Der Server bittet um etwas Geduld …" : "Warte auf Bestätigung …";
      schedulePoll(result.status === "slow_down" ? delay + 3000 : delay, attempt);
    } catch (error) { if (attempt === state.authAttempt) showAuthRetry(errorText(error)); }
  }, delay);
}

async function syncInventory() {
  if (!connectionUsable()) { closeModal(); invalidateClientWorkflow(); setPersistentError("games", state.connection?.serverWarning || "LANReady ist bis zur erfolgreichen Statusprüfung gesperrt."); return; }
  const button = $("confirm-sync"); setBusy(button, true, "Wird synchronisiert …"); setError("confirm-error", "");
  try {
    const result = await api("SyncInventory");
    closeModal(); addActivity("Inventar synchronisiert", `${result.uploaded} Installation(en) wurden bestätigt übertragen.`);
    $("stat-sync").textContent = "Gerade eben"; $("stat-sync-note").textContent = `${result.uploaded} Installation(en) übertragen`;
    showToast(`${result.uploaded} Installation(en) erfolgreich synchronisiert.`);
  } catch (error) {
    closeModal();
    setPersistentError("games", `${errorText(error)} Bitte die persönliche Anmeldung erneut starten.`);
  }
  finally { setBusy(button, false, "Jetzt synchronisieren"); }
}

async function installUpdate() {
  if (!state.connection?.updateAvailable) return;
  if (!window.confirm("Das signierte LANReady-Update jetzt herunterladen, prüfen und installieren? Die Anwendung wird danach automatisch neu gestartet.")) return;
  const button = $("install-update");
  setBusy(button, true, "Update wird verifiziert …");
  setPersistentError("global", "");
  $("update-progress-box").classList.remove("hidden");
  const progressTimer = setInterval(refreshUpdateProgress, 350);
  try {
    await api("InstallUpdate");
  } catch (error) {
    setPersistentError("global", errorText(error));
    setBusy(button, false, "Signiertes Update installieren");
  } finally {
    clearInterval(progressTimer);
    await refreshUpdateProgress();
  }
}

async function refreshUpdateProgress() {
  try {
    const progress = await api("GetUpdateProgress");
    const total = Number(progress.total || 0);
    const downloaded = Number(progress.downloaded || 0);
    const percent = total > 0 ? Math.min(100, Math.round(downloaded * 100 / total)) : 0;
    if (total > 0) $("update-progress").value = percent; else $("update-progress").removeAttribute("value");
    $("update-progress-text").textContent = total > 0 ? `${progress.stage} · ${percent} %` : (progress.stage || "Update wird vorbereitet …");
    $("cancel-update").disabled = !progress.running;
    if (!progress.running && !progress.error) $("update-progress-box").classList.add("hidden");
  } catch (_) {}
}

async function cancelUpdate() {
  $("cancel-update").disabled = true;
  await api("CancelUpdate").catch((error) => setPersistentError("global", errorText(error)));
}

async function disconnect() {
  if (!window.confirm("Die Verbindung dieses Windows-Benutzers wirklich trennen? Der lokale Geräteschlüssel wird gelöscht.")) return;
  try {
    await api("Disconnect"); state.connection = await api("State"); state.discovery = null; state.selected.clear();
    renderConnection(); renderGames(); renderStats(); addActivity("Verbindung getrennt", "Das lokale Geräteprofil wurde gelöscht."); showPage("overview"); showToast("Verbindung wurde getrennt.");
  } catch (error) { setPersistentError("global", errorText(error)); }
}

function addActivity(title, description) {
  state.activities.unshift({ title, description, time: new Date() });
  const list = $("activity-list"); list.replaceChildren(...state.activities.map((item) => {
    const row = document.createElement("div"); row.className = "timeline-item";
    const marker = document.createElement("span"); marker.className = "marker";
    const copy = document.createElement("div"); const heading = document.createElement("strong"); heading.textContent = item.title;
    const text = document.createElement("p"); text.textContent = item.description; copy.append(heading, text);
    const time = document.createElement("time"); time.textContent = item.time.toLocaleTimeString("de-DE", {hour:"2-digit", minute:"2-digit"}); row.append(marker, copy, time); return row;
  }));
}

function renderStats() {
  const count = state.discovery?.installations?.length;
  $("stat-games").textContent = Number.isInteger(count) ? count : "—";
  $("stat-games-note").textContent = Number.isInteger(count) ? "Steam · EA · Ubisoft" : "Noch nicht gesucht";
  updateSelection();
}

function renderConfirmation() {
  const selected = state.authorizedIndices.map((index) => state.discovery.installations[index]);
  $("confirm-list").replaceChildren(...selected.map((game) => {
    const item = document.createElement("div"); item.className = "confirm-item";
    const name = document.createElement("strong"); name.textContent = `${game.displayName || game.externalGameId} · ${launcherName(game.launcher)}`;
    const details = document.createElement("span"); details.textContent = `ID ${game.externalGameId} · ${game.detectedVersion || "Version unbekannt"}`;
    const path = document.createElement("span"); path.textContent = game.installPath;
    item.append(name, details, path); return item;
  }));
}

function showAuthRetry(message) {
  setError("auth-error", message);
  $("auth-status").textContent = "Anmeldung wurde nicht abgeschlossen.";
  $("auth-modal").querySelector(".waiting").classList.add("hidden");
  $("auth-retry").classList.remove("hidden");
}

async function restartAuthorization() {
  const retryButton = $("auth-retry");
  if (retryButton.disabled) return;
  retryButton.disabled = true;
  const authorizationID = state.auth?.authorizationId || "";
  state.authAttempt++;
  state.auth = null;
  state.authorizedIndices = [];
  try {
    if (authorizationID) await api("CancelAuthorization", authorizationID);
    closeModal(true);
    await startAuthorization();
  } catch (error) {
    setError("auth-error", errorText(error));
  } finally {
    retryButton.disabled = false;
  }
}

function openModal(id) {
  modalReturnFocus = document.activeElement;
  document.querySelectorAll(".modal").forEach((modal) => modal.classList.add("hidden"));
  $(id).classList.remove("hidden");
  $("modal-backdrop").classList.remove("hidden");
  $("modal-backdrop").setAttribute("aria-hidden", "false");
  document.querySelector(".app-shell").inert = true;
  setTimeout(() => $(id).querySelector("[autofocus], input:not([disabled]), a[href], button:not(.modal-close):not([disabled])")?.focus(), 20);
}

function closeModal(preserveAuthorization = false) {
  clearTimeout(state.pollTimer); state.pollTimer = null;
  $("modal-backdrop").classList.add("hidden");
  $("modal-backdrop").setAttribute("aria-hidden", "true");
  document.querySelector(".app-shell").inert = false;
  document.querySelectorAll(".modal").forEach((modal) => modal.classList.add("hidden"));
  if (modalReturnFocus?.isConnected) modalReturnFocus.focus();
  modalReturnFocus = null;
  if (!preserveAuthorization && state.auth) {
    const authorizationID = state.auth.authorizationId;
    state.authAttempt++;
    state.auth = null;
    state.authorizedIndices = [];
    api("CancelAuthorization", authorizationID).catch(() => {});
  }
}

function handleModalKeydown(event) {
  const modal = document.querySelector(".modal:not(.hidden)");
  if (!modal) return;
  if (event.key === "Escape") { event.preventDefault(); closeModal(); return; }
  if (event.key !== "Tab") return;
  const focusable = Array.from(modal.querySelectorAll("button:not([disabled]):not(.hidden), input:not([disabled]), a[href]")).filter((element) => element.offsetParent !== null);
  if (focusable.length === 0) { event.preventDefault(); return; }
  const first = focusable[0], last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
}

function setBusy(button, busy, text) { button.disabled = busy; button.textContent = text; }
function setError(id, message) { const box = $(id); box.textContent = message; box.classList.toggle("hidden", !message); }
function setPersistentError(scope, message) { $(scope + "-error-text").textContent = message; $(scope + "-error").classList.toggle("hidden", !message); }
function showToast(message, failure = false) { const toast = $("toast"); toast.textContent = message; toast.setAttribute("role", failure ? "alert" : "status"); toast.style.borderColor = failure ? "rgba(255,111,127,.3)" : ""; toast.style.background = failure ? "#301a25" : ""; toast.classList.remove("hidden"); setTimeout(() => toast.classList.add("hidden"), 5000); }
function launcherName(value) { return ({steam:"Steam", ea_app:"EA App", ubisoft_connect:"Ubisoft Connect"})[value] || value; }
