(function (root, factory) {
  "use strict";
  const helpers = factory();
  if (typeof module === "object" && module.exports) module.exports = helpers;
  root.LANReadyCatalogCache = helpers;
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  function isActive(job) {
    return job?.Status === "queued" || job?.Status === "running";
  }

  function becameTerminal(previous, next) {
    return previous.some((oldJob) => isActive(oldJob) && next.some((newJob) => newJob.ID === oldJob.ID && !isActive(newJob)));
  }

  function canShowAction(canEdit, item, job) {
	return Boolean(canEdit && (isActive(job) || (item?.Enabled && Number(item?.SourceID || 0) > 0)));
  }

  return { isActive, becameTerminal, canShowAction };
});
