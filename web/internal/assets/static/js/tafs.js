// /tafs on a large screen: the nine screens run sideways under the page's own
// vertical scroll, and their stickers drift at their own speed. Without this
// file, or on a phone, the page reads top to bottom (the web-stack design: a JS island,
// progressive enhancement only). With reduced motion, nothing drifts.
(function () {
  var root = document.documentElement;
  root.classList.add("tf-js");
  var wide = matchMedia("(min-width: 900px) and (min-height: 560px)");
  var still = matchMedia("(prefers-reduced-motion: reduce)");

  function start() {
    var hs = document.getElementById("main");
    var stick = hs && hs.querySelector(".stick");
    var track = hs && hs.querySelector(".track");
    if (!track) return;
    var panels = [].slice.call(track.children), n = panels.length;
    var movers = panels.map(function (p) { return [].slice.call(p.querySelectorAll("[data-x]")); });
    var bar = document.querySelector(".bar"), hint = document.querySelector(".hint");
    var queued = false;

    function rest() {
      movers.forEach(function (list) { list.forEach(function (el) { el.style.removeProperty("--tx"); el.style.removeProperty("--tr"); }); });
    }
    function draw() {
      queued = false;
      if (!wide.matches) { track.style.transform = ""; root.classList.remove("tf-past"); rest(); return; }
      var span = (n - 1) * innerHeight, past = scrollY - hs.offsetTop;
      var at = (span ? Math.min(Math.max(past, 0), span) / span : 0) * (n - 1);
      track.style.transform = "translateX(" + (-at * 100 / n) + "%)";
      // Past the stage, the bar fades and the header hides: the footer scrolls under nothing.
      root.classList.toggle("tf-past", past > span + 40);
      if (bar) { bar.firstElementChild.style.width = (at / (n - 1)) * 100 + "%"; bar.style.opacity = past > span + 40 ? 0 : 1; }
      if (hint) hint.style.opacity = at > 0.1 ? 0 : 0.8;
      if (still.matches) { rest(); return; }
      movers.forEach(function (list, i) {
        var off = i - at;
        if (Math.abs(off) > 1.4) return;
        list.forEach(function (el) {
          el.style.setProperty("--tx", off * parseFloat(el.dataset.x) * innerWidth * 0.35 + "px");
          el.style.setProperty("--tr", off * (parseFloat(el.dataset.r) || 0) + "deg");
        });
      });
    }
    function queue() { if (!queued) { queued = true; requestAnimationFrame(draw); } }

    // A screen's place on the page: sideways, the scroll that brings it in.
    function go(panel, smooth) {
      var i = panels.indexOf(panel);
      if (!wide.matches || i < 0) { panel.scrollIntoView({ behavior: smooth ? "smooth" : "auto" }); return; }
      scrollTo({ top: hs.offsetTop + i * innerHeight, behavior: smooth && !still.matches ? "smooth" : "auto" });
    }
    function panelOf(hash) {
      var el = hash.length > 1 && document.getElementById(decodeURIComponent(hash.slice(1)));
      return el && panels.indexOf(el) >= 0 ? el : null;
    }

    document.addEventListener("click", function (e) {
      var a = e.target.closest && e.target.closest('a[href^="#"]');
      var panel = a && panelOf(a.getAttribute("href"));
      if (!panel || !wide.matches) return;
      e.preventDefault();
      go(panel, true);
      history.replaceState(null, "", panel.id === "top" ? location.pathname : "#" + panel.id);
    });
    // Tabbing into a screen brings it in, and keeps the stage from scrolling.
    document.addEventListener("focusin", function (e) {
      if (!wide.matches) return;
      stick.scrollLeft = 0;
      var panel = panels.filter(function (p) { return p.contains(e.target); })[0];
      if (panel && Math.abs(hs.offsetTop + panels.indexOf(panel) * innerHeight - scrollY) > innerHeight / 2) go(panel, false);
    });
    addEventListener("scroll", queue, { passive: true });
    addEventListener("resize", queue);
    if (wide.addEventListener) { wide.addEventListener("change", queue); still.addEventListener("change", queue); }
    draw();
    var first = panelOf(location.hash);
    if (first && wide.matches) setTimeout(function () { go(first, false); }, 0);
  }

  // The videos in their phones (tafsPhone): each plays while at least half of
  // it is in view and pauses when it leaves, or when the tab is hidden. Muted,
  // as browsers play only muted video by themselves, and nothing loads before
  // it plays (preload="none"). With reduced motion, or where the browser won't
  // play it, the poster stays and the play button plays it with its controls.
  // Without this file, the video keeps its own controls.
  function videos() {
    var list = [].slice.call(document.querySelectorAll(".phone video"));
    if (!list.length) return;
    function button(v) { return v.parentNode.querySelector(".play"); }
    function asked(v) {
      var b = button(v);
      if (b) b.hidden = true;
      v.dataset.asked = "1";
      v.controls = true;
      var p = v.play();
      if (p && p.catch) p.catch(function () {});
    }
    function offer(v) { var b = button(v); if (b) b.hidden = false; }
    list.forEach(function (v) {
      v.controls = false;
      var b = button(v);
      if (b) b.addEventListener("click", function () { asked(v); });
    });
    if (still.matches || !("IntersectionObserver" in window)) { list.forEach(offer); return; }
    var seen = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        var v = e.target;
        if (v.dataset.asked) return;
        if (e.intersectionRatio >= 0.5 && !document.hidden && !still.matches) {
          var p = v.play();
          if (p && p.catch) p.catch(function () { offer(v); });
        } else if (!v.paused) v.pause();
      });
    }, { threshold: [0, 0.5] });
    list.forEach(function (v) { seen.observe(v); });
    document.addEventListener("visibilitychange", function () {
      if (document.hidden) list.forEach(function (v) { if (!v.paused) v.pause(); });
    });
  }

  function boot() { start(); videos(); }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})();
