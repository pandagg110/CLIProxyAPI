(function () {
  'use strict';

  var INSTALL_FLAG = '__cpaRequestLogUsageInstalled';
  var MANAGEMENT_PREFIX = '/v0/management';
  var USAGE_ENDPOINT = '/v0/management/request-log-usage';
  var AUTHORIZATION = 'authorization';
  var MANAGEMENT_KEY = 'x-management-key';

  if (window[INSTALL_FLAG]) {
    return;
  }
  window[INSTALL_FLAG] = true;

  var nativeFetch = typeof window.fetch === 'function' ? window.fetch : null;
  var capturedAuth = null;
  var authGeneration = 0;
  var loadGeneration = 0;
  var activeRequest = null;
  var previousBodyOverflow = '';
  var ui = null;

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
      } catch (_) {
        // Fall through for header containers that cannot be converted to Headers.
      }
    }

    if (Array.isArray(headers)) {
      for (var arrayIndex = 0; arrayIndex < headers.length; arrayIndex += 1) {
        var pair = headers[arrayIndex];
        if (!Array.isArray(pair) || pair.length < 2) {
          continue;
        }
        var arrayHeader = normalizeHeader(pair[0], pair[1]);
        if (arrayHeader) {
          return arrayHeader;
        }
      }
      return null;
    }

    if (typeof headers === 'object') {
      var names = Object.keys(headers);
      for (var objectIndex = 0; objectIndex < names.length; objectIndex += 1) {
        var name = names[objectIndex];
        var objectHeader = normalizeHeader(name, headers[name]);
        if (objectHeader) {
          return objectHeader;
        }
      }
    }
    return null;
  }

  function rememberSuccessfulAuth(target, auth, status) {
    if (!target || !auth || status < 200 || status >= 400) {
      return;
    }
    var nextAuth = {
      apiRoot: target.apiRoot,
      headerName: auth.name,
      headerValue: auth.value,
    };
    var changed =
      !capturedAuth ||
      capturedAuth.apiRoot !== nextAuth.apiRoot ||
      capturedAuth.headerName !== nextAuth.headerName ||
      capturedAuth.headerValue !== nextAuth.headerValue;
    capturedAuth = nextAuth;
    if (changed) {
      authGeneration += 1;
      resetUsageUI(false);
    }
    ensureUI();
    if (ui) {
      ui.launcher.hidden = false;
    }
  }

  function resetUsageUI(hideLauncher) {
    loadGeneration += 1;
    if (activeRequest) {
      activeRequest.abort();
      activeRequest = null;
    }
    if (!ui) {
      return;
    }
    if (!ui.overlay.hidden) {
      ui.overlay.hidden = true;
      document.body.style.overflow = previousBodyOverflow;
    }
    ui.loaded = false;
    ui.refresh.disabled = false;
    ui.status.textContent = '';
    delete ui.status.dataset.kind;
    ui.content.replaceChildren();
    ui.launcher.hidden = Boolean(hideLauncher);
  }

  function clearCapturedAuth() {
    capturedAuth = null;
    authGeneration += 1;
    resetUsageUI(true);
  }

  function loginRouteActive() {
    return /^#\/login(?:[/?]|$)/i.test(window.location.hash || '');
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
    if (document.getElementById('cpa-request-log-usage-style')) {
      return;
    }
    var style = document.createElement('style');
    style.id = 'cpa-request-log-usage-style';
    style.textContent = [
      '#cpa-request-log-usage-button{position:fixed;right:20px;bottom:20px;z-index:2147483000;border:0;border-radius:999px;padding:11px 16px;background:var(--primary-color,#2563eb);color:#fff;font:600 14px/1.2 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;box-shadow:0 10px 30px rgba(0,0,0,.22);cursor:pointer;transition:transform .16s ease,box-shadow .16s ease}',
      '#cpa-request-log-usage-button:hover{transform:translateY(-1px);box-shadow:0 13px 34px rgba(0,0,0,.28)}',
      '#cpa-request-log-usage-button:focus-visible,.cpa-rlu-button:focus-visible,.cpa-rlu-close:focus-visible{outline:3px solid color-mix(in srgb,var(--primary-color,#2563eb) 42%,transparent);outline-offset:2px}',
      '#cpa-request-log-usage-overlay[hidden]{display:none!important}',
      '#cpa-request-log-usage-overlay{position:fixed;inset:0;z-index:2147483001;display:flex;align-items:center;justify-content:center;padding:20px;background:rgba(8,12,20,.58);backdrop-filter:blur(3px);font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}',
      '.cpa-rlu-panel{display:flex;flex-direction:column;width:min(1120px,100%);max-height:min(88vh,900px);overflow:hidden;border:1px solid var(--border-color,#d8dee9);border-radius:16px;background:var(--bg-primary,#fff);color:var(--text-primary,#172033);box-shadow:0 28px 80px rgba(0,0,0,.32)}',
      '.cpa-rlu-header{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;padding:20px 22px;border-bottom:1px solid var(--border-color,#d8dee9)}',
      '.cpa-rlu-title{margin:0;font-size:21px;line-height:1.3;font-weight:750}',
      '.cpa-rlu-subtitle{margin:5px 0 0;color:var(--text-secondary,#64748b);font-size:13px;line-height:1.5}',
      '.cpa-rlu-actions{display:flex;align-items:center;gap:8px;flex:none}',
      '.cpa-rlu-button,.cpa-rlu-close{border:1px solid var(--border-color,#d8dee9);border-radius:9px;background:var(--bg-secondary,#f5f7fa);color:var(--text-primary,#172033);font:600 13px/1.2 inherit;cursor:pointer}',
      '.cpa-rlu-button{padding:8px 12px}.cpa-rlu-close{display:grid;place-items:center;width:34px;height:34px;padding:0;font-size:21px}',
      '.cpa-rlu-button:hover,.cpa-rlu-close:hover{border-color:var(--primary-color,#2563eb)}.cpa-rlu-button:disabled{opacity:.55;cursor:wait}',
      '.cpa-rlu-body{overflow:auto;padding:18px 22px 24px}',
      '.cpa-rlu-status{min-height:20px;margin-bottom:12px;color:var(--text-secondary,#64748b);font-size:13px;line-height:1.5}',
      '.cpa-rlu-status[data-kind="error"]{padding:10px 12px;border:1px solid #ef444466;border-radius:9px;background:#ef444414;color:#b91c1c}',
      '.cpa-rlu-status[data-kind="warning"]{padding:10px 12px;border:1px solid #f59e0b66;border-radius:9px;background:#f59e0b14;color:#a16207}',
      '.cpa-rlu-summary{display:grid;grid-template-columns:repeat(5,minmax(0,1fr));gap:10px;margin-bottom:14px}',
      '.cpa-rlu-summary-card,.cpa-rlu-key-card{border:1px solid var(--border-color,#d8dee9);border-radius:12px;background:var(--bg-secondary,#f8fafc)}',
      '.cpa-rlu-summary-card{padding:12px 14px}.cpa-rlu-summary-label{display:block;color:var(--text-secondary,#64748b);font-size:12px}.cpa-rlu-summary-value{display:block;margin-top:5px;font-size:18px;font-weight:750;font-variant-numeric:tabular-nums}',
      '.cpa-rlu-note{margin:0 0 14px;padding:10px 12px;border-left:3px solid var(--primary-color,#2563eb);border-radius:6px;background:color-mix(in srgb,var(--primary-color,#2563eb) 8%,transparent);color:var(--text-secondary,#64748b);font-size:12px;line-height:1.6}',
      '.cpa-rlu-key-list{display:grid;gap:12px}',
      '.cpa-rlu-key-card{padding:15px}',
      '.cpa-rlu-key-head{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:12px}',
      '.cpa-rlu-key-name{min-width:0;overflow-wrap:anywhere;font-size:16px;font-weight:750}',
      '.cpa-rlu-badge{flex:none;border:1px solid var(--border-color,#d8dee9);border-radius:999px;padding:3px 8px;color:var(--text-secondary,#64748b);background:var(--bg-primary,#fff);font-size:11px}',
      '.cpa-rlu-badge[data-configured="true"]{border-color:color-mix(in srgb,var(--primary-color,#2563eb) 45%,var(--border-color,#d8dee9));color:var(--primary-color,#2563eb)}',
      '.cpa-rlu-metrics{display:grid;grid-template-columns:repeat(5,minmax(0,1fr));gap:8px}',
      '.cpa-rlu-metric{min-width:0;padding:9px 10px;border-radius:9px;background:var(--bg-primary,#fff)}',
      '.cpa-rlu-metric-label{display:block;color:var(--text-secondary,#64748b);font-size:11px}.cpa-rlu-metric-value{display:block;margin-top:4px;overflow-wrap:anywhere;font-size:14px;font-weight:700;font-variant-numeric:tabular-nums}',
      '.cpa-rlu-models{display:flex;flex-wrap:wrap;gap:6px;margin-top:10px}.cpa-rlu-model{border:1px solid var(--border-color,#d8dee9);border-radius:999px;padding:3px 8px;background:var(--bg-primary,#fff);color:var(--text-secondary,#64748b);font-size:11px}',
      '.cpa-rlu-details{margin-top:11px}.cpa-rlu-details>summary{cursor:pointer;color:var(--primary-color,#2563eb);font-size:12px;font-weight:650;user-select:none}',
      '.cpa-rlu-table-wrap{margin-top:9px;overflow-x:auto;border:1px solid var(--border-color,#d8dee9);border-radius:9px;background:var(--bg-primary,#fff)}',
      '.cpa-rlu-table{width:100%;border-collapse:collapse;min-width:590px;font-size:12px}.cpa-rlu-table th,.cpa-rlu-table td{padding:8px 10px;border-bottom:1px solid var(--border-color,#d8dee9);text-align:left;white-space:nowrap}.cpa-rlu-table th{color:var(--text-secondary,#64748b);font-weight:650}.cpa-rlu-table tr:last-child td{border-bottom:0}.cpa-rlu-table td:nth-child(n+2),.cpa-rlu-table th:nth-child(n+2){text-align:right}',
      '.cpa-rlu-empty{padding:34px 16px;text-align:center;color:var(--text-secondary,#64748b);font-size:14px}',
      '.cpa-rlu-footer{margin-top:13px;color:var(--text-secondary,#64748b);font-size:11px;line-height:1.5}',
      '@media(prefers-color-scheme:dark){.cpa-rlu-panel{background:var(--bg-primary,#15181e);color:var(--text-primary,#eef2f7);border-color:var(--border-color,#343a46)}.cpa-rlu-summary-card,.cpa-rlu-key-card,.cpa-rlu-button,.cpa-rlu-close{background:var(--bg-secondary,#20242d);border-color:var(--border-color,#343a46)}.cpa-rlu-metric,.cpa-rlu-table-wrap,.cpa-rlu-badge,.cpa-rlu-model{background:var(--bg-primary,#15181e);border-color:var(--border-color,#343a46)}.cpa-rlu-status[data-kind="error"]{color:#fca5a5}.cpa-rlu-status[data-kind="warning"]{color:#fcd34d}}',
      '@media(max-width:760px){#cpa-request-log-usage-button{right:12px;bottom:12px;padding:10px 13px}#cpa-request-log-usage-overlay{align-items:flex-end;padding:0}.cpa-rlu-panel{width:100%;max-height:92vh;border-radius:16px 16px 0 0}.cpa-rlu-header{padding:16px}.cpa-rlu-title{font-size:18px}.cpa-rlu-subtitle{font-size:12px}.cpa-rlu-body{padding:14px 14px 20px}.cpa-rlu-summary{grid-template-columns:repeat(2,minmax(0,1fr))}.cpa-rlu-metrics{grid-template-columns:repeat(2,minmax(0,1fr))}}',
      '@media(max-width:420px){.cpa-rlu-summary,.cpa-rlu-metrics{grid-template-columns:1fr}.cpa-rlu-key-head{align-items:flex-start;flex-direction:column}.cpa-rlu-badge{align-self:flex-start}}',
    ].join('');
    document.head.appendChild(style);
  }

  function buildUI() {
    addStyles();

    var launcher = element('button', '', 'Key 日志用量');
    launcher.id = 'cpa-request-log-usage-button';
    launcher.type = 'button';
    launcher.setAttribute('aria-haspopup', 'dialog');
    launcher.setAttribute('aria-controls', 'cpa-request-log-usage-overlay');

    var overlay = element('div');
    overlay.id = 'cpa-request-log-usage-overlay';
    overlay.hidden = true;

    var panel = element('section', 'cpa-rlu-panel');
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-modal', 'true');
    panel.setAttribute('aria-labelledby', 'cpa-request-log-usage-title');

    var header = element('header', 'cpa-rlu-header');
    var heading = element('div');
    var title = element('h2', 'cpa-rlu-title', '每个 Key 的日志用量');
    title.id = 'cpa-request-log-usage-title';
    var subtitle = element(
      'p',
      'cpa-rlu-subtitle',
      '统计已上传的原始请求日志，并同时显示本地尚存数据。'
    );
    heading.appendChild(title);
    heading.appendChild(subtitle);

    var actions = element('div', 'cpa-rlu-actions');
    var refresh = element('button', 'cpa-rlu-button', '刷新');
    refresh.type = 'button';
    var close = element('button', 'cpa-rlu-close', '×');
    close.type = 'button';
    close.setAttribute('aria-label', '关闭');
    actions.appendChild(refresh);
    actions.appendChild(close);
    header.appendChild(heading);
    header.appendChild(actions);

    var body = element('div', 'cpa-rlu-body');
    var status = element('div', 'cpa-rlu-status');
    status.setAttribute('role', 'status');
    status.setAttribute('aria-live', 'polite');
    var content = element('div');
    body.appendChild(status);
    body.appendChild(content);
    panel.appendChild(header);
    panel.appendChild(body);
    overlay.appendChild(panel);
    document.body.appendChild(launcher);
    document.body.appendChild(overlay);

    launcher.addEventListener('click', function () {
      openPanel();
    });
    close.addEventListener('click', closePanel);
    refresh.addEventListener('click', loadUsage);
    overlay.addEventListener('click', function (event) {
      if (event.target === overlay) {
        closePanel();
      }
    });
    document.addEventListener('keydown', function (event) {
      if (event.key === 'Escape' && !overlay.hidden) {
        closePanel();
      }
    });

    return {
      launcher: launcher,
      overlay: overlay,
      close: close,
      refresh: refresh,
      status: status,
      content: content,
      loaded: false,
    };
  }

  function ensureUI() {
    if (ui || !capturedAuth) {
      return;
    }
    if (!document.body) {
      document.addEventListener('DOMContentLoaded', ensureUI, { once: true });
      return;
    }
    ui = buildUI();
  }

  function openPanel() {
    if (!ui) {
      return;
    }
    previousBodyOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    ui.overlay.hidden = false;
    ui.close.focus();
    if (!ui.loaded) {
      loadUsage();
    }
  }

  function closePanel() {
    if (!ui || ui.overlay.hidden) {
      return;
    }
    ui.overlay.hidden = true;
    document.body.style.overflow = previousBodyOverflow;
    loadGeneration += 1;
    if (activeRequest) {
      activeRequest.abort();
      activeRequest = null;
    }
    ui.launcher.focus();
  }

  function numberValue(value) {
    var parsed = Number(value);
    return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
  }

  function parseErrorCount(value) {
    return Array.isArray(value) ? value.length : numberValue(value);
  }

  function integer(value) {
    return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 0 }).format(numberValue(value));
  }

  function bytes(value) {
    var size = numberValue(value);
    if (size === 0) {
      return '0 B';
    }
    var units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
    var index = Math.min(Math.floor(Math.log(size) / Math.log(1024)), units.length - 1);
    var scaled = size / Math.pow(1024, index);
    var digits = scaled >= 100 || index === 0 ? 0 : scaled >= 10 ? 1 : 2;
    return scaled.toFixed(digits) + ' ' + units[index];
  }

  function dateTime(value, timezone) {
    if (!value) {
      return '—';
    }
    var date = new Date(value);
    if (Number.isNaN(date.getTime())) {
      return String(value);
    }
    var options = {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    };
    if (timezone) {
      try {
        options.timeZone = timezone;
        return date.toLocaleString('zh-CN', options);
      } catch (_) {}
    }
    return date.toLocaleString('zh-CN', options);
  }

  function arrayValue(value) {
    return Array.isArray(value) ? value : [];
  }

  function modelText(models) {
    return arrayValue(models)
      .map(function (model) {
        var name = String(model && (model.model || model.name) ? model.model || model.name : 'unknown');
        return name + ' · ' + integer(model && model.source_count) + ' 条 · ' + bytes(model && model.source_bytes);
      })
      .join(' | ');
  }

  function hoursForKey(payload, keyName) {
    var output = [];
    arrayValue(payload.hours).forEach(function (hour) {
      arrayValue(hour && hour.keys).forEach(function (entry) {
        if (String(entry && entry.key_name) !== keyName) {
          return;
        }
        output.push({
          hour: hour.hour,
          source_count: entry.source_count,
          source_bytes: entry.source_bytes,
          models: entry.models,
        });
      });
    });
    return output;
  }

  function summaryCard(label, value) {
    var card = element('div', 'cpa-rlu-summary-card');
    card.appendChild(element('span', 'cpa-rlu-summary-label', label));
    card.appendChild(element('strong', 'cpa-rlu-summary-value', value));
    return card;
  }

  function metric(label, value) {
    var card = element('div', 'cpa-rlu-metric');
    card.appendChild(element('span', 'cpa-rlu-metric-label', label));
    card.appendChild(element('strong', 'cpa-rlu-metric-value', value));
    return card;
  }

  function buildHourTable(hours, timezone) {
    var details = element('details', 'cpa-rlu-details');
    details.appendChild(element('summary', '', '按小时明细（' + integer(hours.length) + '）'));
    var wrap = element('div', 'cpa-rlu-table-wrap');
    var table = element('table', 'cpa-rlu-table');
    var head = element('thead');
    var headerRow = element('tr');
    ['小时', '日志数', '原始大小', '模型'].forEach(function (label) {
      headerRow.appendChild(element('th', '', label));
    });
    head.appendChild(headerRow);
    table.appendChild(head);
    var body = element('tbody');
    hours.forEach(function (hour) {
      var row = element('tr');
      row.appendChild(element('td', '', dateTime(hour.hour, timezone)));
      row.appendChild(element('td', '', integer(hour.source_count)));
      row.appendChild(element('td', '', bytes(hour.source_bytes)));
      row.appendChild(element('td', '', modelText(hour.models) || '—'));
      body.appendChild(row);
    });
    table.appendChild(body);
    wrap.appendChild(table);
    details.appendChild(wrap);
    return details;
  }

  function renderUsage(payload) {
    var fragment = document.createDocumentFragment();
    var totals = payload && typeof payload.totals === 'object' ? payload.totals : {};
    var keys = arrayValue(payload && payload.keys);
    var timezone = String((payload && payload.timezone) || '');

    var summary = element('div', 'cpa-rlu-summary');
    summary.appendChild(summaryCard('已上传原始日志', bytes(totals.source_bytes)));
    summary.appendChild(summaryCard('已上传日志数', integer(totals.source_count)));
    summary.appendChild(
      summaryCard(
        '本地尚存日志',
        bytes(totals.pending_bytes) + ' / ' + integer(totals.pending_count) + ' 条'
      )
    );
    summary.appendChild(summaryCard('小时批次', integer(totals.batch_count)));
    summary.appendChild(summaryCard('Key 数量', integer(totals.key_count || keys.length)));
    fragment.appendChild(summary);

    fragment.appendChild(
      element(
        'p',
        'cpa-rlu-note',
        '大小按完整原始 .log 文件计算，不是压缩包分摊值。本地尚存日志在保留源文件或清理未完成时可能已上传。Key 改名后，旧名称会作为独立历史记录保留。'
      )
    );

    if (keys.length === 0) {
      fragment.appendChild(element('div', 'cpa-rlu-empty', '暂时没有 Key 日志用量记录。'));
    } else {
      var list = element('div', 'cpa-rlu-key-list');
      keys
        .slice()
        .sort(function (left, right) {
          return numberValue(right.source_bytes) - numberValue(left.source_bytes);
        })
        .forEach(function (entry) {
          var keyName = String((entry && entry.key_name) || 'unknown');
          var displayName = String((entry && entry.display_name) || keyName);
          var card = element('article', 'cpa-rlu-key-card');
          var cardHead = element('div', 'cpa-rlu-key-head');
          cardHead.appendChild(element('div', 'cpa-rlu-key-name', displayName));
          var badge = element('span', 'cpa-rlu-badge', entry && entry.configured ? '当前配置' : '历史名称');
          badge.dataset.configured = entry && entry.configured ? 'true' : 'false';
          cardHead.appendChild(badge);
          card.appendChild(cardHead);

          var metrics = element('div', 'cpa-rlu-metrics');
          metrics.appendChild(metric('已上传原始日志', bytes(entry && entry.source_bytes)));
          metrics.appendChild(metric('已上传日志数', integer(entry && entry.source_count)));
          metrics.appendChild(
            metric(
              '本地尚存日志',
              bytes(entry && entry.pending_bytes) + ' / ' + integer(entry && entry.pending_count) + ' 条'
            )
          );
          metrics.appendChild(metric('小时批次', integer(entry && entry.batch_count)));
          metrics.appendChild(
            metric(
              '时间范围',
              dateTime(entry && entry.first_hour, timezone) + ' — ' + dateTime(entry && entry.last_hour, timezone)
            )
          );
          card.appendChild(metrics);

          var models = arrayValue(entry && entry.models);
          if (models.length > 0) {
            var modelList = element('div', 'cpa-rlu-models');
            models.forEach(function (model) {
              modelList.appendChild(element('span', 'cpa-rlu-model', modelText([model])));
            });
            card.appendChild(modelList);
          }

          var keyHours = hoursForKey(payload, keyName);
          if (keyHours.length > 0) {
            card.appendChild(buildHourTable(keyHours, timezone));
          }
          list.appendChild(card);
        });
      fragment.appendChild(list);
    }

    var parseErrors = parseErrorCount(payload && payload.parse_errors);
    var footerText = timezone ? '统计时区：' + timezone + '。' : '';
    if (parseErrors > 0) {
      footerText += ' 有 ' + integer(parseErrors) + ' 条审计记录无法解析，以上统计已跳过这些记录。';
    }
    if (footerText) {
      fragment.appendChild(element('div', 'cpa-rlu-footer', footerText));
    }

    ui.content.replaceChildren(fragment);
  }

  function setStatus(message, kind) {
    if (!ui) {
      return;
    }
    ui.status.textContent = message || '';
    if (kind) {
      ui.status.dataset.kind = kind;
    } else {
      delete ui.status.dataset.kind;
    }
  }

  function loadUsage() {
    if (!ui || !capturedAuth) {
      return;
    }
    if (!nativeFetch) {
      setStatus('当前浏览器不支持加载统计数据。', 'error');
      return;
    }
    if (activeRequest) {
      activeRequest.abort();
    }

    var controller = typeof AbortController !== 'undefined' ? new AbortController() : null;
    var requestAuthGeneration = authGeneration;
    var requestLoadGeneration = ++loadGeneration;
    activeRequest = controller;
    ui.refresh.disabled = true;
    setStatus(ui.loaded ? '正在刷新…' : '正在加载…');

    var headers = { Accept: 'application/json' };
    headers[capturedAuth.headerName] = capturedAuth.headerValue;
    var endpoint = capturedAuth.apiRoot + USAGE_ENDPOINT.slice(MANAGEMENT_PREFIX.length);
    var options = {
      method: 'GET',
      headers: headers,
      credentials: 'same-origin',
      cache: 'no-store',
    };
    if (controller) {
      options.signal = controller.signal;
    }

    nativeFetch
      .call(window, endpoint, options)
      .then(function (response) {
        if (!response || !response.ok) {
          var status = response ? response.status : 0;
          if (status === 401 || status === 403) {
            clearCapturedAuth();
            if (status === 401) {
              window.dispatchEvent(new Event('unauthorized'));
            }
            throw new Error('认证已失效，请刷新管理页并重新登录。');
          }
          if (status === 404) {
            throw new Error('后端尚未启用 Key 日志用量接口。');
          }
          throw new Error('加载失败（HTTP ' + status + '）。');
        }
        return response.json();
      })
      .then(function (payload) {
        if (
          requestAuthGeneration !== authGeneration ||
          requestLoadGeneration !== loadGeneration ||
          (controller && controller.signal.aborted)
        ) {
          return;
        }
        if (!payload || typeof payload !== 'object') {
          throw new Error('后端返回了无效的统计数据。');
        }
        renderUsage(payload);
        ui.loaded = true;
        var parseErrors = parseErrorCount(payload.parse_errors);
        setStatus(
          parseErrors > 0 ? '统计已更新，但有部分审计记录无法解析。' : '统计已更新。',
          parseErrors > 0 ? 'warning' : ''
        );
      })
      .catch(function (error) {
        if (
          requestAuthGeneration !== authGeneration ||
          requestLoadGeneration !== loadGeneration ||
          (controller && controller.signal.aborted)
        ) {
          return;
        }
        setStatus(error && error.message ? error.message : '加载统计失败。', 'error');
      })
      .finally(function () {
        if (requestLoadGeneration === loadGeneration && activeRequest === controller) {
          activeRequest = null;
          if (ui) {
            ui.refresh.disabled = false;
          }
        }
      });
  }

  try {
    installXHRInterceptor();
    installFetchInterceptor();
    window.addEventListener('unauthorized', clearCapturedAuth);
    window.addEventListener('hashchange', function () {
      if (loginRouteActive()) {
        clearCapturedAuth();
      }
    });
  } catch (_) {
    // The management application must keep working if interception is unavailable.
  }
})();
