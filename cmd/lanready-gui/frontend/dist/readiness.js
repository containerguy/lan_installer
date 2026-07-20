(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.LANReadyReadiness = api;
})(typeof window !== "undefined" ? window : globalThis, function () {
  "use strict";

  const stateLabels = {
    none: "Kein aktives Event",
    ready: "Bereit",
    warning: "Prüfung erforderlich",
    action_required: "Aktion erforderlich",
    security_error: "Sicherheitsprüfung fehlgeschlagen",
    error: "Nicht verfügbar"
  };
  const gameLabels = {
    ready: "Passende Version installiert",
    missing: "Nicht gefunden",
    version_unknown: "Version nicht ermittelbar",
    version_mismatch: "Andere Version installiert",
    unidentifiable: "Keine stabile Spiel-ID im Release"
  };
  const launcherLabels = {
    detected_version_unverified: "Über Spielefund erkannt · Version ungeprüft",
    not_detected: "Nicht über Spielefunde erkannt"
  };

  function clampPercentage(value) {
    const number = Number(value);
    return Number.isFinite(number) ? Math.max(0, Math.min(100, Math.round(number))) : 0;
  }

  function viewModel(readiness) {
    const value = readiness && typeof readiness === "object" ? readiness : { state: "none" };
    const state = Object.hasOwn(stateLabels, value.state) ? value.state : "error";
    const percentage = state === "none" || state === "error" || state === "security_error" ? null : clampPercentage(value.percentage);
    return {
      state,
      label: stateLabels[state],
      tone: state === "ready" ? "ok" : state === "warning" ? "warn" : state === "none" ? "neutral" : "danger",
      percentage,
      ringText: percentage === null ? "—" : percentage + "%",
      eventTitle: value.eventId ? "Event " + value.eventId : "Noch kein signiertes Event aktiviert",
      message: value.message || "Der Eventstatus konnte nicht ausgewertet werden.",
      games: Array.isArray(value.games) ? value.games.map((game) => ({...game, statusLabel: gameLabels[game.status] || "Unbekannter Status"})) : [],
      launchers: Array.isArray(value.launchers) ? value.launchers.map((launcher) => ({...launcher, statusLabel: launcherLabels[launcher.status] || "Unbekannter Status"})) : []
    };
  }

  function componentBadge(status, required) {
    if (status === "ready") return "Bereit";
    if (!required) return "Optional offen";
    if (status === "detected_version_unverified") return "Version offen";
    return "Handlung nötig";
  }

  return { clampPercentage, componentBadge, viewModel };
});
