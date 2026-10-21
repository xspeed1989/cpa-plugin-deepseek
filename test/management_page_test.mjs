import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const source = readFileSync(new URL('../management_page.go', import.meta.url), 'utf8');
const script = source.match(/<script>([\s\S]*?)<\/script>/)[1];
const origin = 'https://cpa.example.test';
const host = new URL(origin).host;
const userAgent = 'management-page-test';

// Mirrors the panel's encryption.ts + secureStorage.ts serialization.
// Auth is Zustand persist's {state, version} under STORAGE_KEY_AUTH.
function obfuscate(text) {
  const key = Buffer.from('cli-proxy-api-webui::secure-storage|' + host + '|' + userAgent);
  const bytes = Buffer.from(text);
  return 'enc::v1::' + Buffer.from(bytes.map((byte, i) => byte ^ key[i % key.length])).toString('base64');
}

function encode(value) {
  return obfuscate(JSON.stringify(value));
}

async function openPage(entries, options = {}) {
  const storage = new Map(Object.entries(entries));
  const elements = new Map();
  function element() {
    return {
      value: '', textContent: '', className: '', style: {},
      addEventListener() {}, appendChild() {}, focus() {},
    };
  }
  const response = options.response ?? { ok: true, body: { keys: [] } };
  const requests = [];
  vm.runInNewContext(script, {
    document: {
      getElementById(id) {
        if (!elements.has(id)) elements.set(id, element());
        return elements.get(id);
      },
      createElement: element,
    },
    window: {
      location: {
        protocol: 'https:', host, hostname: 'cpa.example.test', port: '', origin,
        href: origin + '/v0/resource/plugins/deepseek-cpa/credentials',
      },
    },
    navigator: { userAgent },
    localStorage: {
      getItem: (key) => storage.get(key) ?? null,
      setItem: (key, value) => storage.set(key, value),
      removeItem: (key) => storage.delete(key),
    },
    TextEncoder, TextDecoder, Uint8Array, atob, URL,
    fetch: async (path, init) => {
      requests.push({ path, options: init });
      return {
        ok: response.ok,
        status: response.status ?? (response.ok ? 200 : 401),
        text: async () => JSON.stringify(response.body ?? {}),
      };
    },
  });
  await new Promise(setImmediate);
  return { requests, elements, storage };
}

function rememberedAuth(overrides = {}) {
  return encode({ state: {
    apiBase: origin, managementKey: 'test-management-key', rememberPassword: true,
    ...overrides,
  }, version: 0 });
}

test('remembered panel login automatically loads keys without asking for password', async () => {
  const { requests, elements, storage } = await openPage({ 'cli-proxy-auth': rememberedAuth() });
  assert.equal(requests.length, 1, 'remembered login must trigger credential loading');
  assert.equal(requests[0].options.headers['X-Management-Key'], 'test-management-key');
  assert.equal(elements.get('mgmt-form').style.display, 'none');
  assert.match(elements.get('mgmt-note').textContent, /面板/);
  assert.equal(storage.has('deepseek-cpa:management-key'), false, 'do not duplicate panel secrets');
});

test('current panel record wins over stale legacy and plugin copies', async () => {
  const { requests } = await openPage({
    'cli-proxy-auth': rememberedAuth(),
    managementKey: encode('legacy-stale-key'),
    'deepseek-cpa:management-key': 'plugin-stale-key',
  });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].options.headers['X-Management-Key'], 'test-management-key');
});

test('panel apiBase on the same origin root with trailing slashes is accepted', async () => {
  const { requests } = await openPage({
    'cli-proxy-auth': rememberedAuth({ apiBase: origin + '///' }),
  });
  assert.equal(requests[0].options.headers['X-Management-Key'], 'test-management-key');
});

test('existing legacy remembered key still loads', async () => {
  const { requests } = await openPage({ managementKey: encode('legacy-test-key') });
  assert.equal(requests[0].options.headers['X-Management-Key'], 'legacy-test-key');
});

test('plugin-local saved key still loads without a panel record', async () => {
  const { requests, elements } = await openPage({ 'deepseek-cpa:management-key': 'plugin-saved-key' });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].options.headers['X-Management-Key'], 'plugin-saved-key');
  assert.equal(elements.get('mgmt-form').style.display, 'none');
  assert.match(elements.get('mgmt-note').textContent, /本机/);
});

