const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const jsPath = path.join(__dirname, 'app.js');
const htmlPath = path.join(__dirname, '..', '..', 'index.html');
const cssPath = path.join(__dirname, '..', 'css', 'app.css');
const appSource = fs.readFileSync(jsPath, 'utf8');
const indexHTML = fs.readFileSync(htmlPath, 'utf8');
const appCSS = fs.readFileSync(cssPath, 'utf8');

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
  extractFunctionSource('renderOnboardingChecklist'),
  extractFunctionSource('renderLibraryBookCard'),
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
  extractFunctionSource('renderLibraryImportProgress'),
  extractFunctionSource('renderLibraryImportSummary'),
  extractFunctionSource('libraryImportElapsedSeconds'),
  extractFunctionSource('formatDurationSeconds'),
  extractFunctionSource('renderLibraryScanError'),
  extractFunctionSource('renderLibraryScanReview'),
  extractFunctionSource('renderLibraryScanImportActions'),
  extractFunctionSource('renderLibraryScanTotals'),
  extractFunctionSource('renderLibraryScanToolbar'),
  extractFunctionSource('renderLibraryScanSection'),
  extractFunctionSource('renderLibraryScanCandidate'),
  extractFunctionSource('renderLibraryScanCover'),
  extractFunctionSource('renderLibraryScanDestination'),
  extractFunctionSource('renderLibraryScanDuplicate'),
  extractFunctionSource('renderLibraryScanManualReview'),
  extractFunctionSource('renderLibraryScanMetadataEditor'),
  extractFunctionSource('findLibraryScanCandidate'),
  extractFunctionSource('metadataEditorDraftFromCandidate'),
  extractFunctionSource('openMetadataEditor'),
  extractFunctionSource('closeMetadataEditor'),
  extractFunctionSource('resetMetadataEditor'),
  extractFunctionSource('updateMetadataEditorDraft'),
  extractFunctionSource('updateMetadataEditorPreview'),
  extractFunctionSource('metadataEditorPreview'),
  extractFunctionSource('validateMetadataEditorDraft'),
  extractFunctionSource('metadataEditorPayload'),
  extractFunctionSource('saveMetadataEditor'),
  extractFunctionSource('pathExtension'),
  extractFunctionSource('pathDirname'),
  extractFunctionSource('safeFilenamePart'),
  extractFunctionSource('formatMetadataSource'),
  extractFunctionSource('formatMetadataConfidence'),
  extractFunctionSource('filterLibraryScanCandidates'),
  extractFunctionSource('groupLibraryScanCandidates'),
  extractFunctionSource('readyLibraryScanCandidates'),
  extractFunctionSource('selectedReadyLibraryScanCandidates'),
  extractFunctionSource('formatLibraryScanPhase'),
  extractFunctionSource('formatLibraryScanElapsed'),
  extractFunctionSource('startLibraryScan'),
  extractFunctionSource('pollLibraryScanJob'),
  extractFunctionSource('loadLibraryScanResults'),
  extractFunctionSource('startLibraryImport'),
  extractFunctionSource('pollLibraryImportJob'),
  extractFunctionSource('loadLibraryImportResults'),
  extractFunctionSource('resolveLibraryScanCandidate'),
  extractFunctionSource('retryFailedLibraryImport'),
  extractFunctionSource('refreshLibraryAfterScanImport'),
  extractFunctionSource('diagnosticPayload'),
  extractFunctionSource('renderDiagnosticResult'),
  extractFunctionSource('renderDiagnosticStep'),
  extractFunctionSource('diagnosticStatusClass'),
  extractFunctionSource('diagnosticStatusIcon'),
  extractFunctionSource('diagnosticStatusLabel'),
  extractFunctionSource('testConnection'),
  extractFunctionSource('loadStats'),
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
      selected: new Set(),
      skipped: new Set(),
      editor: {
        candidateId: '',
        draft: null,
        errors: [],
      },
      import: {
        running: false,
        jobId: '',
        pollTimer: null,
        startedAt: null,
        progress: null,
        result: null,
        error: '',
      },
      sections: {
        new: true,
        manual_review: true,
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
    window: { setTimeout: fn => { fn(); return 1; }, clearTimeout: () => {}, prompt: () => null },
    normalizedLibraryMode: () => false,
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

test('main navigation is focused on Librarr 2.0 primary destinations', () => {
  assert.match(indexHTML, /data-arg="home"/);
  assert.match(indexHTML, /data-arg="library"/);
  assert.match(indexHTML, /data-arg="search"[\s\S]*nav_discover/);
  assert.match(indexHTML, /data-arg="settings"/);
  assert.doesNotMatch(indexHTML, /id="lang-toggle"/);
  assert.doesNotMatch(appSource, /lang-toggle|toggleLanguage/);
  assert.doesNotMatch(appCSS, /Russian locale/);
  assert.doesNotMatch(indexHTML, /data-arg="downloads" class="nav-tab/);
  assert.doesNotMatch(indexHTML, /data-arg="wishlist" class="nav-tab/);
});

test('unfinished device actions are not exposed in book cards', () => {
  const context = createContext({
    renderBookCover: () => '<cover />',
    renderFormatBadge: format => `<format>${format}</format>`,
  });

  const html = context.renderLibraryBookCard({ title: 'Book', author: 'Author', formats: ['EPUB'] }, 0);

  assert.doesNotMatch(html, /sendBookToDevices|>Send</);
  assert.doesNotMatch(appSource, /function sendBookToDevices|tab-devices|devices-grid/);
});

test('onboarding checklist does not duplicate the import action', () => {
  const context = createContext({
    state: { currentUser: 'admin', libraryImport: libraryImportState() },
  });
  const html = context.renderOnboardingChecklist();

  assert.match(html, /home_onboarding_title/);
  assert.doesNotMatch(html, /data-action="openImportSettings"/);
});

test('loadStats displays book-oriented header copy', async () => {
  const statsEl = { textContent: '', classList: fakeClassList(['hidden']) };
  const context = createContext({
    document: { getElementById: id => id === 'header-stats' ? statsEl : null },
    normalizedLibraryMode: () => true,
    t: (key, vars) => key === 'n_books_in_library' ? `${vars.n} books in library` : key,
    apiJson: async url => {
      assert.equal(url, '/api/v1/library/summary');
      return { total_books: 3 };
    },
  });

  await context.loadStats();

  assert.equal(statsEl.textContent, '3 books in library');
  assert.equal(statsEl.classList.contains('hidden'), false);
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

test('renderLibraryScanReview shows import actions and selectable ready rows', () => {
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: { ...libraryImportState().scan, result: sampleScanResult(), selected: new Set(['ready-1']) } }) },
  });

  const html = context.renderLibraryScanReview(sampleScanResult());

  assert.match(html, /Select All Ready/);
  assert.match(html, /Import Selected/);
  assert.match(html, /Import All Ready/);
  assert.match(html, /Skip Selected/);
  assert.match(html, /data-candidate-id="ready-1"/);
  assert.match(html, /Embedded metadata/);
  assert.match(html, /High/);
  assert.match(html, /Destination/);
  assert.match(html, /Carla Jablonski → The Guardian\.epub/);
});

