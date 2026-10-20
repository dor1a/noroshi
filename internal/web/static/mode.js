// Light/dark toggle. Loaded in <head> without defer so a stored choice is
// applied before the first paint. With no stored choice the page follows the
// system setting, and picking the mode the system is already in clears the
// stored choice so the page follows it again.
(function () {
  "use strict";
  var KEY = "noroshi-mode";
  var root = document.documentElement;
  var media = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;

  function stored() {
    try { return localStorage.getItem(KEY); } catch (e) { return null; }
  }
  function apply(mode) {
    if (mode === "light" || mode === "dark") root.setAttribute("data-mode", mode);
    else root.removeAttribute("data-mode");
  }
  function system() {
    return media && media.matches ? "dark" : "light";
  }
  function current() {
    return root.getAttribute("data-mode") || system();
  }

  apply(stored());

  document.addEventListener("DOMContentLoaded", function () {
    var button = document.getElementById("mode-toggle");
    if (!button) return;
    function refresh() {
      var dark = current() === "dark";
      button.setAttribute("data-icon", dark ? "sun" : "moon");
      var label = button.getAttribute(dark ? "data-label-light" : "data-label-dark");
      button.setAttribute("aria-label", label);
      button.setAttribute("title", label);
    }
    button.addEventListener("click", function () {
      var next = current() === "dark" ? "light" : "dark";
      if (next === system()) next = null;
      apply(next);
      try {
        if (next) localStorage.setItem(KEY, next);
        else localStorage.removeItem(KEY);
      } catch (e) {}
      refresh();
    });
    if (media && media.addEventListener) media.addEventListener("change", refresh);
    button.hidden = false;
    refresh();
  });
})();