test('panel logout does not disable the plugin-local saved key', async () => {
  const { requests, elements } = await openPage({
    'cli-proxy-auth': encode({ state: {
      apiBase: '', managementKey: '', rememberPassword: true,
    }, version: 0 }),
    'deepseek-cpa:management-key': 'plugin-saved-key',
  });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].options.headers['X-Management-Key'], 'plugin-saved-key');
  assert.equal(elements.get('mgmt-form').style.display, 'none');
});

test('without a saved password the form stays visible and no request is made', async () => {
  const { requests, elements } = await openPage({});
  assert.equal(requests.length, 0);
  assert.equal(elements.get('mgmt-form').style.display, '');
});

test('panel login without rememberPassword does not revive a legacy key', async () => {
  const { requests, elements } = await openPage({
    'cli-proxy-auth': encode({ state: {
      apiBase: origin, managementKey: 'not-remembered-key', rememberPassword: false,
    }, version: 0 }),
    managementKey: encode('legacy-test-key'),
  });
  assert.equal(requests.length, 0);
  assert.equal(elements.get('mgmt-form').style.display, '');
});

test('logged-out panel state does not revive a legacy key', async () => {
  const { requests, elements } = await openPage({
    'cli-proxy-auth': encode({ state: {
      apiBase: '', managementKey: '', rememberPassword: false,
    }, version: 0 }),
    managementKey: encode('legacy-test-key'),
  });
  assert.equal(requests.length, 0);
  assert.equal(elements.get('mgmt-form').style.display, '');
});

test('logout keeps rememberPassword but clears server and key, so no legacy read', async () => {
  const { requests, elements } = await openPage({
    'cli-proxy-auth': encode({ state: {
      apiBase: '', managementKey: '', rememberPassword: true,
    }, version: 0 }),
    managementKey: encode('legacy-test-key'),
  });
  assert.equal(requests.length, 0);
  assert.equal(elements.get('mgmt-form').style.display, '');
});

const unusableRecords = [
  ['empty string', ''],
  ['raw text', 'not-obfuscated'],
  ['invalid JSON', obfuscate('not json')],
  ['decoded string', encode('legacy-test-key')],
  ['decoded array', encode([])],
  ['missing state', encode({ version: 0 })],
  ['state is a string', encode({ state: 'nope', version: 0 })],
  ['missing rememberPassword', encode({ state: {
    apiBase: origin, managementKey: 'panel-key',
  }, version: 0 })],
  ['managementKey is not a string', encode({ state: {
    apiBase: origin, managementKey: 42, rememberPassword: true,
  }, version: 0 })],
  ['managementKey is missing', encode({ state: {
    apiBase: origin, rememberPassword: true,
  }, version: 0 })],
  ['apiBase is missing', encode({ state: {
    managementKey: 'panel-key', rememberPassword: true,
  }, version: 0 })],
  ['apiBase is not a string', encode({ state: {
    apiBase: { origin }, managementKey: 'panel-key', rememberPassword: true,
  }, version: 0 })],
  ['foreign host', rememberedAuth({ apiBase: 'https://other.example.test' })],
  ['different scheme', rememberedAuth({ apiBase: 'http://cpa.example.test' })],
  ['different port', rememberedAuth({ apiBase: 'https://cpa.example.test:8443' })],
  ['path prefix', rememberedAuth({ apiBase: origin + '/cpa' })],
  ['userinfo', rememberedAuth({ apiBase: 'https://user:pass@cpa.example.test' })],
  ['query', rememberedAuth({ apiBase: origin + '/?x=1' })],
  ['fragment', rememberedAuth({ apiBase: origin + '/#x' })],
];

for (const [name, record] of unusableRecords) {
  test(`unusable panel record (${name}) falls back to manual input, not the legacy key`, async () => {
    const { requests, elements } = await openPage({
      'cli-proxy-auth': record,
      managementKey: encode('legacy-test-key'),
    });
    assert.equal(requests.length, 0);
    assert.equal(elements.get('mgmt-form').style.display, '');
  });
}

test('rejected management key brings the form back', async () => {
  const { requests, elements } = await openPage(
    { 'cli-proxy-auth': rememberedAuth() },
    { response: { ok: false, status: 401, body: { error: 'unauthorized' } } },
  );
  assert.equal(requests.length, 1);
  assert.equal(requests[0].options.headers['X-Management-Key'], 'test-management-key');
  assert.equal(elements.get('mgmt-form').style.display, '');
  assert.match(elements.get('mgmt-note').textContent, /请重新输入/);
  assert.match(elements.get('status').textContent, /unauthorized/);
});