test('renderLibraryScanReview shows manual review details and resolution controls', () => {
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: { ...libraryImportState().scan, result: manualReviewScanResult() } }) },
  });

  const html = context.renderLibraryScanReview(manualReviewScanResult());

  assert.match(html, /Manual Review Required/);
  assert.match(html, /Existing title match did not cleanly agree on author/);
  assert.match(html, /Filename parsing/);
  assert.match(html, /Medium/);
  assert.match(html, /Suggested Destination/);
  assert.match(html, /Use Suggested/);
  assert.match(html, /Edit Metadata/);
  assert.match(html, /Skip/);
});

test('renderLibraryScanReview shows duplicate details', () => {
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: { ...libraryImportState().scan, result: sampleScanResult() } }) },
  });

  const html = context.renderLibraryScanReview(sampleScanResult());

  assert.match(html, /Duplicate/);
  assert.match(html, /Identical hash/);
  assert.match(html, /Already There/);
  assert.match(html, /Jane Doe/);
  assert.match(html, /\/library\/dup\.mobi/);
});

test('selection helpers exclude skipped and non-ready candidates', () => {
  const scanState = libraryImportState().scan;
  scanState.result = sampleScanResult();
  scanState.selected = new Set(['ready-1', 'dup-1']);
  scanState.skipped = new Set(['ready-1']);
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: scanState }) },
  });

  assert.equal(context.readyLibraryScanCandidates(sampleScanResult()).length, 0);
  assert.equal(context.selectedReadyLibraryScanCandidates(sampleScanResult()).length, 0);
  assert.equal(context.filterLibraryScanCandidates(sampleScanResult().candidates, 'all', '').length, 1);
});

