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
  var activeProjectFilter = ''; // '' = show all traffic, 'APPLE-BB-001' = filter by project

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
    var hostParam = hostFilter ? '&host=' + encodeURIComponent(hostFilter) : '';
    var projectParam = activeProjectFilter ? '&project=' + encodeURIComponent(activeProjectFilter) : '';
    var data = await api('/api/traffic/list?perPage=500' + hostParam + projectParam);

    if (!data || !data.items) return;

    allTrafficData = data.items;
    applyClientFilter();
  }

  // Chip filter state — sets of method names and mime tokens that the
  // user has clicked. Empty set means "no filter from this dimension"
  // (i.e. don't constrain on it). Multiple selections within the same
  // dimension are OR'd; across dimensions they AND.
  var chipFilters = { method: new Set(), mime: new Set() };

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

    // Method chip filter (OR within the chip set)
    if (chipFilters.method.size > 0) {
      filtered = filtered.filter(function(row) {
        var m = (row.req_json && row.req_json.method) || row.method || '';
        return chipFilters.method.has(m.toUpperCase());
      });
    }

    // Mime chip filter (OR within the chip set). "other" matches anything
    // that doesn't fall in our known buckets.
    if (chipFilters.mime.size > 0) {
      var known = ['json', 'html', 'javascript', 'xml', 'image'];
      filtered = filtered.filter(function(row) {
        var mime = ((row.resp_json && row.resp_json.mime) || row.mime || '').toLowerCase();
        for (var token of chipFilters.mime) {
          if (token === 'other') {
            var matchedKnown = known.some(function(k) { return mime.indexOf(k) >= 0; });
            if (!matchedKnown && mime !== '') return true;
          } else if (mime.indexOf(token) >= 0) {
            return true;
          }
        }
        return false;
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

  // ===========================================================
  // Command palette (Cmd/Ctrl+K) — Caido / VSCode / Linear-style.
  // Registry of available commands; each is an object with id,
  // label, optional hint string, and a run function.
  // ===========================================================
  var paletteCommands = [];
  var paletteVisible = [];
  var paletteIndex = 0;

  function buildPaletteCommands() {
    paletteCommands = [
      { id: 'view-traffic',   label: 'Switch to Traffic',   hint: 'Cmd+1', run: function() { switchView('traffic'); } },
      { id: 'view-repeater',  label: 'Switch to Repeater',  hint: 'Cmd+2', run: function() { switchView('repeater'); } },
      { id: 'view-intercept', label: 'Switch to Intercept', hint: 'Cmd+3', run: function() { switchView('intercept'); } },
      { id: 'view-settings',  label: 'Switch to Settings',  hint: 'Cmd+4', run: function() { switchView('settings'); } },
      { id: 'send-repeater',  label: 'Send selected request to Repeater', hint: 'Cmd+R', run: function() {
        var btn = document.getElementById('btn-send-to-repeater');
        if (btn) btn.click();
      }},
      { id: 'refresh',        label: 'Refresh traffic list', hint: 'Cmd+Shift+R', run: function() {
        var btn = document.getElementById('traffic-refresh');
        if (btn) btn.click();
      }},
      { id: 'find-in-pane',   label: 'Find in request/response', hint: 'Cmd+F', run: function() {
        var pane = document.getElementById('detail-pane-response');
        var bar = pane && pane.querySelector('.find-bar');
        if (bar) openFindBar(bar);
      }},
      { id: 'toggle-intercept', label: 'Toggle intercept', hint: 'Cmd+P', run: function() {
        var btn = document.getElementById('intercept-toggle');
        if (btn) btn.click();
      }},
      { id: 'clear-chips',    label: 'Clear chip filters', run: function() {
        var btn = document.getElementById('chip-clear');
        if (btn) btn.click();
      }},
      { id: 'reset-scope',    label: 'Reset scope (two-click)', run: function() {
        var btn = document.getElementById('scope-reset-btn');
        if (btn) { switchView('settings'); setTimeout(function(){ btn.click(); }, 200); }
      }},
    ];
  }

  function initCommandPalette() {
    buildPaletteCommands();

    var palette = document.getElementById('cmd-palette');
    var input = document.getElementById('cmd-palette-input');
    var list = document.getElementById('cmd-palette-list');
    if (!palette || !input || !list) return;

    function renderList(query) {
      var q = (query || '').trim().toLowerCase();
      paletteVisible = q
        ? paletteCommands.filter(function(c) { return fuzzyMatch(c.label.toLowerCase(), q); })
        : paletteCommands.slice();
      paletteIndex = 0;
      if (paletteVisible.length === 0) {
        list.innerHTML = '<li class="cmd-empty">No matching commands</li>';
        return;
      }
      list.innerHTML = paletteVisible.map(function(c, i) {
        var hint = c.hint ? '<span class="cmd-shortcut">' + escapeHtml(c.hint) + '</span>' : '';
        return '<li class="' + (i === 0 ? 'active' : '') + '" data-idx="' + i + '">' +
          '<span>' + escapeHtml(c.label) + '</span>' + hint + '</li>';
      }).join('');
    }

    function setActive(newIdx) {
      paletteIndex = (newIdx + paletteVisible.length) % paletteVisible.length;
      Array.prototype.slice.call(list.querySelectorAll('li')).forEach(function(li, i) {
        li.classList.toggle('active', i === paletteIndex);
        if (i === paletteIndex) li.scrollIntoView({ block: 'nearest' });
      });
    }

    function execute(idx) {
      var cmd = paletteVisible[idx];
      closePalette();
      if (cmd && typeof cmd.run === 'function') cmd.run();
    }

    function openPalette() {
      palette.classList.remove('hidden');
      input.value = '';
      renderList('');
      input.focus();
    }
    function closePalette() {
      palette.classList.add('hidden');
    }

    // Public helper for the global shortcut listener.
    palette._open = openPalette;
    palette._close = closePalette;

    input.addEventListener('input', function() { renderList(input.value); });
    input.addEventListener('keydown', function(e) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setActive(paletteIndex + 1); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); setActive(paletteIndex - 1); }
      else if (e.key === 'Enter')  { e.preventDefault(); execute(paletteIndex); }
      else if (e.key === 'Escape') { e.preventDefault(); closePalette(); }
    });
    list.addEventListener('click', function(e) {
      var li = e.target.closest('li[data-idx]');
      if (li) execute(parseInt(li.dataset.idx, 10));
    });
    palette.querySelector('.cmd-palette-backdrop').addEventListener('click', closePalette);
  }

  // Tiny non-recursive fuzzy: each query char must appear in order
  // somewhere in the candidate. Cheap and good enough for palette-size
  // command lists. Anchored prefix match wins by being naturally
  // earlier in the result list since we just preserve registration order.
  function fuzzyMatch(s, q) {
    var i = 0, j = 0;
    while (i < s.length && j < q.length) {
      if (s.charCodeAt(i) === q.charCodeAt(j)) j++;
      i++;
    }
    return j === q.length;
  }

  // ===========================================================
  // Global keyboard shortcuts — Caido-inspired.
  // Cmd/Ctrl+K   open command palette
  // Cmd/Ctrl+1-4 switch view
  // Cmd/Ctrl+R   send selected request to repeater
  // Cmd/Ctrl+P   toggle intercept
  // ===========================================================
  function initGlobalShortcuts() {
    document.addEventListener('keydown', function(e) {
      var mod = e.metaKey || e.ctrlKey;
      if (!mod) return;

      // Don't hijack typing inside an input/textarea unless it's a
      // shortcut that's traditionally fine to fire there (Cmd+K, Cmd+R).
      var t = e.target;
      var typing = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable);

      if (e.key === 'k' || e.key === 'K') {
        e.preventDefault();
        var palette = document.getElementById('cmd-palette');
        if (!palette) return;
        if (palette.classList.contains('hidden')) palette._open();
        else palette._close();
        return;
      }

      if (typing) return;

      var viewMap = { '1': 'traffic', '2': 'repeater', '3': 'intercept', '4': 'settings' };
      if (viewMap[e.key]) {
        e.preventDefault();
        switchView(viewMap[e.key]);
        return;
      }

      if (e.key === 'r' || e.key === 'R') {
        e.preventDefault();
        var btn = document.getElementById('btn-send-to-repeater');
        if (btn && !document.getElementById('traffic-detail').classList.contains('hidden')) btn.click();
        return;
      }

      if (e.key === 'p' || e.key === 'P') {
        e.preventDefault();
        var ibtn = document.getElementById('intercept-toggle');
        if (ibtn) ibtn.click();
        return;
      }
    });
  }

  // ===========================================================
  // Match & Replace UI — backed by /api/match-replace REST.
  // List rules with inline enable toggle + delete; an
  // expandable form below adds new rules.
  // ===========================================================
  function initMatchReplace() {
    var addBtn = document.getElementById('mr-add-btn');
    if (!addBtn) return;
    addBtn.addEventListener('click', async function() {
      var body = {
        type:    document.getElementById('mr-type').value,
        match:   document.getElementById('mr-match').value,
        replace: document.getElementById('mr-replace').value,
        scope:   document.getElementById('mr-scope').value,
        comment: document.getElementById('mr-comment').value,
        enabled: true,
      };
      if (!body.match) { document.getElementById('mr-match').focus(); return; }
      var res = await api('/api/match-replace', { method: 'POST', body: JSON.stringify(body) });
      if (res && res.success) {
        ['mr-match','mr-replace','mr-scope','mr-comment'].forEach(function(id){
          document.getElementById(id).value = '';
        });
        loadMatchReplaceRules();
      }
    });
    loadMatchReplaceRules();
  }

  async function loadMatchReplaceRules() {
    var container = document.getElementById('mr-rules');
    if (!container) return;
    var data = await api('/api/match-replace');
    var rules = (data && data.rules) || [];
    if (rules.length === 0) {
      container.innerHTML = '<div class="mr-empty">No match &amp; replace rules. Add one below.</div>';
      return;
    }
    container.innerHTML = rules.map(function(r) {
      var enabled = r.enabled === true || r.enabled === 1 || r.enabled === 'true';
      var scope = r.scope || 'all hosts';
      var comment = r.comment ? ' · ' + escapeHtml(r.comment) : '';
      return '<div class="mr-rule ' + (enabled ? '' : 'disabled') + '" data-id="' + escapeAttr(r.id) + '">' +
        '<span class="mr-toggle ' + (enabled ? 'on' : 'off') + '" data-action="toggle" title="Toggle">' + (enabled ? '✓' : '○') + '</span>' +
        '<span class="mr-type">' + escapeHtml(r.type || '') + '</span>' +
        '<span class="mr-match" title="' + escapeAttr(r.match || '') + '">' + escapeHtml(r.match || '') + '</span>' +
        '<span class="mr-replace" title="' + escapeAttr(r.replace || '') + '">' + escapeHtml(r.replace || '') + '</span>' +
        '<span class="mr-scope" title="' + escapeAttr(scope + comment) + '">' + escapeHtml(scope) + comment + '</span>' +
        '<button class="mr-delete" data-action="delete" title="Delete">×</button>' +
      '</div>';
    }).join('');
    container.querySelectorAll('.mr-rule').forEach(function(row) {
      var id = row.dataset.id;
      row.querySelector('[data-action="toggle"]').addEventListener('click', async function() {
        var nowEnabled = !row.classList.contains('disabled');
        await api('/api/match-replace/' + encodeURIComponent(id), {
          method: 'PATCH',
          body: JSON.stringify({ enabled: !nowEnabled }),
        });
        loadMatchReplaceRules();
      });
      row.querySelector('[data-action="delete"]').addEventListener('click', async function() {
        await api('/api/match-replace/' + encodeURIComponent(id), { method: 'DELETE' });
        loadMatchReplaceRules();
      });
    });
  }

  // ===========================================================
  // Row highlights — right-click "Highlight" submenu sets a
  // localStorage-backed color tint on a traffic row id. Persists
  // across reloads but is per-browser; works without backend.
  // ===========================================================
  var rowHighlights = (function() {
    try { return JSON.parse(localStorage.getItem('lorg-row-hl') || '{}'); }
    catch (e) { return {}; }
  })();
  function saveRowHighlights() {
    try { localStorage.setItem('lorg-row-hl', JSON.stringify(rowHighlights)); } catch (e) {}
  }
  function setRowHighlight(id, color) {
    if (!color || color === 'none') delete rowHighlights[id];
    else rowHighlights[id] = color;
    saveRowHighlights();
    var tr = document.querySelector('#traffic-body tr[data-id="' + cssEscape(id) + '"]');
    if (tr) {
      tr.classList.remove('hl-yellow', 'hl-orange', 'hl-red', 'hl-green', 'hl-blue');
      if (color && color !== 'none') tr.classList.add('hl-' + color);
    }
  }
  function applyStoredHighlights() {
    Object.keys(rowHighlights).forEach(function(id) {
      var tr = document.querySelector('#traffic-body tr[data-id="' + cssEscape(id) + '"]');
      if (tr) {
        tr.classList.remove('hl-yellow', 'hl-orange', 'hl-red', 'hl-green', 'hl-blue');
        tr.classList.add('hl-' + rowHighlights[id]);
      }
    });
  }
  // Minimal CSS.escape polyfill for old WebKit (just enough for our IDs).
  function cssEscape(s) { return s.replace(/([^a-zA-Z0-9_-])/g, '\\$1'); }

  // Wire chip clicks once on init.
  function initChipStrip() {
    var strip = document.getElementById('traffic-chips');
    if (!strip) return;
    strip.addEventListener('click', function(e) {
      var btn = e.target.closest('.chip');
      if (!btn) return;
      if (btn.id === 'chip-clear') {
        chipFilters.method.clear();
        chipFilters.mime.clear();
        Array.prototype.slice.call(strip.querySelectorAll('.chip.active')).forEach(function(b) {
          b.classList.remove('active');
        });
        applyClientFilter();
        return;
      }
      var type = btn.dataset.chipType;
      var token = btn.dataset.chip;
      if (!type || !token) return;
      var bucket = chipFilters[type];
      if (bucket.has(token)) {
        bucket.delete(token);
        btn.classList.remove('active');
      } else {
        bucket.add(token);
        btn.classList.add('active');
      }
      applyClientFilter();
    });
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
    // Negation: leading '-' or 'NOT ' inverts the condition. Strip the
    // marker and recurse so all field clauses get the negation for free.
    if (cond.length > 1 && cond.charAt(0) === '-') {
      return !matchesSingleCondition(row, cond.substring(1).trim());
    }
    if (/^NOT\s+/i.test(cond)) {
      return !matchesSingleCondition(row, cond.replace(/^NOT\s+/i, '').trim());
    }

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
      case 'source': var g = row.generated_by || ''; return g.indexOf('ai/mcp') !== -1 ? 'a' : g.indexOf('repeater/') !== -1 ? 'm' : 'z';
      case 'time': return row.created || '';
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

    if (trafficData.length === 0) {
      tbody.innerHTML = '<tr><td colspan="8" class="empty-state">No traffic captured yet. Start a proxy or send requests via MCP.</td></tr>';
      lastTrafficFingerprint = '';
      return;
    }

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
      var length = resp.length || row.length || '';
      var genBy = row.generated_by || '';
      var source = genBy.indexOf('ai/mcp') !== -1 ? 'AI' : genBy.indexOf('repeater/') !== -1 ? 'Repeater' : 'Proxy';
      var sourceClass = source === 'AI' ? 'source-ai' : source === 'Repeater' ? 'source-repeater' : 'source-proxy';
      var created = row.created || '';
      var timeStr = created ? formatTime(created) : '';
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
        '<td class="col-time">' + escapeHtml(timeStr) + '</td>' +
        '</tr>';
    }).join('');

    $$('#traffic-body tr').forEach(function(tr) {
      tr.addEventListener('click', function() { selectTrafficRow(tr.dataset.id); });
    });

    // Re-apply any persisted highlight tints after each render.
    applyStoredHighlights();
  }

  async function selectTrafficRow(id) {
    selectedTrafficId = id;
    renderTraffic(true);

    // Fetch from unified endpoint (checks _req/_resp, then _data.req/resp, then reconstructs from JSON)
    var detail = await api('/api/traffic/' + id + '/detail');

    var detailPane = $('#traffic-detail');
    detailPane.classList.remove('hidden');
    var resizeHandle = document.getElementById('resize-handle');
    if (resizeHandle) resizeHandle.classList.remove('hidden');

    // Store raw data for format toggle
    detailPane._rawRequest = '';
    detailPane._rawResponse = '';
    detailPane._detailSource = 'none';

    if (detail) {
      detailPane._currentId = id;
      detailPane._rawRequest = detail.request || '';
      detailPane._rawResponse = detail.response || '';
      detailPane._detailSource = detail.source || 'none';
      renderRequestWithFormat(detail.request || 'No request data', 'pretty');
      renderResponseWithFormat(detail.response, 'pretty');
    } else {
      $('#detail-request-raw').innerHTML = 'Failed to load';
      $('#detail-response-raw').innerHTML = 'Failed to load';
    }

    // Store for "send to repeater"
    detailPane.dataset.reqRaw = detailPane._rawRequest;
    detailPane.dataset.host = '';
    detailPane.dataset.port = '';

    var row = trafficData.find(function(r) { return r.id === id; });
    if (row) {
      detailPane.dataset.host = (row.host || '').replace(/^https?:\/\//, '');
      detailPane.dataset.port = row.port || (row.is_https ? '443' : '80');
      detailPane.dataset.tls = row.is_https ? 'true' : 'false';
    }

    populateStatusStrip(row, detail);
  }

  // Populate the at-a-glance status strip in the detail toolbar from the
  // selected row + fetched detail. Inspired by Postman's response header
  // bar — at-a-glance method/url/status/time/size/mime instead of having
  // to hunt for it across the table row and pane headers.
  function populateStatusStrip(row, detail) {
    var methodEl = $('#dss-method');
    var urlEl    = $('#dss-url');
    var statusEl = $('#dss-status');
    var timeEl   = $('#dss-time');
    var sizeEl   = $('#dss-size');
    var mimeEl   = $('#dss-mime');
    if (!methodEl) return;

    // Row fields land nested in req_json / resp_json (see renderTraffic).
    // Fall back to flat keys for tools that flatten the shape.
    var req  = (row && row.req_json)  || {};
    var resp = (row && row.resp_json) || {};
    var method = req.method  || row && row.method || '';
    var path   = req.path    || req.url || row && row.path || '';
    var status = resp.status || (row && row.status) || 0;
    var length = resp.length || (row && (row.length || row.resp_length)) || 0;
    var mime   = resp.mime   || (row && row.mime) || '';

    methodEl.textContent = method;
    methodEl.className = 'dss-method method-' + (method || '').toLowerCase();
    urlEl.textContent = path;

    // Status badge
    statusEl.textContent = status ? String(status) : '—';
    statusEl.className = 'dss-status';
    if (status >= 200 && status < 300) statusEl.classList.add('s2xx');
    else if (status >= 300 && status < 400) statusEl.classList.add('s3xx');
    else if (status >= 400 && status < 500) statusEl.classList.add('s4xx');
    else if (status >= 500) statusEl.classList.add('s5xx');

    // Timing — backend doesn't always supply it; show — if absent.
    var elapsedMs = detail && (detail.elapsed_ms || detail.elapsedMs);
    timeEl.textContent = elapsedMs ? Math.round(elapsedMs) + ' ms' : '— ms';

    sizeEl.textContent = length ? formatBytes(length) : '—';
    mimeEl.textContent = mime || '—';

    // Caido-style at-a-glance footer in the response pane: shows
    // the same size/time at the bottom-right of the pane so it stays
    // visible while scrolling the body.
    var footerStats = $('#resp-pane-stats');
    if (footerStats) {
      var bytes = length ? formatBytes(length) : '—';
      var ms = elapsedMs ? Math.round(elapsedMs) + ' ms' : '— ms';
      footerStats.textContent = bytes + ' · ' + ms;
    }
  }

  // --- Format toggles ---
  // Both request and response panes share the same render logic; only the
  // target element and the button group differ. renderHTTPWithFormat is the
  // shared core; renderRequestWithFormat / renderResponseWithFormat are the
  // pane-specific entry points.
  var currentResponseFormat = 'pretty';
  var currentRequestFormat = 'pretty';

  function renderHTTPWithFormat(el, raw, format, btnSelector) {
    if (!el) return;
    if (!raw) { el.innerHTML = 'No data'; return; }

    // Update active button within the same toggle group only.
    $$(btnSelector).forEach(function(b) { b.classList.toggle('active', b.dataset.fmt === format); });

    if (format === 'pretty') {
      // Image preview path — for image/* content-type, show the image
      // itself instead of trying to highlight binary garbage. Lifted
      // from Burp's Render tab.
      var ct = extractCT(raw);
      if (ct && ct.toLowerCase().indexOf('image/') === 0) {
        var detail = $('#traffic-detail');
        var id = detail && detail._currentId;
        var part = el.id === 'detail-request-raw' ? 'request' : 'response';
        if (id) {
          var src = '/api/traffic/' + encodeURIComponent(id) + '/body?part=' + part;
          el.innerHTML = '<div class="image-preview-wrap">' +
            '<div class="image-preview-meta">' + escapeHtml(ct) + '</div>' +
            '<img class="image-preview" src="' + src + '" alt="response body">' +
          '</div>';
          return;
        }
      }
      el.innerHTML = highlightHTTP(raw);
    } else if (format === 'raw') {
      el.textContent = raw;
    } else if (format === 'headers') {
      var headerEnd = raw.indexOf('\r\n\r\n');
      if (headerEnd < 0) headerEnd = raw.indexOf('\n\n');
      var headers = headerEnd >= 0 ? raw.substring(0, headerEnd) : raw;
      el.innerHTML = highlightHTTP(headers);
    } else if (format === 'cookies') {
      el.innerHTML = renderCookiesView(raw);
    } else if (format === 'tree') {
      el.innerHTML = renderTreeView(raw);
    } else if (format === 'render') {
      el.innerHTML = renderHTMLView(raw);
    } else if (format === 'hex') {
      el.innerHTML = renderHexView(raw);
    }
  }

  // renderCookiesView parses a raw HTTP message and produces an HTML
  // table of cookies. Two sources:
  //   - Set-Cookie headers in a response (with attributes)
  //   - Cookie header in a request (just name=value pairs)
  // The view highlights missing security flags (HttpOnly, Secure,
  // SameSite) for response cookies — common security review surface.
  function renderCookiesView(raw) {
    if (!raw) return '<div class="cookies-view"><div class="empty">No data</div></div>';
    var sep = raw.indexOf('\r\n\r\n');
    if (sep < 0) sep = raw.indexOf('\n\n');
    var headers = sep >= 0 ? raw.substring(0, sep) : raw;
    var lines = headers.split(/\r?\n/);

    var setCookies = [];
    var reqCookies = [];
    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];
      var ci = line.indexOf(':');
      if (ci <= 0) continue;
      var name = line.substring(0, ci).trim().toLowerCase();
      var value = line.substring(ci + 1).trim();
      if (name === 'set-cookie') {
        setCookies.push(parseSetCookie(value));
      } else if (name === 'cookie') {
        // Single header may contain multiple cookies separated by ';'
        value.split(';').forEach(function(pair) {
          var eq = pair.indexOf('=');
          if (eq > 0) {
            reqCookies.push({
              name: pair.substring(0, eq).trim(),
              value: pair.substring(eq + 1).trim(),
            });
          }
        });
      }
    }

    var html = '<div class="cookies-view">';
    if (setCookies.length === 0 && reqCookies.length === 0) {
      html += '<div class="empty">No cookies in this message.</div>';
    }
    if (setCookies.length > 0) {
      html += '<h4>Set-Cookie (' + setCookies.length + ')</h4>';
      html += '<table><thead><tr>' +
        '<th>Name</th><th>Value</th><th>Domain</th><th>Path</th>' +
        '<th>Expires</th><th>HttpOnly</th><th>Secure</th><th>SameSite</th>' +
        '</tr></thead><tbody>';
      setCookies.forEach(function(c) {
        html += '<tr>' +
          '<td>' + escapeHtml(c.name) + '</td>' +
          '<td>' + escapeHtml(c.value || '') + '</td>' +
          '<td>' + escapeHtml(c.domain || '—') + '</td>' +
          '<td>' + escapeHtml(c.path || '—') + '</td>' +
          '<td>' + escapeHtml(c.expires || c.maxAge || 'Session') + '</td>' +
          '<td class="' + (c.httpOnly ? 'flag-on' : 'flag-off') + '">' + (c.httpOnly ? 'yes' : 'no') + '</td>' +
          '<td class="' + (c.secure   ? 'flag-on' : 'flag-off') + '">' + (c.secure   ? 'yes' : 'no') + '</td>' +
          '<td class="' + (c.sameSite ? 'flag-on' : 'flag-off') + '">' + escapeHtml(c.sameSite || '—') + '</td>' +
        '</tr>';
      });
      html += '</tbody></table>';
    }
    if (reqCookies.length > 0) {
      html += '<h4>Cookie (' + reqCookies.length + ')</h4>';
      html += '<table><thead><tr><th>Name</th><th>Value</th></tr></thead><tbody>';
      reqCookies.forEach(function(c) {
        html += '<tr><td>' + escapeHtml(c.name) + '</td><td>' + escapeHtml(c.value) + '</td></tr>';
      });
      html += '</tbody></table>';
    }
    html += '</div>';
    return html;
  }

  // parseSetCookie parses one Set-Cookie header value into a structured
  // record. Cookie name/value comes first, then ;-separated attributes.
  function parseSetCookie(raw) {
    var parts = raw.split(';');
    var first = parts.shift() || '';
    var eq = first.indexOf('=');
    var c = {
      name: eq > 0 ? first.substring(0, eq).trim() : first.trim(),
      value: eq > 0 ? first.substring(eq + 1).trim() : '',
      httpOnly: false,
      secure: false,
    };
    parts.forEach(function(attr) {
      var av = attr.trim();
      var aeq = av.indexOf('=');
      var key = (aeq > 0 ? av.substring(0, aeq) : av).trim().toLowerCase();
      var val = aeq > 0 ? av.substring(aeq + 1).trim() : '';
      switch (key) {
        case 'domain':   c.domain = val; break;
        case 'path':     c.path = val; break;
        case 'expires':  c.expires = val; break;
        case 'max-age':  c.maxAge = val + 's'; break;
        case 'samesite': c.sameSite = val; break;
        case 'httponly': c.httpOnly = true; break;
        case 'secure':   c.secure = true; break;
      }
    });
    return c;
  }

  // ===========================================================
  // JSON Tree view (Postman-style collapsible tree).
  // Parses the body as JSON; if valid, renders <details> blocks
  // for objects/arrays with type-colored values. If invalid,
  // gracefully falls back to a notice + the raw body.
  // ===========================================================
  function renderTreeView(raw) {
    if (!raw) return '<div class="tree-view"><div class="tree-empty">No data</div></div>';
    var sep = raw.indexOf('\r\n\r\n');
    if (sep < 0) sep = raw.indexOf('\n\n');
    var body = sep >= 0 ? raw.substring(sep + (raw.indexOf('\r\n\r\n') >= 0 ? 4 : 2)) : raw;
    body = body.trim();
    if (!body) return '<div class="tree-view"><div class="tree-empty">Empty body</div></div>';

    var parsed;
    try {
      parsed = JSON.parse(body);
    } catch (e) {
      return '<div class="tree-view"><div class="tree-empty">Body is not valid JSON — switch to Pretty for the syntax-highlighted view.</div></div>';
    }
    return '<div class="tree-view">' + renderTreeNode(parsed, /*key*/ null, /*depth*/ 0, /*last*/ true) + '</div>';
  }

  function renderTreeNode(value, key, depth, last) {
    var keyHTML = key !== null ? '<span class="tree-key">' + escapeHtml(String(key)) + '</span><span class="tree-colon">:</span> ' : '';

    if (value === null) {
      return '<div class="tree-row">' + keyHTML + '<span class="tree-null">null</span></div>';
    }
    if (typeof value === 'boolean') {
      return '<div class="tree-row">' + keyHTML + '<span class="tree-bool">' + value + '</span></div>';
    }
    if (typeof value === 'number') {
      return '<div class="tree-row">' + keyHTML + '<span class="tree-number">' + value + '</span></div>';
    }
    if (typeof value === 'string') {
      return '<div class="tree-row">' + keyHTML + '<span class="tree-string">"' + escapeHtml(value) + '"</span></div>';
    }
    if (Array.isArray(value)) {
      var arrLabel = '<span class="tree-bracket">[</span><span class="tree-count">' + value.length + '</span><span class="tree-bracket">]</span>';
      if (value.length === 0) {
        return '<div class="tree-row">' + keyHTML + arrLabel + '</div>';
      }
      var open = depth < 2 ? ' open' : '';
      var children = '';
      for (var i = 0; i < value.length; i++) {
        children += renderTreeNode(value[i], i, depth + 1, i === value.length - 1);
      }
      return '<details class="tree-node"' + open + '><summary>' + keyHTML + arrLabel + '</summary><div class="tree-children">' + children + '</div></details>';
    }
    if (typeof value === 'object') {
      var keys = Object.keys(value);
      var objLabel = '<span class="tree-bracket">{</span><span class="tree-count">' + keys.length + '</span><span class="tree-bracket">}</span>';
      if (keys.length === 0) {
        return '<div class="tree-row">' + keyHTML + objLabel + '</div>';
      }
      var openObj = depth < 2 ? ' open' : '';
      var childrenObj = '';
      for (var k = 0; k < keys.length; k++) {
        childrenObj += renderTreeNode(value[keys[k]], keys[k], depth + 1, k === keys.length - 1);
      }
      return '<details class="tree-node"' + openObj + '><summary>' + keyHTML + objLabel + '</summary><div class="tree-children">' + childrenObj + '</div></details>';
    }
    return '<div class="tree-row">' + keyHTML + escapeHtml(String(value)) + '</div>';
  }

  // ===========================================================
  // HTML render view (Burp Render tab).
  // For text/html responses, render in a sandboxed iframe with
  // sandbox="" (no scripts, no forms, no top-nav, no same-origin)
  // so we can preview layout safely. Falls back to a notice for
  // non-HTML content.
  // ===========================================================
  function renderHTMLView(raw) {
    var ct = extractCT(raw);
    if (!ct || ct.toLowerCase().indexOf('html') < 0) {
      return '<div class="render-view"><div class="render-empty">Render view is only meaningful for HTML responses (this response is ' + escapeHtml(ct || 'unknown') + ').</div></div>';
    }
    var sep = raw.indexOf('\r\n\r\n');
    if (sep < 0) sep = raw.indexOf('\n\n');
    var body = sep >= 0 ? raw.substring(sep + (raw.indexOf('\r\n\r\n') >= 0 ? 4 : 2)) : raw;
    // sandbox="" disables scripts, forms, popups, top-nav, plugins,
    // and same-origin access. Iframe can render layout but cannot
    // exfil cookies, navigate, or run scripts.
    var doc = body.replace(/"/g, '&quot;');
    return '<div class="render-view"><iframe class="render-frame" sandbox="" srcdoc="' + doc + '"></iframe></div>';
  }

  // ===========================================================
  // Hex view (Burp Hex tab).
  // Renders the body as 16-byte rows: 8-digit offset | hex bytes
  // | ASCII (non-printable as '.'). Capped at 16KB per render to
  // keep the DOM responsive — past that, switch to Raw.
  // ===========================================================
  function renderHexView(raw) {
    var sep = raw.indexOf('\r\n\r\n');
    if (sep < 0) sep = raw.indexOf('\n\n');
    var body = sep >= 0 ? raw.substring(sep + (raw.indexOf('\r\n\r\n') >= 0 ? 4 : 2)) : raw;
    if (!body) return '<div class="hex-view"><div class="hex-empty">Empty body</div></div>';

    var maxBytes = 16 * 1024;
    var truncated = false;
    if (body.length > maxBytes) {
      body = body.substring(0, maxBytes);
      truncated = true;
    }

    var lines = [];
    for (var i = 0; i < body.length; i += 16) {
      var chunk = body.substring(i, i + 16);
      var hex = '';
      var ascii = '';
      for (var j = 0; j < 16; j++) {
        if (j < chunk.length) {
          var code = chunk.charCodeAt(j) & 0xff;
          hex += (code < 16 ? '0' : '') + code.toString(16) + ' ';
          ascii += (code >= 0x20 && code < 0x7f) ? chunk[j] : '.';
        } else {
          hex += '   ';
          ascii += ' ';
        }
        if (j === 7) hex += ' '; // visual midpoint gap
      }
      var offset = i.toString(16).padStart(8, '0');
      lines.push(
        '<div class="hex-row">' +
          '<span class="hex-offset">' + offset + '</span>' +
          '<span class="hex-bytes">' + hex.trimEnd() + '</span>' +
          '<span class="hex-ascii">' + escapeHtml(ascii) + '</span>' +
        '</div>'
      );
    }
    var trunc = truncated ? '<div class="hex-trunc">Hex view truncated at 16 KB. Switch to Raw for the full body.</div>' : '';
    return '<div class="hex-view">' + lines.join('') + trunc + '</div>';
  }

  // extractCT pulls Content-Type out of a raw HTTP message (headers part).
  // Returns the bare media type without parameters.
  function extractCT(raw) {
    if (!raw) return '';
    var sep = raw.indexOf('\r\n\r\n');
    if (sep < 0) sep = raw.indexOf('\n\n');
    var headers = sep >= 0 ? raw.substring(0, sep) : raw;
    var lines = headers.split(/\r?\n/);
    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];
      var ci = line.indexOf(':');
      if (ci > 0 && line.substring(0, ci).toLowerCase() === 'content-type') {
        var v = line.substring(ci + 1).trim();
        var sc = v.indexOf(';');
        return sc >= 0 ? v.substring(0, sc).trim() : v;
      }
    }
    return '';
  }

  function renderResponseWithFormat(rawResp, format) {
    currentResponseFormat = format;
    renderHTTPWithFormat($('#detail-response-raw'), rawResp, format, '.fmt-btn');
  }

  function renderRequestWithFormat(rawReq, format) {
    currentRequestFormat = format;
    renderHTTPWithFormat($('#detail-request-raw'), rawReq, format, '.req-fmt-btn');
  }

  // ===========================================================
  // Find-in-pane (Cmd/Ctrl+F) — Burp/Postman-style.
  //
  // Strategy: walk the pane's text nodes, collect matches, wrap
  // each in a <mark class="find-match"> while preserving the
  // surrounding syntax-highlight spans. The pane's pre-find HTML
  // is snapshotted so closing the find bar fully restores the
  // original highlighting (no re-render of the response needed).
  // ===========================================================
  var findState = {
    bar: null,
    pane: null,
    paneEl: null,
    snapshot: null,
    matches: [],
    current: -1,
  };

  function initFindInPane() {
    document.addEventListener('keydown', function(e) {
      // Cmd/Ctrl+F when focus is inside the detail panel: open find
      if ((e.metaKey || e.ctrlKey) && (e.key === 'f' || e.key === 'F')) {
        var detail = document.getElementById('traffic-detail');
        if (!detail || detail.classList.contains('hidden')) return;
        // Pick the most recently focused pane, or fall back to response
        var pane = document.activeElement && document.activeElement.closest && document.activeElement.closest('.detail-split-pane');
        if (!pane) pane = document.getElementById('detail-pane-response');
        var bar = pane.querySelector('.find-bar');
        if (!bar) return;
        e.preventDefault();
        openFindBar(bar);
        return;
      }
      // Esc closes any open find bar
      if (e.key === 'Escape') {
        var open = document.querySelector('.find-bar:not(.hidden)');
        if (open) closeFindBar(open);
      }
    });

    // Wire each pane's bar
    Array.prototype.slice.call(document.querySelectorAll('.find-bar')).forEach(function(bar) {
      var input = bar.querySelector('.find-input');
      var prevBtn = bar.querySelector('.find-prev');
      var nextBtn = bar.querySelector('.find-next');
      var closeBtn = bar.querySelector('.find-close');
      input.addEventListener('input', function() { performFind(bar, input.value); });
      input.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
          e.preventDefault();
          stepFind(bar, e.shiftKey ? -1 : 1);
        }
      });
      prevBtn.addEventListener('click', function() { stepFind(bar, -1); });
      nextBtn.addEventListener('click', function() { stepFind(bar, 1); });
      closeBtn.addEventListener('click', function() { closeFindBar(bar); });
    });
  }

  function openFindBar(bar) {
    bar.classList.remove('hidden');
    var input = bar.querySelector('.find-input');
    // Snapshot the pane's current rendered HTML so we can restore on close.
    var paneEl = document.getElementById(bar.dataset.target);
    if (!paneEl) return;
    findState.bar = bar;
    findState.paneEl = paneEl;
    findState.snapshot = paneEl.innerHTML;
    findState.matches = [];
    findState.current = -1;
    input.focus();
    input.select();
    if (input.value) performFind(bar, input.value);
  }

  function closeFindBar(bar) {
    bar.classList.add('hidden');
    if (findState.paneEl && findState.snapshot !== null) {
      findState.paneEl.innerHTML = findState.snapshot;
    }
    findState.bar = null;
    findState.paneEl = null;
    findState.snapshot = null;
    findState.matches = [];
    findState.current = -1;
    var countEl = bar.querySelector('.find-count');
    if (countEl) countEl.textContent = '0/0';
  }

  function performFind(bar, query) {
    var paneEl = findState.paneEl;
    if (!paneEl) return;
    // Restore baseline before re-marking
    if (findState.snapshot !== null) paneEl.innerHTML = findState.snapshot;
    var countEl = bar.querySelector('.find-count');
    findState.matches = [];
    findState.current = -1;
    if (!query) {
      countEl.textContent = '0/0';
      return;
    }
    // Walk text nodes and wrap matches in <mark>. Avoid recursing into
    // already-marked nodes — but since we just restored the snapshot,
    // there are none.
    var lower = query.toLowerCase();
    var marks = [];
    walkTextNodes(paneEl, function(textNode) {
      var text = textNode.nodeValue;
      var lc = text.toLowerCase();
      var idx = lc.indexOf(lower);
      if (idx < 0) return;
      var pieces = document.createDocumentFragment();
      var pos = 0;
      while (idx >= 0) {
        if (idx > pos) pieces.appendChild(document.createTextNode(text.substring(pos, idx)));
        var mark = document.createElement('mark');
        mark.className = 'find-match';
        mark.textContent = text.substr(idx, query.length);
        pieces.appendChild(mark);
        marks.push(mark);
        pos = idx + query.length;
        idx = lc.indexOf(lower, pos);
      }
      if (pos < text.length) pieces.appendChild(document.createTextNode(text.substring(pos)));
      textNode.parentNode.replaceChild(pieces, textNode);
    });
    findState.matches = marks;
    if (marks.length > 0) {
      findState.current = 0;
      marks[0].classList.add('find-current');
      marks[0].scrollIntoView({ block: 'center' });
    }
    countEl.textContent = (marks.length ? '1' : '0') + '/' + marks.length;
  }

  function stepFind(bar, dir) {
    if (findState.matches.length === 0) return;
    var prev = findState.matches[findState.current];
    if (prev) prev.classList.remove('find-current');
    findState.current = (findState.current + dir + findState.matches.length) % findState.matches.length;
    var next = findState.matches[findState.current];
    next.classList.add('find-current');
    next.scrollIntoView({ block: 'center' });
    var countEl = bar.querySelector('.find-count');
    if (countEl) countEl.textContent = (findState.current + 1) + '/' + findState.matches.length;
  }

  // walkTextNodes invokes cb for every Text node under root, ignoring
  // <script>, <style>, and existing <mark.find-match> nodes.
  function walkTextNodes(root, cb) {
    var stack = [root];
    while (stack.length) {
      var n = stack.pop();
      for (var c = n.firstChild; c; c = c.nextSibling) {
        if (c.nodeType === 3) {
          // Text node
          cb(c);
        } else if (c.nodeType === 1) {
          var tag = c.tagName;
          if (tag === 'SCRIPT' || tag === 'STYLE') continue;
          if (tag === 'MARK' && c.classList && c.classList.contains('find-match')) continue;
          stack.push(c);
        }
      }
    }
  }

  // --- Repeater ---
  var reqInput, reqHighlight;

  // Highlight for editor overlay -- no line numbers, no text transforms.
  // The output must contain the exact same characters as the textarea so
  // the transparent textarea and the coloured <pre> stay aligned.
  function highlightHTTPNoLineNumbers(raw) {
    if (!raw) return '';
    // Do NOT normalise \r\n or pretty-print -- must match textarea verbatim
    var lines = raw.split('\n');
    var inBody = false;
    var result = [];
    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];
      // Detect header/body boundary (empty line)
      if (!inBody && line.replace(/\r$/, '') === '') {
        inBody = true;
        result.push(line); // preserve the blank line as-is
        continue;
      }
      if (inBody) {
        result.push('<span class="hl-body">' + escapeHtml(line) + '</span>');
        continue;
      }
      var escaped = escapeHtml(line);
      if (i === 0) {
        result.push(highlightFirstLine(escaped));
      } else {
        var colonIdx = escaped.indexOf(':');
        if (colonIdx > 0) {
          var hName = escaped.substring(0, colonIdx);
          var hValue = escaped.substring(colonIdx + 1);
          result.push('<span class="hl-header-name">' + hName + '</span><span class="hl-colon">:</span><span class="hl-header-value">' + hValue + '</span>');
        } else {
          result.push(escaped);
        }
      }
    }
    return result.join('\n');
  }

  function syncRequestHighlight() {
    if (!reqInput || !reqHighlight) return;
    reqHighlight.innerHTML = highlightHTTPNoLineNumbers(reqInput.value) || '\n';
  }

  // --- Repeater Tabs ---
  var repeaterTabs = [];
  var activeTabIndex = -1;
  var MAX_REPEATER_TABS = 20;

  function loadRepeaterTabs() {
    try { repeaterTabs = JSON.parse(localStorage.getItem('lorg-repeater-history') || '[]'); } catch(e) { repeaterTabs = []; }
    renderRepeaterTabs();
  }

  function saveRepeaterTabs() {
    try { localStorage.setItem('lorg-repeater-history', JSON.stringify(repeaterTabs)); } catch(e) {}
  }

  function renderRepeaterTabs() {
    var container = $('#repeater-tabs');
    if (!container) return;
    var tabsHtml = repeaterTabs.map(function(tab, idx) {
      var label = tab.host || 'New';
      if (label.length > 20) label = label.substring(0, 20) + '\u2026';
      var active = idx === activeTabIndex ? 'active' : '';
      return '<div class="repeater-tab ' + active + '" data-tab="' + idx + '" title="' + escapeAttr(tab.host || '') + '">' +
        '#' + (idx + 1) + ' ' + escapeHtml(label) +
        '<span class="repeater-tab-close" data-close="' + idx + '">&times;</span>' +
        '</div>';
    }).join('');
    tabsHtml += '<button class="repeater-tab-add" id="rep-tab-add" title="New tab">+</button>';
    container.innerHTML = tabsHtml;

    // Bind tab clicks
    $$('.repeater-tab', container).forEach(function(el) {
      el.addEventListener('click', function(e) {
        if (e.target.classList.contains('repeater-tab-close')) return;
        switchRepeaterTab(parseInt(el.dataset.tab, 10));
      });
    });
    $$('.repeater-tab-close', container).forEach(function(el) {
      el.addEventListener('click', function(e) {
        e.stopPropagation();
        closeRepeaterTab(parseInt(el.dataset.close, 10));
      });
    });
    var addBtn = $('#rep-tab-add');
    if (addBtn) addBtn.addEventListener('click', function() { addRepeaterTab('', '443', true, '', '', ''); });
  }

  function addRepeaterTab(host, port, tls, request, response, time) {
    if (repeaterTabs.length >= MAX_REPEATER_TABS) {
      repeaterTabs.shift();
    }
    repeaterTabs.push({host: host, port: port, tls: tls, request: request, response: response, time: time});
    activeTabIndex = repeaterTabs.length - 1;
    saveRepeaterTabs();
    renderRepeaterTabs();
    loadRepeaterTabData(activeTabIndex);
  }

  function switchRepeaterTab(idx) {
    if (idx < 0 || idx >= repeaterTabs.length) return;
    saveCurrentTabState();
    activeTabIndex = idx;
    renderRepeaterTabs();
    loadRepeaterTabData(idx);
  }

  function closeRepeaterTab(idx) {
    if (idx < 0 || idx >= repeaterTabs.length) return;
    repeaterTabs.splice(idx, 1);
    if (activeTabIndex >= repeaterTabs.length) activeTabIndex = repeaterTabs.length - 1;
    if (activeTabIndex < 0) activeTabIndex = -1;
    saveRepeaterTabs();
    renderRepeaterTabs();
    if (activeTabIndex >= 0) {
      loadRepeaterTabData(activeTabIndex);
    } else {
      $('#rep-host').value = '';
      $('#rep-port').value = '443';
      $('#rep-tls').checked = true;
      if (reqInput) reqInput.value = '';
      syncRequestHighlight();
      $('#rep-response').textContent = '';
      $('#rep-time').textContent = '';
    }
  }

  function saveCurrentTabState() {
    if (activeTabIndex < 0 || activeTabIndex >= repeaterTabs.length) return;
    repeaterTabs[activeTabIndex] = {
      host: $('#rep-host').value,
      port: $('#rep-port').value,
      tls: $('#rep-tls').checked,
      request: reqInput ? reqInput.value : '',
      response: $('#rep-response').innerHTML || '',
      time: $('#rep-time').textContent || '',
    };
    saveRepeaterTabs();
  }

  function loadRepeaterTabData(idx) {
    if (idx < 0 || idx >= repeaterTabs.length) return;
    var tab = repeaterTabs[idx];
    $('#rep-host').value = tab.host || '';
    $('#rep-port').value = tab.port || '443';
    $('#rep-tls').checked = tab.tls !== false;
    if (reqInput) reqInput.value = tab.request || '';
    syncRequestHighlight();
    if (tab.response) {
      $('#rep-response').innerHTML = tab.response;
    } else {
      $('#rep-response').textContent = '';
    }
    $('#rep-time').textContent = tab.time || '';
  }

  function sendToRepeater() {
    var detailPane = $('#traffic-detail');
    var raw = detailPane.dataset.reqRaw || '';
    var host = detailPane.dataset.host || '';
    var port = detailPane.dataset.port || '443';
    var tls = detailPane.dataset.tls === 'true';

    var isH2 = /HTTP\/2/i.test(raw.split('\n')[0] || '');
    $('#rep-http-version').value = isH2 ? '2' : '1';

    addRepeaterTab(host, port, tls, raw, '', '');
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
    saveCurrentTabState();
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

  function formatTime(isoStr) {
    try {
      var d = new Date(isoStr);
      var now = new Date();
      var diff = (now - d) / 1000;
      if (diff < 60) return Math.floor(diff) + 's ago';
      if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
      if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
      var h = d.getHours().toString().padStart(2, '0');
      var m = d.getMinutes().toString().padStart(2, '0');
      var s = d.getSeconds().toString().padStart(2, '0');
      return h + ':' + m + ':' + s;
    } catch(e) { return ''; }
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
  var MAX_HIGHLIGHT_BYTES = 200000; // 200KB for pretty view (was 2MB — too slow for DOM)

  // Content types that should not be rendered as text
  var BINARY_CONTENT_TYPES = [
    'application/octet-stream', 'image/', 'audio/', 'video/', 'font/',
    'application/zip', 'application/gzip', 'application/x-tar',
    'application/pdf', 'application/wasm', 'application/protobuf'
  ];

  function isBinaryContentType(headers) {
    var ct = '';
    var lines = headers.split('\n');
    for (var i = 0; i < lines.length; i++) {
      var lower = lines[i].toLowerCase();
      if (lower.indexOf('content-type:') === 0) {
        ct = lower.substring(13).trim();
        break;
      }
    }
    if (!ct) return false;
    for (var j = 0; j < BINARY_CONTENT_TYPES.length; j++) {
      if (ct.indexOf(BINARY_CONTENT_TYPES[j]) !== -1) return true;
    }
    return false;
  }

  function addLineNumbers(html) {
    // Wrap each logical line in its own block element so the wrap CSS can
    // apply a per-line hanging indent: the gutter sits in the negative
    // text-indent, and any wrapped continuation hangs under the content
    // column instead of running back under the line numbers.
    var lines = html.split('\n');
    return lines.map(function(line, idx) {
      return '<div class="rh-line"><span class="line-num">' + (idx + 1) + '</span>' + line + '</div>';
    }).join('');
  }

  function highlightHTTP(raw) {
    if (!raw) return '';

    // Normalize \r\n to \n early for header detection
    var normalized = raw.replace(/\r\n/g, '\n').replace(/\r/g, '\n');

    // Find header/body boundary
    var headerEnd = normalized.indexOf('\n\n');
    var rawHeaders = headerEnd >= 0 ? normalized.substring(0, headerEnd) : normalized;
    var rawBody = headerEnd >= 0 ? normalized.substring(headerEnd + 2) : '';
    var bodyLen = rawBody.length;

    // Detect binary content — show headers + placeholder instead of garbage
    if (rawBody.length > 0 && isBinaryContentType(rawHeaders)) {
      var escHeaders2 = escapeHtml(rawHeaders);
      var headerLines2 = escHeaders2.split('\n');
      var result2 = [];
      for (var k = 0; k < headerLines2.length; k++) {
        if (k === 0) { result2.push(highlightFirstLine(headerLines2[k])); continue; }
        var ci = headerLines2[k].indexOf(':');
        if (ci > 0) {
          result2.push('<span class="hl-header-name">' + headerLines2[k].substring(0, ci) + '</span><span class="hl-colon">:</span><span class="hl-header-value">' + headerLines2[k].substring(ci + 1) + '</span>');
        } else {
          result2.push(headerLines2[k]);
        }
      }
      result2.push('');
      result2.push('<span class="hl-truncated">[Binary data — ' + formatBytes(bodyLen) + '. Switch to "Raw" to view.]</span>');
      return addLineNumbers(result2.join('\n'));
    }

    // For very large responses, truncate body to keep UI responsive
    var truncated = false;
    if (raw.length > MAX_HIGHLIGHT_BYTES) {
      if (headerEnd >= 0) {
        var headerLen = headerEnd + 2;
        var maxBody = MAX_HIGHLIGHT_BYTES - headerLen;
        if (maxBody > 0 && rawBody.length > maxBody) {
          rawBody = rawBody.substring(0, maxBody);
          truncated = true;
        }
      }
    }

    raw = normalized;

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
      output += '\n<span class="hl-truncated">... response truncated (' + formatBytes(MAX_HIGHLIGHT_BYTES) + ' shown of ' + formatBytes(bodyLen) + '). Switch to "Raw" for full content.</span>';
    }

    // Skip line numbers for very long output (DOM performance), but
    // still wrap each logical line in an .rh-line div so the per-line
    // hanging-indent CSS keeps wrap-continuations aligned. Without this,
    // long responses wrapped to weird positions because the wrap rule
    // only matched .rh-line.
    var lineCount = output.split('\n').length;
    if (lineCount > MAX_HIGHLIGHT_LINES) {
      return wrapLinesNoNumbers(output);
    }
    return addLineNumbers(output);
  }

  function wrapLinesNoNumbers(html) {
    var lines = html.split('\n');
    return lines.map(function(line) {
      return '<div class="rh-line">' + line + '</div>';
    }).join('');
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
    // Request: GET /path HTTP/1.1 (must preserve every character including trailing)
    var reqMatch = line.match(/^(\S+)(\s+)(\S+)(\s+)(HTTP\/[\d.]+)(.*)/i);
    if (reqMatch) {
      return '<span class="hl-method">' + reqMatch[1] + '</span>' + reqMatch[2] +
             '<span class="hl-url">' + reqMatch[3] + '</span>' + reqMatch[4] +
             '<span class="hl-version">' + reqMatch[5] + '</span>' + reqMatch[6];
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
    applyPrefsObject(prefs);
    // Try server in background
    api('/api/collections/_settings/records/UIPREFS________').then(function(rec) {
      if (rec && rec.value) {
        try {
          var serverPrefs = JSON.parse(rec.value);
          if (serverPrefs && serverPrefs.theme) {
            applyPrefsObject(serverPrefs);
            localStorage.setItem('lorg-prefs', JSON.stringify(serverPrefs));
          }
        } catch(e) {}
      }
    }).catch(function() {});
  }

  function applyPrefsObject(prefs) {
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
    // Try to save to server (fire-and-forget)
    api('/api/collections/_settings/records/UIPREFS________', {
      method: 'PATCH',
      body: JSON.stringify({ value: JSON.stringify(prefs) }),
    }).catch(function() {});
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
    var data = await api('/api/scope');
    var el = $('#scope-includes');
    var el2 = $('#scope-excludes');
    if (!data) {
      el.innerHTML = '<span style="color:var(--text-dim); font-size:10px;">Failed to load scope rules</span>';
      el2.innerHTML = '';
      return;
    }

    var includes = data.includes || [];
    var excludes = data.excludes || [];

    if (includes.length === 0 && excludes.length === 0) {
      el.innerHTML = '<span style="color:var(--text-dim); font-size:10px;">No scope rules. Add rules below or load from a file.</span>';
      el2.innerHTML = '';
      return;
    }

    el.innerHTML = includes.map(function(r, i) {
      return '<div class="scope-rule"><span><span class="scope-tag-include">INCLUDE</span><span class="scope-rule-text">' +
        escapeHtml(r.host || '*') + (r.port ? ':' + escapeHtml(r.port) : '') + (r.path ? escapeHtml(r.path) : '') +
        '</span>' + (r.reason ? '<span class="scope-rule-reason">' + escapeHtml(r.reason) + '</span>' : '') +
        '</span><button class="btn-clear scope-remove-btn" data-scope-type="include" data-scope-idx="' + i + '">&times;</button></div>';
    }).join('');

    el2.innerHTML = excludes.map(function(r, i) {
      return '<div class="scope-rule"><span><span class="scope-tag-exclude">EXCLUDE</span><span class="scope-rule-text">' +
        escapeHtml(r.host || '*') + (r.port ? ':' + escapeHtml(r.port) : '') + (r.path ? escapeHtml(r.path) : '') +
        '</span>' + (r.reason ? '<span class="scope-rule-reason">' + escapeHtml(r.reason) + '</span>' : '') +
        '</span><button class="btn-clear scope-remove-btn" data-scope-type="exclude" data-scope-idx="' + i + '">&times;</button></div>';
    }).join('');

    // Bind remove buttons via event delegation
    $$('.scope-remove-btn').forEach(function(btn) {
      btn.addEventListener('click', async function() {
        await api('/api/scope/remove', { method: 'POST', body: JSON.stringify({ type: btn.dataset.scopeType, index: parseInt(btn.dataset.scopeIdx, 10) }) });
        loadScopeRules();
      });
    });
  }

  function initScope() {
    $('#scope-load-btn').addEventListener('click', async function() {
      var filePath = $('#scope-file').value.trim();
      if (!filePath) return;
      var result = await api('/api/scope/load', { method: 'POST', body: JSON.stringify({ filePath: filePath }) });
      if (result && result.success) {
        loadScopeRules();
      } else {
        var errMsg = (result && result.error) ? result.error : 'Failed to load scope file';
        $('#scope-includes').innerHTML = '<span style="color:var(--red); font-size:10px;">' + escapeHtml(errMsg) + '</span>';
      }
    });

    $('#scope-add-btn').addEventListener('click', async function() {
      var type = $('#scope-type').value;
      var host = $('#scope-host').value.trim();
      var port = $('#scope-port').value.trim();
      var path = $('#scope-path').value.trim();
      if (!host) { $('#scope-host').focus(); return; }
      await api('/api/scope/add', {
        method: 'POST',
        body: JSON.stringify({ type: type, host: host, port: port, path: path }),
      });
      $('#scope-host').value = '';
      $('#scope-port').value = '';
      $('#scope-path').value = '';
      loadScopeRules();
    });

    // Two-click confirm pattern. The previous version used native
    // confirm() which is unreliable across contexts: some browsers
    // suppress dialogs on un-interacted pages, headless automation
    // returns false by default, and a quick mis-click could either
    // wipe everything or do nothing. The two-click approach is
    // explicit, works everywhere, and self-resets after 3s.
    (function() {
      var armed = false;
      var armedTimer = null;
      var origText = null;
      var btn = $('#scope-reset-btn');
      btn.addEventListener('click', async function() {
        if (origText === null) origText = btn.textContent;
        if (!armed) {
          armed = true;
          btn.textContent = 'Click again to confirm';
          btn.classList.add('confirm-pending');
          armedTimer = setTimeout(function() {
            armed = false;
            btn.textContent = origText;
            btn.classList.remove('confirm-pending');
          }, 3000);
          return;
        }
        clearTimeout(armedTimer);
        armed = false;
        btn.textContent = origText;
        btn.classList.remove('confirm-pending');
        await api('/api/scope/reset', { method: 'POST', body: JSON.stringify({}) });
        loadScopeRules();
      });
    })();

    $('#scope-refresh-btn').addEventListener('click', loadScopeRules);
    loadScopeRules();
  }

  // --- Intercept View ---
  var interceptPolling = null;

  async function toggleIntercept() {
    var data = await api('/api/proxy/list');
    if (!data || !data.proxies || data.proxies.length === 0) {
      $('#intercept-status').textContent = 'No proxy running';
      return;
    }
    var proxy = data.proxies[0];
    var proxyId = proxy.id;
    var currentState = proxy.intercept;
    var newState = !currentState;

    await api('/api/collections/_proxies/records/' + proxyId, {
      method: 'PATCH',
      body: JSON.stringify({ intercept: newState }),
    });

    updateInterceptUI(newState);
  }

  function updateInterceptUI(enabled) {
    var statusEl = $('#intercept-status');
    var toggleEl = $('#intercept-toggle');
    if (enabled) {
      statusEl.textContent = 'Intercept is on';
      statusEl.style.color = 'var(--accent)';
      toggleEl.textContent = 'Disable';
      toggleEl.classList.add('btn-primary');
      startInterceptPolling();
    } else {
      statusEl.textContent = 'Intercept is off';
      statusEl.style.color = '';
      toggleEl.textContent = 'Enable';
      toggleEl.classList.remove('btn-primary');
      stopInterceptPolling();
    }
  }

  function startInterceptPolling() {
    if (interceptPolling) return;
    pollIntercepts();
    interceptPolling = setInterval(pollIntercepts, 1000);
  }

  function stopInterceptPolling() {
    if (interceptPolling) { clearInterval(interceptPolling); interceptPolling = null; }
  }

  async function pollIntercepts() {
    var data = await api('/api/collections/_intercept/records?perPage=50&sort=-created');
    if (!data || !data.items) return;
    var items = data.items;
    var countEl = $('#intercept-count');
    if (countEl) countEl.textContent = items.length;

    var queue = $('#intercept-queue');
    var emptyEl = $('#intercept-empty');
    if (items.length === 0) {
      if (emptyEl) emptyEl.style.display = '';
      return;
    }
    if (emptyEl) emptyEl.style.display = 'none';

    queue.innerHTML = items.map(function(item) {
      var req = item.req_json || {};
      var method = req.method || '?';
      var host = item.host || '';
      var path = req.path || req.url || '/';
      return '<div class="intercept-item" data-id="' + escapeAttr(item.id) + '">' +
        '<span class="method-' + method.toLowerCase() + '">' + escapeHtml(method) + '</span> ' +
        '<span style="color:var(--text-secondary)">' + escapeHtml(host) + '</span>' +
        '<span style="color:var(--text-tertiary)">' + escapeHtml(path) + '</span>' +
        '</div>';
    }).join('');

    $$('.intercept-item', queue).forEach(function(el) {
      el.addEventListener('click', function() { selectIntercept(el.dataset.id); });
    });

    if (!$('#intercept-editor').dataset.currentId && items.length > 0) {
      selectIntercept(items[0].id);
    }
  }

  async function selectIntercept(id) {
    var detail = await api('/api/traffic/' + id + '/detail');
    var editor = $('#intercept-editor');
    editor.classList.remove('hidden');
    editor.dataset.currentId = id;
    $('#intercept-req-id').textContent = '#' + id.substring(0, 8);

    var raw = (detail && detail.request) ? detail.request : '';
    var interceptInput = $('#intercept-request');
    var interceptHighlight = $('#intercept-highlight');
    interceptInput.value = raw;
    interceptHighlight.innerHTML = highlightHTTPNoLineNumbers(raw) || '\n';

    interceptInput.oninput = function() {
      interceptHighlight.innerHTML = highlightHTTPNoLineNumbers(interceptInput.value) || '\n';
    };
    interceptInput.onscroll = function() {
      interceptHighlight.scrollTop = interceptInput.scrollTop;
      interceptHighlight.scrollLeft = interceptInput.scrollLeft;
    };
  }

  async function interceptAction(action) {
    var editor = $('#intercept-editor');
    var id = editor.dataset.currentId;
    if (!id) return;

    var isEdited = action === 'forward';
    var editedReq = $('#intercept-request').value;

    await api('/api/intercept/action', {
      method: 'POST',
      body: JSON.stringify({
        id: id,
        action: action,
        is_req_edited: isEdited,
        req_edited: isEdited ? editedReq : '',
      }),
    });

    editor.classList.add('hidden');
    editor.dataset.currentId = '';
    pollIntercepts();
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

  // --- Project Switcher ---
  function loadProjectInfo() {
    var projName = document.getElementById('project-name');
    if (projName) projName.textContent = activeProjectFilter || 'All Traffic';
  }

  async function loadProjectList() {
    var container = document.getElementById('project-list');
    if (!container) return;

    var data = await api('/api/project/active');
    var projects = (data && data.projects) ? data.projects : [];

    // "All Traffic" option
    var allActive = !activeProjectFilter ? 'project-item active' : 'project-item';
    var html = '<div class="' + allActive + '" data-project-name="__all__" title="Show all traffic">' +
      '<span class="project-item-name">All Traffic</span>' +
      '</div>';

    // Per-project entries
    html += projects.map(function(p) {
      var activeClass = activeProjectFilter === p.name ? 'project-item active' : 'project-item';
      var detail = p.addr + (p.count ? ' \u00b7 ' + p.count + ' reqs' : '');
      return '<div class="' + activeClass + '" data-project-name="' + escapeAttr(p.name) + '" title="Proxy: ' + escapeAttr(p.addr) + '">' +
        '<span class="project-item-name">' + escapeHtml(p.name) + '</span>' +
        '<span class="project-item-size">' + escapeHtml(detail) + '</span>' +
        '</div>';
    }).join('');

    container.innerHTML = html;

    $$('.project-item', container).forEach(function(el) {
      el.addEventListener('click', function() {
        if (el.dataset.projectName === '__all__') {
          switchToLive();
        } else {
          switchProject(el.dataset.projectName);
        }
      });
    });
  }

  function closeProjectDropdown() {
    var dropdown = document.getElementById('project-dropdown');
    var toggle = document.getElementById('project-toggle');
    if (dropdown) dropdown.classList.add('hidden');
    if (toggle) toggle.classList.remove('dropdown-open');
  }

  function switchToLive() {
    activeProjectFilter = '';
    var projName = document.getElementById('project-name');
    if (projName) projName.textContent = 'All Traffic';
    closeProjectDropdown();
    loadTraffic();
  }

  async function switchProject(name) {
    activeProjectFilter = name;
    var projName = document.getElementById('project-name');
    if (projName) projName.textContent = name;
    closeProjectDropdown();
    loadTraffic();
  }

  function toggleProjectDropdown() {
    var dropdown = document.getElementById('project-dropdown');
    var toggle = document.getElementById('project-toggle');
    if (!dropdown) return;
    var isVisible = !dropdown.classList.contains('hidden');
    if (isVisible) {
      dropdown.classList.add('hidden');
      if (toggle) toggle.classList.remove('dropdown-open');
    } else {
      dropdown.classList.remove('hidden');
      if (toggle) toggle.classList.add('dropdown-open');
      loadProjectList();
    }
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

    // Sync request textarea with highlight overlay (reqInput/reqHighlight are module-scoped)
    reqInput = $('#rep-request');
    reqHighlight = $('#rep-request-highlight');

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
        if (e.shiftKey && e.key === 'R') { e.preventDefault(); switchView('repeater'); }
        if (e.shiftKey && e.key === 'T') { e.preventDefault(); switchView('traffic'); }
        if (e.key === 'f' || e.key === 'F') {
          if (currentView === 'traffic') { e.preventDefault(); $('#traffic-filter').focus(); }
        }
      }
      if (e.key === 'Escape') {
        var detail = $('#traffic-detail');
        if (detail && !detail.classList.contains('hidden')) {
          detail.classList.add('hidden');
          var handle = document.getElementById('resize-handle');
          if (handle) handle.classList.add('hidden');
          selectedTrafficId = null;
          renderTraffic(true);
        } else {
          $('#traffic-filter').value = '';
          applyClientFilter();
        }
      }
      // Arrow up/down to navigate traffic list when a row is selected
      if ((e.key === 'ArrowUp' || e.key === 'ArrowDown') && currentView === 'traffic' && selectedTrafficId) {
        // Don't hijack if user is actively typing in filter or textarea
        var activeEl = document.activeElement;
        var tag = activeEl && activeEl.tagName;
        if (tag === 'TEXTAREA') return;
        if (tag === 'INPUT' && activeEl.value !== '') return;
        // Blur filter input so arrow keys navigate traffic rows
        if (tag === 'INPUT') activeEl.blur();
        e.preventDefault();
        var idx = -1;
        for (var i = 0; i < trafficData.length; i++) {
          if (trafficData[i].id === selectedTrafficId) { idx = i; break; }
        }
        if (idx < 0) return;
        var next = e.key === 'ArrowUp' ? idx - 1 : idx + 1;
        if (next < 0 || next >= trafficData.length) return;
        var nextId = trafficData[next].id;
        selectTrafficRow(nextId);
        // Scroll the selected row into view
        var selectedTr = document.querySelector('#traffic-body tr[data-id="' + nextId + '"]');
        if (selectedTr) selectedTr.scrollIntoView({ block: 'nearest' });
      }
    });

    // Response format toggle
    initFindInPane();
    initChipStrip();
    initCommandPalette();
    initGlobalShortcuts();
    initMatchReplace();

    document.addEventListener('click', function(e) {
      if (e.target.classList.contains('fmt-btn')) {
        var detailPane = $('#traffic-detail');
        renderResponseWithFormat(detailPane._rawResponse || '', e.target.dataset.fmt);
      }
      if (e.target.classList.contains('req-fmt-btn')) {
        var detailPane = $('#traffic-detail');
        renderRequestWithFormat(detailPane._rawRequest || '', e.target.dataset.fmt);
      }
    });

    // Intercept view
    $('#intercept-toggle').addEventListener('click', toggleIntercept);
    var fwdBtn = $('#intercept-forward');
    if (fwdBtn) fwdBtn.addEventListener('click', function() { interceptAction('forward'); });
    var dropBtn = $('#intercept-drop');
    if (dropBtn) dropBtn.addEventListener('click', function() { interceptAction('drop'); });

    // Load repeater tabs
    loadRepeaterTabs();

    // Resizable detail pane (drag handle between table and detail).
    // After a drag, the pane heights are pinned in px. When the window is
    // resized the parent container changes size, but pinned panes stay at
    // their old absolute heights and the layout breaks. We track the ratio
    // (table height / total) and re-derive pixel heights on window resize.
    (function() {
      var handle = document.getElementById('resize-handle');
      var tableContainer = document.querySelector('#view-traffic .table-container');
      var detailPane = document.getElementById('traffic-detail');
      if (!handle || !tableContainer || !detailPane) return;

      var isDragging = false;
      var startY = 0;
      var startTableH = 0;
      var startDetailH = 0;
      var tableRatio = null; // 0..1, set after a drag; null while flex defaults are in effect

      function applyRatio() {
        if (tableRatio == null) return;
        var parent = tableContainer.parentElement;
        if (!parent) return;
        var parentH = parent.offsetHeight;
        // Subtract the handle height (and any inter-pane chrome) so the two
        // panes sum to the available space. 50px is a conservative cushion
        // matching the original drag clamp.
        var avail = parentH - 50;
        if (avail < 200) return;
        var newTableH = Math.round(avail * tableRatio);
        var newDetailH = avail - newTableH;
        if (newTableH < 80) newTableH = 80;
        if (newDetailH < 120) newDetailH = 120;
        tableContainer.style.flex = 'none';
        tableContainer.style.height = newTableH + 'px';
        detailPane.style.flex = 'none';
        detailPane.style.height = newDetailH + 'px';
      }

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
        if (newTableH < 80) newTableH = 80;
        if (newDetailH < 120) newDetailH = 120;
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
        // Capture ratio from the current pinned heights so window resize
        // can re-derive proportional pixel values.
        var totalH = tableContainer.offsetHeight + detailPane.offsetHeight;
        if (totalH > 0) tableRatio = tableContainer.offsetHeight / totalH;
      });

      window.addEventListener('resize', applyRatio);
    })();

    // Resizable request/response split (horizontal drag handle).
    // Same fix pattern as the vertical splitter above: after a drag, store
    // the ratio so a later window resize re-derives proportional widths.
    (function() {
      var handle = document.getElementById('detail-split-handle');
      var reqPane = document.getElementById('detail-pane-request');
      var respPane = document.getElementById('detail-pane-response');
      if (!handle || !reqPane || !respPane) return;

      var isDragging = false;
      var startX = 0;
      var startReqW = 0;
      var startRespW = 0;
      var reqRatio = null; // null when flex defaults apply

      function applyRatio() {
        if (reqRatio == null) return;
        var parent = reqPane.parentElement;
        if (!parent) return;
        var parentW = parent.offsetWidth;
        var handleW = handle.offsetWidth || 0;
        var avail = parentW - handleW;
        if (avail < 160) return;
        var newReqW = Math.round(avail * reqRatio);
        var newRespW = avail - newReqW;
        if (newReqW < 80) newReqW = 80;
        if (newRespW < 80) newRespW = 80;
        reqPane.style.flex = 'none';
        reqPane.style.width = newReqW + 'px';
        respPane.style.flex = 'none';
        respPane.style.width = newRespW + 'px';
      }

      handle.addEventListener('mousedown', function(e) {
        isDragging = true;
        startX = e.clientX;
        startReqW = reqPane.offsetWidth;
        startRespW = respPane.offsetWidth;
        handle.classList.add('dragging');
        document.body.style.cursor = 'col-resize';
        document.body.style.userSelect = 'none';
        e.preventDefault();
      });

      document.addEventListener('mousemove', function(e) {
        if (!isDragging) return;
        var delta = e.clientX - startX;
        var newReqW = startReqW + delta;
        var newRespW = startRespW - delta;
        if (newReqW < 80) newReqW = 80;
        if (newRespW < 80) newRespW = 80;
        var totalW = startReqW + startRespW;
        if (newReqW + newRespW > totalW) return;
        reqPane.style.flex = 'none';
        reqPane.style.width = newReqW + 'px';
        respPane.style.flex = 'none';
        respPane.style.width = newRespW + 'px';
      });

      document.addEventListener('mouseup', function() {
        if (!isDragging) return;
        isDragging = false;
        handle.classList.remove('dragging');
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        var totalW = reqPane.offsetWidth + respPane.offsetWidth;
        if (totalW > 0) reqRatio = reqPane.offsetWidth / totalW;
      });

      // Double-click to reset to 50/50 (clear pinned widths and ratio).
      handle.addEventListener('dblclick', function() {
        reqPane.style.flex = '1';
        reqPane.style.width = '';
        respPane.style.flex = '1';
        respPane.style.width = '';
        reqRatio = null;
      });

      window.addEventListener('resize', applyRatio);
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

        // Highlight actions — set color and close menu, no backend call.
        if (action.indexOf('hl-') === 0) {
          menu.classList.add('hidden');
          var color = action.substring(3); // 'none', 'yellow', etc.
          setRowHighlight(contextRowId, color);
          return;
        }

        menu.classList.add('hidden');

        if (action === 'send-repeater') {
          selectTrafficRow(contextRowId).then(function() { sendToRepeater(); });
          return;
        }

        // Fetch raw request for curl conversion
        var reqDetail = await api('/api/traffic/' + contextRowId + '/detail');
        if (!reqDetail || !reqDetail.request) { alert('Failed to load request'); return; }
        var rawReq = reqDetail.request;

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

    // Project switcher
    var projToggle = document.getElementById('project-toggle');
    if (projToggle) {
      projToggle.addEventListener('click', toggleProjectDropdown);
    }

    // Close project dropdown when clicking elsewhere
    document.addEventListener('click', function(e) {
      var dropdown = document.getElementById('project-dropdown');
      var toggle = document.getElementById('project-toggle');
      if (dropdown && !dropdown.contains(e.target) && toggle && !toggle.contains(e.target)) {
        closeProjectDropdown();
      }
    });

    // Initial load
    initSettings();
    checkStatus();
    loadHosts();
    loadTraffic();
    loadProjectInfo();

    // Check initial intercept state
    api('/api/proxy/list').then(function(data) {
      if (data && data.proxies && data.proxies.length > 0) {
        updateInterceptUI(data.proxies[0].intercept || false);
      }
    });

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
