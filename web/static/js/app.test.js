const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const jsPath = path.join(__dirname, 'app.js');
const htmlPath = path.join(__dirname, '..', '..', 'index.html');
const appSource = fs.readFileSync(jsPath, 'utf8');
const indexHTML = fs.readFileSync(htmlPath, 'utf8');

function extractFunctionSource(name) {
  const asyncStart = appSource.indexOf(`async function ${name}`);
  const plainStart = appSource.indexOf(`function ${name}`);
  const start = asyncStart !== -1 ? asyncStart : plainStart;
  if (start === -1) {
    throw new Error(`function ${name} not found`);
  }
  let bodyStart = -1;
  let parenDepth = 0;
  let sawParams = false;
  for (let i = start; i < appSource.length; i++) {
    const ch = appSource[i];
    if (ch === '(') {
      parenDepth++;
      sawParams = true;
      continue;
    }
    if (ch === ')') {
      parenDepth--;
      continue;
    }
    if (ch === '{' && sawParams && parenDepth === 0) {
      bodyStart = i;
      break;
    }
  }
  if (bodyStart === -1) {
    throw new Error(`function ${name} has no body`);
  }
  let braceDepth = 0;
  for (let i = bodyStart; i < appSource.length; i++) {
    const ch = appSource[i];
    if (ch === '{') {
      braceDepth++;
    } else if (ch === '}') {
      braceDepth--;
      if (braceDepth === 0) {
        return appSource.slice(start, i + 1);
      }
    }
  }
  throw new Error(`unterminated function ${name}`);
}

const functionBundle = [
  extractFunctionSource('currentLibraryCount'),
  extractFunctionSource('buildHomeDashboardMarkup'),
  extractFunctionSource('getLibraryImportFormValues'),
  extractFunctionSource('sanitizeLibraryImportValues'),
  extractFunctionSource('validateLibraryImportSettings'),
  extractFunctionSource('libraryImportSummaryLines'),
  extractFunctionSource('applyLibraryImportLoadedState'),
  extractFunctionSource('setLibraryImportStep2State'),
  extractFunctionSource('renderLibraryScanWorkspace'),
  extractFunctionSource('renderLibraryScanReady'),
  extractFunctionSource('renderLibraryScanProgress'),
  extractFunctionSource('renderLibraryScanProgressMetric'),
  extractFunctionSource('renderLibraryScanError'),
  extractFunctionSource('renderLibraryScanReview'),
  extractFunctionSource('renderLibraryScanTotals'),
  extractFunctionSource('renderLibraryScanToolbar'),
  extractFunctionSource('renderLibraryScanSection'),
  extractFunctionSource('renderLibraryScanCandidate'),
  extractFunctionSource('filterLibraryScanCandidates'),
  extractFunctionSource('groupLibraryScanCandidates'),
  extractFunctionSource('formatLibraryScanPhase'),
  extractFunctionSource('formatLibraryScanElapsed'),
  extractFunctionSource('startLibraryScan'),
  extractFunctionSource('pollLibraryScanJob'),
  extractFunctionSource('loadLibraryScanResults'),
  extractFunctionSource('updateLibraryImportSaveState'),
  extractFunctionSource('saveLibraryImportSettings'),
].join('\n\n');

function libraryImportState(overrides = {}) {
  return {
    completed: false,
    dirty: false,
    lastSaved: null,
    flashTimer: null,
    scan: {
      running: false,
      jobId: '',
      pollTimer: null,
      startedAt: null,
      progress: null,
      result: null,
      filter: 'all',
      search: '',
      sections: {
        new: true,
        already_imported: false,
        duplicate: true,
        unsupported: false,
        unreadable: false,
      },
      error: '',
    },
    ...overrides,
  };
}