test('successful import selected posts, polls, refreshes review, and clears imported selection', async () => {
  const workspace = { innerHTML: '' };
  const scanState = libraryImportState().scan;
  scanState.result = sampleScanResult();
  scanState.selected = new Set(['ready-1']);
  const calls = [];
  const context = createContext({
    state: { currentTab: 'home', libraryImport: libraryImportState({ completed: true, scan: scanState }) },
    document: { getElementById: id => id === 'settings-library-scan-workspace' ? workspace : null },
    loadHomeDashboard: async () => calls.push({ url: 'home-refresh', method: 'CALL' }),
    apiJson: async (url, options = {}) => {
      calls.push({ url, method: options.method || 'GET', body: options.body || '' });
      if (url === '/api/v1/library/import') return { job_id: 'imp-1', job: { started_at: '2026-01-01T00:00:00Z', progress: { status: 'importing', total: 1, imported: 0, started_at: '2026-01-01T00:00:00Z' } } };
      if (url === '/api/v1/library/import/imp-1') return { id: 'imp-1', status: 'completed', progress: { status: 'completed', total: 1, imported: 1 } };
      if (url === '/api/v1/library/import/imp-1/results') return { job_id: 'imp-1', scan_job_id: 'job-1', summary: { imported: 1, duplicates: 0, failed: 0 }, items: [{ candidate_id: 'ready-1', status: 'imported' }] };
      if (url === '/api/v1/library/scan/job-1/results') return importedScanResult();
      throw new Error(`unexpected ${url}`);
    },
  });

  await context.startLibraryImport(false);

  assert.deepEqual(calls.map(c => `${c.method} ${c.url}`), [
    'POST /api/v1/library/import',
    'GET /api/v1/library/import/imp-1',
    'GET /api/v1/library/import/imp-1/results',
    'GET /api/v1/library/scan/job-1/results',
    'CALL home-refresh',
  ]);
  assert.match(calls[0].body, /"candidate_ids":\["ready-1"\]/);
  assert.equal(context.state.libraryImport.scan.selected.has('ready-1'), false);
  assert.match(workspace.innerHTML, /Import Complete/);
  assert.match(workspace.innerHTML, /View Imported/);
  assert.match(workspace.innerHTML, /Retry Failed/);
  assert.match(workspace.innerHTML, /Close/);
  assert.match(workspace.innerHTML, /Already Imported/);
});

