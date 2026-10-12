const test = require('node:test');
const assert = require('node:assert/strict');
const routes = require('./assets/routes.js');
const snapshots = ['2017-01-01T00:00:00Z','2020-07-01T12:00:00Z','2026-10-01T23:59:59Z'].map(date => ({date, files:[{path:'db.go'},{path:'nested/a b&é.go'}]}));
const actors = {
  people:new Map([['p1',{id:'p1',name:'Ben Johnson',email:'ben@example.org'}],['p2',{id:'p2',name:'Ben Johnson',email:'other@example.org'}]]),
  organizations:new Map([['o1',{id:'o1',name:'Red Hat'}],['unknown',{id:'unknown',name:'Unknown affiliation'}]])
};
const initial = {view:'overview',group:'people',metric:'lines',baseline:0,current:2,scope:'',actor:''};
const read = (url, samples=snapshots) => routes.read(new URL(url,'http://localhost:8787'),samples,actors);

test('overview is a bare route; comparisons use actual dates', () => {
  assert.equal(routes.href(initial,snapshots,actors),'/overview');
  assert.equal(routes.href({...initial,view:'evolution',scope:'db.go'},snapshots,actors),'/evolution?path=db.go&from=2017-01-01&to=2026-10-01');
});

test('single-date workspaces omit defaults and preserve a chosen date and baseline', () => {
  const state={...initial,view:'codebase',baseline:1,current:1,scope:'db.go'};
  const url=routes.href(state,snapshots,actors);
  assert.equal(url,'/codebase?path=db.go&from=2020-07-01&at=2020-07-01');
  assert.deepEqual(read(url),state);
});

test('people with identical names round-trip by email; organizations by name', () => {
  for (const actor of ['p1','p2']) {
    const state={...initial,actor};
    const url=routes.href(state,snapshots,actors);
    assert.match(url,/person=/);
    assert.ok(!url.includes('actor='));
    assert.deepEqual(read(url),state);
  }
  const state={...initial,group:'organizations',actor:'o1',metric:'changedLines'};
  assert.equal(routes.href(state,snapshots,actors),'/overview?organization=Red+Hat&measure=recent-changes');
  assert.deepEqual(read(routes.href(state,snapshots,actors)),state);
});

test('path punctuation and Unicode survive URL encoding', () => {
  const state={...initial,view:'codebase',scope:'nested/a b&é.go'};
  assert.deepEqual(read(routes.href(state,snapshots,actors)),state);
});

test('date links keep their meaning when more snapshots are inserted', () => {
  const state={...initial,view:'evolution',baseline:1};
  const url=routes.href(state,snapshots,actors);
  const expanded=[snapshots[0],{...snapshots[0],date:'2018-01-01T00:00:00Z'},...snapshots.slice(1)];
  const restored=read(url,expanded);
  assert.equal(restored.baseline,2);
  assert.equal(restored.current,3);
  assert.equal(expanded[restored.baseline].date,snapshots[1].date);
});

test('intraday snapshots remain individually addressable', () => {
  const samples=[{...snapshots[0],date:'2026-10-01T01:00:00Z'},{...snapshots[0],date:'2026-10-01T13:00:00Z'}];
  const state={...initial,view:'evolution',current:1};
  const url=routes.href(state,samples,actors);
  assert.equal(new URL(url,'http://localhost').searchParams.get('from'),'2026-10-01T01:00:00Z');
  assert.deepEqual(read(url,samples),state);
  assert.equal(read('/codebase?at=2026-10-01',samples).current,1);
});

test('unavailable dates resolve to the previous sample and invalid dates use defaults', () => {
  assert.equal(read('/codebase?at=2021-01-01').current,1);
  assert.equal(read('/codebase?at=2010-01-01').current,0);
  assert.equal(read('/codebase?at=2026-02-30').current,2);
  assert.equal(read('/evolution?from=garbage&to=Infinity').current,2);
  assert.equal(read('/evolution?from=2026-10-01&to=2017-01-01').current,2);
});

test('old hash links migrate without losing actor, scope or dates', () => {
  const state=read('/#view=evolution&path=db.go&actor=p1&a=1&b=2');
  const url=routes.href(state,snapshots,actors);
  assert.equal(url,'/evolution?path=db.go&person=ben%40example.org&from=2020-07-01&to=2026-10-01');
  assert.equal(read('/#path=db.go&a=0&b=2').view,'codebase');
  assert.equal(read('/codebase?at=2020-07-01#main').current,1);
});

test('unknown actors and paths do not enter the state', () => {
  const state=read('/codebase?person=missing&path=missing');
  assert.equal(state.actor,'');
  assert.equal(state.scope,'');
  assert.equal(read('/overview?organization=Unknown+affiliation').actor,'unknown');
});

test('requested calendar dates survive before finer snapshots are loaded', () => {
  const url = new URL('/evolution?path=db.go&from=2020-07-16&to=2020-09-23','http://localhost');
  assert.deepEqual(routes.requestedRange(url),{from:'2020-07-16',to:'2020-09-23'});
  const state = {...initial,view:'evolution',scope:'db.go',rangeFrom:'2020-07-16',rangeTo:'2020-09-23'};
  assert.equal(routes.href(state,snapshots,actors),url.pathname+url.search);
  assert.deepEqual(routes.requestedRange(new URL('/codebase?at=2020-07-16','http://localhost')),{from:'',to:'2020-07-16'});
});

