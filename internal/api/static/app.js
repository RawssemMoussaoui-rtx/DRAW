/* DRAW Research — Chatbox SPA
 * Pure browser, no build toolchain, no external dependencies.
 *
 * Endpoint contract — plan §8 (exact strings embedded below):
 *   POST /api/v1/sessions                 {query, seeds, userId} -> 201 {session_id}
 *   GET  /api/v1/sessions/{id}            ResearchState snapshot JSON
 *   GET  /api/v1/sessions/{id}/result     ResultEnvelope JSON
 *   GET  /api/v1/sessions/{id}/evidence   []Evidence JSON
 *   GET  /api/v1/sessions/{id}/events     SSE: phase, progress, report, error
 *
 * SSE event data shapes — plan §9:
 *   phase    {phase, status}                      status in started|running|completed
 *   progress {progress, phase, evidence, contradictions, budget_used, budget_total}
 *   report   <ResultEnvelope JSON>                (emit on Terminal)
 *   error    {error}
 */
(function () {
  'use strict';

  // Plan §8 endpoint strings (exact, for T2 server alignment).
  var API = '/api/v1';
  var ENDPOINT = {
    createSession:   API + '/sessions',
    sessionState:    API + '/sessions/{id}',
    sessionResult:   API + '/sessions/{id}/result',
    sessionEvidence: API + '/sessions/{id}/evidence',
    sessionEvents:   API + '/sessions/{id}/events'
  };

  // {query, seeds, userId} is the POST body (plan §8: IntentRequest).
  // ResultEnvelope (plan §9/§10) fields are Go-struct JSON (capitalized):
  //   Status, SessionID, CompletedAt, BudgetUsed, PlanPhases, Payload(Report), Errors[]string
  // Report fields: Intent, Entities, TimeRange{From,To}, Sources[], Findings[],
  //   Contradictions[], Completeness[0..1], Confidence[0..1]
  // ReportSource: Name, URL, Class, Quality ; Finding: Claim, Value, EvidenceIDs[], Status
  // ContradictionView: ClaimA, ClaimB, EvidenceIDA, EvidenceIDB, Strength[0..1]
  // Evidence: ID, SessionID, TaskID, SourceID(domain), Topic, Claim, Value,
  //   Confidence[0..1], Verification, CollectedAt

  var state = {
    sessionId: null,
    source: null,          // EventSource
    userId: null,
    done: false            // set true when terminal report received
  };

  var els = {
    log: null, composer: null, query: null, seeds: null,
    submit: null, newSession: null
  };

  function ready(fn) {
    if (document.readyState !== 'loading') { fn(); return; }
    document.addEventListener('DOMContentLoaded', fn);
  }
  ready(init);

  function init() {
    els.log = document.getElementById('log');
    els.composer = document.getElementById('composer');
    els.query = document.getElementById('query');
    els.seeds = document.getElementById('seeds');
    els.submit = document.getElementById('submit');
    els.newSession = document.getElementById('new-session');

    // Anonymous user context: stable per-browser id (server-side session
    // lifecycle + Cor.6 "close prior ACTIVE" is handled server-side).
    state.userId = localStorage.getItem('draw.userId');
    if (!state.userId) {
      state.userId = 'anon_' + Math.random().toString(36).slice(2, 10);
      localStorage.setItem('draw.userId', state.userId);
    }

    els.composer.addEventListener('submit', onSubmit);
    els.newSession.addEventListener('click', onNewSession);
  }

  function resolve(path, id) {
    return path.replace('{id}', encodeURIComponent(id));
  }

  function make(tag, className, text) {
    var el = document.createElement(tag);
    if (className) el.className = className;
    if (text != null) el.textContent = text;
    return el;
  }

  function appendLog(node) {
    els.log.appendChild(node);
    els.log.scrollTop = els.log.scrollHeight;
  }

  function fmtTime() {
    return new Date().toLocaleTimeString();
  }

  function renderLine(label, detail, kind) {
    var row = make('div', 'line line-' + (kind || 'info'), '');
    row.appendChild(make('span', 'ts', fmtTime()));
    row.appendChild(make('span', 'lbl', '[' + (label || '') + ']'));
    row.appendChild(make('span', 'txt', detail || ''));
    appendLog(row);
  }

  function parseJSON(s) {
    if (s == null || s === '') return {};
    try { return JSON.parse(s); } catch (e) { return { _raw: String(s) }; }
  }

  // Completeness/Confidence/Strength are [0..1]; progress% may be 0..100.
  function fmtPct(v) {
    var n = Number(v);
    if (n !== n) return '—';
    if (n >= 0 && n <= 1) return Math.round(n * 100) + '%';
    if (n > 1 && n <= 100) return Math.round(n) + '%';
    if (n > 100) return Math.round(n) + '%';
    return n.toFixed ? n.toFixed(1) + '%' : String(n);
  }

  function setBusy(busy) {
    var on = !!busy;
    els.submit.disabled = on;
    els.query.disabled = on;
    els.seeds.disabled = on;
    els.newSession.disabled = !on; // allow abort while researching
  }

  function onSubmit(e) {
    e.preventDefault();
    if (state.sessionId) {
      renderLine('session', 'an active session is running; press "New session" first', 'warn');
      return;
    }
    var query = (els.query.value || '').trim();
    if (!query) { els.query.focus(); return; }
    var seeds = (els.seeds.value || '')
      .split(',')
      .map(function (s) { return s.trim(); })
      .filter(Boolean);

    var payload = { query: query, seeds: seeds, userId: state.userId };
    setBusy(true);
    renderLine('request', 'POST ' + ENDPOINT.createSession, 'info');

    fetch(ENDPOINT.createSession, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })
      .then(function (resp) {
        if (!resp.ok) {
          return resp.text().then(function (t) {
            throw new Error('create session failed: ' + resp.status + ' ' + (t || ''));
          });
        }
        return resp.json();
      })
      .then(function (data) {
        var id = data.session_id || data.sessionId || data.id;
        if (!id) throw new Error('session_id missing in response');
        state.sessionId = id;
        state.done = false;
        renderLine('session', 'started session ' + id, 'success');
        fetchState(id);
        openEvents(id);
      })
      .catch(function (err) {
        var msg = (err && err.message) ? err.message : String(err);
        renderLine('error', msg, 'error');
        setBusy(false);
      });
  }

  function onNewSession() {
    // Close prior SSE; server closes prior ACTIVE session (Cor.6).
    closeEvents();
    state.sessionId = null;
    state.done = false;
    while (els.log.firstChild) els.log.removeChild(els.log.firstChild);
    els.query.value = '';
    els.seeds.value = '';
    setBusy(false);
    renderLine('session', 'previous session closed; ready for a new one', 'info');
  }

  function fetchState(id) {
    fetch(resolve(ENDPOINT.sessionState, id))
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (s) {
        if (!s) return;
        var phase = s.phase || s.phase_name || '(unknown)';
        var prog = s.progress != null ? s.progress : 0;
        renderLine('state',
          'phase=' + phase + ' progress=' + fmtPct(prog) +
          ' evidence=' + (s.evidence || 0) +
          ' contradictions=' + (s.contradictions || 0) +
          ' budget=' + (s.budget_used || 0) + '/' + (s.budget_total || 0),
          s.terminal ? 'success' : 'info');
      })
      .catch(function () { /* non-fatal */ });
  }

  function openEvents(id) {
    state.source = new EventSource(resolve(ENDPOINT.sessionEvents, id));
    var src = state.source;

    src.addEventListener('phase', function (e) {
      if (state.done) return;
      var d = parseJSON(e.data);
      var kind = d.status === 'completed' ? 'success' : 'running';
      renderLine('phase', (d.phase || '?') + ' — ' + (d.status || '?'), kind);
    });

    src.addEventListener('progress', function (e) {
      if (state.done) return;
      var d = parseJSON(e.data);
      renderLine('progress',
        (d.phase || '') + ' progress=' + fmtPct(d.progress != null ? d.progress : 0) +
        ' evidence=' + (d.evidence || 0) +
        ' contradictions=' + (d.contradictions || 0) +
        ' budget=' + (d.budget_used || 0) + '/' + (d.budget_total || 0),
        'running');
    });

    src.addEventListener('report', function (e) {
      var env = parseJSON(e.data);
      renderReport(env);
      state.done = true;
      closeEvents();
      setBusy(false);
    });

    src.addEventListener('error', function (e) {
      // Native EventSource connection errors carry no data; server-sent
      // `event: error` carries JSON {error}.
      if (state.done) return;
      if (!e.data) {
        // readyState CONNECTING => silent auto-reconnect.
        if (src.readyState === EventSource.CONNECTING) return;
        renderLine('event', 'connection lost — retrying…', 'warn');
        return;
      }
      var d = parseJSON(e.data);
      renderLine('error', d.error || 'unknown error', 'error');
    });
  }

  function closeEvents() {
    if (state.source) {
      state.source.close();
      state.source = null;
    }
  }

  function renderReport(env) {
    var report = (env && env.Payload) || (env && env.Report) || env || {};
    var card = make('div', 'card report', '');
    card.appendChild(make('h2', '', 'Research Report'));

    var meta = make('div', 'meta', '');
    meta.appendChild(make('span', 'tag', 'status: ' + (env.Status || 'terminal')));
    meta.appendChild(make('span', 'tag', 'completeness: ' + fmtPct(report.Completeness)));
    meta.appendChild(make('span', 'tag', 'confidence: ' + fmtPct(report.Confidence)));
    meta.appendChild(make('span', 'tag', 'budget used: ' + (env.BudgetUsed || 0)));
    if (report.Intent && report.Intent.Entity) {
      meta.appendChild(make('span', 'tag', 'entity: ' + report.Intent.Entity));
    }
    if (env.Errors && env.Errors.length) {
      meta.appendChild(make('span', 'tag err', 'errors: ' + env.Errors.length));
    }
    card.appendChild(meta);

    renderSection(card, 'Sources', report.Sources, renderSource);
    renderSection(card, 'Findings', report.Findings, renderFinding);
    renderSection(card, 'Contradictions', report.Contradictions, renderContradiction);

    card.appendChild(make('div', 'hint', 'Click "New session" to research another topic.'));
    appendLog(card);

    fetchEvidence(state.sessionId);
  }

  function renderSection(parent, label, items, fn) {
    if (!items || !items.length) return;
    var sec = make('div', 'section', '');
    sec.appendChild(make('h3', '', label + ' (' + items.length + ')'));
    items.forEach(function (it) { sec.appendChild(fn(it)); });
    parent.appendChild(sec);
  }

  function renderSource(s) {
    var item = make('div', 'item source', '');
    var url = s.URL || (s.Name ? ('https://' + s.Name) : '');
    var a = make('a', 'url', s.Name || url);
    a.href = url || '#';
    a.target = '_blank';
    a.rel = 'noopener noreferrer';
    item.appendChild(a);
    item.appendChild(make('span', 'badge', s.Class || ''));
    if (s.Quality != null) item.appendChild(make('span', 'q', 'quality=' + s.Quality));
    return item;
  }

  function renderFinding(f) {
    var item = make('div', 'item finding', '');
    item.appendChild(make('div', 'claim', f.Claim || ''));
    var val = make('div', 'value', '');
    if (f.Value != null) val.appendChild(make('span', '', 'value: '));
    val.appendChild(make('span', '', String(f.Value == null ? '' : f.Value)));
    item.appendChild(val);
    item.appendChild(make('span', 'badge', 'status: ' + (f.Status || 'UNVERIFIED')));
    if (f.EvidenceIDs && f.EvidenceIDs.length) {
      item.appendChild(make('div', 'evids', 'evidence: ' + f.EvidenceIDs.join(', ')));
    }
    return item;
  }

  function renderContradiction(c) {
    var item = make('div', 'item contradiction', '');
    item.appendChild(make('div', 'claim', 'A: ' + (c.ClaimA || '')));
    item.appendChild(make('div', 'claim', 'B: ' + (c.ClaimB || '')));
    item.appendChild(make('span', 'badge err', 'strength: ' + fmtPct(c.Strength)));
    return item;
  }

  function fetchEvidence(id) {
    var url = resolve(ENDPOINT.sessionEvidence, id);
    fetch(url)
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (ev) {
        if (!ev || !ev.length) return;
        var wrap = make('div', 'card evidence', '');
        wrap.appendChild(make('h2', '', 'Evidence'));
        var sec = make('div', 'section', '');
        sec.appendChild(make('h3', '', 'Evidence (' + ev.length + ')'));
        ev.forEach(function (e) {
          var srcUrl = e.SourceID ? ('https://' + e.SourceID) : '';
          var item = make('div', 'item evidence', '');
          var a = make('a', 'url', e.SourceID || '');
          a.href = srcUrl || '#';
          a.target = '_blank';
          a.rel = 'noopener noreferrer';
          item.appendChild(a);
          item.appendChild(make('div', 'claim', '[' + (e.Topic || '') + '] ' + (e.Claim || '')));
          item.appendChild(make('div', 'value', 'value: ' + String(e.Value == null ? '' : e.Value)));
          item.appendChild(make('span', 'badge', 'ver: ' + (e.Verification || 'UNVERIFIED')));
          sec.appendChild(item);
        });
        wrap.appendChild(sec);
        appendLog(wrap);
      })
      .catch(function () { /* non-fatal */ });
  }
})();
