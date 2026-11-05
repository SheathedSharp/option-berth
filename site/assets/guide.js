'use strict';
(() => {
  // This file runs before site.js (see templates/guide.html): it marks the
  // reading blocks for the reveal observer site.js installs a moment later.
  const reveal = (selector, scope = document) => {
    for (const element of scope.querySelectorAll(selector)) element.classList.add('reveal');
  };
  reveal([
    '.home-hero h1', '.home-lede', '.home-actions', '.home-stage',
    '.home-thesis h2', '.home-statement',
    '.home-chain h2', '.home-chain > .wrap > .home-section-lede', '.chain',
    '.home-loop h2', '.home-loop > .wrap > .home-section-lede', '.loop-table',
    '.home-contents h2', '.home-contents > .wrap > .home-section-lede',
    '.contents-part', '.home-readers h2', '.reader',
    '.home-not h2', '.not-grid p', '.home-close h2', '.home-close-copy p', '.close-panel',
  ].join(','));
  const body = document.querySelector('[data-chapter-body]');
  if (body) {
    reveal('h2, figure, table, aside.note, dl.chapter-dl', body);
    reveal('.chapter-head h1, .chapter-head .chapter-lede');
    reveal('.chapter-nav .chapter-link');
  }

  // One rail, five stations: the chain explorer on the overview.
  const chain = document.querySelector('[data-chain]');
  if (chain) {
    const stops = [...chain.querySelectorAll('.chain-stop')];
    const panels = [...chain.querySelectorAll('.chain-panel')];
    const show = key => {
      for (const stop of stops) {
        const on = stop.dataset.station === key;
        stop.classList.toggle('is-active', on);
        stop.setAttribute('aria-pressed', String(on));
      }
      for (const panel of panels) {
        const on = panel.dataset.panel === key;
        panel.classList.toggle('is-active', on);
        panel.setAttribute('aria-hidden', String(!on));
      }
    };
    for (const stop of stops) {
      stop.addEventListener('click', () => show(stop.dataset.station));
      stop.addEventListener('keydown', event => {
        if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
        event.preventDefault();
        const index = stops.indexOf(stop);
        const next = event.key === 'Home' ? 0
          : event.key === 'End' ? stops.length - 1
            : (index + (event.key === 'ArrowRight' ? 1 : stops.length - 1)) % stops.length;
        stops[next].focus();
        show(stops[next].dataset.station);
      });
    }
    // Mark the visible panel as observed so assistive technology reads the
    // same one the screen shows. Without script every panel stays visible.
    show(stops.find(stop => stop.classList.contains('is-active'))?.dataset.station || stops[0].dataset.station);
  }

  // The chapter's own contents: mark the section currently in view.
  const pageToc = document.querySelector('.page-toc');
  if (pageToc) {
    const links = [...pageToc.querySelectorAll('a[href^="#"]')];
    const headings = links
      .map(link => document.getElementById(link.getAttribute('href').slice(1)))
      .filter(Boolean);
    const mark = id => {
      for (const link of links) {
        const on = link.getAttribute('href') === '#' + id;
        link.classList.toggle('is-active', on);
        if (on) link.setAttribute('aria-current', 'location');
        else link.removeAttribute('aria-current');
      }
    };
    if (headings.length && 'IntersectionObserver' in window) {
      const observer = new IntersectionObserver(entries => {
        const visible = entries
          .filter(entry => entry.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
        if (visible[0]) mark(visible[0].target.id);
      }, { rootMargin: '-18% 0px -64% 0px' });
      for (const heading of headings) observer.observe(heading);
      mark(headings[0].id);
    }
  }

  // On a phone the rail's contents is a disclosure, not a wall of links.
  const railToc = document.querySelector('.rail-toc');
  if (railToc && matchMedia('(max-width: 900px)').matches) railToc.open = false;
})();
