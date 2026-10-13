// Tooltip for the uptime ticks: the day's uptime in the tick's colour, the
// date, and any downtime. One element is reused and placed under the tick,
// kept inside the viewport, with its arrow still pointing at the tick.
(function () {
  "use strict";
  var tip, head, date, note;

  function build() {
    tip = document.createElement("div");
    tip.className = "tip";
    tip.setAttribute("role", "tooltip");
    tip.hidden = true;
    head = document.createElement("div");
    date = document.createElement("div");
    date.className = "tip-date";
    note = document.createElement("div");
    note.className = "tip-note";
    tip.append(head, date, note);
    document.body.appendChild(tip);
  }

  function show(el) {
    head.className = "tip-head " + el.className;
    head.textContent = el.getAttribute("data-tip-head");
    date.textContent = el.getAttribute("data-tip-date");
    var n = el.getAttribute("data-tip-note") || "";
    note.textContent = n;
    note.hidden = !n;
    tip.hidden = false;

    var r = el.getBoundingClientRect();
    var w = tip.offsetWidth;
    var center = r.left + r.width / 2;
    var x = Math.max(8, Math.min(center - w / 2, document.documentElement.clientWidth - w - 8));
    tip.style.left = x + window.scrollX + "px";
    tip.style.top = r.bottom + 10 + window.scrollY + "px";
    tip.style.setProperty("--arrow-x", center - x + "px");
  }

  function hide() {
    if (tip) tip.hidden = true;
  }

  document.addEventListener("DOMContentLoaded", build);
  document.addEventListener("mouseover", function (e) {
    var el = e.target.closest ? e.target.closest(".ticks i[data-tip-head]") : null;
    if (el && tip) show(el);
    else hide();
  });
  document.addEventListener("htmx:beforeSwap", hide);
  window.addEventListener("scroll", hide, { passive: true });
})();
