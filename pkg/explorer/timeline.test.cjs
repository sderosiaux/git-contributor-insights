const test=require('node:test');
const assert=require('node:assert/strict');
const {project}=require('./assets/timeline.js');
const value=(personId,organizationId,lines,changedLines)=>({personId,organizationId,lines,changedLines});
const point=(revision,date,contributions,kind='revision')=>({revision,date,contributions,kind});
const dates=['2020-01-01T00:00:00Z','2020-01-02T00:00:00Z','2020-01-02T00:00:00Z','2020-01-03T00:00:00Z','2020-04-03T00:00:00Z'];
const dense=[
  point('a',dates[0],[value('alice','company',10,4),value('alice','unknown',5,2),value('bob','unknown',3,1)]),
  point('b',dates[1],[value('alice','company',12,6),value('alice','unknown',5,2),value('bob','unknown',3,1)]),
  point('c',dates[2],[value('alice','company',12,6),value('alice','unknown',5,2)]),
  point('d',dates[3],[value('alice','company',12,6),value('alice','unknown',5,2)]),
  point('d',dates[4],[value('alice','company',12,0),value('alice','unknown',5,0)],'expiry')
];
const sparse=[
  dense[0],point('b',dates[1],[value('alice','company',12,6)]),
  point('c',dates[2],[value('bob','unknown',0,0)]),point('d',dates[3],[]),dense[4]
];
const simplify=result=>({...result,points:result.points.map(point=>({...point,counts:[...point.counts].sort()}))});

test('absolute sparse updates equal dense history for people and organizations, both measurements',()=>{
  for(const group of ['people','organizations'])for(const metric of ['lines','changedLines']){
    const options={group,metric,selectedActor:group==='people'?'bob':'unknown'};
    assert.deepEqual(simplify(project({...options,points:sparse,encoding:'changes-v1'})),simplify(project({...options,points:dense})));
  }
});

test('zero replacements remove a pair; one person retains ownership under its other affiliation',()=>{
  const result=project({points:sparse,encoding:'changes-v1',group:'people',metric:'lines'});
  assert.deepEqual(result.points.map(point=>point.total),[18,20,17,17,17]);
  assert.equal(result.points[2].counts.get('alice'),17);assert.equal(result.points[2].counts.get('bob'),undefined);
  assert.deepEqual(result.points.map(point=>point.revision),['a','b','c','d','d']);
  assert.equal(result.points[1].date,result.points[2].date);
});

test('a window reconstructs earlier sparse changes before its first visible point',()=>{
  const options={group:'people',metric:'changedLines',from:Date.parse('2020-01-02T12:00:00Z')};
  const result=project({...options,points:sparse,encoding:'changes-v1'});
  assert.deepEqual(result.points.map(point=>point.total),[8,0]);
  assert.deepEqual(simplify(result),simplify(project({...options,points:dense})));
});

test('all states remain present while visible maps stay bounded, including selected and unknown actors',()=>{
  const people=Array.from({length:1000},(_,id)=>value('person-'+id,id===999?'unknown':'org-'+id,1000-id,0));
  const points=Array.from({length:20000},(_,i)=>point(String(i),new Date(Date.UTC(2000,0,1)+i*1000).toISOString(),i===0?people:[]));
  const result=project({points,encoding:'changes-v1',group:'people',metric:'lines',selectedActor:'person-999'});
  assert.equal(result.points.length,20000);assert.equal(result.points[19999].revision,'19999');
  assert.equal(result.shown.length,6);assert.ok(result.points.every(point=>point.counts.size<=6));
  assert.equal(result.points[19999].total,500500);
  assert.equal(result.points[19999].other,500500-(1000+999+998+997+996+1));
  const organizations=project({points:[points[0]],encoding:'changes-v1',group:'organizations',metric:'lines'});
  assert.ok(organizations.shown.includes('unknown'));
});

test('unsupported encodings fail explicitly rather than drawing false ownership',()=>{
  assert.throws(()=>project({points:[],encoding:'future-v9',group:'people',metric:'lines'}),/Unsupported/);
});

test('a late contributor remains visible over full history and when zoomed',()=>{
  const founders=Array.from({length:5},(_,i)=>value('founder-'+i,'company-'+i,20,20));
  const points=Array.from({length:100},(_,i)=>point(String(i),new Date(Date.UTC(2020,0,i+1)).toISOString(),i===0?founders:[]));
  points.push(point('arrival','2020-05-01T00:00:00Z',[value('emma','new-company',90,90)]));
  for(const group of ['people','organizations'])for(const metric of ['lines','changedLines']){
    const actor=group==='people'?'emma':'new-company';
    const options={points,encoding:'changes-v1',group,metric};
    const full=project(options),zoom=project({...options,from:Date.parse('2020-05-01')});
    assert.equal(full.ranked.indexOf(actor),5,'late arrival is not a historical top-five author');
    assert.ok(full.shown.includes(actor));assert.ok(zoom.shown.includes(actor));
    assert.equal(full.points.at(-1).counts.get(actor),90);
    assert.equal(full.points.at(-1).total,zoom.points.at(-1).total);
  }
});

test('middle-period leaders and unchanged contributors crossing 10% stay visible',()=>{
  const initial=Array.from({length:6},(_,i)=>value('large-'+i,'org',100,0));initial.push(value('steady','org',20,0));
  const points=Array.from({length:100},(_,i)=>point(String(i),new Date(Date.UTC(2020,0,i+1)).toISOString(),i?[]:initial));
  points.push(point('brief','2020-05-01',[value('brief-leader','org',400,0)]));
  points.push(point('shrink','2020-05-02',[...initial.filter(x=>x.personId!=='steady').map(x=>({...x,lines:0})),value('brief-leader','org',0,0)]));
  points.push(point('restore','2020-05-03',initial));
  const result=project({points,encoding:'changes-v1',group:'people',metric:'lines'});
  assert.ok(result.shown.includes('brief-leader'));
  assert.ok(result.shown.includes('steady'),'denominator shrink must count even if this author has no sparse update');
  assert.equal(result.points.length,103);
});

test('show all expands actor categories without changing any state or denominator',()=>{
  const values=Array.from({length:80},(_,i)=>value('p'+i,'org',i+1,i+1));
  const options={points:[point('all','2020-01-01',values)],encoding:'changes-v1',group:'people',metric:'lines'};
  const compact=project(options),all=project({...options,includeAll:true});
  assert.equal(all.shown.length,80);assert.equal(all.hasOthers,false);assert.equal(all.points[0].other,0);
  assert.equal(all.points[0].total,compact.points[0].total);
  assert.equal([...all.points[0].counts.values()].reduce((a,b)=>a+b,0),compact.points[0].total);
});

test('one hit surface resolves exact states and timestamp collisions without sampling',()=>{
  const {nearestPoint}=require('./assets/timeline.js');
  const points=['2020-01-01','2020-01-03','2020-01-03','2020-01-10'].map(date=>({date}));
  assert.equal(nearestPoint([],0),-1);
  assert.equal(nearestPoint(points,Date.parse('2019-01-01')),0);
  assert.equal(nearestPoint(points,Date.parse('2020-01-03')),2);
  assert.equal(nearestPoint(points,Date.parse('2020-01-07')),3);
  assert.equal(nearestPoint(points,Date.parse('2021-01-01')),3);
});
