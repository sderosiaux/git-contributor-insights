const {test} = require('node:test');
const assert = require('node:assert/strict');
const {project} = require('./assets/source.js');
const file = {lines:Array.from({length:30},(_,n)=>`source ${n+1}`),blocks:[
  {start:1,end:10,personId:'alice',organizationId:'Old'},
  {start:11,end:12,personId:'bob',organizationId:'New'},
  {start:13,end:20,personId:'alice',organizationId:'New'},
  {start:21,end:30,personId:'bob',organizationId:'New'}
]};
test('passages retain original line numbers and surrounding context without highlighting other owners',()=>{
  const p=project(file,'people','bob',true,2);
  assert.equal(p.attributed,12);
  assert.deepEqual(p.rows.map(r=>r.number),[9,10,11,12,13,14,19,20,21,22,23,24,25,26,27,28,29,30]);
  assert.deepEqual(p.rows.filter(r=>r.matched).map(r=>r.number),[11,12,21,22,23,24,25,26,27,28,29,30]);
  assert.equal(p.rows[0].text,'source 9');
});
test('person and historical organization filters highlight different line sets',()=>{
  assert.equal(project(file,'people','alice',false).attributed,18);
  assert.equal(project(file,'organizations','Old',false).attributed,10);
  assert.equal(project(file,'organizations','New',false).attributed,20);
  assert.equal(project(file,'people','alice',false).rows.length,30);
});
test('overlapping contexts never duplicate lines; all lines remain available without a filter',()=>{
  const p=project(file,'people','bob',true,5);
  assert.equal(new Set(p.rows.map(r=>r.number)).size,p.rows.length);
  assert.equal(project(file,'people','',true).rows.length,30);
  assert.equal(project(file,'people','missing',true).rows.length,0);
  assert.equal(project(file,'people','missing',false).rows.length,30);
  assert.deepEqual(project({lines:[],blocks:[]},'people','',true),{rows:[],attributed:0,total:0});
});