function createContext(overrides = {}) {
  const base = {
    t: key => key,
    renderOnboardingChecklist: () => '<section id="onboarding">onboarding</section>',
    renderCompactBookCard: (book, index) => `<article data-book="${index}">${book.title}</article>`,
    renderMetricCard: (label, value) => `<metric data-label="${label}">${value}</metric>`,
    renderCompactDownload: item => `<download>${item.title}</download>`,
    renderCompactWishlist: item => `<wishlist>${item.title}</wishlist>`,
    renderActivityRow: item => `<activity>${item.title}</activity>`,
    renderDashboardEmpty: () => '<empty />',
    escapeHtml: value => String(value),
    LIBRARY_IMPORT_FIELDS: ['incoming_dir', 'ebook_dir', 'audiobook_dir', 'manga_dir'],
    state: { libraryImport: libraryImportState() },
    document: { getElementById: () => null },
    apiJson: async () => ({ success: true }),
    showToast: () => {},
    scrollToSettingsSection: () => {},
    window: { setTimeout: fn => { fn(); return 1; }, clearTimeout: () => {} },
  };
  const context = vm.createContext({ ...base, ...overrides });
  vm.runInContext(functionBundle, context);
  return context;
}

test('currentLibraryCount uses normalized total_books', () => {
  const context = createContext();
  assert.equal(context.currentLibraryCount(true, { total_books: 0, total_items: 9 }), 0);
  assert.equal(context.currentLibraryCount(true, { total_books: 4, total_items: 0 }), 4);
});

test('currentLibraryCount uses legacy total_items', () => {
  const context = createContext();
  assert.equal(context.currentLibraryCount(false, { total_books: 7, total_items: 0 }), 0);
  assert.equal(context.currentLibraryCount(false, { total_books: 0, total_items: 3 }), 3);
});

test('buildHomeDashboardMarkup returns onboarding-only markup for empty libraries', () => {
  const context = createContext();
  const markup = context.buildHomeDashboardMarkup({
    showOnboarding: true,
    recentBooks: [],
    formatCounts: {},
    downloads: [],
    wishlist: [],
    activity: [],
    stats: { total_books: 0 },
    bookCount: 0,
  });

  assert.match(markup, /onboarding/);
  assert.doesNotMatch(markup, /dashboard_recent/);
  assert.doesNotMatch(markup, /dashboard_totals/);
});

test('buildHomeDashboardMarkup returns dashboard panels for non-empty libraries', () => {
  const context = createContext();
  const markup = context.buildHomeDashboardMarkup({
    showOnboarding: false,
    recentBooks: [{ title: 'The Martian' }],
    formatCounts: { EPUB: 1 },
    downloads: [],
    wishlist: [],
    activity: [],
    stats: { ebooks: 1, audiobooks: 0, manga: 0 },
    bookCount: 1,
  });

  assert.match(markup, /dashboard_recent/);
  assert.match(markup, /dashboard_totals/);
  assert.doesNotMatch(markup, /id="onboarding"/);
});

test('index.html uses renamed import folder label and helper text', () => {
  assert.match(indexHTML, /Import Folder \(Downloads\)/);
  assert.match(indexHTML, /Folder where Librarr watches for newly downloaded books\./);
  assert.doesNotMatch(indexHTML, /Incoming Directory/);
});

test('index.html uses requested field ordering', () => {
  const labels = [
    'Import Folder (Downloads)',
    'Ebook Library',
    'Audiobook Library',
    'Manga Library',
  ];
  const positions = labels.map(label => indexHTML.indexOf(label));
  positions.forEach((pos, idx) => {
    assert.notEqual(pos, -1, `missing label ${labels[idx]}`);
  });
  assert.ok(positions[0] < positions[1] && positions[1] < positions[2] && positions[2] < positions[3], 'field order does not match requested progression');
});

test('index.html uses improved Step 2 copy and Save & Continue action', () => {
  assert.match(indexHTML, /Step 2 – Scan Your Existing Library/);
  assert.match(indexHTML, /Available after saving your library folders\./);
  assert.match(indexHTML, /Save & Continue/);
  assert.match(indexHTML, /Step 1 Complete/);
  assert.match(indexHTML, /settings-library-scan-workspace/);
  assert.match(appSource, /Scan Library/);
  assert.doesNotMatch(indexHTML, /Next: Scan Existing Collection/);
});

