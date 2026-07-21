(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.LANReadyInstalls = api;
})(typeof window !== "undefined" ? window : globalThis, function () {
  "use strict";

  // Downloads here are multi-gigabyte, so the size is never hidden behind a
  // spinner: the user sees it before confirming.
  function formatSize(bytes) {
    const value = Number(bytes);
    if (!Number.isFinite(value) || value < 0) return "unbekannte Größe";
    if (value < 1024) return `${value} B`;
    const units = ["KB", "MB", "GB", "TB"];
    let size = value / 1024;
    let unit = 0;
    while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit += 1; }
    const rounded = size >= 100 ? Math.round(size) : Math.round(size * 10) / 10;
    return `${String(rounded).replace(".", ",")} ${units[unit]}`;
  }

  // viewModel turns the backend list into what the panel renders. An empty or
  // failed list must never look like "everything installed".
  function viewModel(pending, error) {
    if (error) return { state: "error", message: String(error), items: [], pendingCount: 0 };
    if (!Array.isArray(pending)) return { state: "unavailable", message: "Installationsstand ist nicht verfügbar.", items: [], pendingCount: 0 };
    const items = pending.map((entry) => ({
      gameId: String(entry?.gameId || ""),
      name: String(entry?.name || entry?.gameId || "Unbenanntes Spiel"),
      sizeLabel: formatSize(entry?.sizeBytes),
      targetDir: String(entry?.targetDir || ""),
      installed: entry?.installed === true
    })).filter((entry) => entry.gameId !== "");
    const pendingCount = items.filter((entry) => !entry.installed).length;
    if (items.length === 0) return { state: "none", message: "Dieses Event liefert keine Spiele über LANReady aus.", items, pendingCount: 0 };
    if (pendingCount === 0) return { state: "complete", message: "Alle Spiele dieses Events sind installiert.", items, pendingCount };
    return {
      state: "pending",
      message: `${pendingCount} ${pendingCount === 1 ? "Spiel wartet" : "Spiele warten"} auf die Installation.`,
      items,
      pendingCount
    };
  }

  // confirmText spells out what happens before a large download starts,
  // including where the files land, so the choice is informed.
  function confirmText(item) {
    return `${item.name} (${item.sizeLabel}) herunterladen und nach\n${item.targetDir}\nentpacken?\n\nDer Download kann je nach Netzwerk lange dauern.`;
  }

  // candidateLabel marks the suggestion without hiding that it is only a guess.
  function candidateLabel(candidate) {
    const size = formatSize(candidate?.sizeBytes);
    const path = String(candidate?.relativePath || "");
    return candidate?.recommended === true ? `${path} · ${size} · Vorschlag` : `${path} · ${size}`;
  }

  return { formatSize, viewModel, confirmText, candidateLabel };
});
