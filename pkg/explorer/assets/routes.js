/* Human-readable URLs. Snapshot positions and actor IDs stay inside the app. */
((root) => {
  'use strict';
  const views = ['overview', 'codebase', 'evolution', 'history'];
  const validTip = value => /^(?:[a-f0-9]{40}|[a-f0-9]{64})$/i.test(value || '') ? value.toLowerCase() : '';
  const list = (actors, group) => [...(actors[group]?.values() || [])];
  const day = snapshot => new Date(snapshot.date).toISOString().slice(0, 10);

  function snapshotDate(snapshots, index) {
    const selected = snapshots[index], date = day(selected);
    // Keep intraday samples distinguishable without making ordinary URLs noisy.
    return snapshots.filter(snapshot => day(snapshot) === date).length > 1 ? selected.date : date;
  }

  function requestedRange(location) {
    const params = new URLSearchParams(location.search);
    const valid = value => {
      if (!value) return '';
      const dateOnly = /^\d{4}-\d{2}-\d{2}$/.test(value);
      if (!dateOnly && !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value)) return '';
      const parsed = Date.parse(value);
      return Number.isFinite(parsed) && (!dateOnly || new Date(parsed).toISOString().slice(0,10) === value) ? value : '';
    };
    return {from:valid(params.get('from')),to:valid(params.get('to') || params.get('at'))};
  }

  function snapshotIndex(snapshots, value, fallback) {
    if (!value) return fallback;
    const exact = snapshots.findIndex((snapshot, index) => snapshotDate(snapshots, index) === value);
    if (exact >= 0) return exact;
    const dateOnly = /^\d{4}-\d{2}-\d{2}$/.test(value);
    if (!dateOnly && !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value)) return fallback;
    const date = Date.parse(dateOnly ? value + 'T23:59:59.999Z' : value);
    if (!Number.isFinite(date) || (dateOnly && new Date(date).toISOString().slice(0, 10) !== value)) return fallback;
    // A requested date selects the last available snapshot on or before it.
    let index = 0;
    snapshots.forEach((snapshot, candidate) => { if (Date.parse(snapshot.date) <= date) index = candidate; });
    return index;
  }

  function href(state, snapshots, actors) {
    const params = new URLSearchParams();
    if (validTip(state.tip)) params.set('tip', validTip(state.tip));
    if (state.view === 'history') {
      if (state.historyQuery) params.set('q', state.historyQuery);
      if (state.historyFrom) params.set('from', state.historyFrom);
      if (state.historyTo) params.set('to', state.historyTo);
      if (state.historyOffset > 0) params.set('offset', Math.floor(state.historyOffset));
      if (validTip(state.historyCommit)) params.set('commit', validTip(state.historyCommit));
      return '/history' + (params.size ? '?' + params.toString() : '');
    }
    if (state.scope) params.set('path', state.scope);
    const actor = actors[state.group]?.get(state.actor);
    if (actor) params.set(state.group === 'people' ? 'person' : 'organization', state.group === 'people' ? actor.email || actor.id : actor.name);
    else if (state.group === 'organizations') params.set('group', 'organizations');
    if (state.metric === 'changedLines') params.set('measure', 'recent-changes');
    if (state.view === 'evolution') {
      params.set('from', state.rangeFrom || snapshotDate(snapshots, state.baseline));
      params.set('to', state.rangeTo || snapshotDate(snapshots, state.current));
    } else {
      if (state.rangeFrom ? state.rangeFrom !== state.rangeDefaultFrom : state.baseline !== 0) params.set('from', state.rangeFrom || snapshotDate(snapshots, state.baseline));
      if (state.rangeTo ? state.rangeTo !== state.rangeDefaultTo : state.current !== snapshots.length - 1) params.set('at', state.rangeTo || snapshotDate(snapshots, state.current));
    }
    const query = params.toString();
    return '/' + state.view + (query ? '?' + query : '');
  }

  function read(location, snapshots, actors, options = {}) {
    const state = {view:'overview', group:'people', metric:'lines', baseline:0, current:snapshots.length-1, scope:'', actor:''};
    const route = location.pathname.replace(/^\/+|\/+$/g, '');
    const legacy = location.hash && /(?:^#|&)(?:view|path|actor|group|metric|a|b)=/.test(location.hash);
    const params = new URLSearchParams(legacy ? location.hash.slice(1) : location.search);
    state.view = legacy ? (views.includes(params.get('view')) ? params.get('view') : params.has('path') || params.has('actor') ? 'codebase' : 'overview') : views.includes(route) ? route : 'overview';
    if (validTip(params.get('tip'))) state.tip = validTip(params.get('tip'));
    if (state.view === 'history') {
      const range=requestedRange(location);
      state.historyQuery=params.get('q') || '';
      const offset=Number(params.get('offset'));state.historyOffset=Number.isFinite(offset)?Math.max(0,Math.min(Number.MAX_SAFE_INTEGER,Math.floor(offset))):0;
      state.historyFrom=range.from;state.historyTo=range.to;
      state.historyCommit=validTip(params.get('commit'));
    }
    if (params.get('group') === 'organizations' || (!legacy && params.has('organization'))) state.group = 'organizations';
    if (params.get('measure') === 'recent-changes' || (legacy && params.get('metric') === 'changedLines')) state.metric = 'changedLines';
    if (legacy) {
      for (const [key, field] of [['a','baseline'], ['b','current']]) {
        if (params.has(key) && Number.isFinite(Number(params.get(key)))) state[field] = Math.max(0, Math.min(snapshots.length-1, Math.round(Number(params.get(key)))));
      }
    } else {
      state.baseline = snapshotIndex(snapshots, params.get('from'), state.baseline);
      state.current = snapshotIndex(snapshots, params.get(state.view === 'evolution' ? 'to' : 'at'), state.current);
    }
    state.current = Math.max(state.baseline, state.current);
    const path = params.get('path') || '';
    if (path && (options.allowUnloadedScope || snapshots.some(snapshot => (snapshot.files || []).some(file => file.path === path || file.path.startsWith(path + '/'))))) state.scope = path;
    if (legacy) {
      if (actors[state.group]?.has(params.get('actor'))) state.actor = params.get('actor');
      else if(state.group==='people'){
        const matches=list(actors,state.group).filter(actor=>(actor.aliasIds||[]).includes(params.get('actor')));
        if(matches.length===1)state.actor=matches[0].id;
      }
    } else {
      const reference = params.get(state.group === 'people' ? 'person' : 'organization');
      const matches = list(actors, state.group).filter(actor => state.group==='people'?[actor.email,actor.id,...(actor.emails||[]),...(actor.aliasIds||[])].filter(Boolean).includes(reference):actor.name===reference);
      if (matches.length === 1) state.actor = matches[0].id;
    }
    return state;
  }

  const routes = {href, read, requestedRange, validTip};
  if (typeof module !== 'undefined' && module.exports) module.exports = routes;
  else root.AtlasRoutes = routes;
})(globalThis);