test('index.html places Save & Continue before Step 2', () => {
  const buttonPos = indexHTML.indexOf('Save & Continue');
  const step2Pos = indexHTML.indexOf('Step 2 – Scan Your Existing Library');
  assert.notEqual(buttonPos, -1);
  assert.notEqual(step2Pos, -1);
  assert.ok(buttonPos < step2Pos, 'Save & Continue should appear before Step 2');
});

test('index.html shows Step 2 as disabled before save', () => {
  assert.match(indexHTML, /data-state="locked"/);
  assert.match(indexHTML, /🔒/);
  assert.match(indexHTML, /opacity-75/);
  assert.match(indexHTML, /Available after saving your library folders\./);
});

test('index.html uses Step 1 heading', () => {
  assert.match(indexHTML, /Step 1 – Configure Library Folders/);
  assert.doesNotMatch(indexHTML, /Import starts here\./);
});

test('validateLibraryImportSettings rejects empty, duplicate, double-slash, and trailing-space paths', () => {
  const context = createContext();
  const result = context.validateLibraryImportSettings({
    incoming_dir: '/downloads  ',
    ebook_dir: '',
    audiobook_dir: '/books//audio',
    manga_dir: '/downloads',
    file_org_enabled: false,
  });

  assert.match(result.errors.join('\n'), /Ebook Library is required\./);
  assert.match(result.errors.join('\n'), /Import Folder contains leading or trailing whitespace\./);
  assert.match(result.errors.join('\n'), /Audiobook Library contains a double slash\./);
});

test('saveLibraryImportSettings uses existing settings save path', async () => {
  const elements = {
    'setting-incoming_dir': { value: '/downloads' },
    'setting-ebook_dir': { value: '/books' },
    'setting-audiobook_dir': { value: '/audiobooks' },
    'setting-manga_dir': { value: '/manga' },
    'setting-file_org_enabled': { checked: true },
    'settings-library-import-save-standard': { disabled: false, classList: fakeClassList() },
    'settings-library-import-step2': { dataset: {}, classList: fakeClassList(), focus() {} },
    'settings-library-import-step2-icon': { textContent: '', className: '' },
    'settings-library-import-step2-copy': { textContent: '' },
    'settings-library-import-summary': { innerHTML: '', classList: fakeClassList(['hidden']) },
    'settings-library-import-save-continue': { classList: fakeClassList() },
    'settings-library-import-complete': { classList: fakeClassList(['hidden']) },
    'settings-library-import-complete-title': { textContent: '' },
    'settings-library-import-complete-copy': { textContent: '' },
    'settings-library-import-unsaved': { classList: fakeClassList(['hidden']) },
    'settings-library-import-validation': { classList: fakeClassList(['hidden']), innerHTML: '' },
  };
  let request = null;
  const context = createContext({
    document: { getElementById: id => elements[id] || null },
    apiJson: async (url, options) => {
      request = { url, options };
      return { success: true };
    },
  });

  await context.saveLibraryImportSettings(true);

  assert.equal(request.url, '/api/settings');
  assert.equal(request.options.method, 'POST');
  const body = JSON.parse(request.options.body);
  assert.deepEqual(body, {
    incoming_dir: '/downloads',
    ebook_dir: '/books',
    audiobook_dir: '/audiobooks',
    manga_dir: '/manga',
    file_org_enabled: true,
  });
});

