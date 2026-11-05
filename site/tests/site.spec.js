import { test, expect } from '@playwright/test';
import { mkdir } from 'node:fs/promises';
import { readFileSync } from 'node:fs';

const book = JSON.parse(readFileSync(new URL('../guide/book.json', import.meta.url), 'utf8'));
const guidePath = (locale, slug) => {
  const base = locale === 'en' ? 'guide/en/' : 'guide/';
  return slug === 'overview' ? base : `${base}${slug}/`;
};
const chapterPath = slug => (slug === 'overview' ? '' : `${slug}/`);

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
  await expect(page.locator('#proof-title')).toContainText('Done is the metric.');
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

test('guide: every chapter renders both languages without console errors', async ({page}) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
  for (const chapter of book.chapters) {
    for (const locale of ['zh', 'en']) {
      const response = await page.goto(guidePath(locale, chapter.slug));
      expect(response.status()).toBe(200);
      await expect(page.locator('h1')).toContainText(chapter[locale].title);
      await expect(page.locator('html')).toHaveAttribute('lang', locale === 'zh' ? 'zh-CN' : 'en');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
      await expect(page.locator('.page-toc a, .home-hero')).not.toHaveCount(0);
    }
  }
  expect(errors).toEqual([]);
});

test('guide: the chain explorer switches by pointer and keyboard', async ({page}) => {
  await page.goto('guide/');
  await expect(page.locator('.chain-panel')).toHaveCount(5);
  await expect(page.locator('.chain-panel.is-active')).toHaveCount(1);
  await page.locator('.chain-stop[data-station="manifest"]').click();
  await expect(page.locator('.chain-panel[data-panel="manifest"]')).toHaveClass(/is-active/);
  await expect(page.locator('.chain-stop[data-station="manifest"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.chain-panel[data-panel="worktree"]')).toBeHidden();
  await page.locator('.chain-stop[data-station="manifest"]').press('ArrowRight');
  await expect(page.locator('.chain-panel[data-panel="service"]')).toHaveClass(/is-active/);
});

test('guide: no JavaScript still shows every chain panel and the whole contents', async ({browser, baseURL}) => {
  const context = await browser.newContext({javaScriptEnabled: false});
  const page = await context.newPage();
  await page.goto(baseURL + 'guide/');
  await expect(page.locator('h1')).toBeVisible();
  await expect(page.locator('.chain-panel')).toHaveCount(5);
  for (const panel of await page.locator('.chain-panel').all()) await expect(panel).toBeVisible();
  await expect(page.locator('.contents-list a')).toHaveCount(book.chapters.length);
  await page.locator('.language').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('h1')).toHaveText('The option-berth guide');
  await context.close();
});

test('guide: each edition keeps its own copy', async ({page}) => {
  await page.goto('guide/');
  await expect(page.locator('#chain-worktree')).toHaveText('worktree');
  await expect(page.locator('.chain-panel[data-panel="worktree"] .chain-copy p')).toContainText('代码观察');
  await expect(page.locator('.guide-foot-cols h2').first()).toHaveText('指南');
  await page.goto('guide/en/');
  await expect(page.locator('#chain-worktree')).toHaveText('Worktree');
  await expect(page.locator('.chain-panel[data-panel="worktree"] .chain-copy p')).toContainText('Code observes');
  await expect(page.locator('.guide-foot-cols h2').first()).toHaveText('Guide');
});

test('guide: language switch reaches the counterpart chapter', async ({page}) => {
  await page.goto('guide/status/');
  await expect(page.locator('.language')).toHaveAttribute('href', '../en/status/');
  await expect(page.locator('.language')).toHaveText('EN');
  await page.locator('.language').click();
  await expect(page).toHaveURL(/\/guide\/en\/status\/$/);
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('.language')).toHaveText('中文');
  await page.locator('.language').click();
  await expect(page).toHaveURL(/\/guide\/status\/$/);
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN');
});

test('guide: machine entry points list every chapter', async ({request, baseURL}) => {
  const llms = await (await request.get(baseURL + 'llms.txt')).text();
  const full = await (await request.get(baseURL + 'llms-full.txt')).text();
  const sitemap = await (await request.get(baseURL + 'sitemap.xml')).text();
  const robots = await (await request.get(baseURL + 'robots.txt')).text();
  expect(llms.startsWith('# option-berth')).toBe(true);
  expect(robots).toContain('llms.txt');
  for (const chapter of book.chapters) {
    for (const locale of ['zh-CN', 'en']) {
      const prefix = locale === 'en' ? 'guide/en/' : 'guide/';
      const url = `https://oberth.zayju.de/${prefix}${chapterPath(chapter.slug)}`;
      expect(llms).toContain(`](${url})`);
      expect(sitemap).toContain(`<loc>${url}</loc>`);
    }
    expect(full).toContain(`URL: https://oberth.zayju.de/guide/en/${chapterPath(chapter.slug)}`);
    expect(full).toContain(chapter.en.title);
  }
  expect(llms).toContain('https://oberth.zayju.de/llms-full.txt');
  expect(full).toContain('oberth up --wait');
});

for (const [path, lang] of [['guide/', 'zh-CN'], ['guide/en/status/', 'en']]) for (const width of [360, 390, 768, 1440]) {
  test(`guide layout ${path} at ${width}`, async ({page}) => {
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
    await page.setViewportSize({width, height: 900});
    await page.emulateMedia({reducedMotion: 'reduce'});
    await page.goto(path);
    await expect(page.locator('h1')).toBeVisible();
    await expect(page.locator('html')).toHaveAttribute('lang', lang);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    if (path === 'guide/') {
      await expect(page.locator('.home-stage pre')).toBeVisible();
      await expect(page.locator('.home-rail-station')).toHaveCount(5);
      await page.locator('.contents-part').first().scrollIntoViewIfNeeded();
      await expect(page.locator('.contents-list a').first()).toBeVisible();
    } else {
      await expect(page.locator('.chapter-body pre').first()).toBeVisible();
      await expect(page.locator('.rail-station')).toHaveCount(5);
      await page.locator('.chapter-nav').scrollIntoViewIfNeeded();
      await expect(page.locator('.chapter-nav .chapter-link').first()).toBeVisible();
    }
    expect(errors).toEqual([]);
  });
}

test('homepage links to the guide in both languages', async ({page}) => {
  await page.goto('./');
  await expect(page.locator('.head-end a[href="guide/"]')).toHaveCount(1);
  await expect(page.locator('.site-foot a[href="guide/"]')).toHaveCount(1);
  await page.locator('.language').click();
  await expect(page.locator('.head-end a[href="../guide/en/"]')).toHaveCount(1);
  await expect(page.locator('.site-foot a[href="../guide/en/"]')).toHaveCount(1);
});
