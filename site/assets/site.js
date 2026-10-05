'use strict';
(() => {
  const controls = document.querySelector('.gallery-controls');
  const tabs = [...document.querySelectorAll('[data-tab]')];
  function selectTab(tab, focus = false) {
    for (const button of tabs) {
      const selected = button === tab;
      button.setAttribute('aria-selected', String(selected));
      button.tabIndex = selected ? 0 : -1;
      document.getElementById(button.getAttribute('aria-controls')).hidden = !selected;
    }
    if (focus) tab.focus();
  }
  if (controls && tabs.length) {
    controls.hidden = false;
    for (const tab of tabs) {
      tab.addEventListener('click', () => selectTab(tab));
      tab.addEventListener('keydown', event => {
        const index = tabs.indexOf(tab);
        let next;
        if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
        if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length;
        if (event.key === 'Home') next = 0;
        if (event.key === 'End') next = tabs.length - 1;
        if (next !== undefined) { event.preventDefault(); selectTab(tabs[next], true); }
      });
    }
  }
  const dialog = document.querySelector('.lightbox');
  if (dialog && typeof dialog.showModal === 'function') {
    let opener;
    for (const link of document.querySelectorAll('[data-zoom]')) {
      link.addEventListener('click', event => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
        const image = link.querySelector('img');
        if (!image) return;
        event.preventDefault();
        opener = link;
        const preview = dialog.querySelector('img');
        preview.src = image.currentSrc || image.src;
        preview.alt = image.alt;
        dialog.querySelector('p').textContent = image.alt;
        dialog.showModal();
      });
    }
    dialog.addEventListener('close', () => { if (opener?.isConnected) opener.focus({preventScroll: true}); });
    dialog.addEventListener('click', event => {
      const box = dialog.getBoundingClientRect();
      if (event.target === dialog && (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom)) dialog.close();
    });
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
    }, {threshold: 0.08});
    document.documentElement.classList.add('motion');
    for (const element of document.querySelectorAll('.reveal')) observer.observe(element);
  }
  motionPreference();
  preference.addEventListener('change', motionPreference);
})();
