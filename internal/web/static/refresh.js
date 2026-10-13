// Counts down to the next live refresh. htmx swaps the live section every
// data-refresh-in seconds, and the count starts over after each swap.
(function () {
  "use strict";
  var left = 0;

  function target() {
    return document.querySelector("[data-refresh-in]");
  }

  function draw() {
    var el = target();
    if (!el) return;
    var m = Math.floor(left / 60);
    var s = left % 60;
    el.textContent = el.getAttribute("data-label").replace("%s", m + ":" + (s < 10 ? "0" : "") + s);
  }

  function reset() {
    var el = target();
    if (!el) return;
    left = parseInt(el.getAttribute("data-refresh-in"), 10) || 60;
    draw();
  }

  document.addEventListener("DOMContentLoaded", function () {
    if (!target()) return;
    reset();
    setInterval(function () {
      if (left > 0) left--;
      draw();
    }, 1000);
  });
  document.addEventListener("htmx:afterSettle", reset);
})();
