import { test, expect } from '@playwright/test';
import { mkdir } from 'node:fs/promises';
import { readFileSync } from 'node:fs';

const book = JSON.parse(readFileSync(new URL('../guide/book.json', import.meta.url), 'utf8'));
const locales = book.locales.map(locale => locale.id);
// English is the default edition and lives at the guide root.
const chapterPath = slug => (slug === 'overview' ? '' : `${slug}/`);
const guidePath = (locale, slug) => {
  const localeHome = book.locales.find(item => item.id === locale).prefix;
  return `guide/${localeHome}${chapterPath(slug)}`;
};

/* ------------------------------------------------------------------ product page */

for (const locale of ['', 'en/']) for (const width of [360, 390, 768, 1440]) {
  test(`${locale || 'zh'} layout at ${width}`, async ({ page }, testInfo) => {
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
    await page.setViewportSize({width, height: 980});
    await page.emulateMedia({reducedMotion: 'reduce'});
    await page.goto(locale || './');
    await expect(page.locator('h1')).toBeVisible();
    await expect(page.locator('html')).toHaveAttribute('lang', locale ? 'en' : 'zh-CN');
    await page.locator('#install').scrollIntoViewIfNeeded();
    await expect(page.locator('#install h2')).toBeVisible();
    await expect(page.locator('#install a[href$="releases/latest"]')).toBeVisible();
    await expect(page.locator('#install a[href$="docs/install.md"]')).toBeVisible();
    await expect(page.locator('.install-panel-release pre code')).toContainText('install.sh');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    for (const image of await page.locator('img:visible').all()) {
      await image.scrollIntoViewIfNeeded();
      await expect.poll(() => image.evaluate(img => img.complete && img.naturalWidth > 0), {
        message: `Image did not load: ${await image.getAttribute('src')}`, timeout: 10000
      }).toBe(true);
      expect(await image.getAttribute('alt')).not.toBeNull();
    }
    await page.evaluate(() => scrollTo(0, 0));
    if (width === 1440 || width === 390) {
      await mkdir('test-results/previews', {recursive: true});
      // Page assertions retain strict CSP and require zero console errors.
      expect(errors).toEqual([]);
      // Playwright 1.63 WebKit unconditionally inserts "body {}" to synchronize
      // animation state. Track that exact capture-only mutation; do not relax
      // CSP or filter unrelated page errors to make the screenshot pass.
      await page.evaluate(() => {
        window.captureStyles = [];
        window.captureObserver = new MutationObserver(records => {
          for (const record of records) for (const node of record.addedNodes)
            if (node.nodeName === 'STYLE') window.captureStyles.push(node.textContent);
        });
        window.captureObserver.observe(document.head, {childList: true});
      });
      await page.screenshot({path: `test-results/previews/${testInfo.project.name}-${locale ? 'en' : 'zh'}-${width}.png`, fullPage: true, caret: 'initial'});
      const injected = await page.evaluate(() => { window.captureObserver.disconnect(); return window.captureStyles; });
      if (testInfo.project.name === 'webkit') {
        expect(injected).toEqual(['body {}']);
        expect(errors).toEqual(["Refused to apply a stylesheet because its hash, its nonce, or 'unsafe-inline' does not appear in the style-src directive of the Content Security Policy."]);
      } else {
        expect(injected).toEqual([]);
        expect(errors).toEqual([]);
      }
    } else expect(errors).toEqual([]);
  });
}

test('command filter, empty state and Escape reset', async ({page}) => {
  await page.goto('./');
  const input = page.locator('#command-filter');
  const rows = page.locator('#command-list li');
  await expect(rows).toHaveCount(6);
  await input.fill('logs');
  await expect(rows).toHaveCount(6);
  await expect(page.locator('#command-list li:visible')).toHaveCount(1);
  await expect(page.locator('#command-list li:visible')).toContainText('oberth logs');
  await expect(page.locator('#command-status')).toContainText('1');
  await input.fill('zzz');
  await expect(page.locator('#command-list li:visible')).toHaveCount(0);
  await expect(page.locator('#command-empty')).toBeVisible();
  await input.press('Escape');
  await expect(input).toHaveValue('');
  await expect(page.locator('#command-list li:visible')).toHaveCount(6);
  await expect(page.locator('#command-empty')).toBeHidden();
});

