'use strict';
(() => {
  // Progressive enhancement only. Every control below starts hidden in the
  // markup and is revealed here, so a page with no script shows the default
  // state and no dead buttons.

  // Command filter: keep every row in the DOM, hide the ones that do not match.
  const input = document.getElementById('command-filter');
  const list = document.getElementById('command-list');
  const empty = document.getElementById('command-empty');
  const status = document.getElementById('command-status');
  const search = document.querySelector('[data-command-search]');
  if (input && list && empty && status) {
    if (search) search.hidden = false;
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
    button.hidden = false;
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

  /* ------------------------------------------------------------ workbench */
  // The Human view is rendered in the markup for every worked state. This
  // table only supplies the compact --json shape, so the two views describe
  // the same fixture and neither one has to fetch anything.

  const worktrees = [
    { key: 'main', identity: 'wt_018a9f2c4b10', branch: 'main', root: '~/src/hello/main', api: 21730, web: 21731, gateway: 21732, otherBranch: 'feat/billing' },
    { key: 'billing', identity: 'wt_94c2d07e1a88', branch: 'feat/billing', root: '~/src/hello/billing', api: 21740, web: 21741, gateway: 21742, otherBranch: 'main' },
  ];
  const reports = {
    healthy: wt => ({
      health: 'healthy',
      services: [
        { name: 'api', state: 'ready', running: true, ready: true, port_actual: wt.api, health: { status: 'ok', code: 200 } },
        { name: 'web', state: 'ready', running: true, ready: true, port_actual: wt.web, health: { status: 'ok', code: 200 } },
        { name: 'worker', state: 'ready', running: true, ready: true },
      ],
      machine: [{ name: 'postgres', port: 5432, listening: true, listening_known: true }],
      diagnosis: { healthy: true, summary: 'all declared services ready', root_causes: [] },
      attention: { mode: 'assist', notes: [] },
    }),
    cascade: wt => ({
      health: 'broken',
      services: [
        { name: 'auth', state: 'crashed', running: false, ready: false, last_exit: { reason: 'crashed', code: 1, at: '12:04:17Z' } },
        { name: 'api', state: 'blocked_by_dep', running: false, ready: false, port_declared: wt.api, blocked_by: ['auth'] },
        { name: 'gateway', state: 'blocked_by_dep', running: false, ready: false, port_declared: wt.gateway, blocked_by: ['api'] },
        { name: 'web', state: 'ready', running: true, ready: true, port_actual: wt.web, health: { status: 'ok', code: 200 } },
      ],
      diagnosis: {
        healthy: false,
        summary: '1 root cause (auth) blocking 2 downstream services',
        root_causes: [{
          id: 'rc_8f31a0c4e912d0ab',
          service: 'auth',
          kind: 'service_crashed',
          severity: 'critical',
          summary: 'auth crashed with exit code 1',
          impacted: ['api', 'gateway'],
          first_error: 'connection refused: postgres:5432 (redacted)',
          command: 'oberth logs auth --once --run current --first-error',
        }],
        next_actions: [{
          priority: 1,
          safety: 'read_only',
          service: 'auth',
          command: 'oberth logs auth --once --run current --first-error',
        }],
      },
      attention: { mode: 'assist', notes: [{ state: 'triaged', urgency: 0.92, action: 'inspect_logs', target: 'auth' }] },
    }),
    port: wt => ({
      health: 'broken',
      services: [
        {
          name: 'billing',
          state: 'port_conflict',
          running: false,
          ready: false,
          port_declared: 8080,
          port_holder: { port: 8080, pid: 44120, kind: 'oberth_run', group: `hello@${wt.otherBranch}` },
        },
        { name: 'api', state: 'ready', running: true, ready: true, port_actual: wt.api, health: { status: 'ok', code: 200 } },
        { name: 'web', state: 'ready', running: true, ready: true, port_actual: wt.web, health: { status: 'ok', code: 200 } },
      ],
      diagnosis: {
        healthy: false,
        summary: 'billing port 8080 occupied by another worktree run',
        root_causes: [{
          id: 'rc_41c0d9ab72e108fa',
          service: 'billing',
          kind: 'port_occupied',
          severity: 'high',
          summary: `port 8080 held by ${wt.otherBranch} (pid 44120)`,
          command: 'oberth status --only billing',
        }],
        next_actions: [{
          priority: 1,
          safety: 'human_decision',
          service: 'billing',
          command: 'oberth status --only billing',
        }],
      },
      attention: { mode: 'strict', notes: [{ state: 'open', urgency: 0.64, action: 'inspect_port', target: 'billing' }] },
    }),
    drift: wt => ({
      health: 'drifted',
      services: [
        {
          name: 'web',
          state: 'ready',
          running: true,
          ready: true,
          port_actual: wt.web,
          drifts: [{ kind: 'code_stale', severity: 'warning', summary: '3 source files changed since start', command: 'oberth restart --only web' }],
        },
        { name: 'api', state: 'ready', running: true, ready: true, port_actual: wt.api, health: { status: 'ok', code: 200 } },
        { name: 'worker', state: 'ready', running: true, ready: true },
      ],
      machine: [{ name: 'postgres', port: 5432, listening: true, listening_known: true }],
      diagnosis: {
        healthy: false,
        summary: 'web is running stale code (3 files changed)',
        root_causes: [{
          id: 'rc_77e4b2c1a0f831de',
          service: 'web',
          kind: 'code_stale',
          severity: 'warning',
          summary: '3 files changed since 12:01:08',
          command: 'oberth restart --only web',
        }],
        next_actions: [{
          priority: 2,
          safety: 'local_restart',
          service: 'web',
          command: 'oberth restart --only web',
        }],
      },
      attention: {
        mode: 'assist',
        notes: [{ state: 'triaged', urgency: 0.81, action: 'restart_service', target: 'web', command: 'oberth restart --only web' }],
      },
    }),
  };

  const workbench = document.querySelector('[data-workbench]');
  if (workbench) {
    const controls = workbench.querySelector('[data-wb-controls]');
    const staticLine = workbench.querySelector('[data-wb-static]');
    const human = workbench.querySelector('[data-wb-human]');
    const json = workbench.querySelector('[data-wb-json]');
    const jsonCode = workbench.querySelector('[data-wb-json-code]');
    const jsonBranch = workbench.querySelector('[data-wb-json-branch]');
    const live = workbench.querySelector('[data-wb-status]');
    const wtButtons = [...workbench.querySelectorAll('[data-wt]')];
    const scenarioButtons = [...workbench.querySelectorAll('[data-scenario]')];
    const viewButtons = [...workbench.querySelectorAll('[data-view]')];
    const wtPanels = [...workbench.querySelectorAll('[data-wt-panel]')];
    let wt = 0;
    let scenario = 'healthy';
    let view = 'human';

    const reportFor = (index, name) => {
      const tree = worktrees[index];
      return {
        schema: 'oberth.runtime.world/v1',
        worktree: { identity: tree.identity, group: 'hello-worktree', branch: tree.branch, root: tree.root, manifest_present: true },
        coverage: 'observable',
        ...reports[name](tree),
      };
    };

    const render = () => {
      const tree = worktrees[wt];
      for (const button of wtButtons) {
        button.setAttribute('aria-pressed', String(Number(button.dataset.wt) === wt));
      }
      for (const button of scenarioButtons) {
        button.setAttribute('aria-pressed', String(button.dataset.scenario === scenario));
      }
      for (const button of viewButtons) {
        button.setAttribute('aria-pressed', String(button.dataset.view === view));
      }
      for (const panel of wtPanels) {
        const active = Number(panel.dataset.wtPanel) === wt;
        panel.hidden = !active;
        if (active) {
          for (const state of panel.querySelectorAll('[data-scenario-panel]')) {
            state.hidden = state.dataset.scenarioPanel !== scenario;
          }
        }
      }
      human.hidden = view !== 'human';
      json.hidden = view !== 'json';
      if (jsonBranch) jsonBranch.textContent = tree.branch;
      if (jsonCode) jsonCode.textContent = JSON.stringify(reportFor(wt, scenario), null, 2);
      if (live) live.textContent = `${tree.branch} · ${scenario} · ${view}`;
    };

    if (controls) controls.hidden = false;
    if (staticLine) staticLine.hidden = true;
    for (const button of wtButtons) {
      button.addEventListener('click', () => {
        wt = Number(button.dataset.wt);
        render();
      });
    }
    for (const button of scenarioButtons) {
      button.addEventListener('click', () => {
        scenario = button.dataset.scenario;
        render();
      });
    }
    for (const button of viewButtons) {
      button.addEventListener('click', () => {
        view = button.dataset.view;
        render();
      });
    }
    render();
  }
})();
