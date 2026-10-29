import { test, expect } from '@playwright/test';
import { mkdir } from 'node:fs/promises';

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
  await page.locator('.language').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await context.close();
});
