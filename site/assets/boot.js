'use strict';
(() => {
  const root = document.documentElement;
  // The guide may be painted in a light or a dark skin. The reader's explicit
  // choice is applied before first paint so the page never flashes the other
  // one; with no stored choice the CSS follows the system.
  try {
    const theme = localStorage.getItem('oberth-theme');
    if (theme === 'light' || theme === 'dark') root.dataset.theme = theme;
  } catch {
    /* storage blocked: follow the system skin */
  }
})();