test('import all ready sends all_ready and import progress renders counts', async () => {
  const workspace = { innerHTML: '' };
  const scanState = libraryImportState().scan;
  scanState.result = sampleScanResult();
  const calls = [];
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: scanState }) },
    document: { getElementById: id => id === 'settings-library-scan-workspace' ? workspace : null },
    apiJson: async (url, options = {}) => {
      calls.push({ url, method: options.method || 'GET', body: options.body || '' });
      if (url === '/api/v1/library/import') return { job_id: 'imp-2', job: { progress: { status: 'importing', total: 1, imported: 0, current_title: 'The Guardian', started_at: new Date().toISOString() } } };
      if (url === '/api/v1/library/import/imp-2') return { id: 'imp-2', status: 'completed', progress: { status: 'completed', total: 1, imported: 1 } };
      if (url === '/api/v1/library/import/imp-2/results') return { job_id: 'imp-2', scan_job_id: 'job-1', summary: { imported: 1, duplicates: 0, failed: 0 }, items: [] };
      if (url === '/api/v1/library/scan/job-1/results') return importedScanResult();
      throw new Error(`unexpected ${url}`);
    },
  });

  assert.match(context.renderLibraryImportProgress({ startedAt: new Date().toISOString(), progress: { total: 17, imported: 8, failed: 0, duplicates: 0, current_title: 'The War of the Worlds' } }), /8 \/ 17 books/);
  await context.startLibraryImport(true);

  assert.match(calls[0].body, /"all_ready":true/);
});

test('import failure keeps review visible and reports error', async () => {
  const workspace = { innerHTML: '' };
  const scanState = libraryImportState().scan;
  scanState.result = sampleScanResult();
  scanState.selected = new Set(['ready-1']);
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: scanState }) },
    document: { getElementById: id => id === 'settings-library-scan-workspace' ? workspace : null },
    apiJson: async url => {
      if (url === '/api/v1/library/import') return { job_id: 'imp-err' };
      if (url === '/api/v1/library/import/imp-err') return { id: 'imp-err', status: 'failed', error: 'permission denied' };
      throw new Error(`unexpected ${url}`);
    },
  });

  await context.startLibraryImport(false);

  assert.match(workspace.innerHTML, /permission denied/);
  assert.match(workspace.innerHTML, /Ready to Import/);
});

test('import completion summary renders partial failures', () => {
  const context = createContext();
  const html = context.renderLibraryImportSummary({
    summary: { imported: 15, skipped: 0, duplicates: 0, failed: 2, total: 17 },
    started_at: '2026-01-01T00:00:00Z',
    completed_at: '2026-01-01T00:00:05Z',
    items: [
      { status: 'failed', title: 'Missing Book', error: 'file disappeared' },
      { status: 'failed', path: '/books/locked.epub', error: 'permission denied' },
    ],
  });

  assert.match(html, /15 imported/);
  assert.match(html, /2 failed/);
  assert.match(html, /3\.0\/s/);
  assert.match(html, /Show Details/);
  assert.match(html, /Retry Failed/);
  assert.match(html, /file disappeared/);
});

test('manual review use suggested resolves candidate through scan endpoint', async () => {
  const scanState = libraryImportState().scan;
  scanState.result = manualReviewScanResult();
  const calls = [];
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: scanState }) },
    apiJson: async (url, options = {}) => {
      calls.push({ url, method: options.method || 'GET', body: options.body || '' });
      return {
        ...manualReviewScanResult(),
        totals: { found: 1, ready_to_import: 1, manual_review: 0 },
        candidates: [{ ...manualReviewScanResult().candidates[0], classification: 'new', classification_reason: 'Ready to import after manual review', manual_review: null }],
      };
    },
  });

  await context.resolveLibraryScanCandidate('review-1', 'use_suggested');

  assert.equal(calls[0].url, '/api/v1/library/scan/job-1/resolve');
  assert.equal(calls[0].method, 'POST');
  assert.match(calls[0].body, /"action":"use_suggested"/);
  assert.equal(context.state.libraryImport.scan.result.totals.ready_to_import, 1);
  assert.equal(context.state.libraryImport.scan.selected.has('review-1'), true);
});

