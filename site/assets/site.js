'use strict';
(() => {
  const input = document.getElementById('command-filter');
  const list = document.getElementById('command-list');
  const empty = document.getElementById('command-empty');
  const status = document.getElementById('command-status');
  if (input && list && empty && status) {
    const rows = [...list.querySelectorAll('li')];
    const filtered = input.dataset.countFiltered || '{n}';
    const all = input.dataset.countAll || '{n}';
    const update = () => {
      const query = input.value.trim().toLowerCase();
      let visible = 0;
      for (const row of rows) {
        const match = !query || row.textContent.toLowerCase().includes(query);
        row.hidden = !match;
        if (match) visible += 1;
      }
      empty.hidden = !(query && visible === 0);
      status.textContent = (query ? filtered : all).replace('{n}', String(visible));
    };
    input.addEventListener('input', update);
    input.addEventListener('keydown', event => {
      if (event.key === 'Escape' && input.value) {
        input.value = '';
        update();
      }
    });
    update();
  }

  const preference = matchMedia('(prefers-reduced-motion: reduce)');
  let observer;
  function motionPreference() {
    observer?.disconnect();
    document.documentElement.classList.remove('motion');
    if (preference.matches || !('IntersectionObserver' in window)) return;
    observer = new IntersectionObserver(entries => {
      for (const entry of entries) if (entry.isIntersecting) {
        entry.target.classList.add('is-visible');
        observer.unobserve(entry.target);
      }
    }, {threshold: 0.12});
    document.documentElement.classList.add('motion');
    for (const element of document.querySelectorAll('.reveal')) observer.observe(element);
  }
  motionPreference();
  preference.addEventListener('change', motionPreference);
})();
