// Tooltip for the uptime ticks: the day's uptime in the tick's colour, the
// date, the time spent in each outage or maintenance status, and related
// incidents. One element is reused and placed under the tick, kept inside the
// viewport, with its arrow still pointing at the tick. The detail comes from
// a hidden .tip-src inside the tick, which the server renders and escapes.
// A tooltip with links stays open while the pointer moves onto it.
(function () {
  "use strict";
  var tip, head, date, body, current, timer;

  function build() {
    tip = document.createElement("div");
    tip.className = "tip";
    tip.setAttribute("role", "tooltip");
    tip.hidden = true;
    head = document.createElement("div");
    date = document.createElement("div");
    date.className = "tip-date";
    body = document.createElement("div");
    body.className = "tip-body";
    tip.append(head, date, body);
    document.body.appendChild(tip);
  }

  function show(el) {
    clearTimeout(timer);
    if (el === current && !tip.hidden) return;
    current = el;
    head.className = "tip-head " + el.className;
    head.textContent = el.getAttribute("data-tip-head");
    date.textContent = el.getAttribute("data-tip-date");
    var src = el.querySelector(".tip-src");
    body.innerHTML = src ? src.innerHTML : "";
    tip.classList.toggle("interactive", !!body.querySelector("a"));
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
    clearTimeout(timer);
    current = null;
    if (tip) tip.hidden = true;
  }

  // Leave time to cross the gap between the tick and a tooltip with links.
  function hideSoon() {
    clearTimeout(timer);
    timer = setTimeout(hide, tip && tip.classList.contains("interactive") ? 250 : 0);
  }

  function tick(target) {
    return target.closest ? target.closest(".ticks i[data-tip-head]") : null;
  }

  document.addEventListener("DOMContentLoaded", build);
  document.addEventListener("mouseover", function (e) {
    if (!tip) return;
    var el = tick(e.target);
    if (el) show(el);
    else if (tip.contains(e.target)) clearTimeout(timer);
    else hideSoon();
  });
  // Touch screens have no hover: a tap opens the tooltip, a tap elsewhere closes it.
  document.addEventListener("click", function (e) {
    if (!tip) return;
    var el = tick(e.target);
    if (el) show(el);
    else if (!tip.contains(e.target)) hide();
  });
  document.addEventListener("htmx:beforeSwap", hide);
  window.addEventListener("scroll", hide, { passive: true });
})();