test('metadata editor renders live preview and validation for manual review items', () => {
  const scanState = libraryImportState().scan;
  scanState.result = manualReviewScanResult();
  scanState.editor = {
    candidateId: 'review-1',
    draft: {
      title: 'The Guardian’s Path',
      subtitle: '',
      author: 'Carla Jablonski',
      series: 'Prince of Persia',
      series_number: '1',
      publisher: 'Disney',
      publication_year: '2004',
      isbn: '978-1234567890',
      language: 'en',
      description: 'Adventure novel',
      tags: 'fantasy, tie-in',
      library: 'ebook',
    },
    errors: [],
  };
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: scanState }) },
  });

  const html = context.renderLibraryScanReview(manualReviewScanResult());

  assert.match(html, /Metadata Editor/);
  assert.match(html, /Destination Folder/);
  assert.match(html, /Filename/);
  assert.match(html, /Import Location/);
  assert.match(html, /Carla Jablonski - The Guardian’s Path\.mobi/);
  assert.match(html, /Save & Import/);
});

test('metadata editor validation blocks missing title, bad year, bad ISBN, and duplicate filename', () => {
  const scanState = libraryImportState().scan;
  scanState.result = {
    ...manualReviewScanResult(),
    candidates: [
      manualReviewScanResult().candidates[0],
      {
        ...sampleScanResult().candidates[0],
        id: 'ready-2',
        classification: 'new',
        destination_path: '/books/ebooks/Disney Book Group/Carla Jablonski - Existing.mobi',
      },
    ],
  };
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: scanState }) },
  });
  const candidate = scanState.result.candidates[0];

  const errors = context.validateMetadataEditorDraft(candidate, {
    title: '',
    author: '',
    publication_year: '20x4',
    isbn: 'abc',
  });

  assert(errors.some(error => /Title is required/.test(error)));
  assert(errors.some(error => /Author is required/.test(error)));
  assert(errors.some(error => /four-digit year/.test(error)));
  assert(errors.some(error => /ISBN/.test(error)));

  const duplicateErrors = context.validateMetadataEditorDraft(candidate, {
    title: 'Existing',
    author: 'Carla Jablonski',
  });
  assert(duplicateErrors.some(error => /destination filename/.test(error)));
});

test('metadata editor save posts edited fields and save import starts a single import', async () => {
  const scanState = libraryImportState().scan;
  scanState.result = manualReviewScanResult();
  scanState.editor = {
    candidateId: 'review-1',
    draft: {
      title: 'The Guardian’s Path',
      author: 'Carla Jablonski',
      publication_year: '2004',
      isbn: '9781234567890',
      tags: 'fantasy, tie-in',
      library: 'ebook',
    },
    errors: [],
  };
  const calls = [];
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: scanState }) },
    document: { getElementById: () => ({ innerHTML: '', scrollIntoView: () => {} }), querySelector: () => null },
    apiJson: async (url, options = {}) => {
      calls.push({ url, method: options.method || 'GET', body: options.body || '' });
      if (url === '/api/v1/library/scan/job-1/resolve') {
        return {
          ...manualReviewScanResult(),
          totals: { found: 1, ready_to_import: 1, manual_review: 0 },
          candidates: [{
            ...manualReviewScanResult().candidates[0],
            classification: 'new',
            title: 'The Guardian’s Path',
            author: 'Carla Jablonski',
            metadata: { title: 'The Guardian’s Path', author: 'Carla Jablonski', source: 'manual_edit', confidence: 'high' },
            manual_review: null,
          }],
        };
      }
      if (url === '/api/v1/library/import') return { job_id: 'imp-1', job: { progress: { status: 'importing', total: 1, started_at: '2026-01-01T00:00:00Z' } } };
      if (url === '/api/v1/library/import/imp-1') return { id: 'imp-1', status: 'completed', progress: { status: 'completed', total: 1, imported: 1 } };
      if (url === '/api/v1/library/import/imp-1/results') return { job_id: 'imp-1', scan_job_id: 'job-1', summary: { imported: 1, duplicates: 0, failed: 0 }, items: [] };
      if (url === '/api/v1/library/scan/job-1/results') return importedScanResult();
      throw new Error(`unexpected ${url}`);
    },
  });

  await context.saveMetadataEditor(true);

  assert.match(calls[0].body, /"title":"The Guardian’s Path"/);
  assert.match(calls[0].body, /"publication_year":"2004"/);
  assert.match(calls[0].body, /"tags":\["fantasy","tie-in"\]/);
  assert.equal(calls[1].url, '/api/v1/library/import');
  assert.match(calls[1].body, /"candidate_ids":\["review-1"\]/);
});

