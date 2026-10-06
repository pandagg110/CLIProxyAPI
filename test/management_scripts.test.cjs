// Run with: node --test test/management_scripts.test.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

function browser(script, status) {
  const nodes = [];
  function element() {
    const node = {
      style: {}, dataset: {}, hidden: false, listeners: {},
      appendChild() {}, setAttribute() {}, replaceChildren() {}, focus() {},
      addEventListener(type, callback) { this.listeners[type] = callback; },
    };
    nodes.push(node);
    return node;
  }
  const calls = [];
  const window = {
    location: { href: 'https://example.test/proxy/management.html', hash: '' },
    addEventListener() {},
    fetch(url, options) {
      url = url instanceof Request ? url.url : String(url);
      calls.push({ url, options });
      // Keep panel requests pending; these tests only exercise authentication and routing.
      if (!String(url).endsWith('/config')) return new Promise(() => {});
      return Promise.resolve({ status });
    },
  };
  class XMLHttpRequest {
    open(method, url) { this.url = url; }
    setRequestHeader() {}
    addEventListener(type, callback) { (this.callbacks ||= []).push(callback); }
    send() { this.status = status; for (const callback of this.callbacks || []) callback(); }
  }
  const context = {
    window, URL, Headers, Request, XMLHttpRequest, Promise,
    document: {
      head: element(), body: element(), createElement: element,
      getElementById(id) { return nodes.find(node => node.id === id); },
      addEventListener() {},
    },
  };
  vm.createContext(context);
  for (const file of [].concat(script)) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, '../internal/managementasset', file), 'utf8'), context);
  }
  return { window, XMLHttpRequest, calls, nodes };
}

for (const [script, button, endpoint] of [
  ['request_log_usage.js', 'cpa-request-log-usage-button', '/request-log-usage'],
  ['log_qa.js', 'cpa-log-qa-button', '/log-qa/status'],
]) {
  for (const version of ['v0', 'v8']) {
    for (const transport of ['fetch', 'xhr']) {
      test(`${script}: ${version} ${transport} login displays button and preserves API root`, async () => {
        const env = browser(script, 200);
        const root = `https://example.test/proxy/${version}/management`;
        assert.equal(env.nodes.some(node => node.id === button), false);
        if (transport === 'fetch') {
          await env.window.fetch(new Request(root + '/config', { headers: { Authorization: 'Bearer test' } }));
        } else {
          const xhr = new env.XMLHttpRequest();
          xhr.open('GET', root + '/config');
          xhr.setRequestHeader('X-Management-Key', 'test');
          xhr.send();
        }
        const launcher = env.nodes.find(node => node.id === button);
        assert.ok(launcher);
        assert.equal(launcher.hidden, false);
        launcher.listeners.click();
        const request = env.calls.find(call => call.url === root + endpoint);
        assert.ok(request, 'panel must use the detected API version and proxy prefix');
        assert.equal(request.options.headers[transport === 'fetch' ? 'Authorization' : 'X-Management-Key'], transport === 'fetch' ? 'Bearer test' : 'test');
      });
    }
  }
  for (const [prefix, status, headers] of [
    ['/v8/management', 401, { Authorization: 'Bearer test' }],
    ['/v8/management', 200, {}],
    ['/v8/management-other', 200, { Authorization: 'Bearer test' }],
    ['/v9/management', 200, { Authorization: 'Bearer test' }],
  ]) {
    test(`${script}: ignores ${prefix} status ${status} headers ${Object.keys(headers)}`, async () => {
      const env = browser(script, status);
      await env.window.fetch(prefix + '/config', { headers });
      assert.equal(env.nodes.some(node => node.id === button), false);
    });
  }
}

for (const transport of ['fetch', 'xhr']) {
  test(`both injected scripts coexist with v8 ${transport}`, async () => {
    const env = browser(['log_qa.js', 'request_log_usage.js'], 200);
    if (transport === 'fetch') {
      await env.window.fetch('/v8/management/config', { headers: { Authorization: 'Bearer test' } });
    } else {
      const xhr = new env.XMLHttpRequest();
      xhr.open('GET', '/v8/management/config');
      xhr.setRequestHeader('Authorization', 'Bearer test');
      xhr.send();
    }
    for (const id of ['cpa-request-log-usage-button', 'cpa-log-qa-button']) {
      assert.equal(env.nodes.find(node => node.id === id)?.hidden, false);
    }
  });
}