test('successful Save & Continue advances focus to Step 2 panel', async () => {
  const events = [];
  const step2 = { focus: () => events.push('focus') };
  const elements = {
    'setting-incoming_dir': { value: '/downloads' },
    'setting-ebook_dir': { value: '/books' },
    'setting-audiobook_dir': { value: '/audiobooks' },
    'setting-manga_dir': { value: '/manga' },
    'setting-file_org_enabled': { checked: false },
    'settings-library-import-save-standard': { disabled: false, classList: fakeClassList() },
    'settings-library-import-step2': Object.assign(step2, { dataset: {}, classList: fakeClassList() }),
    'settings-library-import-step2-icon': { textContent: '', className: '' },
    'settings-library-import-step2-copy': { textContent: '' },
    'settings-library-import-summary': { innerHTML: '', classList: fakeClassList(['hidden']) },
    'settings-library-import-save-continue': { classList: fakeClassList() },
    'settings-library-import-complete': { classList: fakeClassList(['hidden']) },
    'settings-library-import-complete-title': { textContent: '' },
    'settings-library-import-complete-copy': { textContent: '' },
    'settings-library-import-unsaved': { classList: fakeClassList(['hidden']) },
    'settings-library-import-validation': { classList: fakeClassList(['hidden']), innerHTML: '' },
  };
  const context = createContext({
    document: { getElementById: id => elements[id] || null },
    apiJson: async () => ({ success: true }),
    showToast: (msg, kind) => events.push(`toast:${kind}:${msg}`),
    scrollToSettingsSection: id => events.push(`scroll:${id}`),
  });

  await context.saveLibraryImportSettings(true);

  assert.deepEqual(events, [
    'toast:success:Library and import settings saved',
    'scroll:settings-library-import-step2',
    'focus',
  ]);
  assert.equal(elements['settings-library-import-step2'].dataset.state, 'ready');
  assert.equal(elements['settings-library-import-step2-copy'].textContent, 'Your folders are saved. Librarr is ready to scan your existing collection.');
  assert.match(elements['settings-library-import-summary'].innerHTML, /Import Folder/);
  assert.match(elements['settings-library-import-summary'].innerHTML, /\/downloads/);
  assert.equal(elements['settings-library-import-save-continue'].classList.contains('hidden'), true);
  assert.equal(elements['settings-library-import-complete'].classList.contains('hidden'), false);
  assert.equal(elements['settings-library-import-complete-title'].textContent, 'Step 1 Complete');
});

test('failed Save & Continue shows an error and does not advance', async () => {
  const events = [];
  const elements = {
    'setting-incoming_dir': { value: '/downloads' },
    'setting-ebook_dir': { value: '/books' },
    'setting-audiobook_dir': { value: '/audiobooks' },
    'setting-manga_dir': { value: '/manga' },
    'setting-file_org_enabled': { checked: false },
    'settings-library-import-save-standard': { disabled: false, classList: fakeClassList() },
    'settings-library-import-step2': { dataset: {}, classList: fakeClassList(), focus: () => events.push('focus') },
    'settings-library-import-step2-icon': { textContent: '', className: '' },
    'settings-library-import-step2-copy': { textContent: '' },
    'settings-library-import-summary': { innerHTML: 'old', classList: fakeClassList() },
    'settings-library-import-save-continue': { classList: fakeClassList() },
    'settings-library-import-complete': { classList: fakeClassList(['hidden']) },
    'settings-library-import-complete-title': { textContent: '' },
    'settings-library-import-complete-copy': { textContent: '' },
    'settings-library-import-unsaved': { classList: fakeClassList(['hidden']) },
    'settings-library-import-validation': { classList: fakeClassList(['hidden']), innerHTML: '' },
  };
  const context = createContext({
    document: { getElementById: id => elements[id] || null },
    apiJson: async () => ({ success: false, error: 'Nope' }),
    showToast: (msg, kind) => events.push(`toast:${kind}:${msg}`),
    scrollToSettingsSection: id => events.push(`scroll:${id}`),
  });

  await context.saveLibraryImportSettings(true);

  assert.deepEqual(events, ['toast:error:Nope']);
  assert.equal(elements['settings-library-import-step2'].dataset.state, 'locked');
  assert.equal(elements['settings-library-import-summary'].innerHTML, '');
  assert.equal(elements['settings-library-import-save-continue'].classList.contains('hidden'), false);
  assert.equal(elements['settings-library-import-complete'].classList.contains('hidden'), true);
});