test('retry failed import posts only failed ready candidates', async () => {
  const scanState = libraryImportState().scan;
  scanState.result = {
    ...sampleScanResult(),
    candidates: [
      sampleScanResult().candidates[0],
      { ...sampleScanResult().candidates[0], id: 'ready-2', title: 'Other Book', path: '/books/other.epub' },
    ],
  };
  scanState.import.result = {
    job_id: 'imp-1',
    scan_job_id: 'job-1',
    summary: { imported: 1, failed: 1 },
    items: [
      { candidate_id: 'ready-1', status: 'failed', title: 'The Guardian', error: 'permission denied' },
      { candidate_id: 'ready-2', status: 'imported', title: 'Other Book' },
    ],
  };
  const calls = [];
  const context = createContext({
    state: { libraryImport: libraryImportState({ completed: true, scan: scanState }) },
    apiJson: async (url, options = {}) => {
      calls.push({ url, method: options.method || 'GET', body: options.body || '' });
      if (url === '/api/v1/library/import') return { job_id: 'imp-retry' };
      if (url === '/api/v1/library/import/imp-retry') return { id: 'imp-retry', status: 'completed', progress: { total: 1, imported: 1 } };
      if (url === '/api/v1/library/import/imp-retry/results') return { job_id: 'imp-retry', scan_job_id: 'job-1', summary: { imported: 1, failed: 0 }, items: [] };
      if (url === '/api/v1/library/scan/job-1/results') return importedScanResult();
      throw new Error(`unexpected ${url}`);
    },
  });

  await context.retryFailedLibraryImport();

  assert.equal(calls[0].url, '/api/v1/library/import');
  assert.match(calls[0].body, /"candidate_ids":\["ready-1"\]/);
  assert.doesNotMatch(calls[0].body, /ready-2/);
});

test('renderDiagnosticResult shows staged actionable diagnostics', () => {
  const context = createContext();
  const html = context.renderDiagnosticResult({
    service: 'prowlarr',
    status: 'failed',
    success: false,
    duration_ms: 128,
    summary: 'HTTP 401 Unauthorized',
    steps: [
      { name: 'DNS Lookup', status: 'success', duration_ms: 3, message: 'Resolved 1 address.' },
      { name: 'Authentication', status: 'failed', message: 'HTTP 401 Unauthorized', suggestion: 'Verify API key.' },
    ],
  });

  assert.match(html, /HTTP 401 Unauthorized/);
  assert.match(html, /DNS Lookup/);
  assert.match(html, /Authentication/);
  assert.match(html, /Suggestion: Verify API key\./);
  assert.match(html, /128 ms/);
});

test('testConnection posts current qBittorrent settings and renders diagnostics', async () => {
  const elements = {
    'setting-qb_url': { value: 'https://qb.example' },
    'setting-qb_user': { value: 'admin' },
    'setting-qb_pass': { value: 'secret' },
    'test-qbittorrent-status': { textContent: '', className: '' },
    'diagnostic-qbittorrent-result': { innerHTML: '' },
    'diagnostic-qbittorrent': { querySelector: () => ({ disabled: false }) },
  };
  const calls = [];
  const context = createContext({
    document: { getElementById: id => elements[id] || null },
    apiJson: async (url, options = {}) => {
      calls.push({ url, method: options.method, body: options.body });
      return {
        service: 'qbittorrent',
        status: 'connected',
        success: true,
        duration_ms: 84,
        summary: 'Connected',
        steps: [{ name: 'API Version', status: 'success', message: 'v5.0.0' }],
      };
    },
  });

  await context.testConnection('qbittorrent');

  assert.equal(calls[0].url, '/api/test/qbittorrent');
  assert.equal(calls[0].method, 'POST');
  assert.deepEqual(JSON.parse(calls[0].body), {
    url: 'https://qb.example',
    username: 'admin',
    password: 'secret',
  });
  assert.equal(elements['test-qbittorrent-status'].textContent, 'Connected');
  assert.match(elements['diagnostic-qbittorrent-result'].innerHTML, /API Version/);
});

