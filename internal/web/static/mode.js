// Light/dark toggle. Loaded in <head> without defer so a stored choice is
// applied before the first paint. With no stored choice the page starts in
// NOROSHI_MODE (data-default-mode) or, without one, follows the system
// setting. Picking that starting mode clears the stored choice.
(function () {
  "use strict";
  var KEY = "noroshi-mode";
  var root = document.documentElement;
  var media = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;
  var preset = root.getAttribute("data-default-mode");
  if (preset !== "light" && preset !== "dark") preset = null;

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
  function base() {
    return preset || system();
  }
  function current() {
    return root.getAttribute("data-mode") || system();
  }

  apply(stored() || preset);

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
      var keep = next !== base();
      apply(keep ? next : preset);
      try {
        if (keep) localStorage.setItem(KEY, next);
        else localStorage.removeItem(KEY);
      } catch (e) {}
      refresh();
    });
    if (media && media.addEventListener) media.addEventListener("change", refresh);
    button.hidden = false;
    refresh();
  });
})();
