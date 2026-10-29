'use strict';
(() => {
  const root = document.documentElement;
  root.classList.add('js');
  if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) root.classList.add('motion');
})();