test('diagnosticPayload reads current unsaved Prowlarr form values', () => {
  const elements = {
    'setting-prowlarr_url': { value: 'http://unsaved-prowlarr:9697' },
    'setting-prowlarr_api_key': { value: 'unsaved-key' },
  };
  const context = createContext({
    document: { getElementById: id => elements[id] || null },
  });

  assert.deepEqual(JSON.parse(JSON.stringify(context.diagnosticPayload('prowlarr'))), {
    url: 'http://unsaved-prowlarr:9697',
    api_key: 'unsaved-key',
  });
});

function sampleScanResult() {
  return {
    job_id: 'job-1',
    status: 'completed',
    totals: {
      found: 2,
      ready_to_import: 1,
      manual_review: 0,
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
        destination_path: '/books/Carla Jablonski/The Guardian.epub',
        metadata: { source: 'embedded_metadata', confidence: 'high', title: 'The Guardian', author: 'Carla Jablonski' },
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
        metadata: { source: 'filename_fallback', confidence: 'medium' },
        duplicate: { reason: 'Identical hash', existing_title: 'Already There', existing_author: 'Jane Doe', existing_format: 'mobi', existing_path: '/library/dup.mobi' },
        classification_reason: 'Identical hash',
      },
    ],
  };
}

function importedScanResult() {
  return {
    job_id: 'job-1',
    status: 'completed',
    totals: {
      found: 2,
      ready_to_import: 0,
      manual_review: 0,
      duplicates: 1,
      already_imported: 1,
      unsupported: 0,
      unreadable: 0,
    },
    candidates: [
      {
        id: 'ready-1',
        classification: 'already_imported',
        title: 'The Guardian',
        author: 'Carla Jablonski',
        filename: 'guardian.epub',
        format: 'epub',
        media_type: 'ebook',
        path: '/books/guardian.epub',
        existing_path: '/books/guardian.epub',
        metadata: { source: 'embedded_metadata', title: 'The Guardian', author: 'Carla Jablonski' },
        classification_reason: 'Imported into library',
      },
      sampleScanResult().candidates[1],
    ],
  };
}

function manualReviewScanResult() {
  return {
    job_id: 'job-1',
    status: 'completed',
    totals: {
      found: 1,
      ready_to_import: 0,
      manual_review: 1,
      duplicates: 0,
      already_imported: 0,
      unsupported: 0,
      unreadable: 0,
    },
    candidates: [
      {
        id: 'review-1',
        classification: 'manual_review',
        title: 'Prince Of Persia',
        author: 'Disney Book Group',
        filename: 'guardian.mobi',
        format: 'mobi',
        media_type: 'ebook',
        path: '/books/guardian.mobi',
        destination_path: '/books/ebooks/Disney Book Group/Prince Of Persia.mobi',
        metadata: { source: 'filename_fallback', confidence: 'medium', title: 'Prince Of Persia', author: 'Disney Book Group' },
        classification_reason: 'Existing title match did not cleanly agree on author',
        manual_review: {
          reason: 'Existing title match did not cleanly agree on author',
          suggested_destination: '/books/ebooks/Disney Book Group/Prince Of Persia.mobi',
          metadata_source: 'filename_fallback',
          confidence: 'medium',
        },
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