test('fresh configuration shows Save & Continue and keeps Step 2 locked', () => {
  const elements = {
    'setting-incoming_dir': { value: '' },
    'setting-ebook_dir': { value: '' },
    'setting-audiobook_dir': { value: '' },
    'setting-manga_dir': { value: '' },
    'setting-file_org_enabled': { checked: false },
    'settings-library-import-save-standard': { disabled: false, classList: fakeClassList() },
    'settings-library-import-step2': { dataset: { state: 'locked' }, classList: fakeClassList(['opacity-75']) },
    'settings-library-import-step2-icon': { textContent: '🔒', className: '' },
    'settings-library-import-step2-copy': { textContent: 'Available after saving your library folders.' },
    'settings-library-import-summary': { innerHTML: '', classList: fakeClassList(['hidden']) },
    'settings-library-import-save-continue': { classList: fakeClassList(['hidden']), textContent: '' },
    'settings-library-import-complete': { classList: fakeClassList(['hidden']) },
    'settings-library-import-complete-title': { textContent: '' },
    'settings-library-import-complete-copy': { textContent: '' },
    'settings-library-import-unsaved': { classList: fakeClassList(['hidden']) },
    'settings-library-import-validation': { classList: fakeClassList(['hidden']), innerHTML: '' },
  };
  const context = createContext({
    document: { getElementById: id => elements[id] || null },
  });

  context.updateLibraryImportSaveState();

  assert.equal(elements['settings-library-import-save-continue'].classList.contains('hidden'), false);
  assert.equal(elements['settings-library-import-complete'].classList.contains('hidden'), true);
  assert.equal(elements['settings-library-import-step2'].dataset.state, 'locked');
});

test('completed state restores from loaded settings and keeps Step 2 active', () => {
  const elements = {
    'setting-incoming_dir': { value: '/downloads' },
    'setting-ebook_dir': { value: '/books' },
    'setting-audiobook_dir': { value: '/audiobooks' },
    'setting-manga_dir': { value: '/manga' },
    'setting-file_org_enabled': { checked: true },
    'settings-library-import-save-standard': { disabled: false, classList: fakeClassList() },
    'settings-library-import-step2': { dataset: { state: 'ready' }, classList: fakeClassList() },
    'settings-library-import-step2-icon': { textContent: '✅', className: '' },
    'settings-library-import-step2-copy': { textContent: '' },
    'settings-library-import-summary': { innerHTML: '', classList: fakeClassList(['hidden']) },
    'settings-library-import-save-continue': { classList: fakeClassList(['hidden']), textContent: '' },
    'settings-library-import-complete': { classList: fakeClassList() },
    'settings-library-import-complete-title': { textContent: '' },
    'settings-library-import-complete-copy': { textContent: '' },
    'settings-library-import-unsaved': { classList: fakeClassList(['hidden']) },
    'settings-library-import-validation': { classList: fakeClassList(['hidden']), innerHTML: '' },
  };
  const context = createContext({
    document: { getElementById: id => elements[id] || null },
  });

  context.applyLibraryImportLoadedState({
    incoming_dir: '/downloads',
    ebook_dir: '/books',
    audiobook_dir: '/audiobooks',
    manga_dir: '/manga',
    file_org_enabled: true,
  });

  assert.equal(context.state.libraryImport.completed, true);
  assert.equal(elements['settings-library-import-save-continue'].classList.contains('hidden'), true);
  assert.equal(elements['settings-library-import-complete'].classList.contains('hidden'), false);
  assert.equal(elements['settings-library-import-step2'].dataset.state, 'ready');
  assert.match(elements['settings-library-import-summary'].innerHTML, /\/downloads/);
});

