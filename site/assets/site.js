'use strict';
(() => {
  const root = document.documentElement;
  const gsap = window.gsap;
  const reduce = window.matchMedia('(prefers-reduced-motion: reduce)');
  const still = () => !gsap || reduce.matches;

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

  // Copy a fragment without leaving the page.
  for (const button of document.querySelectorAll('.copy')) {
    const labelEl = button.querySelector('.copy-label');
    const original = labelEl ? labelEl.textContent : '';
    const copied = button.dataset.copied || original;
    const manual = button.dataset.select || copied;
    const box = button.parentElement.querySelector('.copy-status');
    button.addEventListener('click', async () => {
      const target = document.getElementById(button.dataset.copy);
      if (!target) return;
      try {
        await navigator.clipboard.writeText(target.innerText);
        if (labelEl) labelEl.textContent = copied;
        if (box) box.textContent = copied;
      } catch {
        const selection = window.getSelection();
        const range = document.createRange();
        range.selectNodeContents(target);
        selection.removeAllRanges();
        selection.addRange(range);
        if (labelEl) labelEl.textContent = manual;
        if (box) box.textContent = manual;
      }
      clearTimeout(button.copyTimer);
      button.copyTimer = setTimeout(() => {
        if (labelEl) labelEl.textContent = original;
        if (box) box.textContent = '';
      }, 2400);
    });
  }

  // The one live thing: two worktrees of one repository, each with its own berth.
  const switchButtons = [...document.querySelectorAll('.wt-switch button')];
  const panels = [...document.querySelectorAll('[data-wt-panel]')];
  const setPressed = index => {
    for (const button of switchButtons) {
      button.setAttribute('aria-pressed', String(Number(button.dataset.wt) === index));
    }
  };
  const showWorktree = index => {
    const current = panels.find(panel => !panel.hidden);
    const next = panels[index];
    if (!next || next === current) return;
    setPressed(index);
    next.hidden = false;
    if (still()) {
      if (current) current.hidden = true;
      return;
    }
    const out = current ? [...current.querySelectorAll('.ln')] : [];
    const into = [...next.querySelectorAll('.ln')];
    gsap.to(out, { y: -8, autoAlpha: 0, duration: 0.22, stagger: 0.03, ease: 'power2.in' });
    gsap.fromTo(into, { y: 10, autoAlpha: 0 }, {
      y: 0, autoAlpha: 1, duration: 0.36, stagger: 0.04, ease: 'power2.out', delay: 0.12,
      onComplete: () => { if (current) { current.hidden = true; gsap.set(current, { clearProps: 'all' }); } },
    });
  };
  for (const button of switchButtons) {
    button.addEventListener('click', () => showWorktree(Number(button.dataset.wt)));
  }

  // A press answers on every control.
  if (gsap && !reduce.matches) {
    for (const el of document.querySelectorAll('.btn, .copy, .wt-switch button, .command-list a')) {
      el.addEventListener('pointerdown', () => gsap.to(el, { scale: 0.97, duration: 0.12, ease: 'power2.out' }));
      el.addEventListener('pointerup', () => gsap.to(el, { scale: 1, duration: 0.24, ease: 'power2.out' }));
      el.addEventListener('pointerleave', () => gsap.to(el, { scale: 1, duration: 0.24, ease: 'power2.out' }));
    }
  }

  // Motion: tiles arrive in rows, the log stream keeps moving, nothing hides without script.
  if (!gsap) return;
  const mm = gsap.matchMedia();
  mm.add('(prefers-reduced-motion: no-preference)', () => {
    const tiles = [...document.querySelectorAll('.board > .tile')];
    let batch = [];
    let timer;
    const play = () => {
      const group = batch.filter(tile => tile.dataset.arrived !== '1');
      batch = [];
      if (!group.length) return;
      for (const tile of group) tile.dataset.arrived = '1';
      gsap.from(group, {
        y: 26, autoAlpha: 0, duration: 0.7, stagger: 0.06, ease: 'power2.out',
        onComplete: () => gsap.set(group, { clearProps: 'transform,opacity,visibility' }),
      });
      const parts = group.flatMap(tile => [...tile.querySelectorAll('.stage, .mark, .fact')]);
      if (parts.length) {
        gsap.from(parts, { y: 14, autoAlpha: 0, duration: 0.55, delay: 0.18, stagger: 0.05, ease: 'power2.out' });
      }
    };
    const observer = new IntersectionObserver(entries => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        batch.push(entry.target);
        observer.unobserve(entry.target);
      }
      clearTimeout(timer);
      timer = setTimeout(play, 40);
    }, { rootMargin: '0px 0px -8% 0px', threshold: 0.08 });
    for (const tile of tiles) observer.observe(tile);

    const caret = document.querySelector('.tile-status .caret');
    if (caret) gsap.to(caret, { autoAlpha: 0, duration: 0.5, repeat: -1, yoyo: true, ease: 'steps(1)' });

    const stream = document.querySelector('.logs-stream .term');
    const loop = stream ? gsap.to(stream, { y: -72, duration: 6, ease: 'none', repeat: -1 }) : null;

    return () => {
      observer.disconnect();
      clearTimeout(timer);
      if (loop) loop.kill();
      gsap.set(tiles, { clearProps: 'transform,opacity,visibility' });
    };
  });
})();
