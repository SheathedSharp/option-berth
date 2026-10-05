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
    await page.locator('#download').scrollIntoViewIfNeeded();
    await expect(page.locator('#download .primary')).toHaveAttribute('href', 'https://github.com/SheathedSharp/option-berth/releases/latest');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    for (const image of await page.locator('main img:visible').all()) {
      await image.scrollIntoViewIfNeeded();
      await expect(image).toHaveJSProperty('complete', true);
      expect(await image.evaluate(img => img.naturalWidth)).toBeGreaterThan(0);
      if (await image.evaluate(el => Boolean(el.closest('[aria-hidden="true"]')))) expect(await image.getAttribute('alt')).toBe('');
      else expect(await image.getAttribute('alt')).toBeTruthy();
    }
    await page.evaluate(() => scrollTo(0, 0));
    if (width === 1440 || width === 390) {
      await mkdir('test-results/previews', {recursive: true});
      // Application assertions retain strict CSP and require zero console errors.
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

test('keyboard gallery, modal close and focus restoration', async ({page}) => {
  await page.goto('./');
  const first = page.getByRole('tab', {name: '服务', exact: true});
  await first.focus();
  await page.keyboard.press('ArrowRight');
  const git = page.getByRole('tab', {name: 'Git', exact: true});
  await expect(git).toBeFocused();
  await expect(git).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#panel-services')).toBeHidden();
  await expect(page.locator('#panel-git')).toBeVisible();
  const link = page.locator('#panel-git a');
  await link.click();
  await expect(page.locator('dialog')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('dialog')).toBeHidden();
  await expect(link).toBeFocused();
  await link.click();
  await page.locator('dialog button').click();
  await expect(page.locator('dialog')).toBeHidden();
});

test('real static language navigation and accessible download disclosure', async ({page}) => {
  await page.goto('./');
  await page.locator('.language').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('h1')).toContainText('Every worktree.');
  await page.locator('#download summary').click();
  await expect(page.locator('#download details')).toHaveAttribute('open', '');
  await expect(page.locator('#download details p')).toContainText('notarization');
  await page.locator('.language').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN');
});

test('reduced motion on first load and live preference change', async ({page}) => {
  await page.emulateMedia({reducedMotion: 'reduce'});
  await page.goto('./');
  await expect(page.locator('html')).not.toHaveClass(/motion/);
  expect(await page.locator('.hero-emblem img').evaluate(el => getComputedStyle(el).animationName)).toBe('none');
  await page.emulateMedia({reducedMotion: 'no-preference'});
  await expect(page.locator('html')).toHaveClass(/motion/);
  await page.emulateMedia({reducedMotion: 'reduce'});
  await expect(page.locator('html')).not.toHaveClass(/motion/);
  expect(await page.locator('.reveal').first().evaluate(el => getComputedStyle(el).opacity)).toBe('1');
});

test('no-JavaScript page keeps copy, language, image and download links', async ({browser, baseURL}) => {
  const context = await browser.newContext({javaScriptEnabled: false});
  const page = await context.newPage();
  await page.goto(baseURL);
  await expect(page.locator('h1')).toBeVisible();
  await expect(page.locator('.gallery-controls')).toBeHidden();
  await expect(page.locator('#panel-services img')).toBeVisible();
  await page.locator('#download').scrollIntoViewIfNeeded();
  await expect(page.locator('#download .primary')).toBeVisible();
  await page.locator('.language').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await context.close();
});