test('Save & Continue does not return after later edits', () => {
  const elements = {
    'setting-incoming_dir': { value: '/downloads-2' },
    'setting-ebook_dir': { value: '/books' },
    'setting-audiobook_dir': { value: '/audiobooks' },
    'setting-manga_dir': { value: '/manga' },
    'setting-file_org_enabled': { checked: true },
    'settings-library-import-save-standard': { disabled: false, classList: fakeClassList() },
    'settings-library-import-step2': { dataset: { state: 'ready' }, classList: fakeClassList() },
    'settings-library-import-step2-icon': { textContent: '✅', className: '' },
    'settings-library-import-step2-copy': { textContent: '' },
    'settings-library-import-summary': { innerHTML: '', classList: fakeClassList(['hidden']) },
    'settings-library-import-save-continue': { classList: fakeClassList(['hidden']), textContent: '' },
    'settings-library-import-complete': { classList: fakeClassList() },
    'settings-library-import-complete-title': { textContent: '' },
    'settings-library-import-complete-copy': { textContent: '' },
    'settings-library-import-unsaved': { classList: fakeClassList(['hidden']) },
    'settings-library-import-validation': { classList: fakeClassList(['hidden']), innerHTML: '' },
  };
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, dirty: false, lastSaved: {
      incoming_dir: '/downloads',
      ebook_dir: '/books',
      audiobook_dir: '/audiobooks',
      manga_dir: '/manga',
      file_org_enabled: true,
    } }) },
    document: { getElementById: id => elements[id] || null },
  });

  context.updateLibraryImportSaveState();

  assert.equal(elements['settings-library-import-save-continue'].classList.contains('hidden'), true);
  assert.equal(elements['settings-library-import-complete'].classList.contains('hidden'), false);
  assert.equal(elements['settings-library-import-unsaved'].classList.contains('hidden'), false);
  assert.equal(context.state.libraryImport.dirty, true);
  assert.equal(elements['settings-library-import-step2'].dataset.state, 'ready');
});

test('standard purple Save persists Library & Import changes after onboarding and updates summary', async () => {
  const events = [];
  const elements = {
    'setting-incoming_dir': { value: '/downloads-2' },
    'setting-ebook_dir': { value: '/books' },
    'setting-audiobook_dir': { value: '/audiobooks' },
    'setting-manga_dir': { value: '/manga' },
    'setting-file_org_enabled': { checked: true },
    'settings-library-import-save-standard': { disabled: false, classList: fakeClassList() },
    'settings-library-import-step2': { dataset: {}, classList: fakeClassList(), focus: () => events.push('focus') },
    'settings-library-import-step2-icon': { textContent: '', className: '' },
    'settings-library-import-step2-copy': { textContent: '' },
    'settings-library-import-summary': { innerHTML: '', classList: fakeClassList(['hidden']) },
    'settings-library-import-save-continue': { classList: fakeClassList(), textContent: '' },
    'settings-library-import-complete': { classList: fakeClassList(['hidden']) },
    'settings-library-import-complete-title': { textContent: '' },
    'settings-library-import-complete-copy': { textContent: '' },
    'settings-library-import-unsaved': { classList: fakeClassList(['hidden']) },
    'settings-library-import-validation': { classList: fakeClassList(['hidden']), innerHTML: '' },
  };
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, dirty: true, lastSaved: {
      incoming_dir: '/downloads',
      ebook_dir: '/books',
      audiobook_dir: '/audiobooks',
      manga_dir: '/manga',
      file_org_enabled: true,
    } }) },
    document: { getElementById: id => elements[id] || null },
    apiJson: async () => ({ success: true }),
    showToast: (msg, kind) => events.push(`toast:${kind}:${msg}`),
    scrollToSettingsSection: id => events.push(`scroll:${id}`),
    window: { setTimeout: fn => { events.push('timer'); fn(); return 1; }, clearTimeout: () => {} },
  });

  await context.saveLibraryImportSettings(false);

  assert.equal(elements['settings-library-import-complete'].classList.contains('hidden'), false);
  assert.equal(elements['settings-library-import-complete-title'].textContent, 'Step 1 Complete');
  assert.equal(elements['settings-library-import-save-continue'].classList.contains('hidden'), true);
  assert.equal(elements['settings-library-import-step2'].dataset.state, 'ready');
  assert.match(elements['settings-library-import-summary'].innerHTML, /\/downloads-2/);
  assert.equal(context.state.libraryImport.dirty, false);
  assert.match(events.join('\n'), /toast:success:Library and import settings saved/);
  assert.ok(!events.includes('scroll:settings-library-import-step2'));
});

