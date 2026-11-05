'use strict';
(() => {
  // Everything here is an enhancement: without this file the whole book is
  // still readable, the rail is still in the flow and the language menu is
  // still a plain <details>. Controls that only work with script start hidden
  // in the HTML and are revealed here, so no dead control is ever shown.
  const root = document.documentElement;
  const body = document.body;
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)');
  const wide = window.matchMedia('(min-width: 1001px)');

  const store = {
    get(key) {
      try { return localStorage.getItem(key); } catch { return null; }
    },
    set(key, value) {
      try { localStorage.setItem(key, value); } catch { /* private mode: no memory, still works */ }
    },
  };

  /* ---------------------------------------------------------------- theme */

  const themeButton = document.querySelector('[data-theme-toggle]');
  const systemDark = () => window.matchMedia('(prefers-color-scheme: dark)').matches;

  const currentTheme = () => root.dataset.theme || (systemDark() ? 'dark' : 'light');

  const syncThemeButton = () => {
    if (!themeButton) return;
    const next = currentTheme() === 'dark' ? 'light' : 'dark';
    themeButton.dataset.next = next;
    themeButton.setAttribute(
      'aria-label',
      next === 'light' ? themeButton.dataset.labelLight : themeButton.dataset.labelDark,
    );
  };

  if (themeButton) {
    themeButton.hidden = false;
    syncThemeButton();
    themeButton.addEventListener('click', () => {
      const next = themeButton.dataset.next === 'light' ? 'light' : 'dark';
      root.dataset.theme = next;
      store.set('oberth-theme', next);
      syncThemeButton();
    });
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', syncThemeButton);
  }

  /* ------------------------------------------------------- rail and contents */

  const railButton = document.querySelector('[data-rail-toggle]');
  const rail = document.querySelector('[data-rail]');
  const setRail = open => {
    if (!railButton || !rail) return;
    root.dataset.railOpen = String(open);
    railButton.setAttribute('aria-expanded', String(open));
  };

  if (railButton && rail) {
    railButton.hidden = false;
    setRail(false);
    railButton.addEventListener('click', () => setRail(root.dataset.railOpen !== 'true'));
    rail.addEventListener('click', event => {
      if (event.target.closest('a') && !wide.matches) setRail(false);
    });
  }

  const toc = document.querySelector('[data-toc]');
  const syncToc = () => { if (toc) toc.open = wide.matches; };
  if (toc) {
    syncToc();
    wide.addEventListener('change', syncToc);
  }

  document.addEventListener('keydown', event => {
    if (event.key !== 'Escape') return;
    if (root.dataset.railOpen === 'true') setRail(false);
  });

  /* --------------------------------------------------------- heading anchors */

  for (const heading of document.querySelectorAll('.doc-prose h2[id], .doc-prose h3[id]')) {
    const anchor = document.createElement('a');
    anchor.className = 'anchor';
    anchor.href = '#' + heading.id;
    anchor.setAttribute('aria-label', heading.textContent.trim());
    anchor.textContent = '#';
    heading.prepend(anchor);
  }

  /* ------------------------------------------------------------- copy buttons */

  const fragmentLabel = (figure, index) => {
    const label = figure.querySelector('.fragment-label');
    return label ? label.textContent.trim() : `code ${index + 1}`;
  };

  document.querySelectorAll('.doc-prose figure.fragment').forEach((figure, index) => {
    const pre = figure.querySelector('pre');
    if (!pre) return;
    const head = figure.querySelector('.fragment-label') || figure;
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'copy-button';
    button.textContent = body.dataset.copy || 'Copy';
    button.setAttribute('aria-label', `${body.dataset.copy || 'Copy'}: ${fragmentLabel(figure, index)}`);
    button.addEventListener('click', async () => {
      const text = pre.textContent;
      try {
        await navigator.clipboard.writeText(text);
      } catch {
        const field = document.createElement('textarea');
        field.value = text;
        field.setAttribute('readonly', '');
        field.className = 'sr-only';
        figure.append(field);
        field.select();
        document.execCommand('copy');
        field.remove();
      }
      button.textContent = body.dataset.copied || 'Copied';
      button.classList.add('is-copied');
      setTimeout(() => {
        button.textContent = body.dataset.copy || 'Copy';
        button.classList.remove('is-copied');
      }, 1600);
    });
    if (head === figure) {
      const bar = document.createElement('div');
      bar.className = 'fragment-label';
      bar.append(button);
      figure.prepend(bar);
    } else {
      head.append(button);
    }
  });

  /* --------------------------------------------------------------- this page */

  const tocLinks = [...document.querySelectorAll('.doc-toc a[href^="#"]')];
  const tocTargets = tocLinks
    .map(link => document.getElementById(decodeURIComponent(link.getAttribute('href').slice(1))))
    .filter(Boolean);

  if (tocTargets.length && 'IntersectionObserver' in window) {
    const mark = id => {
      for (const link of tocLinks) {
        const on = link.getAttribute('href') === '#' + id;
        link.classList.toggle('is-active', on);
        if (on) link.setAttribute('aria-current', 'location');
        else link.removeAttribute('aria-current');
      }
    };
    const visible = new Map();
    const observer = new IntersectionObserver(
      entries => {
        for (const entry of entries) {
          if (entry.isIntersecting) visible.set(entry.target.id, entry.boundingClientRect.top);
          else visible.delete(entry.target.id);
        }
        if (!visible.size) return;
        const [first] = [...visible.entries()].sort((a, b) => a[1] - b[1]);
        mark(first[0]);
      },
      { rootMargin: '-12% 0px -70% 0px' },
    );
    for (const target of tocTargets) observer.observe(target);
    mark(tocTargets[0].id);
  }

  /* ------------------------------------------------------------------ motion */

  // Stagger through a data attribute rather than an inline style: the CSP
  // forbids style attributes, and this keeps the page free of them too.
  const reveal = elements => {
    const list = elements.filter(Boolean);
    list.forEach((element, index) => {
      element.classList.add('reveal');
      element.dataset.reveal = String(index);
    });
    if (reduced.matches) {
      for (const element of list) element.classList.add('is-in');
      return;
    }
    requestAnimationFrame(() => {
      for (const element of list) element.classList.add('is-in');
    });
  };

  reveal(['.doc-crumbs', '.doc-article h1'].map(selector => document.querySelector(selector)));
  reveal([...document.querySelectorAll('.doc-aside, .doc-rail')]);

  const blocks = [...document.querySelectorAll('.doc-prose > *, .doc-pager, .doc-edit')];
  if ('IntersectionObserver' in window && !reduced.matches) {
    for (const block of blocks) block.classList.add('reveal');
    const entrance = new IntersectionObserver(
      entries => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          entry.target.classList.add('is-in');
          entrance.unobserve(entry.target);
        }
      },
      { rootMargin: '0px 0px -8% 0px' },
    );
    for (const block of blocks) entrance.observe(block);
  }

  /* ------------------------------------------------------------------ search */

  const dialog = document.querySelector('[data-search]');
  const openButton = document.querySelector('[data-search-open]');
  const input = dialog && dialog.querySelector('[data-search-input]');
  const results = dialog && dialog.querySelector('[data-search-results]');
  const hint = dialog && dialog.querySelector('[data-search-hint]');
  const empty = dialog && dialog.querySelector('[data-search-empty]');
  const status = dialog && dialog.querySelector('[data-search-status]');
  const closeButton = dialog && dialog.querySelector('[data-search-close]');
  let index = null;
  let loading = null;

  const loadIndex = () => {
    if (index) return Promise.resolve(index);
    if (loading) return loading;
    const url = body.dataset.searchIndex;
    loading = fetch(url, { credentials: 'same-origin' })
      .then(response => (response.ok ? response.json() : Promise.reject(new Error(String(response.status)))))
      .then(data => {
        index = [];
        for (const chapter of data.chapters) {
          for (const section of chapter.sections) {
            index.push({
              chapter: chapter.title,
              section: section.title,
              url: chapter.url,
              id: section.id,
              text: section.text,
              haystack: `${chapter.title} ${section.title} ${section.text}`.toLowerCase(),
            });
          }
        }
        return index;
      })
      .catch(() => {
        loading = null;
        return null;
      });
    return loading;
  };

  const snippet = (text, terms) => {
    const plain = text.replace(/\s+/g, ' ').trim();
    if (!plain) return '';
    const at = terms.map(term => plain.toLowerCase().indexOf(term)).filter(position => position >= 0);
    const start = Math.max(0, (at.length ? Math.min(...at) : 0) - 40);
    const cut = plain.slice(start, start + 170);
    return (start > 0 ? '…' : '') + cut + (start + 170 < plain.length ? '…' : '');
  };

  const search = query => {
    const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
    if (!terms.length) return [];
    return index
      .map(entry => {
        let score = 0;
        for (const term of terms) {
          if (!entry.haystack.includes(term)) return null;
          if (entry.chapter.toLowerCase().includes(term)) score += 4;
          if (entry.section.toLowerCase().includes(term)) score += 3;
          score += 1;
        }
        return { entry, score };
      })
      .filter(Boolean)
      .sort((a, b) => b.score - a.score)
      .slice(0, 12)
      .map(hit => hit.entry);
  };

  const renderResults = (query, hits) => {
    results.textContent = '';
    const hasQuery = Boolean(query.trim());
    if (hint) hint.hidden = hasQuery;
    if (empty) empty.hidden = !hasQuery || hits.length > 0;
    if (status) status.textContent = hasQuery ? `${hits.length} · ${query}` : '';
    const guideRoot = dialog.dataset.guideRoot || './';
    for (const hit of hits) {
      const item = document.createElement('li');
      const link = document.createElement('a');
      link.className = 'doc-search-result';
      link.href = guideRoot + hit.url + (hit.id ? '#' + hit.id : '');
      const head = document.createElement('div');
      head.className = 'result-head';
      const title = document.createElement('strong');
      title.textContent = hit.chapter;
      head.append(title);
      if (hit.section) {
        const label = document.createElement('span');
        label.textContent = hit.section;
        head.append(label);
      }
      const text = document.createElement('p');
      text.textContent = snippet(hit.text, query.toLowerCase().split(/\s+/).filter(Boolean));
      link.append(head, text);
      item.append(link);
      results.append(item);
    }
  };

  const openSearch = async () => {
    if (!dialog) return;
    if (typeof dialog.showModal === 'function') dialog.showModal();
    else dialog.setAttribute('open', '');
    input.value = '';
    renderResults('', []);
    input.focus();
    const loaded = await loadIndex();
    if (!loaded && hint) {
      hint.textContent = empty ? empty.textContent : hint.textContent;
      hint.hidden = false;
    }
  };

  const closeSearch = () => {
    if (!dialog) return;
    if (typeof dialog.close === 'function') dialog.close();
    else dialog.removeAttribute('open');
  };

  if (dialog && openButton && input && results) {
    openButton.hidden = false;
    openButton.addEventListener('click', openSearch);
    closeButton.addEventListener('click', closeSearch);
    dialog.addEventListener('click', event => {
      if (event.target === dialog) closeSearch();
    });
    let timer = 0;
    input.addEventListener('input', () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        const query = input.value;
        if (!index) return;
        renderResults(query, search(query));
      }, 90);
    });
    input.addEventListener('keydown', event => {
      if (event.key === 'Escape') {
        event.preventDefault();
        closeSearch();
        return;
      }
      if (event.key !== 'ArrowDown') return;
      const first = results.querySelector('a');
      if (!first) return;
      event.preventDefault();
      first.focus();
    });
    results.addEventListener('keydown', event => {
      const links = [...results.querySelectorAll('a')];
      const current = links.indexOf(document.activeElement);
      if (event.key === 'ArrowDown' && current > -1 && current + 1 < links.length) {
        event.preventDefault();
        links[current + 1].focus();
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        if (current > 0) links[current - 1].focus();
        else input.focus();
      }
    });
    document.addEventListener('keydown', event => {
      if (event.key === '/' && !/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName)) {
        event.preventDefault();
        openSearch();
      }
    });
  }

  /* ---------------------------------------------------------- language menu */

  const langMenu = document.querySelector('[data-lang-menu]');
  if (langMenu) {
    document.addEventListener('click', event => {
      if (langMenu.open && !langMenu.contains(event.target)) langMenu.open = false;
    });
    document.addEventListener('keydown', event => {
      if (event.key === 'Escape' && langMenu.open) {
        langMenu.open = false;
        langMenu.querySelector('summary').focus();
      }
    });
  }
})();
