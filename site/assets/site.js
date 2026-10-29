'use strict';
(() => {
  const root = document.documentElement;

  // Command filter: keep every row in the DOM, hide the ones that do not match.
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

  // One fragment changes while four short statements pass: the stage control.
  const how = document.getElementById('how');
  if (how) {
    const range = how.querySelector('.stage-range');
    const views = [...how.querySelectorAll('.stage-view')];
    const copies = [...how.querySelectorAll('.stage-copy')];
    const names = [...how.querySelectorAll('.stage-names button')];
    const show = index => {
      const n = Number(index);
      how.dataset.stage = String(n);
      for (const view of views) view.classList.toggle('is-active', Number(view.dataset.stage) === n);
      for (const copy of copies) copy.classList.toggle('is-active', Number(copy.dataset.stage) === n);
      for (const button of names) {
        const on = Number(button.dataset.stage) === n;
        button.classList.toggle('is-active', on);
        if (on) button.setAttribute('aria-current', 'true');
        else button.removeAttribute('aria-current');
      }
      if (range) {
        range.value = String(n);
        const name = names[n]?.textContent?.trim();
        if (name) range.setAttribute('aria-valuetext', name);
      }
    };
    if (range) range.addEventListener('input', () => show(range.value));
    for (const button of names) button.addEventListener('click', () => show(button.dataset.stage));
    if (range) show(range.value);
  }

  // The hero panel quietly moves between the status and the logs of one run.
  const views = [...document.querySelectorAll('.hero-view')];
  const panel = document.querySelector('.hero-panel');
  const reduce = window.matchMedia('(prefers-reduced-motion: reduce)');
  let heroTimer;
  const stopHero = () => {
    if (heroTimer) clearInterval(heroTimer);
    heroTimer = undefined;
  };
  const showHero = index => {
    for (let i = 0; i < views.length; i += 1) {
      const on = i === index;
      views[i].classList.toggle('is-active', on);
      views[i].setAttribute('aria-hidden', on ? 'false' : 'true');
    }
  };
  const startHero = () => {
    stopHero();
    if (views.length < 2 || reduce.matches) return;
    let index = Math.max(0, views.findIndex(view => view.classList.contains('is-active')));
    heroTimer = setInterval(() => {
      if (document.hidden) return;
      index = (index + 1) % views.length;
      showHero(index);
    }, 6000);
  };
  if (panel) {
    panel.addEventListener('pointerenter', stopHero);
    panel.addEventListener('pointerleave', startHero);
    panel.addEventListener('focusin', stopHero);
    panel.addEventListener('focusout', startHero);
  }

  // Copy a fragment without leaving the page.
  for (const button of document.querySelectorAll('.copy')) {
    const label = button.textContent;
    const copied = button.dataset.copied || label;
    const manual = button.dataset.select || label;
    button.addEventListener('click', async () => {
      const target = document.getElementById(button.dataset.copy);
      if (!target) return;
      const status = button.parentElement.querySelector('.copy-status');
      try {
        await navigator.clipboard.writeText(target.innerText);
        button.textContent = copied;
        if (status) status.textContent = copied;
      } catch {
        const selection = window.getSelection();
        const selectionRange = document.createRange();
        selectionRange.selectNodeContents(target);
        selection.removeAllRanges();
        selection.addRange(selectionRange);
        button.textContent = manual;
        if (status) status.textContent = manual;
      }
      clearTimeout(button.copyTimer);
      button.copyTimer = setTimeout(() => {
        button.textContent = label;
        if (status) status.textContent = '';
      }, 2400);
    });
  }

  // Sections arrive once, in reading order. No observer, no hidden content.
  let observer;
  function motionPreference() {
    if (observer) observer.disconnect();
    root.classList.remove('motion');
    if (reduce.matches || !('IntersectionObserver' in window)) {
      startHero();
      return;
    }
    observer = new IntersectionObserver(entries => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        entry.target.classList.add('is-visible');
        observer.unobserve(entry.target);
      }
    }, { rootMargin: '0px 0px -12% 0px', threshold: 0.1 });
    root.classList.add('motion');
    for (const element of document.querySelectorAll('.reveal')) {
      if (element.getBoundingClientRect().top < innerHeight) element.classList.add('is-visible');
      observer.observe(element);
    }
    startHero();
  }
  motionPreference();
  reduce.addEventListener('change', motionPreference);
})();