test('failed standard save leaves Step 2 active and does not update summary', async () => {
  const events = [];
  const elements = {
    'setting-incoming_dir': { value: '/downloads-2' },
    'setting-ebook_dir': { value: '/books' },
    'setting-audiobook_dir': { value: '/audiobooks' },
    'setting-manga_dir': { value: '/manga' },
    'setting-file_org_enabled': { checked: true },
    'settings-library-import-save-standard': { disabled: false, classList: fakeClassList() },
    'settings-library-import-step2': { dataset: { state: 'ready' }, classList: fakeClassList() },
    'settings-library-import-step2-icon': { textContent: '✅', className: '' },
    'settings-library-import-step2-copy': { textContent: 'Your folders are saved. Librarr is ready to scan your existing collection.' },
    'settings-library-import-summary': { innerHTML: '<div>/downloads</div>', classList: fakeClassList() },
    'settings-library-import-save-continue': { classList: fakeClassList(['hidden']), textContent: '' },
    'settings-library-import-complete': { classList: fakeClassList() },
    'settings-library-import-complete-title': { textContent: 'Step 1 Complete' },
    'settings-library-import-complete-copy': { textContent: 'Library folders configured successfully.' },
    'settings-library-import-unsaved': { classList: fakeClassList(['hidden']) },
    'settings-library-import-validation': { classList: fakeClassList(['hidden']), innerHTML: '' },
  };
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, dirty: true, lastSaved: {
      incoming_dir: '/downloads',
      ebook_dir: '/books',
      audiobook_dir: '/audiobooks',
      manga_dir: '/manga',
      file_org_enabled: true,
    } }) },
    document: { getElementById: id => elements[id] || null },
    apiJson: async () => ({ success: false, error: 'Nope' }),
    showToast: (msg, kind) => events.push(`toast:${kind}:${msg}`),
  });

  await context.saveLibraryImportSettings(false);

  assert.deepEqual(events, ['toast:error:Nope']);
  assert.equal(elements['settings-library-import-step2'].dataset.state, 'ready');
  assert.match(elements['settings-library-import-summary'].innerHTML, /\/downloads/);
  assert.equal(elements['settings-library-import-save-continue'].classList.contains('hidden'), true);
});

test('index.html keeps the standard Settings save control', () => {
  assert.match(indexHTML, /id="settings-library-import-save-standard"/);
  assert.match(indexHTML, /data-action="saveLibraryImportStandard"/);
  assert.match(indexHTML, />Save<\/button>/);
});

test('renderLibraryScanWorkspace shows active scan button after onboarding', () => {
  const workspace = { innerHTML: '' };
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true }) },
    document: { getElementById: id => id === 'settings-library-scan-workspace' ? workspace : null },
  });

  context.renderLibraryScanWorkspace();

  assert.match(workspace.innerHTML, /data-action="startLibraryScan"/);
  assert.match(workspace.innerHTML, /Review results here before importing/);
});

test('successful scan flow posts, polls, loads results, and renders review', async () => {
  const workspace = { innerHTML: '' };
  const calls = [];
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true }) },
    document: { getElementById: id => id === 'settings-library-scan-workspace' ? workspace : null },
    apiJson: async (url, options = {}) => {
      calls.push({ url, method: options.method || 'GET' });
      if (url === '/api/v1/library/scan') return { job_id: 'job-1', job: { started_at: '2026-01-01T00:00:00Z', progress: { status: 'scanning', current_phase: 'scanning', started_at: '2026-01-01T00:00:00Z' } } };
      if (url === '/api/v1/library/scan/job-1') return { id: 'job-1', status: 'completed', progress: { status: 'completed', files_processed: 2 } };
      if (url === '/api/v1/library/scan/job-1/results') return sampleScanResult();
      throw new Error(`unexpected ${url}`);
    },
  });

  await context.startLibraryScan();

  assert.deepEqual(calls.map(c => `${c.method} ${c.url}`), [
    'POST /api/v1/library/scan',
    'GET /api/v1/library/scan/job-1',
    'GET /api/v1/library/scan/job-1/results',
  ]);
  assert.match(workspace.innerHTML, /Files Found/);
  assert.match(workspace.innerHTML, /Ready to Import/);
  assert.match(workspace.innerHTML, /The Guardian/);
});