test('calendar defaults are independent of the merged snapshot cache', () => {
  const state = {...initial,current:1,rangeFrom:'2017-01-01',rangeTo:'2026-10-01',rangeDefaultFrom:'2017-01-01',rangeDefaultTo:'2026-10-01'};
  assert.equal(routes.href(state,snapshots,actors),'/overview');
  assert.equal(routes.href({...state,view:'codebase',rangeTo:'2020-07-16'},snapshots,actors),'/codebase?at=2020-07-16');
  const invalid = new URL('/evolution?from=2026-02-30&to=garbage','http://localhost');
  assert.deepEqual(routes.requestedRange(invalid),{from:'',to:''});
});

test('index-backed links retain paths that only exist between boundary snapshots', () => {
  const url=new URL('/evolution?path=removed-between-endpoints.go&from=2020-08-01&to=2020-08-31','http://localhost');
  const state=routes.read(url,snapshots,actors,{allowUnloadedScope:true});
  assert.equal(state.scope,'removed-between-endpoints.go');
  assert.equal(routes.read(url,snapshots,actors).scope,'');
  assert.equal(routes.href({...state,rangeFrom:'2020-08-01',rangeTo:'2020-08-31'},snapshots,actors),url.pathname+url.search);
});

test('commit catalogue search, date bounds and pages round-trip independently of ownership filters', () => {
  const state={...initial,view:'history',scope:'db.go',actor:'p1',historyQuery:'merge & fix',historyFrom:'2020-01-01',historyTo:'2021-12-31',historyOffset:80};
  const href=routes.href(state,snapshots,actors);
  assert.equal(href,'/history?q=merge+%26+fix&from=2020-01-01&to=2021-12-31&offset=80');
  const restored=read(href);
  assert.equal(restored.view,'history');assert.equal(restored.historyQuery,state.historyQuery);assert.equal(restored.historyOffset,80);
  assert.equal(restored.historyFrom,state.historyFrom);assert.equal(restored.historyTo,state.historyTo);assert.equal(restored.scope,'');assert.equal(restored.actor,'');
});

test('a selected history tip stays explicit across views without duplicating it', () => {
  const tip='0123456789abcdef0123456789abcdef01234567';
  for(const view of ['overview','codebase','evolution','history']){
    const href=routes.href({...initial,view,tip},snapshots,actors);
    assert.equal(new URL(href,'http://localhost').searchParams.getAll('tip').length,1);
    assert.equal(read(href).tip,tip);
  }
});

test('commit pagination and tip inputs reject non-finite offsets and non-SHA references', () => {
  assert.equal(read('/history?offset=Infinity').historyOffset,0);
  assert.equal(read('/history?offset=-4').historyOffset,0);
  assert.equal(read('/history?offset=40.9').historyOffset,40);
  for(const tip of ['HEAD','a'.repeat(41),'$(cmd)','a'.repeat(65)])assert.equal(routes.validTip(tip),'');
  assert.equal(routes.validTip('A'.repeat(40)),'a'.repeat(40));
  assert.equal(routes.validTip('b'.repeat(64)),'b'.repeat(64));
});

test('grouped identities resolve old email and actor-ID links to the same person', () => {
  const grouped={...actors,people:new Map([['merged',{id:'merged',name:'Benjamin Wang',email:'work@example.org',emails:['work@example.org','personal@example.org'],aliasIds:['old-work','old-personal'],identityBasis:'matching-name'}]])};
  for(const reference of ['work@example.org','personal@example.org','old-personal']){
    const state=routes.read(new URL('/codebase?person='+encodeURIComponent(reference),'http://localhost'),snapshots,grouped);
    assert.equal(state.actor,'merged');
    assert.equal(routes.href(state,snapshots,grouped),'/codebase?person=work%40example.org');
  }
  const legacy=routes.read(new URL('/#view=evolution&actor=old-work','http://localhost'),snapshots,grouped);
  assert.equal(legacy.actor,'merged');
});

test('an exact commit deep link preserves catalogue filters and its full SHA', () => {
  const sha='0123456789abcdef0123456789abcdef01234567';
  const url='/history?q=merge&from=2020-01-01&offset=40&commit='+sha;
  const restored=read(url);
  assert.equal(restored.historyCommit,sha);assert.equal(restored.historyOffset,40);assert.equal(restored.historyQuery,'merge');
  assert.equal(routes.href(restored,snapshots,actors),url);
  assert.equal(routes.href({...restored,historyCommit:''},snapshots,actors),'/history?q=merge&from=2020-01-01&offset=40');
  assert.equal(read('/history?commit=HEAD').historyCommit,'');
});

test('hosted URLs preserve the repository and exact indexed generation', () => {
  const routes = require('./assets/routes.js');
  const index = 'a'.repeat(40);
  const location = {pathname:'/r/conduktor/console-plus/evolution',search:'?index='+index};
  assert.equal(routes.basePath(location.pathname), '/r/conduktor/console-plus');
  assert.equal(routes.url('/api/ownership?from=2024-01-01',location), '/r/conduktor/console-plus/api/ownership?from=2024-01-01&index='+index);
  assert.equal(routes.url('/r/conduktor/console-plus/ai',location), '/r/conduktor/console-plus/ai?index='+index);
  assert.equal(routes.url('/codebase?path=src%2Fa.ts', {pathname:'/codebase',search:''}), '/codebase?path=src%2Fa.ts');
});
