// lorg minimal frontend -- zero dependencies
(function() {
  'use strict';

  var API = window.location.origin;
  var currentView = 'traffic';
  var selectedTrafficId = null;
  var hostFilter = '';
  var trafficData = [];
  var hosts = [];
  var sortColumn = 'index';
  var sortAsc = false;

  // --- DOM Helpers ---
  function $(sel, ctx) { return (ctx || document).querySelector(sel); }
  function $$(sel, ctx) { return Array.prototype.slice.call((ctx || document).querySelectorAll(sel)); }

  // --- API Helpers ---
  async function api(path, opts) {
    opts = opts || {};
    try {
      var res = await fetch(API + path, {
        headers: Object.assign({ 'Content-Type': 'application/json' }, opts.headers || {}),
        method: opts.method || 'GET',
        body: opts.body || undefined,
      });
      if (!res.ok) throw new Error(res.status + ' ' + res.statusText);
      return await res.json();
    } catch (e) {
      console.error('API error: ' + path, e);
      return null;
    }
  }

  // --- Status Check ---
  async function checkStatus() {
    var info = await api('/mcp/health');
    var dot = $('#status-indicator');
    var txt = $('#status-text');
    if (info && info.status === 'ok') {
      dot.classList.add('connected');
      txt.textContent = 'v' + (info.version || '?') + ' · ' + (info.tools ? info.tools.length : '?') + ' tools';
    } else {
      dot.classList.remove('connected');
      txt.textContent = 'Disconnected';
    }

    // Update project status in sidebar footer
    var projStatus = document.getElementById('project-status');
    if (projStatus) {
      api('/api/proxy/list').then(function(data) {
        if (data && data.proxies && data.proxies.length > 0) {
          var p = data.proxies[0];
          projStatus.textContent = 'Proxy: ' + p.listenAddr;
        } else {
          projStatus.textContent = 'No proxy running';
        }
      });
    }
  }

  // --- Host List ---
  async function loadHosts() {
    var data = await api('/api/collections/_hosts/records?perPage=200&sort=-created');
    if (!data || !data.items) return;
    hosts = data.items;
    renderHosts();
  }

  function renderHosts() {
    var container = $('#hosts');
    var clearBtn = $('#clear-host-filter');
    clearBtn.classList.toggle('hidden', !hostFilter);

    container.innerHTML = hosts.map(function(h) {
      var name = h.host || h.id;
      var active = hostFilter === name ? 'active' : '';
      return '<div class="host-item ' + active + '" data-host="' + escapeAttr(name) + '">' + escapeHtml(name) + '</div>';
    }).join('');

    var hostCountEl = document.getElementById('host-count');
    if (hostCountEl) hostCountEl.textContent = hosts.length;

    $$('.host-item', container).forEach(function(el) {
      el.addEventListener('click', function() {
        hostFilter = hostFilter === el.dataset.host ? '' : el.dataset.host;
        renderHosts();
        loadTraffic();
      });
    });
  }

  // --- Traffic Table ---
  var allTrafficData = [];

  async function loadTraffic() {
    // Fetch all traffic from backend (server-side filter only for host sidebar)
    var serverFilter = '';
    if (hostFilter) {
      serverFilter = '&filter=' + encodeURIComponent('host~"' + hostFilter + '"');
    }

    var data = await api('/api/collections/_data/records?perPage=500&sort=-index' + serverFilter);
    if (!data || !data.items) return;

    allTrafficData = data.items;
    applyClientFilter();
  }

  function applyClientFilter() {
    var filterText = $('#traffic-filter').value.trim();
    var aiOnly = $('#filter-ai-only').checked;

    var filtered = allTrafficData;

    // AI-only checkbox
    if (aiOnly) {
      filtered = filtered.filter(function(row) {
        return (row.generated_by || '').indexOf('ai/mcp') !== -1;
      });
    }

    // Hide noise traffic (Chrome, Google, CDN, etc.)
    var hideNoise = document.getElementById('filter-hide-noise');
    if (hideNoise && hideNoise.checked) {
      var noisePatterns = ['googleapis.com', 'gvt1.com', 'google.com', 'typekit.net',
                           'gstatic.com', 'cloudfront.net', 'accounts.google.com',
                           'safebrowsing', 'optimizationguide', 'clientservices.google'];
      filtered = filtered.filter(function(row) {
        var host = (row.host || '').toLowerCase();
        for (var i = 0; i < noisePatterns.length; i++) {
          if (host.indexOf(noisePatterns[i]) !== -1) return false;
        }
        return true;
      });
    }

    // Parse and apply text filter
    if (filterText) {
      filtered = filtered.filter(function(row) {
        return matchesFilter(row, filterText);
      });
    }

    trafficData = filtered;
    renderTraffic();
  }

  // Smart filter parser: supports status:200, method:GET, path:/api, host:example,
  // source:ai, length:>1000, and plain text search.
  // Combine with AND / OR (case-insensitive). AND binds tighter than OR.
  // Examples:
  //   status:200
  //   method:GET AND status:200
  //   status:404 OR status:500
  //   method:POST AND path:/api OR method:PUT AND path:/api
  //   httpbin (plain text — searches host + path)
  function matchesFilter(row, filterText) {
    // Split on OR first (lower precedence)
    var orGroups = filterText.split(/\s+OR\s+/i);
    for (var i = 0; i < orGroups.length; i++) {
      // Each OR group is AND-ed conditions
      var andParts = orGroups[i].split(/\s+AND\s+/i);
      var allMatch = true;
      for (var j = 0; j < andParts.length; j++) {
        if (!matchesSingleCondition(row, andParts[j].trim())) {
          allMatch = false;
          break;
        }
      }
      if (allMatch) return true; // Any OR group matching is enough
    }
    return false;
  }

  function matchesSingleCondition(row, cond) {
    var req = row.req_json || {};
    var resp = row.resp_json || {};

    // key:value patterns
    var m;

    // status:200, status:>400, status:<300, status:4xx
    m = cond.match(/^status:([><!]*)(\d+|[1-5]xx)$/i);
    if (m) {
      var op = m[1] || '';
      var val = m[2];
      var status = resp.status || 0;
      if (val.match(/^[1-5]xx$/i)) {
        // Range match: 2xx = 200-299
        var base = parseInt(val[0], 10) * 100;
        return status >= base && status < base + 100;
      }
      var num = parseInt(val, 10);
      if (op === '>') return status > num;
      if (op === '>=') return status >= num;
      if (op === '<') return status < num;
      if (op === '<=') return status <= num;
      if (op === '!') return status !== num;
      return status === num;
    }

    // method:GET, method:POST
    m = cond.match(/^method:(\S+)$/i);
    if (m) {
      return (req.method || '').toUpperCase() === m[1].toUpperCase();
    }

    // path:/api, path:login
    m = cond.match(/^path:(.+)$/i);
    if (m) {
      return (req.path || req.url || '').toLowerCase().indexOf(m[1].toLowerCase()) !== -1;
    }

    // host:example
    m = cond.match(/^host:(.+)$/i);
    if (m) {
      return (row.host || '').toLowerCase().indexOf(m[1].toLowerCase()) !== -1;
    }

    // source:ai, source:proxy
    m = cond.match(/^source:(\S+)$/i);
    if (m) {
      var isAi = (row.generated_by || '').indexOf('ai/mcp') !== -1;
      if (m[1].toLowerCase() === 'ai') return isAi;
      if (m[1].toLowerCase() === 'proxy') return !isAi;
      return false;
    }

    // length:>1000, length:<500
    m = cond.match(/^length:([><!]*)(\d+)$/i);
    if (m) {
      var lenOp = m[1] || '';
      var lenVal = parseInt(m[2], 10);
      var respLen = resp.length || 0;
      if (lenOp === '>') return respLen > lenVal;
      if (lenOp === '<') return respLen < lenVal;
      if (lenOp === '>=') return respLen >= lenVal;
      if (lenOp === '<=') return respLen <= lenVal;
      if (lenOp === '!') return respLen !== lenVal;
      return respLen === lenVal;
    }

    // ext:.json, ext:.js
    m = cond.match(/^ext:(.+)$/i);
    if (m) {
      return (req.ext || '').toLowerCase() === m[1].toLowerCase();
    }

    // Plain text: search in host + path + method
    var lower = cond.toLowerCase();
    var haystack = ((row.host || '') + ' ' + (req.path || req.url || '') + ' ' + (req.method || '')).toLowerCase();
    return haystack.indexOf(lower) !== -1;
  }

  function getRowSortValue(row, col) {
    var req = row.req_json || {};
    var resp = row.resp_json || {};
    switch (col) {
      case 'index': return row.index || 0;
      case 'method': return (req.method || '').toLowerCase();
      case 'host': return (row.host || '').toLowerCase();
      case 'path': return (req.path || req.url || '/').toLowerCase();
      case 'status': return resp.status || 0;
      case 'length': return resp.length || 0;
      case 'source': return (row.generated_by || '').indexOf('ai/mcp') !== -1 ? 'a' : 'z';
      default: return 0;
    }
  }

  function sortTraffic() {
    trafficData.sort(function(a, b) {
      var va = getRowSortValue(a, sortColumn);
      var vb = getRowSortValue(b, sortColumn);
      if (va < vb) return sortAsc ? -1 : 1;
      if (va > vb) return sortAsc ? 1 : -1;
      return 0;
    });
  }

  var lastTrafficFingerprint = '';

  function renderTraffic(forceRender) {
    var tbody = $('#traffic-body');

    // Build a fingerprint of current data to avoid unnecessary re-renders (prevents flashing on poll)
    var fingerprint = trafficData.length + ':' + trafficData.map(function(r) { return r.id; }).join(',');
    if (!forceRender && fingerprint === lastTrafficFingerprint) {
      return; // Data unchanged, skip re-render
    }
    lastTrafficFingerprint = fingerprint;

    $('#traffic-count').textContent = trafficData.length;

    // Update sort indicators on headers
    $$('#traffic-table th').forEach(function(th) {
      th.classList.remove('sort-asc', 'sort-desc');
      if (th.dataset.sort === sortColumn) {
        th.classList.add(sortAsc ? 'sort-asc' : 'sort-desc');
      }
    });

    sortTraffic();

    tbody.innerHTML = trafficData.map(function(row) {
      var req = row.req_json || {};
      var resp = row.resp_json || {};
      var method = req.method || '?';
      var path = req.path || req.url || '/';
      var status = resp.status || '';
      var length = resp.length || '';
      var source = (row.generated_by || '').indexOf('ai/mcp') !== -1 ? 'AI' : 'Proxy';
      var sourceClass = source === 'AI' ? 'source-ai' : 'source-proxy';
      var methodClass = 'method-' + method.toLowerCase();
      var statusClass = status >= 500 ? 'status-5xx' : status >= 400 ? 'status-4xx' : status >= 300 ? 'status-3xx' : status >= 200 ? 'status-2xx' : '';
      var selected = row.id === selectedTrafficId ? 'selected' : '';

      return '<tr class="' + selected + '" data-id="' + escapeAttr(row.id) + '">' +
        '<td class="col-id">' + Math.round(row.index || 0) + '</td>' +
        '<td class="col-method"><span class="' + methodClass + '">' + escapeHtml(method) + '</span></td>' +
        '<td class="col-host">' + escapeHtml(row.host || '') + '</td>' +
        '<td class="col-path" title="' + escapeAttr(path) + '">' + escapeHtml(path) + '</td>' +
        '<td class="col-status"><span class="' + statusClass + '">' + escapeHtml(String(status)) + '</span></td>' +
        '<td class="col-length">' + (length ? formatBytes(length) : '') + '</td>' +
        '<td class="col-source"><span class="' + sourceClass + '">' + source + '</span></td>' +
        '</tr>';
    }).join('');

    $$('#traffic-body tr').forEach(function(tr) {
      tr.addEventListener('click', function() { selectTrafficRow(tr.dataset.id); });
    });
  }

  async function selectTrafficRow(id) {
    selectedTrafficId = id;
    renderTraffic(true);

    // Fetch raw request and response
    var reqData = await api('/api/collections/_req/records/' + id);
    var respData = await api('/api/collections/_resp/records/' + id);

    var detailPane = $('#traffic-detail');
    detailPane.classList.remove('hidden');
    var resizeHandle = document.getElementById('resize-handle');
    if (resizeHandle) resizeHandle.classList.remove('hidden');

    $('#detail-request-raw').innerHTML = reqData ? highlightHTTP(reqData.raw || 'No request data') : 'Failed to load';
    $('#detail-response-raw').innerHTML = respData ? highlightHTTP(respData.raw || 'No response data') : 'Failed to load';

    // Store for "send to repeater"
    detailPane.dataset.reqRaw = reqData ? reqData.raw || '' : '';
    detailPane.dataset.host = '';
    detailPane.dataset.port = '';

    // Try to extract host/port from the _data record
    var row = trafficData.find(function(r) { return r.id === id; });
    if (row) {
      detailPane.dataset.host = (row.host || '').replace(/^https?:\/\//, '');
      detailPane.dataset.port = row.port || (row.is_https ? '443' : '80');
      detailPane.dataset.tls = row.is_https ? 'true' : 'false';
    }
  }

  // --- Repeater ---
  function sendToRepeater() {
    var detailPane = $('#traffic-detail');
    var raw = detailPane.dataset.reqRaw || '';
    var host = detailPane.dataset.host || '';
    var port = detailPane.dataset.port || '443';
    var tls = detailPane.dataset.tls === 'true';

    // Auto-detect HTTP version from request line
    var isH2 = /HTTP\/2/i.test(raw.split('\n')[0] || '');

    $('#rep-host').value = host;
    $('#rep-port').value = port;
    $('#rep-tls').checked = tls;
    $('#rep-http-version').value = isH2 ? '2' : '1';
    $('#rep-request').value = raw;
    $('#rep-request-highlight').innerHTML = highlightHTTP(raw) || '\n';
    $('#rep-response').textContent = '';
    $('#rep-time').textContent = '';
    $('#rep-note').textContent = '';

    switchView('repeater');
  }

  // Normalize HTTP version in request line to match selected protocol
  function normalizeRequestVersion(rawRequest, useHttp2) {
    var target = useHttp2 ? 'HTTP/2.0' : 'HTTP/1.1';
    // Replace the first line's HTTP version
    return rawRequest.replace(/^((?:GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS|TRACE|CONNECT)\s+\S+\s+)HTTP\/[\d.]+/i, '$1' + target);
  }

  async function sendRepeaterRequest() {
    var host = $('#rep-host').value.trim();
    var port = $('#rep-port').value.trim();
    var tls = $('#rep-tls').checked;
    var httpVersion = parseInt($('#rep-http-version').value, 10);
    var request = $('#rep-request').value;

    if (!host || !request) {
      $('#rep-note').textContent = 'Host and request are required';
      return;
    }

    // Normalize request line to match selected HTTP version
    request = normalizeRequestVersion(request, httpVersion === 2);

    $('#rep-send').disabled = true;
    $('#rep-send').textContent = 'Sending...';
    $('#rep-note').textContent = '';

    var resp = await api('/api/repeater/send', {
      method: 'POST',
      body: JSON.stringify({
        host: host,
        port: port,
        tls: tls,
        request: request,
        timeout: 30,
        http2: httpVersion === 2,
        index: 0,
        url: (tls ? 'https' : 'http') + '://' + host + ':' + port,
        generated_by: 'frontend/repeater',
      }),
    });

    $('#rep-send').disabled = false;
    $('#rep-send').textContent = 'Send';

    if (resp) {
      $('#rep-response').innerHTML = highlightHTTP(resp.response || 'Empty response');
      $('#rep-time').textContent = resp.time || '';
      if (resp.userdata) {
        $('#rep-note').textContent = 'Saved as #' + (resp.userdata.index || '?');
      }
    } else {
      $('#rep-response').textContent = 'Request failed -- check host and port';
    }
  }

  // --- Navigation ---
  function switchView(view) {
    currentView = view;
    $$('.view').forEach(function(v) { v.classList.toggle('active', v.id === 'view-' + view); });
    $$('.nav-btn').forEach(function(b) { b.classList.toggle('active', b.dataset.view === view); });
  }

  // --- Helpers ---
  function formatBytes(bytes) {
    if (bytes < 1024) return bytes + 'B';
    if (bytes < 1048576) return (bytes / 1024).toFixed(1) + 'K';
    return (bytes / 1048576).toFixed(1) + 'M';
  }

  function escapeHtml(str) {
    var div = document.createElement('div');
    div.appendChild(document.createTextNode(str));
    return div.innerHTML;
  }

  function escapeAttr(str) {
    return String(str).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/'/g, '&#39;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  // --- HTTP Syntax Highlighting ---
  var MAX_HIGHLIGHT_LINES = 500;
  var MAX_HIGHLIGHT_BYTES = 50000;

  function addLineNumbers(html) {
    var lines = html.split('\n');
    return lines.map(function(line, idx) {
      return '<span class="line-num">' + (idx + 1) + '</span>' + line;
    }).join('\n');
  }

  function highlightHTTP(raw) {
    if (!raw) return '';

    // For very large responses, truncate body to keep UI responsive
    var truncated = false;
    if (raw.length > MAX_HIGHLIGHT_BYTES) {
      var bodyStart = raw.indexOf('\r\n\r\n');
      if (bodyStart < 0) bodyStart = raw.indexOf('\n\n');
      if (bodyStart >= 0) {
        var headerLen = bodyStart + (raw.charAt(bodyStart) === '\r' ? 4 : 2);
        var maxBody = MAX_HIGHLIGHT_BYTES - headerLen;
        if (maxBody > 0 && raw.length - headerLen > maxBody) {
          raw = raw.substring(0, headerLen + maxBody);
          truncated = true;
        }
      }
    }
    // Normalize \r\n to \n to prevent double line spacing
    raw = raw.replace(/\r\n/g, '\n').replace(/\r/g, '\n');

    // Split headers from body on the RAW (unescaped) text so we can pretty-print
    var headerEnd = raw.indexOf('\n\n');
    var rawHeaders = headerEnd >= 0 ? raw.substring(0, headerEnd) : raw;
    var rawBody = headerEnd >= 0 ? raw.substring(headerEnd + 2) : '';

    // Highlight headers (escape then highlight)
    var escHeaders = escapeHtml(rawHeaders);
    var headerLines = escHeaders.split('\n');
    var result = [];

    for (var i = 0; i < headerLines.length; i++) {
      var line = headerLines[i];
      if (i === 0) {
        result.push(highlightFirstLine(line));
        continue;
      }
      var colonIdx = line.indexOf(':');
      if (colonIdx > 0) {
        var hName = line.substring(0, colonIdx);
        var hValue = line.substring(colonIdx + 1);
        result.push('<span class="hl-header-name">' + hName + '</span><span class="hl-colon">:</span><span class="hl-header-value">' + hValue + '</span>');
      } else {
        result.push(line);
      }
    }

    // Empty line separator
    if (headerEnd >= 0) {
      result.push('');
    }

    // Highlight body — pretty-print JSON before escaping
    if (rawBody.trim()) {
      var prettyBody = prettyPrintBody(rawBody);
      var escBody = escapeHtml(prettyBody);
      result.push(highlightBody(escBody));
    }

    var output = result.join('\n');
    if (truncated) {
      output += '\n<span class="hl-truncated">... response truncated (' + formatBytes(MAX_HIGHLIGHT_BYTES) + ' shown). Use "Copy raw request" for full content.</span>';
    }

    // Skip line numbers for very long output (DOM performance)
    var lineCount = output.split('\n').length;
    if (lineCount > MAX_HIGHLIGHT_LINES) {
      return output;
    }
    return addLineNumbers(output);
  }

  // Pretty-print JSON body if detected. Returns the raw (unescaped) body string.
  function prettyPrintBody(rawBody) {
    var trimmed = rawBody.trim();
    if ((trimmed.charAt(0) === '{' || trimmed.charAt(0) === '[') &&
        (trimmed.charAt(trimmed.length - 1) === '}' || trimmed.charAt(trimmed.length - 1) === ']')) {
      try {
        var parsed = JSON.parse(trimmed);
        return JSON.stringify(parsed, null, 2);
      } catch (e) {
        // Not valid JSON, return as-is
      }
    }
    return rawBody;
  }

  function highlightFirstLine(line) {
    // Request: GET /path HTTP/1.1
    var reqMatch = line.match(/^(<span[^>]*>)?(\S+)(\s+)(\S+)(\s+)(HTTP\/[\d.]+)/i);
    if (reqMatch) {
      return '<span class="hl-method">' + reqMatch[2] + '</span>' + reqMatch[3] +
             '<span class="hl-url">' + reqMatch[4] + '</span>' + reqMatch[5] +
             '<span class="hl-version">' + reqMatch[6] + '</span>';
    }
    // Also try on the raw escaped line without spans
    var parts = line.match(/^(\S+)\s+(\S+)\s+(HTTP\/[\d.]+)/i);
    if (parts) {
      return '<span class="hl-method">' + parts[1] + '</span> ' +
             '<span class="hl-url">' + parts[2] + '</span> ' +
             '<span class="hl-version">' + parts[3] + '</span>';
    }
    // Response: HTTP/1.1 200 OK
    var respMatch = line.match(/^(HTTP\/[\d.]+)\s+(\d{3})\s*(.*)/i);
    if (respMatch) {
      var statusNum = parseInt(respMatch[2], 10);
      var statusClass = statusNum >= 500 ? 'hl-status-5xx' : statusNum >= 400 ? 'hl-status-4xx' : statusNum >= 300 ? 'hl-status-3xx' : 'hl-status-2xx';
      return '<span class="hl-version">' + respMatch[1] + '</span> ' +
             '<span class="' + statusClass + '">' + respMatch[2] + ' ' + respMatch[3] + '</span>';
    }
    return '<span class="hl-first-line">' + line + '</span>';
  }

  function highlightBody(body) {
    var trimmed = body.trim();
    // JSON detection
    if ((trimmed.charAt(0) === '{' || trimmed.charAt(0) === '[') && (trimmed.charAt(trimmed.length - 1) === '}' || trimmed.charAt(trimmed.length - 1) === ']')) {
      return highlightJSON(body);
    }
    // HTML detection
    if (trimmed.indexOf('&lt;') !== -1 || trimmed.indexOf('<!') !== -1) {
      return highlightHTML(body);
    }
    return '<span class="hl-body">' + body + '</span>';
  }

  function highlightJSON(text) {
    return text
      .replace(/"([^"\\]*(\\.[^"\\]*)*)"\s*:/g, '<span class="hl-json-key">"$1"</span>:')
      .replace(/:\s*"([^"\\]*(\\.[^"\\]*)*)"/g, ': <span class="hl-json-string">"$1"</span>')
      .replace(/:\s*(\d+\.?\d*)/g, ': <span class="hl-json-number">$1</span>')
      .replace(/:\s*(true|false)/g, ': <span class="hl-json-bool">$1</span>')
      .replace(/:\s*(null)/g, ': <span class="hl-json-null">$1</span>');
  }

  function highlightHTML(text) {
    return text
      .replace(/(&lt;\/?)([\w-]+)/g, '$1<span class="hl-html-tag">$2</span>')
      .replace(/([\w-]+)(=)(&quot;[^&]*&quot;)/g, '<span class="hl-html-attr">$1</span>$2<span class="hl-html-value">$3</span>');
  }

  function debounce(fn, ms) {
    var timer;
    return function() {
      var args = arguments;
      var ctx = this;
      clearTimeout(timer);
      timer = setTimeout(function() { fn.apply(ctx, args); }, ms);
    };
  }

  // --- Settings / Preferences ---
  var PREVIEW_SAMPLE = 'GET /api/users?id=1 HTTP/1.1\nHost: example.com\nAuthorization: Bearer eyJhbGci...\nAccept: application/json\n\n';
  var PREVIEW_RESP = 'HTTP/1.1 200 OK\nContent-Type: application/json\nX-Request-Id: abc123\n\n{\n  "id": 1,\n  "name": "admin",\n  "role": "superuser",\n  "active": true\n}';

  function loadPreferences() {
    var prefs = {};
    try { prefs = JSON.parse(localStorage.getItem('lorg-prefs') || '{}'); } catch(e) {}
    if (prefs.theme) { applyTheme(prefs.theme); var el = $('#pref-theme'); if (el) el.value = prefs.theme; }
    if (prefs.font) { document.documentElement.style.setProperty('--font', prefs.font); $('#pref-font').value = prefs.font; }
    if (prefs.fontSize) { document.documentElement.style.setProperty('--font-size', prefs.fontSize); $('#pref-font-size').value = prefs.fontSize; }
    if (prefs.lineHeight) {
      document.querySelectorAll('.raw-http, .raw-editor').forEach(function(el) { el.style.lineHeight = prefs.lineHeight; });
      $('#pref-line-height').value = prefs.lineHeight;
    }
    if (prefs.wrap) {
      document.querySelectorAll('.raw-editor.readonly, .raw-http').forEach(function(el) { el.style.whiteSpace = prefs.wrap; });
      $('#pref-wrap').value = prefs.wrap;
    }
  }

  function applyTheme(theme) {
    if (theme === 'obsidian' || !theme) {
      document.documentElement.removeAttribute('data-theme');
    } else {
      document.documentElement.setAttribute('data-theme', theme);
    }
  }

  function savePreferences() {
    var themeEl = $('#pref-theme');
    var prefs = {
      theme: themeEl ? themeEl.value : 'obsidian',
      font: $('#pref-font').value,
      fontSize: $('#pref-font-size').value,
      lineHeight: $('#pref-line-height').value,
      wrap: $('#pref-wrap').value,
    };
    localStorage.setItem('lorg-prefs', JSON.stringify(prefs));
  }

  function applyPreference(key, value) {
    switch (key) {
      case 'font':
        document.documentElement.style.setProperty('--font', value);
        break;
      case 'fontSize':
        document.documentElement.style.setProperty('--font-size', value);
        break;
      case 'lineHeight':
        document.querySelectorAll('.raw-http, .raw-editor').forEach(function(el) { el.style.lineHeight = value; });
        break;
      case 'wrap':
        document.querySelectorAll('.raw-editor.readonly, .raw-http').forEach(function(el) { el.style.whiteSpace = value; });
        break;
    }
    savePreferences();
    updateSettingsPreview();
  }

  function updateSettingsPreview() {
    var el = $('#settings-preview-text');
    if (el) { el.innerHTML = highlightHTTP(PREVIEW_SAMPLE + PREVIEW_RESP); }
  }

  function initSettings() {
    loadPreferences();
    var themeEl = $('#pref-theme');
    if (themeEl) {
      themeEl.addEventListener('change', function() {
        applyTheme(this.value);
        applyPreference('theme', this.value);
      });
    }
    $('#pref-font').addEventListener('change', function() { applyPreference('font', this.value); });
    $('#pref-font-size').addEventListener('change', function() { applyPreference('fontSize', this.value); });
    $('#pref-line-height').addEventListener('change', function() { applyPreference('lineHeight', this.value); });
    $('#pref-wrap').addEventListener('change', function() { applyPreference('wrap', this.value); });
    updateSettingsPreview();
    initScope();
    loadProxyInfo();
  }

  // --- Scope Management ---
  async function loadScopeRules() {
    var data = await api('/mcp/message', { method: 'POST' }); // Can't call MCP tools from frontend directly
    // Use REST fallback — fetch scope rules via a simple proxy endpoint
    // For now, we'll store scope state client-side and sync via the settings UI
    var el = $('#scope-includes');
    var el2 = $('#scope-excludes');
    el.innerHTML = '<span style="color:var(--text-dim); font-size:10px;">Scope rules are managed via MCP tools (scopeLoad, scopeAddRule, scopeCheck). Use Claude Code or the CLI to manage scope.</span>';
    el2.innerHTML = '';
  }

  function initScope() {
    $('#scope-load-btn').addEventListener('click', async function() {
      var filePath = $('#scope-file').value.trim();
      if (!filePath) return;
      // This would need an API endpoint to trigger scopeLoad — for now show instructions
      alert('Use MCP tool: scopeLoad with filePath "' + filePath + '"\n\nScope management is done via Claude Code MCP tools.\nThe UI shows the current state after MCP operations.');
    });

    $('#scope-add-btn').addEventListener('click', function() {
      var type = $('#scope-type').value;
      var host = $('#scope-host').value.trim();
      var port = $('#scope-port').value.trim();
      var path = $('#scope-path').value.trim();
      if (!host) { alert('Host is required'); return; }
      alert('Use MCP tool: scopeAddRule\n  type: ' + type + '\n  host: ' + host + (port ? '\n  port: ' + port : '') + (path ? '\n  path: ' + path : ''));
    });

    $('#scope-reset-btn').addEventListener('click', function() {
      if (confirm('Reset all scope rules?')) {
        alert('Use MCP tool: scopeReset with confirm: true');
      }
    });

    $('#scope-refresh-btn').addEventListener('click', loadScopeRules);
    loadScopeRules();
  }

  // --- Proxy Info ---
  async function loadProxyInfo() {
    var data = await api('/api/proxy/list');
    var el = $('#proxy-info');
    if (!data || !data.proxies || data.proxies.length === 0) {
      el.innerHTML = 'No proxies running. Start one with MCP tool: <code>proxyStart</code>';
      return;
    }
    el.innerHTML = data.proxies.map(function(p) {
      return '<div style="margin-bottom:4px;">' +
        '<span style="color:var(--accent);">' + escapeHtml(p.listenAddr) + '</span>' +
        ' &mdash; ' + escapeHtml(p.label || 'unnamed') +
        ' (browser: ' + escapeHtml(p.browser || 'none') + ')' +
        ' <span style="color:var(--text-dim);">id: ' + escapeHtml(p.id) + '</span>' +
        '</div>';
    }).join('');
  }

  // --- Event Listeners ---
  function init() {
    // Navigation
    $$('.nav-btn').forEach(function(btn) {
      btn.addEventListener('click', function() { switchView(btn.dataset.view); });
    });

    // Send to Repeater button
    $('#btn-send-to-repeater').addEventListener('click', sendToRepeater);

    // Clear host filter
    $('#clear-host-filter').addEventListener('click', function() {
      hostFilter = '';
      renderHosts();
      loadTraffic();
    });

    // Column sorting
    $$('#traffic-table th.sortable').forEach(function(th) {
      th.addEventListener('click', function() {
        var col = th.dataset.sort;
        if (sortColumn === col) {
          sortAsc = !sortAsc;
        } else {
          sortColumn = col;
          sortAsc = true;
        }
        renderTraffic(true);
      });
    });

    // Traffic controls
    $('#traffic-refresh').addEventListener('click', function() { loadTraffic(); loadHosts(); });
    $('#traffic-filter').addEventListener('input', debounce(applyClientFilter, 150));
    $('#filter-ai-only').addEventListener('change', applyClientFilter);
    var hideNoiseEl = $('#filter-hide-noise');
    if (hideNoiseEl) hideNoiseEl.addEventListener('change', applyClientFilter);

    // Repeater
    $('#rep-send').addEventListener('click', sendRepeaterRequest);

    // Sync request textarea with highlight overlay
    var reqInput = $('#rep-request');
    var reqHighlight = $('#rep-request-highlight');

    function syncRequestHighlight() {
      reqHighlight.innerHTML = highlightHTTP(reqInput.value) || '\n';
    }

    reqInput.addEventListener('input', syncRequestHighlight);
    reqInput.addEventListener('scroll', function() {
      reqHighlight.scrollTop = reqInput.scrollTop;
      reqHighlight.scrollLeft = reqInput.scrollLeft;
    });

    // Keyboard shortcuts
    document.addEventListener('keydown', function(e) {
      if (e.ctrlKey || e.metaKey) {
        if (e.key === 'Enter' && currentView === 'repeater') {
          e.preventDefault();
          sendRepeaterRequest();
        }
      }
    });

    // Resizable detail pane (drag handle between table and detail)
    (function() {
      var handle = document.getElementById('resize-handle');
      var tableContainer = document.querySelector('#view-traffic .table-container');
      var detailPane = document.getElementById('traffic-detail');
      if (!handle || !tableContainer || !detailPane) return;

      var isDragging = false;
      var startY = 0;
      var startTableH = 0;
      var startDetailH = 0;

      handle.addEventListener('mousedown', function(e) {
        isDragging = true;
        startY = e.clientY;
        startTableH = tableContainer.offsetHeight;
        startDetailH = detailPane.offsetHeight;
        handle.classList.add('dragging');
        document.body.style.cursor = 'row-resize';
        document.body.style.userSelect = 'none';
        e.preventDefault();
      });

      document.addEventListener('mousemove', function(e) {
        if (!isDragging) return;
        var delta = e.clientY - startY;
        var newTableH = startTableH + delta;
        var newDetailH = startDetailH - delta;
        // Enforce minimums
        if (newTableH < 80) newTableH = 80;
        if (newDetailH < 120) newDetailH = 120;
        // Enforce total doesn't exceed parent
        var parentH = tableContainer.parentElement.offsetHeight;
        if (newTableH + newDetailH > parentH - 50) return;
        tableContainer.style.flex = 'none';
        tableContainer.style.height = newTableH + 'px';
        detailPane.style.flex = 'none';
        detailPane.style.height = newDetailH + 'px';
      });

      document.addEventListener('mouseup', function() {
        if (!isDragging) return;
        isDragging = false;
        handle.classList.remove('dragging');
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
      });
    })();

    // Context menu (right-click on traffic rows)
    (function() {
      var menu = document.getElementById('context-menu');
      if (!menu) return;
      var contextRowId = null;
      var contextRowData = null;

      // Show context menu on right-click in traffic table
      document.getElementById('traffic-body').addEventListener('contextmenu', function(e) {
        var tr = e.target.closest('tr');
        if (!tr || !tr.dataset.id) return;
        e.preventDefault();
        contextRowId = tr.dataset.id;
        contextRowData = trafficData.find(function(r) { return r.id === contextRowId; });
        menu.classList.remove('hidden');
        menu.style.left = e.clientX + 'px';
        menu.style.top = e.clientY + 'px';
        // Keep menu on screen
        var rect = menu.getBoundingClientRect();
        if (rect.right > window.innerWidth) menu.style.left = (e.clientX - rect.width) + 'px';
        if (rect.bottom > window.innerHeight) menu.style.top = (e.clientY - rect.height) + 'px';
      });

      // Also on right-click in the detail request/response panes
      var detailPanes = document.querySelectorAll('#detail-request-raw, #detail-response-raw');
      detailPanes.forEach(function(pane) {
        pane.addEventListener('contextmenu', function(e) {
          if (!selectedTrafficId) return;
          e.preventDefault();
          contextRowId = selectedTrafficId;
          contextRowData = trafficData.find(function(r) { return r.id === contextRowId; });
          menu.classList.remove('hidden');
          menu.style.left = e.clientX + 'px';
          menu.style.top = e.clientY + 'px';
        });
      });

      // Hide on click elsewhere
      document.addEventListener('click', function() { menu.classList.add('hidden'); });
      document.addEventListener('contextmenu', function(e) {
        if (!e.target.closest('#traffic-body') && !e.target.closest('#detail-request-raw') && !e.target.closest('#detail-response-raw')) {
          menu.classList.add('hidden');
        }
      });

      // Handle menu actions
      menu.addEventListener('click', async function(e) {
        var action = e.target.dataset.action;
        if (!action || !contextRowId) return;
        menu.classList.add('hidden');

        if (action === 'send-repeater') {
          selectTrafficRow(contextRowId).then(function() { sendToRepeater(); });
          return;
        }

        // Fetch raw request for curl conversion
        var reqData = await api('/api/collections/_req/records/' + contextRowId);
        if (!reqData || !reqData.raw) { alert('Failed to load request'); return; }
        var rawReq = reqData.raw;

        if (action === 'copy-raw') {
          navigator.clipboard.writeText(rawReq);
          return;
        }

        // Convert raw HTTP to curl
        var curl = rawToCurl(rawReq, contextRowData);
        if (action === 'copy-curl-proxy') {
          curl += ' -x http://127.0.0.1:9090 -k';
        }
        navigator.clipboard.writeText(curl);
      });

      function rawToCurl(raw, rowData) {
        raw = raw.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
        var parts = raw.split('\n\n');
        var headerSection = parts[0] || '';
        var body = parts.slice(1).join('\n\n');
        var lines = headerSection.split('\n');
        if (lines.length === 0) return 'curl ""';

        // Parse request line
        var reqLine = lines[0].match(/^(\S+)\s+(\S+)/);
        var method = reqLine ? reqLine[1] : 'GET';
        var path = reqLine ? reqLine[2] : '/';

        // Build URL from row data
        var host = '';
        var protocol = 'http';
        if (rowData) {
          host = (rowData.host || '').replace(/^https?:\/\//, '');
          if (rowData.is_https) protocol = 'https';
          if (rowData.port) host = host.split(':')[0] + ':' + rowData.port;
        }
        // Fallback: extract Host header
        if (!host) {
          for (var i = 1; i < lines.length; i++) {
            var m = lines[i].match(/^Host:\s*(.+)/i);
            if (m) { host = m[1].trim(); break; }
          }
        }
        var url = protocol + '://' + host + path;

        var esc = function(s) { return s.replace(/'/g, "'\\''"); };
        var parts2 = [];

        // Method + URL
        if (method !== 'GET') {
          parts2.push("curl -X " + method + " '" + esc(url) + "'");
        } else {
          parts2.push("curl '" + esc(url) + "'");
        }

        // Headers — include all except Connection and Content-Length
        for (var j = 1; j < lines.length; j++) {
          var line = lines[j].trim();
          if (!line) continue;
          var lower = line.toLowerCase();
          if (lower.indexOf('connection:') === 0 || lower.indexOf('content-length:') === 0) continue;
          parts2.push("  -H '" + esc(line) + "'");
        }

        // Body
        if (body && body.trim()) {
          parts2.push("  -d '" + esc(body.trim()) + "'");
        }

        return parts2.join(' \\\n');
      }
    })();

    // Initial load
    initSettings();
    checkStatus();
    loadHosts();
    loadTraffic();

    // Auto-refresh every 5 seconds
    setInterval(function() {
      if (currentView === 'traffic') {
        loadTraffic();
      }
    }, 5000);

    setInterval(checkStatus, 15000);
    setInterval(loadHosts, 10000);
  }

  // Boot
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
