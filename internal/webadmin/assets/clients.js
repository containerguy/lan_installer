"use strict";

document.addEventListener("DOMContentLoaded", () => {
  document.querySelectorAll(".bulk-import-form").forEach((form) => {
    const key = document.createElement("input");
    key.type = "hidden";
    key.name = "idempotency_key";
    key.value = window.crypto.randomUUID();
    form.append(key);
  });
});
