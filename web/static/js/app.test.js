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
  extractFunctionSource('libraryImportSummaryLines'),
  extractFunctionSource('setLibraryImportStep2State'),
  extractFunctionSource('saveLibraryImportSettings'),
].join('\n\n');

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
    document: { getElementById: () => null },
    apiJson: async () => ({ success: true }),
    showToast: () => {},
    scrollToSettingsSection: () => {},
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
  assert.match(indexHTML, /Scan Library/);
  assert.match(indexHTML, /Library scanning will be available in the next release\./);
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

test('saveLibraryImportSettings uses existing settings save path', async () => {
  const elements = {
    'setting-incoming_dir': { value: '/downloads' },
    'setting-ebook_dir': { value: '/books' },
    'setting-audiobook_dir': { value: '/audiobooks' },
    'setting-manga_dir': { value: '/manga' },
    'setting-file_org_enabled': { checked: true },
    'settings-library-import-step2': { dataset: {}, classList: fakeClassList(), focus() {} },
    'settings-library-import-step2-icon': { textContent: '', className: '' },
    'settings-library-import-step2-copy': { textContent: '' },
    'settings-library-import-summary': { innerHTML: '', classList: fakeClassList(['hidden']) },
    'settings-library-import-save-continue': { classList: fakeClassList() },
    'settings-library-import-complete': { classList: fakeClassList(['hidden']) },
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
    'settings-library-import-step2': Object.assign(step2, { dataset: {}, classList: fakeClassList() }),
    'settings-library-import-step2-icon': { textContent: '', className: '' },
    'settings-library-import-step2-copy': { textContent: '' },
    'settings-library-import-summary': { innerHTML: '', classList: fakeClassList(['hidden']) },
    'settings-library-import-save-continue': { classList: fakeClassList() },
    'settings-library-import-complete': { classList: fakeClassList(['hidden']) },
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
});

test('failed Save & Continue shows an error and does not advance', async () => {
  const events = [];
  const elements = {
    'setting-incoming_dir': { value: '/downloads' },
    'setting-ebook_dir': { value: '/books' },
    'setting-audiobook_dir': { value: '/audiobooks' },
    'setting-manga_dir': { value: '/manga' },
    'setting-file_org_enabled': { checked: false },
    'settings-library-import-step2': { dataset: {}, classList: fakeClassList(), focus: () => events.push('focus') },
    'settings-library-import-step2-icon': { textContent: '', className: '' },
    'settings-library-import-step2-copy': { textContent: '' },
    'settings-library-import-summary': { innerHTML: 'old', classList: fakeClassList() },
    'settings-library-import-save-continue': { classList: fakeClassList() },
    'settings-library-import-complete': { classList: fakeClassList(['hidden']) },
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
