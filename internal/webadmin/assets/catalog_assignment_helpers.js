(function (root, factory) {
  "use strict";
  const helpers = factory();
  if (typeof module === "object" && module.exports) module.exports = helpers;
  root.LANReadyCatalogAssignments = helpers;
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  function retainedID(items, requestedID) {
    const values = Array.isArray(items) ? items : [];
    const requested = Number(requestedID || 0);
    if (values.some((item) => Number(item?.ID) === requested)) return requested;
    return values.length ? Number(values[0]?.ID || 0) : 0;
  }

  function availableVersions(gameVersions, eventGames, eventID) {
    const selectedEventID = Number(eventID || 0);
    if (!selectedEventID) return [];
    const assigned = new Set(
      (Array.isArray(eventGames) ? eventGames : [])
        .filter((item) => Number(item?.EventID) === selectedEventID)
        .map((item) => Number(item?.GameVersionID || 0)),
    );
    return (Array.isArray(gameVersions) ? gameVersions : []).filter(
      (item) => Boolean(item?.Enabled) && !assigned.has(Number(item?.ID || 0)),
    );
  }

  function canSubmit(eventID, versionID) {
    return Number(eventID || 0) > 0 && Number(versionID || 0) > 0;
  }

  return { retainedID, availableVersions, canSubmit };
});