test('measured proof is present in both locales', async ({page}) => {
  await page.goto('./');
  await expect(page.locator('#proof-title')).toBeVisible();
  await expect(page.locator('.proof-table')).toContainText('40/40');
  await expect(page.locator('.proof-table')).toContainText('29/40');
  await page.locator('.language').click();
  await expect(page.locator('#proof-title')).toContainText('What the measured runs show');
  await expect(page.locator('.proof-table')).toContainText('40/40');
});

test('real static language navigation and product links', async ({page}) => {
  await page.goto('./');
  await expect(page.locator('.close-products a')).toHaveCount(3);
  await page.locator('.language').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('h1')).toContainText('Stop guessing');
  await expect(page.locator('.close-products a[href$="option-berth-macos"]')).toHaveCount(1);
  await page.locator('.language').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN');
});

test('reduced motion on first load and live preference change', async ({page}) => {
  await page.emulateMedia({reducedMotion: 'reduce'});
  await page.goto('./');
  await expect(page.locator('html')).not.toHaveClass(/motion/);
  expect(await page.locator('.hero-line .line').first().evaluate(el => getComputedStyle(el).animationName)).toBe('none');
  await page.emulateMedia({reducedMotion: 'no-preference'});
  await expect(page.locator('html')).toHaveClass(/motion/);
  await page.emulateMedia({reducedMotion: 'reduce'});
  await expect(page.locator('html')).not.toHaveClass(/motion/);
  expect(await page.locator('.reveal').first().evaluate(el => getComputedStyle(el).opacity)).toBe('1');
});

test('no-JavaScript page keeps copy, language, commands and release link', async ({browser, baseURL}) => {
  const context = await browser.newContext({javaScriptEnabled: false});
  const page = await context.newPage();
  await page.goto(baseURL);
  await expect(page.locator('h1')).toBeVisible();
  await expect(page.locator('#command-list li')).toHaveCount(6);
  await expect(page.locator('#command-list li:visible')).toHaveCount(6);
  await expect(page.locator('#command-empty')).toBeHidden();
  await page.locator('#install').scrollIntoViewIfNeeded();
  await expect(page.locator('#install a[href$="releases/latest"]')).toBeVisible();
  await expect(page.locator('#install a[href$="docs/install.md"]')).toBeVisible();
  await expect(page.locator('.install-panel-release pre code')).toContainText('install.sh');
  await page.locator('.language').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await context.close();
});

test('homepage links to the English and Chinese guides', async ({page}) => {
  await page.goto('./');
  await expect(page.locator('.head-end a[href="guide/zh/"]')).toHaveCount(1);
  await expect(page.locator('.site-foot a[href="guide/zh/"]')).toHaveCount(1);
  await page.locator('.language').click();
  await expect(page.locator('.head-end a[href="../guide/"]')).toHaveCount(1);
  await expect(page.locator('.site-foot a[href="../guide/"]')).toHaveCount(1);
});

/* --------------------------------------------------------------------- guide */

test('guide: every chapter renders in every locale without console errors', async ({page}) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
  for (const chapter of book.chapters) {
    for (const locale of book.locales) {
      const response = await page.goto(guidePath(locale.id, chapter.slug));
      expect(response.status()).toBe(200);
      await expect(page.locator('h1')).toHaveText(chapter.locales[locale.id].title);
      await expect(page.locator('html')).toHaveAttribute('lang', locale.lang);
      await expect(page.locator('.doc-prose h2').first()).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    }
  }
  expect(errors).toEqual([]);
});

test('guide: the rail is the whole book, with the current chapter marked', async ({page}) => {
  await page.goto(guidePath('en', 'status'));
  await expect(page.locator('.doc-rail .rail-part')).toHaveCount(book.parts.length);
  await expect(page.locator('.doc-rail .rail-link')).toHaveCount(book.chapters.length);
  await expect(page.locator('.doc-rail a[aria-current="page"]')).toHaveCount(1);
  await expect(page.locator('.doc-rail a[aria-current="page"]')).toHaveText('status');
  await expect(page.locator('.doc-crumbs')).toContainText('The core loop');
  await page.locator('.doc-rail a[href$="quickstart/"]').click();
  await expect(page).toHaveURL(/\/guide\/quickstart\/$/);
  await expect(page.locator('.doc-rail a[aria-current="page"]')).toHaveText('Quickstart');
});

