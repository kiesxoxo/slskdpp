// Applies the saved theme before the page paints. External, since the
// content security policy allows no inline scripts.
(function () {
  try {
    var t = localStorage.getItem('slskdpp-theme');
    if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t;
  } catch (e) {}
})();
