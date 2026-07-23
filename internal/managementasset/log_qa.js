(function () {
  'use strict';

  var INSTALL_FLAG = '__cpaLogQAInstalled';
  var MANAGEMENT_PREFIX = '/v0/management';
  var STATUS_ENDPOINT = '/v0/management/log-qa/status';
  var SUMMARY_ENDPOINT = '/v0/management/log-qa/summary';
  var SESSIONS_ENDPOINT = '/v0/management/log-qa/sessions';
  var AUTHORIZATION = 'authorization';
  var MANAGEMENT_KEY = 'x-management-key';

  if (window[INSTALL_FLAG]) {
    return;
  }
  window[INSTALL_FLAG] = true;

  var nativeFetch = typeof window.fetch === 'function' ? window.fetch : null;
  var capturedAuth = null;
  var ui = null;
  var previousBodyOverflow = '';

  function managementTarget(input) {
    var raw = input;
    if (typeof Request !== 'undefined' && input instanceof Request) {
      raw = input.url;
    }
    if (raw === null || raw === undefined) {
      return null;
    }
    try {
      var url = new URL(String(raw), window.location.href);
      var prefixIndex = url.pathname.lastIndexOf(MANAGEMENT_PREFIX);
      if (prefixIndex < 0) {
        return null;
      }
      var suffix = url.pathname.slice(prefixIndex + MANAGEMENT_PREFIX.length);
      if (suffix && suffix.charAt(0) !== '/') {
        return null;
      }
      return {
        url: url,
        apiRoot: url.origin + url.pathname.slice(0, prefixIndex) + MANAGEMENT_PREFIX,
      };
    } catch (_) {
      return null;
    }
  }

  function normalizeHeader(name, value) {
    var lowered = String(name || '').trim().toLowerCase();
    if (lowered !== AUTHORIZATION && lowered !== MANAGEMENT_KEY) {
      return null;
    }
    var text = String(value || '').trim();
    if (!text) {
      return null;
    }
    return {
      name: lowered === AUTHORIZATION ? 'Authorization' : 'X-Management-Key',
      value: text,
    };
  }

  function authFromHeaders(headers) {
    if (!headers) {
      return null;
    }
    if (typeof Headers !== 'undefined') {
      try {
        var normalized = headers instanceof Headers ? headers : new Headers(headers);
        var authorization = normalizeHeader('Authorization', normalized.get('Authorization'));
        if (authorization) {
          return authorization;
        }
        return normalizeHeader('X-Management-Key', normalized.get('X-Management-Key'));
      } catch (_) {}
    }
    if (Array.isArray(headers)) {
      for (var i = 0; i < headers.length; i += 1) {
        var pair = headers[i];
        if (Array.isArray(pair) && pair.length >= 2) {
          var h = normalizeHeader(pair[0], pair[1]);
          if (h) {
            return h;
          }
        }
      }
    }
    if (typeof headers === 'object') {
      var names = Object.keys(headers);
      for (var j = 0; j < names.length; j += 1) {
        var oh = normalizeHeader(names[j], headers[names[j]]);
        if (oh) {
          return oh;
        }
      }
    }
    return null;
  }

  function rememberSuccessfulAuth(target, auth, status) {
    if (!target || !auth || status < 200 || status >= 400) {
      return;
    }
    capturedAuth = {
      apiRoot: target.apiRoot,
      headerName: auth.name,
      headerValue: auth.value,
    };
    ensureUI();
    if (ui) {
      ui.launcher.hidden = false;
    }
  }

  function installFetchInterceptor() {
    if (!nativeFetch) {
      return;
    }
    window.fetch = function (input, init) {
      var target = managementTarget(input);
      var auth = null;
      if (target) {
        auth = authFromHeaders(init && init.headers);
        if (!auth && typeof Request !== 'undefined' && input instanceof Request) {
          auth = authFromHeaders(input.headers);
        }
      }
      var responsePromise = nativeFetch.apply(window, arguments);
      if (target && auth) {
        Promise.resolve(responsePromise).then(
          function (response) {
            rememberSuccessfulAuth(target, auth, Number(response && response.status));
          },
          function () {}
        );
      }
      return responsePromise;
    };
  }

  function installXHRInterceptor() {
    if (typeof XMLHttpRequest === 'undefined') {
      return;
    }
    var prototype = XMLHttpRequest.prototype;
    var nativeOpen = prototype.open;
    var nativeSetRequestHeader = prototype.setRequestHeader;
    var nativeSend = prototype.send;
    var metadata = new WeakMap();
    prototype.open = function () {
      var target = managementTarget(arguments[1]);
      metadata.set(this, { target: target, auth: null });
      return nativeOpen.apply(this, arguments);
    };
    prototype.setRequestHeader = function (name, value) {
      var current = metadata.get(this);
      if (current && current.target) {
        var auth = normalizeHeader(name, value);
        if (auth) {
          current.auth = auth;
        }
      }
      return nativeSetRequestHeader.apply(this, arguments);
    };
    prototype.send = function () {
      var xhr = this;
      var current = metadata.get(xhr);
      if (current && current.target && current.auth) {
        xhr.addEventListener(
          'loadend',
          function () {
            rememberSuccessfulAuth(current.target, current.auth, Number(xhr.status || 0));
          },
          { once: true }
        );
      }
      return nativeSend.apply(xhr, arguments);
    };
  }

  function element(tag, className, text) {
    var node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text !== undefined && text !== null) {
      node.textContent = String(text);
    }
    return node;
  }

  function addStyles() {
    if (document.getElementById('cpa-log-qa-style')) {
      return;
    }
    var style = document.createElement('style');
    style.id = 'cpa-log-qa-style';
    style.textContent = [
      '#cpa-log-qa-button{position:fixed;right:0;top:calc(50% + 90px);z-index:2147483000;border:1px solid color-mix(in srgb,var(--primary-color,#2563eb) 72%,#fff);border-right:0;border-radius:12px 0 0 12px;padding:13px 9px;background:#0f766e;color:#fff;font:650 12px/1.2 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;letter-spacing:.08em;writing-mode:vertical-rl;box-shadow:0 10px 28px rgba(0,0,0,.2);cursor:pointer;transform:translateY(-50%)}',
      '#cpa-log-qa-overlay[hidden]{display:none!important}',
      '#cpa-log-qa-overlay{position:fixed;inset:0;z-index:2147483001;display:flex;align-items:center;justify-content:center;padding:20px;background:rgba(8,12,20,.58);backdrop-filter:blur(3px);font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}',
      '.cpa-lqa-panel{display:flex;flex-direction:column;width:min(1100px,100%);max-height:min(88vh,900px);overflow:hidden;border:1px solid var(--border-color,#d8dee9);border-radius:16px;background:var(--bg-primary,#fff);color:var(--text-primary,#172033);box-shadow:0 28px 80px rgba(0,0,0,.32)}',
      '.cpa-lqa-header{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;padding:20px 22px;border-bottom:1px solid var(--border-color,#d8dee9)}',
      '.cpa-lqa-title{margin:0;font-size:21px;font-weight:750}',
      '.cpa-lqa-subtitle{margin:5px 0 0;color:var(--text-secondary,#64748b);font-size:13px;line-height:1.5}',
      '.cpa-lqa-actions{display:flex;gap:8px}',
      '.cpa-lqa-button,.cpa-lqa-close{border:1px solid var(--border-color,#d8dee9);border-radius:9px;background:var(--bg-secondary,#f5f7fa);color:var(--text-primary,#172033);font:600 13px/1.2 inherit;cursor:pointer}',
      '.cpa-lqa-button{padding:8px 12px}.cpa-lqa-close{width:34px;height:34px;font-size:21px}',
      '.cpa-lqa-body{overflow:auto;padding:18px 22px 24px}',
      '.cpa-lqa-status{min-height:20px;margin-bottom:12px;color:var(--text-secondary,#64748b);font-size:13px}',
      '.cpa-lqa-status[data-kind="error"]{padding:10px 12px;border:1px solid #ef444466;border-radius:9px;background:#ef444414;color:#b91c1c}',
      '.cpa-lqa-note{margin:0 0 14px;padding:10px 12px;border-left:3px solid #0f766e;border-radius:6px;background:color-mix(in srgb,#0f766e 10%,transparent);color:var(--text-secondary,#64748b);font-size:12px;line-height:1.6}',
      '.cpa-lqa-summary{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px;margin-bottom:14px}',
      '.cpa-lqa-card{padding:12px 14px;border:1px solid var(--border-color,#d8dee9);border-radius:12px;background:var(--bg-secondary,#f8fafc)}',
      '.cpa-lqa-card span{display:block}.cpa-lqa-card .label{color:var(--text-secondary,#64748b);font-size:12px}.cpa-lqa-card .value{margin-top:5px;font-size:18px;font-weight:750;font-variant-numeric:tabular-nums}',
      '.cpa-lqa-filters{display:flex;flex-wrap:wrap;gap:8px;margin:0 0 12px}',
      '.cpa-lqa-filters select,.cpa-lqa-filters input{border:1px solid var(--border-color,#d8dee9);border-radius:8px;padding:7px 10px;background:var(--bg-primary,#fff);color:inherit}',
      '.cpa-lqa-table{width:100%;border-collapse:collapse;font-size:12px}',
      '.cpa-lqa-table th,.cpa-lqa-table td{border-bottom:1px solid var(--border-color,#d8dee9);padding:8px 6px;text-align:left;vertical-align:top}',
      '.cpa-lqa-table th{color:var(--text-secondary,#64748b);font-weight:650}',
      '.cpa-lqa-fail{color:#b91c1c}.cpa-lqa-pass{color:#047857}',
      '.cpa-lqa-mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;word-break:break-all}',
    ].join('');
    document.head.appendChild(style);
  }

  function ensureUI() {
    if (ui) {
      return ui;
    }
    addStyles();
    var launcher = element('button', '', 'LOG QA');
    launcher.id = 'cpa-log-qa-button';
    launcher.type = 'button';
    launcher.hidden = true;
    launcher.addEventListener('click', openOverlay);

    var overlay = element('div', '');
    overlay.id = 'cpa-log-qa-overlay';
    overlay.hidden = true;

    var panel = element('div', 'cpa-lqa-panel');
    var header = element('div', 'cpa-lqa-header');
    var titles = element('div', '');
    titles.appendChild(element('h2', 'cpa-lqa-title', 'Log QA'));
    titles.appendChild(
      element(
        'p',
        'cpa-lqa-subtitle',
        'Local unuploaded request-log quality check (does not block uploader)'
      )
    );
    var actions = element('div', 'cpa-lqa-actions');
    var refresh = element('button', 'cpa-lqa-button', 'Refresh');
    refresh.type = 'button';
    refresh.addEventListener('click', function () {
      loadData();
    });
    var close = element('button', 'cpa-lqa-close', '×');
    close.type = 'button';
    close.addEventListener('click', closeOverlay);
    actions.appendChild(refresh);
    actions.appendChild(close);
    header.appendChild(titles);
    header.appendChild(actions);

    var body = element('div', 'cpa-lqa-body');
    var status = element('div', 'cpa-lqa-status');
    var content = element('div', '');
    body.appendChild(status);
    body.appendChild(content);
    panel.appendChild(header);
    panel.appendChild(body);
    overlay.appendChild(panel);
    overlay.addEventListener('click', function (event) {
      if (event.target === overlay) {
        closeOverlay();
      }
    });

    document.body.appendChild(launcher);
    document.body.appendChild(overlay);
    ui = {
      launcher: launcher,
      overlay: overlay,
      status: status,
      content: content,
      refresh: refresh,
      statusFilter: 'fail',
      reasonFilter: '',
      query: '',
    };
    return ui;
  }

  function openOverlay() {
    ensureUI();
    previousBodyOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    ui.overlay.hidden = false;
    loadData();
  }

  function closeOverlay() {
    if (!ui) {
      return;
    }
    ui.overlay.hidden = true;
    document.body.style.overflow = previousBodyOverflow;
  }

  function apiURL(path) {
    if (!capturedAuth) {
      return path;
    }
    return capturedAuth.apiRoot.replace(/\/v0\/management$/, '') + path;
  }

  function authedFetch(path) {
    var headers = {};
    if (capturedAuth) {
      headers[capturedAuth.headerName] = capturedAuth.headerValue;
    }
    return nativeFetch(apiURL(path), { headers: headers }).then(function (response) {
      if (!response.ok) {
        throw new Error('HTTP ' + response.status);
      }
      return response.json();
    });
  }

  function pct(rate) {
    if (typeof rate !== 'number' || isNaN(rate)) {
      return '-';
    }
    return (rate * 100).toFixed(1) + '%';
  }

  function renderEmpty(message) {
    ui.content.replaceChildren();
    ui.content.appendChild(element('p', 'cpa-lqa-note', message || 'No QA report yet. Start the log-qa service.'));
  }

  function render(summaryPayload, sessionsPayload) {
    ui.content.replaceChildren();
    ui.content.appendChild(
      element(
        'p',
        'cpa-lqa-note',
        'Only checks local unuploaded .log files. Uploaded/deleted logs are not included. QA never blocks or modifies uploader.'
      )
    );

    if (!summaryPayload || !summaryPayload.has_report || !summaryPayload.summary) {
      renderEmpty((summaryPayload && summaryPayload.message) || 'No QA report yet.');
      return;
    }

    var s = summaryPayload.summary;
    var cards = element('div', 'cpa-lqa-summary');
    function card(label, value) {
      var node = element('div', 'cpa-lqa-card');
      node.appendChild(element('span', 'label', label));
      node.appendChild(element('span', 'value', value));
      cards.appendChild(node);
    }
    card('Pass rate', pct(s.pass_rate));
    card('Sessions', String(s.sessions_total || 0));
    card('Pass', String(s.sessions_pass || 0));
    card('Fail', String(s.sessions_fail || 0));
    card('Files scanned', String(s.files_scanned || 0));
    card('Partial run', s.partial ? 'yes' : 'no');
    ui.content.appendChild(cards);

    var hist = s.fail_reason_hist || {};
    ui.content.appendChild(
      element(
        'p',
        'cpa-lqa-status',
        'Fail reasons — prompt_rounds: ' +
          (hist.prompt_rounds || 0) +
          ', no_tool_call: ' +
          (hist.no_tool_call || 0) +
          ', duplicate_assistant: ' +
          (hist.duplicate_assistant || 0) +
          ' | run: ' +
          (s.run_id || '-')
      )
    );

    var filters = element('div', 'cpa-lqa-filters');
    var statusSelect = document.createElement('select');
    ;['fail', 'pass', 'all'].forEach(function (value) {
      var opt = document.createElement('option');
      opt.value = value;
      opt.textContent = value;
      if (value === ui.statusFilter) {
        opt.selected = true;
      }
      statusSelect.appendChild(opt);
    });
    statusSelect.addEventListener('change', function () {
      ui.statusFilter = statusSelect.value;
      loadData();
    });
    var reasonSelect = document.createElement('select');
    ;[
      ['', 'all reasons'],
      ['prompt_rounds', 'prompt_rounds'],
      ['no_tool_call', 'no_tool_call'],
      ['duplicate_assistant', 'duplicate_assistant'],
    ].forEach(function (pair) {
      var opt = document.createElement('option');
      opt.value = pair[0];
      opt.textContent = pair[1];
      if (pair[0] === ui.reasonFilter) {
        opt.selected = true;
      }
      reasonSelect.appendChild(opt);
    });
    reasonSelect.addEventListener('change', function () {
      ui.reasonFilter = reasonSelect.value;
      loadData();
    });
    var search = document.createElement('input');
    search.type = 'search';
    search.placeholder = 'session / key';
    search.value = ui.query || '';
    search.addEventListener('change', function () {
      ui.query = search.value;
      loadData();
    });
    filters.appendChild(statusSelect);
    filters.appendChild(reasonSelect);
    filters.appendChild(search);
    ui.content.appendChild(filters);

    var table = element('table', 'cpa-lqa-table');
    var thead = document.createElement('thead');
    var headRow = document.createElement('tr');
    ;['status', 'session_id', 'prompts', 'tools', 'dup', 'reasons', 'keys'].forEach(function (h) {
      headRow.appendChild(element('th', '', h));
    });
    thead.appendChild(headRow);
    table.appendChild(thead);
    var tbody = document.createElement('tbody');
    var sessions = (sessionsPayload && sessionsPayload.sessions) || [];
    if (!sessions.length) {
      var emptyRow = document.createElement('tr');
      var td = element('td', '', 'No sessions for this filter');
      td.colSpan = 7;
      emptyRow.appendChild(td);
      tbody.appendChild(emptyRow);
    }
    sessions.forEach(function (row) {
      var tr = document.createElement('tr');
      tr.appendChild(element('td', row.ok ? 'cpa-lqa-pass' : 'cpa-lqa-fail', row.ok ? 'pass' : 'fail'));
      tr.appendChild(element('td', 'cpa-lqa-mono', row.session_id || ''));
      tr.appendChild(element('td', '', String(row.prompt_rounds)));
      tr.appendChild(element('td', '', String(row.tool_calls)));
      tr.appendChild(element('td', '', String(row.dup_assistant_groups)));
      tr.appendChild(element('td', '', (row.fail_reasons || []).join(', ')));
      tr.appendChild(element('td', '', (row.key_names || []).join(', ')));
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    ui.content.appendChild(table);
    if (sessionsPayload && typeof sessionsPayload.total === 'number') {
      ui.content.appendChild(
        element('p', 'cpa-lqa-status', 'Showing ' + sessions.length + ' of ' + sessionsPayload.total + ' matched sessions')
      );
    }
  }

  function loadData() {
    if (!capturedAuth) {
      ui.status.textContent = 'Sign in to Management first, then open Log QA again.';
      ui.status.dataset.kind = 'error';
      return;
    }
    ui.status.textContent = 'Loading…';
    delete ui.status.dataset.kind;
    ui.refresh.disabled = true;

    var sessionsQuery =
      SESSIONS_ENDPOINT +
      '?status=' +
      encodeURIComponent(ui.statusFilter || 'fail') +
      '&limit=50' +
      (ui.reasonFilter ? '&reason=' + encodeURIComponent(ui.reasonFilter) : '') +
      (ui.query ? '&q=' + encodeURIComponent(ui.query) : '');

    Promise.all([authedFetch(SUMMARY_ENDPOINT), authedFetch(sessionsQuery), authedFetch(STATUS_ENDPOINT)])
      .then(function (parts) {
        ui.refresh.disabled = false;
        ui.status.textContent = parts[2] && parts[2].message ? parts[2].message : 'ok';
        delete ui.status.dataset.kind;
        render(parts[0], parts[1]);
      })
      .catch(function (err) {
        ui.refresh.disabled = false;
        ui.status.textContent = String(err && err.message ? err.message : err);
        ui.status.dataset.kind = 'error';
      });
  }

  installFetchInterceptor();
  installXHRInterceptor();
})();