test('guide: previous and next follow the book order', async ({page}) => {
  await page.goto(guidePath('en', 'logs'));
  const index = book.chapters.findIndex(chapter => chapter.slug === 'logs');
  const previous = book.chapters[index - 1];
  const next = book.chapters[index + 1];
  await expect(page.locator('.pager-prev strong')).toHaveText(previous.locales.en.title);
  await expect(page.locator('.pager-next strong')).toHaveText(next.locales.en.title);
  await page.locator('.pager-next').click();
  await expect(page).toHaveURL(new RegExp(`/guide/${next.slug}/$`));
});

test('guide: language menu reaches the counterpart chapter', async ({page}) => {
  await page.goto(guidePath('en', 'status'));
  await page.locator('[data-lang-menu] summary').click();
  const chinese = page.locator('[data-lang-menu] a[lang="zh-CN"]');
  await expect(chinese).toHaveAttribute('href', '../zh/status/');
  await chinese.click();
  await expect(page).toHaveURL(/\/guide\/zh\/status\/$/);
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN');
  await page.locator('[data-lang-menu] summary').click();
  await page.locator('[data-lang-menu] a[lang="en"]').click();
  await expect(page).toHaveURL(/\/guide\/status\/$/);
});

test('guide: the theme follows the system until the reader chooses', async ({page}) => {
  await page.emulateMedia({colorScheme: 'dark'});
  await page.goto(guidePath('en', 'status'));
  await expect(page.locator('html')).not.toHaveAttribute('data-theme', 'light');
  const dark = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  await page.locator('[data-theme-toggle]').click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  const light = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  expect(light).not.toBe(dark);
  await expect(page.locator('[data-theme-toggle]')).toHaveAttribute('aria-label', /dark/i);
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).toBe(light);
});