test('renderLibraryScanProgress shows progress fields', () => {
  const context = createContext();
  const html = context.renderLibraryScanProgress({
    running: true,
    startedAt: new Date().toISOString(),
    progress: {
      current_phase: 'processing_metadata',
      directories_scanned: 3,
      files_discovered: 29,
      files_processed: 17,
      candidates_ready: 12,
      current_path: '/books/example.epub',
    },
  });

  assert.match(html, /Processing Metadata/);
  assert.match(html, /Directories/);
  assert.match(html, /\/books\/example.epub/);
});

test('renderLibraryScanReview handles empty and duplicate-only scans', () => {
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true }) },
  });

  assert.match(context.renderLibraryScanReview({ totals: { found: 0 }, candidates: [] }), /No books found/);
  assert.match(context.renderLibraryScanReview({
    totals: { found: 2, ready_to_import: 0, duplicates: 2 },
    candidates: [
      { classification: 'duplicate', title: 'A', filename: 'a.epub', format: 'epub', metadata: {}, existing_path: '/library/a.epub' },
      { classification: 'duplicate', title: 'B', filename: 'b.epub', format: 'epub', metadata: {} },
    ],
  }), /Everything found in this scan already appears to be in the library/);
  assert.match(context.renderLibraryScanReview({
    totals: { found: 1, ready_to_import: 0, duplicates: 1 },
    candidates: [{ classification: 'duplicate', title: 'A', filename: 'a.epub', format: 'epub', metadata: {}, existing_path: '/library/a.epub' }],
  }), /Existing: \/library\/a\.epub/);
});

test('filterLibraryScanCandidates filters by bucket and search', () => {
  const context = createContext();
  const candidates = sampleScanResult().candidates;

  assert.equal(context.filterLibraryScanCandidates(candidates, 'duplicate', '').length, 1);
  assert.equal(context.filterLibraryScanCandidates(candidates, 'all', 'guardian').length, 1);
  assert.equal(context.filterLibraryScanCandidates(candidates, 'all', 'missing').length, 0);
});

test('scan failure renders retry state', async () => {
  const workspace = { innerHTML: '' };
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true }) },
    document: { getElementById: id => id === 'settings-library-scan-workspace' ? workspace : null },
    apiJson: async url => {
      if (url === '/api/v1/library/scan') return { job_id: 'job-1' };
      if (url === '/api/v1/library/scan/job-1') return { id: 'job-1', status: 'failed', error: 'scan failed' };
      throw new Error(`unexpected ${url}`);
    },
  });

  await context.startLibraryScan();

  assert.match(workspace.innerHTML, /Library scan failed/);
  assert.match(workspace.innerHTML, /Retry/);
});

function sampleScanResult() {
  return {
    job_id: 'job-1',
    status: 'completed',
    totals: {
      found: 2,
      ready_to_import: 1,
      duplicates: 1,
      already_imported: 0,
      unsupported: 0,
      unreadable: 0,
    },
    candidates: [
      {
        id: 'ready-1',
        classification: 'new',
        title: 'The Guardian',
        author: 'Carla Jablonski',
        filename: 'guardian.epub',
        format: 'epub',
        media_type: 'ebook',
        path: '/books/guardian.epub',
        metadata: { source: 'embedded_metadata', title: 'The Guardian', author: 'Carla Jablonski' },
        classification_reason: 'Ready to import',
      },
      {
        id: 'dup-1',
        classification: 'duplicate',
        title: 'Already There',
        author: 'Jane Doe',
        filename: 'dup.mobi',
        format: 'mobi',
        media_type: 'ebook',
        path: '/books/dup.mobi',
        existing_path: '/library/dup.mobi',
        metadata: { source: 'filename_fallback' },
        classification_reason: 'Existing library file already has matching content',
      },
    ],
  };
}

function fakeClassList(initial = []) {
  const set = new Set(initial);
  return {
    add: (...names) => names.forEach(name => set.add(name)),
    remove: (...names) => names.forEach(name => set.delete(name)),
    toggle: (name, force) => {
      if (force === undefined) {
        if (set.has(name)) set.delete(name);
        else set.add(name);
        return set.has(name);
      }
      if (force) set.add(name);
      else set.delete(name);
      return force;
    },
    contains: name => set.has(name),
  };
}