test('guide: search finds a section of another chapter and navigates to it', async ({page}) => {
  await page.goto(guidePath('en', 'install'));
  await page.locator('[data-search-open]').click();
  const dialog = page.locator('[data-search]');
  await expect(dialog).toBeVisible();
  await page.locator('[data-search-input]').fill('observation');
  const results = dialog.locator('.doc-search-result');
  await expect(results.first()).toBeVisible();
  const hrefs = await results.evaluateAll(nodes => nodes.map(node => node.getAttribute('href')));
  expect(hrefs.some(href => href.includes('status/#blind'))).toBe(true);
  const target = dialog.locator('.doc-search-result[href*="status/#blind"]');
  await target.click();
  await expect(page).toHaveURL(/\/guide\/status\/#blind$/);
  await expect(page.locator('#blind')).toBeVisible();
});

test('guide: search closes on Escape and is not offered without a query', async ({page}) => {
  await page.goto(guidePath('en', 'status'));
  await page.keyboard.press('/');
  await expect(page.locator('[data-search]')).toBeVisible();
  await expect(page.locator('[data-search-hint]')).toBeVisible();
  await expect(page.locator('[data-search-empty]')).toBeHidden();
  await page.locator('[data-search-input]').fill('zzzz');
  await expect(page.locator('[data-search-empty]')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('[data-search]')).toBeHidden();
});

test('guide: every code block offers a copy button', async ({page, context, browserName}) => {
  // WebKit in Playwright has no clipboard permission to grant; there the copy
  // falls back to execCommand, so only the button's own feedback is asserted.
  if (browserName === 'chromium') await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto(guidePath('en', 'quickstart'));
  const fragments = page.locator('.doc-prose figure.fragment');
  const button = fragments.first().locator('.copy-button');
  await expect(button).toBeVisible();
  await expect(page.locator('.doc-prose .copy-button')).toHaveCount(await fragments.count());
  await button.click();
  await expect(button).toHaveText(book.labels.en.copied);
  if (browserName === 'chromium') {
    expect(await page.evaluate(() => navigator.clipboard.readText())).toContain('git clone');
  }
});

test('guide: no JavaScript still reads and navigates', async ({browser, baseURL}) => {
  const context = await browser.newContext({javaScriptEnabled: false});
  const page = await context.newPage();
  await page.goto(baseURL + guidePath('en', 'status'));
  await expect(page.locator('h1')).toBeVisible();
  await expect(page.locator('.doc-rail .rail-link')).toHaveCount(book.chapters.length);
  await expect(page.locator('.doc-prose pre').first()).toBeVisible();
  await expect(page.locator('[data-search-open]')).toBeHidden();
  await expect(page.locator('[data-theme-toggle]')).toBeHidden();
  await expect(page.locator('[data-rail-toggle]')).toBeHidden();
  await page.locator('[data-lang-menu] summary').click();
  await page.locator('[data-lang-menu] a[lang="zh-CN"]').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN');
  await expect(page.locator('h1')).toHaveText(book.chapters.find(c => c.slug === 'status').locales.zh.title);
  await context.close();
});

test('guide: machine entry points list every chapter in every locale', async ({request, baseURL}) => {
  const llms = await (await request.get(baseURL + 'llms.txt')).text();
  const full = await (await request.get(baseURL + 'llms-full.txt')).text();
  const sitemap = await (await request.get(baseURL + 'sitemap.xml')).text();
  const robots = await (await request.get(baseURL + 'robots.txt')).text();
  expect(llms.startsWith('# option-berth')).toBe(true);
  expect(robots).toContain('llms.txt');
  for (const chapter of book.chapters) {
    for (const locale of book.locales) {
      const url = `https://oberth.zayju.de/${guidePath(locale.id, chapter.slug)}`;
      expect(llms).toContain(`](${url})`);
      expect(sitemap).toContain(`<loc>${url}</loc>`);
    }
    expect(full).toContain(`URL: https://oberth.zayju.de/guide/${chapterPath(chapter.slug)}`);
    expect(full).toContain(chapter.locales.en.title);
  }
  expect(llms).toContain('https://oberth.zayju.de/llms-full.txt');
  expect(full).toContain('oberth up --wait');
});

test('guide: the first English URLs still forward to the default edition', async ({page}) => {
  await page.goto('guide/en/status/');
  await expect(page).toHaveURL(/\/guide\/status\/$/);
  await page.goto('guide/en/');
  await expect(page).toHaveURL(/\/guide\/$/);
});

test('assets are versioned so a cache cannot pair old CSS with new markup', async ({page, request, baseURL}) => {
  for (const path of ['', 'en/', 'guide/', 'guide/zh/status/']) {
    await page.goto(path);
    const refs = await page.evaluate(() => [
      ...[...document.querySelectorAll('link[rel="stylesheet"], script[src], img[src]')]
        .map(element => element.getAttribute('href') || element.getAttribute('src')),
    ].filter(href => href && !/^(https?:)?\/\//.test(href)));
    expect(refs.length).toBeGreaterThan(0);
    for (const href of refs) expect(href, `${path} → ${href}`).toMatch(/\?v=[0-9a-f]{8}$/);
  }
  const css = await request.get(baseURL + 'assets/guide.css');
  expect(css.status()).toBe(200);
  expect(await css.text()).toContain('.doc-shell');
});

test('guide: the search index is versioned and covers every chapter', async ({request, baseURL}) => {
  const page = await request.get(baseURL + guidePath('en', 'install'));
  const index = (await page.text()).match(/data-search-index="([^"]+)"/)[1];
  expect(index).toMatch(/^(\.\.\/)+search-en\.json\?v=[0-9a-f]{8}$/);
  const versioned = index.split('/').pop();
  const data = await (await request.get(baseURL + 'guide/' + versioned)).json();
  expect(data.chapters.map(chapter => chapter.slug)).toEqual(book.chapters.map(chapter => chapter.slug));
});

for (const path of ['guide/', 'guide/status/', 'guide/zh/', 'guide/zh/quickstart/']) for (const width of [360, 390, 768, 1440]) {
  test(`guide layout ${path} at ${width}`, async ({page}) => {
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
    await page.setViewportSize({width, height: 900});
    await page.emulateMedia({reducedMotion: 'reduce'});
    await page.goto(path);
    await expect(page.locator('h1')).toBeVisible();
    await expect(page.locator('.doc-prose > *').first()).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    await page.locator('.doc-pager').scrollIntoViewIfNeeded();
    await expect(page.locator('.doc-pager .pager-link').first()).toBeVisible();
    expect(errors).toEqual([]);
  });
}

test('guide: the rail opens as a drawer on a phone', async ({page}) => {
  await page.setViewportSize({width: 390, height: 820});
  await page.goto(guidePath('en', 'status'));
  const toggle = page.locator('[data-rail-toggle]');
  await expect(toggle).toBeVisible();
  await expect(page.locator('[data-rail]')).toBeHidden();
  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('[data-rail]')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('[data-rail]')).toBeHidden();
});
