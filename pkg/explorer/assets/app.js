/* A dependency-free, local-first explorer. All repository strings are escaped. */
(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const esc = value => String(value ?? '').replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
  const nf = new Intl.NumberFormat('en-US');
  const dateFormat = new Intl.DateTimeFormat('en-US', {month:'short',day:'numeric',year:'numeric',timeZone:'UTC'});
  const timeFormat = new Intl.DateTimeFormat('en-GB',{hour:'2-digit',minute:'2-digit',hour12:false,timeZone:'UTC'});
  const shortDateFormat = new Intl.DateTimeFormat('en-US', {month:'short',year:'2-digit',timeZone:'UTC'});
  const formatDate = value => {const date = new Date(value); return Number.isNaN(date.valueOf()) ? String(value || 'Undated') : dateFormat.format(date);};
  const formatTime = value => timeFormat.format(new Date(value));
  const shortDate = value => {const date = new Date(value); return Number.isNaN(date.valueOf()) ? String(value || 'Undated') : shortDateFormat.format(date);};
  const number = value => nf.format(Math.round(value || 0));
  const pct = (value, total) => total ? value / total * 100 : 0;
  const percent = value => `${value > 0 && value < .1 ? '<0.1' : value.toFixed(1)}%`;
  const palette = ['#7188bd','#91ada4','#c7aa80','#ab9ebb','#c89183','#9ca974','#7e9eaf','#af8a9e','#aea475','#929daa','#bb9e88','#9ca99f'];
  const state = {view:'overview',group:'people',metric:'lines',baseline:0,current:0,scope:'',actor:'',search:'',tip:'',historyQuery:'',historyFrom:'',historyTo:'',historyOffset:0,historyCommit:'',allActors:false,allDomains:false,allMovers:false,timelineAll:false,evidenceLimit:12};
  let data, actors, colors, evidenceRows = [], previousFocus, duplicateNames = new Set(), repoLabel = '', proofRequest = 0, historyMode = 'replace';
  const DAY = 86400000;
  const temporal = {bounds:null,active:null,cache:new Map(),request:0,controller:null,timer:null,status:'idle',error:'',empty:false};
  let locationRequest = 0, restoringRoute = false, indexInfo = null, initialized = false;
  let timelinePoints = [], revisionChoices = [], revisionCursor = 0, revisionRequest = 0, revisionController, inspectedPoint;
  let timelineProjection, timelineProjectionKey='', timelineProjectionPayload, timelineRenderKey='', timelineGeometry;
  const commitList = {data:null,key:'',request:0,controller:null,timer:null,status:'idle',error:'',limit:40};
  const sourceCode = {data:null,key:'',request:0,controller:null,status:'idle',error:'',cache:new Map(),passages:true,limit:250,filter:'',renderKey:''};

  function name(id) { const actor = actors[state.group].get(id); if (actor && state.group === 'people' && duplicateNames.has(actor.name)) return `${actor.name} (${actor.email || actor.id})`; return actor?.name || (id === 'unknown' ? 'Unknown affiliation' : id || 'Unknown'); }
  function actorEmails(actor) { return [...new Set([actor?.email,...(actor?.emails||[])].filter(Boolean))]; }
  function matchesActor(id, query) { const actor = actors[state.group].get(id); return name(id).toLowerCase().includes(query) || id.toLowerCase().includes(query) || actorEmails(actor).some(email=>email.toLowerCase().includes(query)); }
  function color(id) { return id === 'unknown' ? '#c5c5c5' : colors[state.group].get(id) || '#c5c5c5'; }
  function valueOf(contribution) { return Math.max(0, Number(contribution[state.metric]) || 0); }
  function actorID(contribution) { return state.group === 'people' ? contribution.personId : contribution.organizationId; }
  function metricUnit() { return state.metric === 'lines' ? 'lines' : 'changed lines'; }
  function metricName() { return state.metric === 'lines' ? 'surviving code' : 'recent changes'; }
  function inScope(path) { return !state.scope || path === state.scope || path.startsWith(state.scope + '/'); }
  function filesAt(index) { return (data.snapshots[index]?.files || []).filter(file => inScope(file.path)); }
  function aggregate(files) {
    const counts = new Map(); let total = 0;
    for (const file of files) for (const contribution of file.contributions || []) {
      const value = valueOf(contribution), id = actorID(contribution);
      counts.set(id, (counts.get(id) || 0) + value); total += value;
    }
    return {counts,total};
  }
  function comparison(before, after) {
    return [...new Set([...before.counts.keys(), ...after.counts.keys()])].map(id => ({id,before:before.counts.get(id)||0,after:after.counts.get(id)||0,beforePct:pct(before.counts.get(id)||0,before.total),afterPct:pct(after.counts.get(id)||0,after.total)})).filter(actor => actor.before || actor.after).map(actor => ({...actor,delta:actor.afterPct-actor.beforePct})).sort((a,b) => b.after-a.after || b.before-a.before || name(a.id).localeCompare(name(b.id)));
  }
  function deltaHTML(delta, suffix = 'pp') {
    const rounded = Math.abs(delta) < .05 ? 0 : delta;
    const cls = rounded > 0 ? 'positive' : rounded < 0 ? 'negative' : 'neutral';
    return `<span class="delta ${cls}">${rounded > 0 ? '+' : rounded < 0 ? '−' : ''}${Math.abs(rounded).toFixed(1)} ${esc(suffix)}</span>`;
  }
  function empty(title, message, action = '') { return `<div class="empty-state"><h3>${esc(title)}</h3><p>${esc(message)}</p>${action}</div>`; }
  function announce(message) { $('announcements').textContent = message; }
  function saveState() {
    if(restoringRoute)return;
    const url=AtlasRoutes.href(state,data.snapshots,actors);
    if (url !== location.pathname+location.search || location.hash) history[historyMode==='push'?'pushState':'replaceState'](null,'',url);
    historyMode='replace';
  }
  function restoreState() {
    Object.assign(state,readLocation(location));
    const requested=state.view==='history'?{from:'',to:''}:AtlasRoutes.requestedRange(location),legacy=!!location.hash;
    const clamp=value=>{const day=dayNumber(value);return day<temporal.bounds.from?state.rangeDefaultFrom:day>temporal.bounds.to?state.rangeDefaultTo:value;};
    state.rangeFrom=clamp(requested.from||(legacy?data.snapshots[state.baseline].date:state.rangeDefaultFrom));
    state.rangeTo=clamp(requested.to||(legacy?data.snapshots[state.current].date:state.rangeDefaultTo));
    if(Date.parse(state.rangeTo)<Date.parse(state.rangeFrom))state.rangeTo=state.rangeFrom;
    syncPeriodIndices();
  }
  const dayNumber = value => Math.floor(Date.parse(value) / DAY);
  const dayString = day => new Date(day * DAY).toISOString().slice(0,10);
  const rangeKey = () => JSON.stringify([state.tip,state.scope,state.rangeFrom,state.rangeTo]);
  const canQuery = () => temporal.initial?.timeline?.adaptive === true;
  const completeIndex = () => temporal.initial?.timeline?.complete === true;
  const snapshotKey = snapshot => `${snapshot.date}\0${snapshot.revision || ''}`;
  const readLocation = location => AtlasRoutes.read(location,data.snapshots,actors,{allowUnloadedScope:canQuery()});
  const historyLabel = () => completeIndex() ? (!canQuery()&&state.scope?'Boundary states':'Every first-parent revision') : 'Available history';
  const indexedCount = () => indexInfo?.total ? `${number(indexInfo.completed)} / ${number(indexInfo.total)} commits indexed` : '';

  function renderIndexStatus(status) {
    indexInfo=status;
    if(status.state==='ready')return;
    $('workspace').hidden=true;$('loading').hidden=false;
    const total=Math.max(0,Number(status.total)||0),done=Math.max(0,Math.min(total,Number(status.completed)||0)),failed=status.state==='error',preparing=status.phase==='metadata';
    const phaseTotal=preparing?Math.max(0,Number(status.phaseTotal)||0):total;
    const phaseDone=preparing?Math.max(0,Math.min(phaseTotal,Number(status.phaseCompleted)||0)):done;
    const progress=phaseTotal?Math.floor(phaseDone/phaseTotal*100):0;
    const repository=String(status.repository||'');
    if(repository){$('repo-label').textContent=repository;document.title=`${repository} · Opening ownership index`;}
    let card=$('loading').querySelector('.index-card');
    if(!card||card.dataset.failed!==String(failed)){
      $('loading').className='initial-state index-state'+(failed?' index-error':'');
      $('loading').innerHTML=`<div class="index-card" data-failed="${failed}"><div class="index-symbol" aria-hidden="true"><i></i><i></i><i></i><i></i></div><span class="eyebrow">LOCAL OWNERSHIP INDEX</span><p class="index-repository"></p><h1></h1><p class="index-description"></p><div class="index-progress-heading"><strong></strong><span></span></div><div class="index-progress" role="progressbar" aria-valuemin="0"><span></span></div><div class="index-detail"><span></span><span></span></div><p class="index-saved-count"></p><p class="index-parallel"></p><p class="index-resume">Completed commits are saved on this computer. Restarting the explorer resumes from the saved index.</p>${failed?'<button class="primary-button" id="retry-index">Resume indexing</button>':'<div class="index-live"><span></span><span class="index-live-label"></span></div>'}</div>`;
      card=$('loading').querySelector('.index-card');
      if(failed)$('retry-index').addEventListener('click',async()=>{
        $('retry-index').disabled=true;
        try { const response=await AtlasRoutes.fetch('/api/index/retry',{method:'POST'});if(!response.ok&&response.status!==202)throw new Error(`Unable to resume indexing (${response.status}).`);location.reload(); }
        catch(error){renderIndexStatus({...status,error:error.message});$('retry-index').disabled=false;}
      });
    }
    card.querySelector('.index-repository').textContent=repository;card.querySelector('.index-repository').hidden=!repository;
    card.querySelector('h1').textContent=failed?'The index could not finish':preparing?'Preparing contribution history':'Indexing every commit';
    card.querySelector('.index-description').textContent=failed?(status.error||'The local index encountered an error.'):preparing?'Reading file changes across all reachable commits, including excluded files, before calculating exact code states.':'Saving the exact code state of every reachable commit, including work before it was merged.';
    card.querySelector('.index-progress-heading strong').innerHTML=`${number(phaseDone)} <span>/ ${phaseTotal?number(phaseTotal):'…'} ${preparing?'file changes':'commits'}</span>`;
    card.querySelector('.index-progress-heading > span').textContent=phaseTotal?progress+'%':preparing?'Reading file changes…':'Reading history…';
    const bar=card.querySelector('.index-progress');bar.setAttribute('aria-label',preparing?'File changes prepared':'Commits indexed');bar.setAttribute('aria-valuemax',phaseTotal||100);
    if(phaseTotal)bar.setAttribute('aria-valuenow',phaseDone);else bar.removeAttribute('aria-valuenow');
    bar.firstElementChild.style.width=progress+'%';
    card.querySelector('.index-detail span').textContent=preparing?'Preparation · exclusions included':status.commits?number(status.commits)+' reachable commits':'Reading commit metadata';
    card.querySelector('.index-detail span:last-child').textContent=status.head?'HEAD '+String(status.head).slice(0,8):'';
    card.querySelector('.index-saved-count').hidden=!preparing;
    card.querySelector('.index-saved-count').textContent=`${number(done)} / ${total?number(total):'…'} exact commit states indexed`;
    const workers=Math.max(0,Math.floor(Number(status.workers)||0));card.querySelector('.index-parallel').hidden=!workers;card.querySelector('.index-parallel').textContent=workers?`Up to ${number(workers)} parallel Git ${workers===1?'task':'tasks'}`:'';
    if(!failed)card.querySelector('.index-live-label').textContent=preparing?'Preparing the index':'Indexing in progress';
  }
  function waitForPoll(signal,delay=800) {
    return new Promise((resolve,reject)=>{
      if(signal?.aborted){reject(new DOMException('Aborted','AbortError'));return;}
      const abort=()=>{clearTimeout(timer);reject(new DOMException('Aborted','AbortError'));};
      const timer=setTimeout(()=>{signal?.removeEventListener('abort',abort);resolve();},delay);
      signal?.addEventListener('abort',abort,{once:true});
    });
  }
  async function waitForIndex(initialStatus,signal) {
    let status=initialStatus,lastStatus=initialStatus,failures=0;
    while(true){
      if(!status){
        try {
          const response=await AtlasRoutes.fetch('/api/index',{signal});if(response.status===404)return null;if(!response.ok)throw new Error(`The local index returned ${response.status}.`);status=await response.json();failures=0;
        } catch(error){
          if(error.name==='AbortError')throw error;
          ++failures;
          if(lastStatus){renderIndexStatus(lastStatus);const label=$('loading').querySelector('.index-live-label');if(label)label.textContent=failures<10?'Reconnecting to local index…':'Could not reconnect. The last reported counts are shown.';}
          else {const description=$('loading').querySelector('p');if(description)description.textContent=failures<10?'Reconnecting to the local index…':'Could not reconnect to the local index.';}
          if(failures>=10){
            if(lastStatus){const retry=document.createElement('button');retry.className='primary-button';retry.textContent='Try again';retry.addEventListener('click',()=>location.reload());$('loading').querySelector('.index-card').appendChild(retry);error.index=true;}
            throw error;
          }
          await waitForPoll(signal,Math.min(5000,800*failures));continue;
        }
      }
      lastStatus=status;
      indexInfo=status;
      if(status.state==='ready')return status;
      renderIndexStatus(status);
      if(status.state==='error'){const error=new Error(status.error||'Indexing failed.');error.index=true;throw error;}
      await waitForPoll(signal);status=null;
    }
  }
  async function readOwnership(params,signal) {
    while(true){
      const response=await AtlasRoutes.fetch('/api/ownership'+(params?'?'+params:''),{signal});
      if(response.status===202){await waitForIndex(await response.json(),signal);continue;}
      if(!response.ok){let message=`The index query failed (${response.status}).`;try{const body=await response.json();message=body.error||message;}catch(_){}throw new Error(message);}
      const payload=await response.json();
      if(!payload.repository||!Array.isArray(payload.snapshots))throw new Error('The server returned an invalid ownership dataset.');
      return payload;
    }
  }
  function periodPosition(day) { return temporal.bounds.to > temporal.bounds.from ? (day - temporal.bounds.from) / (temporal.bounds.to - temporal.bounds.from) : .5; }
  function boundaryIndex(value, end = false) {
    const target = Date.parse(/^\d{4}-\d{2}-\d{2}$/.test(value) ? value + (end ? 'T23:59:59.999Z' : 'T00:00:00Z') : value);
    let result = 0;
    data.snapshots.forEach((snapshot,index) => { if (Date.parse(snapshot.date) <= target) result = index; });
    return result;
  }
  function syncPeriodIndices() {
    state.baseline = boundaryIndex(state.rangeFrom);
    state.current = Math.max(state.baseline,boundaryIndex(state.rangeTo,true));
  }
  function cancelPeriodRequest() {
    clearTimeout(temporal.timer);temporal.timer=null;++temporal.request;
    temporal.controller?.abort();temporal.controller=null;
  }
  function mergeDataset(payload) {
    const snapshots = new Map();
    for(const cached of [temporal.initial,...temporal.cache.values(),payload])for(const snapshot of cached?.snapshots||[])snapshots.set(snapshotKey(snapshot),snapshot);
    data.snapshots = [...snapshots.values()].sort((a,b)=>Date.parse(a.date)-Date.parse(b.date));
    for (const group of ['people','organizations']) {
      for (const actor of payload[group] || []) {
        actors[group].set(actor.id,actor);
        // Append new identities without changing any color already on screen.
        if (!colors[group].has(actor.id)) colors[group].set(actor.id,palette[colors[group].size % palette.length]);
      }
      data[group] = [...actors[group].values()];
    }
    const names = new Map();for (const person of actors.people.values()) names.set(person.name,(names.get(person.name)||0)+1);
    duplicateNames = new Set([...names].filter(([,count])=>count>1).map(([name])=>name));
    temporal.active = payload;temporal.activeScope=state.scope;temporal.activeKey=rangeKey();temporal.empty = !payload.snapshots.length;
    if(payload.snapshots.length){state.baseline=data.snapshots.findIndex(snapshot=>snapshotKey(snapshot)===snapshotKey(payload.snapshots[0]));state.current=data.snapshots.findIndex(snapshot=>snapshotKey(snapshot)===snapshotKey(payload.snapshots[payload.snapshots.length-1]));}else syncPeriodIndices();
  }
  function renderTemporalStatus() {
    const pending=temporal.status==='loading'||temporal.status==='preview';
    const label=[historyLabel(),indexedCount()].filter(Boolean).join(' · ');
    $('timeline-resolution').textContent=temporal.status==='loading'?'Reading index…':temporal.status==='preview'?'Choose a period':label;
    $('timeline-resolution').classList.toggle('is-loading',pending);
    $('timeline-loading').hidden=!pending&&!temporal.error;
    $('timeline-loading').classList.toggle('is-error',!!temporal.error);
    $('timeline-loading-text').textContent=temporal.error||(temporal.status==='loading'?`Updating chart · ${formatDate(state.rangeFrom)} – ${formatDate(state.rangeTo)}`:'Release to update the chart');
    $('period-retry').hidden=!temporal.error;
    $('snapshot-pending').hidden=!pending&&!temporal.error;
    $('snapshot-pending').textContent=temporal.error?'Index query failed':pending?'Reading index…':'';
    document.querySelector('.time-panel').setAttribute('aria-busy',pending);$('evolution-content').setAttribute('aria-busy',pending);$('timeline-chart').setAttribute('aria-busy',pending);
    for(const id of ['revision-previous','revision-next','revision-inspect'])if(pending)$(id).disabled=true;
  }
  async function loadPeriod() {
    clearTimeout(temporal.timer);temporal.timer=null;
    temporal.controller?.abort();
    const request=++temporal.request;
    if(!canQuery()) {
      state.baseline=boundaryIndex(state.rangeFrom,true);state.current=Math.max(state.baseline,boundaryIndex(state.rangeTo,true));
      const readable=index=>{const date=data.snapshots[index].date,day=dayNumber(date);return data.snapshots.filter(snapshot=>dayNumber(snapshot.date)===day).length>1?date:dayString(day);};
      state.rangeFrom=readable(state.baseline);state.rangeTo=readable(state.current);
      temporal.active=temporal.initial;temporal.activeScope=state.scope;temporal.activeKey=rangeKey();temporal.status='idle';temporal.error='';temporal.controller=null;
      if(!$('workspace').hidden)render();return true;
    }
    const key=rangeKey(),from=state.rangeFrom,to=state.rangeTo,path=state.scope;
    const controller=new AbortController();temporal.controller=controller;
    temporal.error='';
    if(temporal.cache.has(key)) {
      mergeDataset(temporal.cache.get(key));temporal.status='idle';temporal.controller=null;
      if(!$('workspace').hidden)render();return true;
    }
    temporal.status='loading';
    if(!$('workspace').hidden)render();else if($('loading').querySelector('p'))$('loading').querySelector('p').textContent='Reading the exact dates and path in your link from the index…';
    try {
      const payload=await readOwnership(new URLSearchParams({from,to,...(path?{path}:{}),...(state.tip?{tip:state.tip}:{})}),controller.signal);
      if(request!==temporal.request||key!==rangeKey())return false;
      temporal.cache.set(key,payload);
      while(temporal.cache.size>3){const oldest=[...temporal.cache.keys()].find(candidate=>candidate!==temporal.initialKey&&candidate!==key);if(!oldest)break;temporal.cache.delete(oldest);}
      mergeDataset(payload);temporal.status='idle';temporal.controller=null;
      if(initialized){$('loading').hidden=true;$('workspace').hidden=false;render();}
      announce(`Indexed ownership loaded from ${formatDate(from)} to ${formatDate(to)} in ${path||'the entire repository'}.`);
      return true;
    } catch(error) {
      if(error.name==='AbortError'||request!==temporal.request)return false;
      temporal.status='error';temporal.error=error.message;temporal.controller=null;
      if(!$('workspace').hidden)render();return false;
    }
  }
  function schedulePeriod(delay=320) { clearTimeout(temporal.timer);temporal.timer=setTimeout(loadPeriod,delay); }
  function setPeriod(which,day,push=false) {
    const currentFrom=dayNumber(state.rangeFrom),currentTo=dayNumber(state.rangeTo);
    const next=Math.round(which==='baseline'?Math.max(temporal.bounds.from,Math.min(currentTo,day)):Math.max(currentFrom,Math.min(temporal.bounds.to,day)));
    const field=which==='baseline'?'rangeFrom':'rangeTo';
    if(state[field]===dayString(next))return false;
    cancelPeriodRequest();++locationRequest;restoringRoute=false;
    state[field]=dayString(next);syncPeriodIndices();state.evidenceLimit=12;
    temporal.status='preview';temporal.error='';if(push)historyMode='push';
    render();return true;
  }
  function setSnapshotDate(value) {
    const day=dayNumber(value);if(!Number.isFinite(day))return;
    cancelPeriodRequest();++locationRequest;restoringRoute=false;
    state.rangeTo=dayString(Math.max(temporal.bounds.from,Math.min(temporal.bounds.to,day)));
    if(dayNumber(state.rangeFrom)>dayNumber(state.rangeTo))state.rangeFrom=state.rangeTo;
    syncPeriodIndices();state.evidenceLimit=12;historyMode='push';
    render();loadPeriod();announcePeriod();
  }
  function announcePeriod() { announce(`Selected ${formatDate(state.rangeFrom)} to ${formatDate(state.rangeTo)}.`); }
  function bindPeriod() {
    const slider=$('period-slider'),handles=[['baseline',$('period-start')],['current',$('period-end')]];
    let drag;
    function move(event) {
      if(!drag||event.pointerId!==drag.pointerId)return;
      const rect=$('period-track').getBoundingClientRect(),position=Math.max(0,Math.min(1,(event.clientX-drag.offset-rect.left)/Math.max(1,rect.width)));
      const day=Math.round(temporal.bounds.from+position*(temporal.bounds.to-temporal.bounds.from));
      if(setPeriod(drag.which,day,!drag.changed))drag.changed=true;
    }
    slider.addEventListener('pointerdown',event=>{
      if(event.button!==0||temporal.bounds.from===temporal.bounds.to||drag)return;
      event.preventDefault();
      const rect=$('period-track').getBoundingClientRect(),position=(event.clientX-rect.left)/Math.max(1,rect.width);
      const target=event.target.closest('[role="slider"]');
      const from=periodPosition(dayNumber(state.rangeFrom)),to=periodPosition(dayNumber(state.rangeTo));
      const which=target?target.id==='period-start'?'baseline':'current':position<(from+to)/2?'baseline':'current';
      const handle=which==='baseline'?$('period-start'):$('period-end');
      drag={which,pointerId:event.pointerId,changed:false,offset:target?event.clientX-rect.left-(which==='baseline'?from:to)*rect.width:0};
      handle.focus({preventScroll:true});handle.classList.add('is-dragging');slider.setPointerCapture(event.pointerId);
      if(!target)move(event);
    });
    slider.addEventListener('pointermove',move);
    const finish=event=>{
      if(!drag||event.pointerId!==drag.pointerId)return;
      if(event.type==='pointerup')move(event);
      const changed=drag.changed;drag=null;
      handles.forEach(([,handle])=>handle.classList.remove('is-dragging'));
      if(slider.hasPointerCapture(event.pointerId))slider.releasePointerCapture(event.pointerId);
      if(changed){loadPeriod();announcePeriod();}
    };
    for(const type of ['pointerup','pointercancel','lostpointercapture'])slider.addEventListener(type,finish);
    for(const [which,handle] of handles)handle.addEventListener('keydown',event=>{
      if(temporal.bounds.from===temporal.bounds.to)return;
      const from=dayNumber(state.rangeFrom),to=dayNumber(state.rangeTo),value=which==='baseline'?from:to;
      const min=which==='baseline'?temporal.bounds.from:from,max=which==='baseline'?to:temporal.bounds.to,step=event.shiftKey?7:1;
      let keys={ArrowLeft:value-step,ArrowDown:value-step,ArrowRight:value+step,ArrowUp:value+step,Home:min,End:max,PageDown:value-30,PageUp:value+30};
      if(!canQuery()) {
        const days=[...new Set(temporal.initial.snapshots.map(snapshot=>dayNumber(snapshot.date)))].sort((a,b)=>a-b),index=Math.max(0,days.findLastIndex(day=>day<=value));
        const at=offset=>days[Math.max(0,Math.min(days.length-1,index+offset))];
        keys={ArrowLeft:at(-1),ArrowDown:at(-1),ArrowRight:at(1),ArrowUp:at(1),Home:min,End:max,PageDown:at(-5),PageUp:at(5)};
      }
      if(!(event.key in keys))return;
      event.preventDefault();if(setPeriod(which,keys[event.key],!event.repeat)){schedulePeriod();announcePeriod();}
    });
    for(const [which,id] of [['baseline','period-start-date'],['current','period-end-date']])$(id).addEventListener('change',event=>{
      const day=dayNumber(event.target.value);if(Number.isFinite(day)&&setPeriod(which,day,true)){loadPeriod();announcePeriod();}else renderDates(aggregate(filesAt(state.baseline)),aggregate(filesAt(state.current)));
    });
    $('period-reset').addEventListener('click',()=>{
      cancelPeriodRequest();++locationRequest;restoringRoute=false;
      state.rangeFrom=state.rangeDefaultFrom;state.rangeTo=state.rangeDefaultTo;state.evidenceLimit=12;historyMode='push';
      syncPeriodIndices();loadPeriod();announce('Showing all available history.');
    });
    $('period-retry').addEventListener('click',loadPeriod);
  }
  function navigate(view, options = {}) {
    if (!['overview','codebase','evolution','history'].includes(view)) return;
    ++locationRequest;restoringRoute=false;
    const changed=view!==state.view || (options.scope!==undefined && options.scope!==state.scope) || (options.actor!==undefined && options.actor!==state.actor) || (options.snapshot!==undefined && Number(options.snapshot)!==state.current);
    state.view=view;
    if(view!=='history')state.historyCommit='';
    if(options.scope!==undefined)state.scope=options.scope;
    if(options.actor!==undefined)state.actor=options.actor;
    if(options.snapshot!==undefined){state.current=Math.max(0,Math.min(data.snapshots.length-1,Math.round(Number(options.snapshot)||0)));state.baseline=Math.min(state.baseline,state.current);state.rangeTo=data.snapshots[state.current].date;if(dayNumber(state.rangeFrom)>dayNumber(state.rangeTo))state.rangeFrom=state.rangeTo;}
    state.search='';state.allDomains=false;state.allMovers=false;state.evidenceLimit=12;$('search').value='';
    if(changed){historyMode='push';closeRevision();++proofRequest;if($('proof-dialog')?.open)$('proof-dialog').close();}
    render();
    if(view==='history')loadCommits();
    else {clearTimeout(commitList.timer);commitList.controller?.abort();++commitList.request;if(temporal.activeKey!==rangeKey()){cancelPeriodRequest();loadPeriod();}}
    if(changed){$('page-title').focus({preventScroll:true});window.scrollTo({top:0,behavior:'instant'});}
    announce(`${view==='history'?'All indexed commits':view==='overview'?'Repository overview':view==='codebase'?'Codebase explorer':'Ownership evolution'}: ${state.scope||'entire repository'}.`);
  }
  function setScope(path) { navigate('codebase',{scope:path}); }

  function setActor(id) { ++locationRequest;restoringRoute=false;state.actor = state.actor === id ? '' : id; state.evidenceLimit = 12; historyMode='push'; render(); announce(state.actor ? `Showing domains and evidence for ${name(state.actor)}.` : 'Actor filter cleared.'); }
  function clearActor() { if(!state.actor)return;++locationRequest;restoringRoute=false;state.actor='';state.evidenceLimit=12;historyMode='push';render();announce('Showing all actors in this scope.'); }

  function historyHref(tip) {
    const params=new URLSearchParams();if(tip)params.set('tip',tip);
    if(state.view!=='history'&&state.scope)params.set('path',state.scope);
    const actor=actors[state.group]?.get(state.actor);
    if(state.view!=='history'&&actor)params.set(state.group==='people'?'person':'organization',state.group==='people'?actor.email||actor.id:actor.name);
    else if(state.group==='organizations')params.set('group','organizations');
    if(state.metric==='changedLines')params.set('measure','recent-changes');
    return '/evolution'+(params.size?'?'+params.toString():'');
  }
  const commitsKey=()=>JSON.stringify([state.historyQuery,state.historyFrom,state.historyTo,state.historyOffset]);
  async function loadCommits() {
    clearTimeout(commitList.timer);commitList.controller?.abort();const request=++commitList.request,key=commitsKey();
    if(commitList.key===key&&commitList.data){commitList.status='idle';renderHistory();return;}
    const controller=new AbortController();commitList.controller=controller;commitList.status='loading';commitList.error='';renderHistory();
    const params=new URLSearchParams({offset:state.historyOffset,limit:commitList.limit});
    if(state.historyQuery)params.set('q',state.historyQuery);if(state.historyFrom)params.set('from',state.historyFrom);if(state.historyTo)params.set('to',state.historyTo);
    try {
      const response=await AtlasRoutes.fetch('/api/commits?'+params,{signal:controller.signal});
      if(!response.ok){let message=`Unable to read commits (${response.status}).`;try{message=(await response.json()).error||message;}catch(_){}throw new Error(message);}
      const payload=await response.json();if(request!==commitList.request||key!==commitsKey()||state.view!=='history')return;
      if(!Array.isArray(payload.commits))throw new Error('The index returned an invalid commit list.');
      if(payload.total>0&&state.historyOffset>=payload.total){state.historyOffset=Math.floor((payload.total-1)/commitList.limit)*commitList.limit;saveState();loadCommits();return;}
      commitList.data=payload;commitList.key=key;commitList.status='idle';commitList.controller=null;renderHistory();
      announce(`${number(payload.total)} indexed commits match. Showing ${number(state.historyOffset+1)} to ${number(state.historyOffset+payload.commits.length)}.`);
    } catch(error){if(error.name==='AbortError'||request!==commitList.request)return;commitList.status='error';commitList.error=error.message;renderHistory();}
  }
  function renderHistory() {
    $('history-search').value=state.historyQuery;$('history-from').value=state.historyFrom.slice(0,10);$('history-to').value=state.historyTo.slice(0,10);
    $('history-from').max=state.historyTo.slice(0,10);$('history-to').min=state.historyFrom.slice(0,10);
    $('history-clear').hidden=!state.historyQuery&&!state.historyFrom&&!state.historyTo;
    const loading=commitList.status==='loading',fresh=commitList.key===commitsKey(),payload=fresh?commitList.data:null;
    $('history-screen').setAttribute('aria-busy',loading);$('history-pagination').innerHTML='';
    $('history-count').textContent=loading?'Reading indexed commits…':payload?`${number(payload.total)} ${payload.total===1?'commit':'commits'}${state.historyQuery||state.historyFrom||state.historyTo?' match':' indexed'}`:'All reachable commits';
    if(loading){$('history-list').innerHTML='<div class="commit-list-loading">Reading exact commit states from the local index…</div>';return;}
    if(commitList.status==='error'){$('history-list').innerHTML=empty('Commits could not be loaded',commitList.error,'<button class="quiet-button" data-retry-commits>Try again</button>');return;}
    if(!payload)return;
    if(!payload.commits.length){$('history-list').innerHTML=empty('No matching commits','Try another author, message or SHA, or widen the dates.');return;}
    $('history-list').innerHTML=`<div class="commit-table"><div class="commit-table-head"><span>COMMIT / EXACT CODE STATE</span><span>AUTHOR</span><span>COMMITTED · UTC</span></div>${payload.commits.map(commit=>`<article class="commit-row"><div class="commit-main"><button class="commit-open" data-open-commit="${esc(commit.sha)}" aria-haspopup="dialog"><strong>${esc(commit.summary||'Untitled commit')}</strong><span class="commit-identifiers"><code>${esc(String(commit.sha).slice(0,10))}</code><span class="history-badge ${commit.mainline?'is-mainline':''}">${commit.mainline?'HEAD first-parent':'Side history'}</span>${aiBadges(commit.ai)}${(commit.refs||[]).map(ref=>`<span class="commit-ref">${esc(ref)}</span>`).join('')}</span></button><span class="commit-state-count">${number(commit.lines)} lines · ${number(commit.files)} files${(commit.parents||[]).length>1?' · Merge · '+number(commit.parents.length)+' parents':''}</span></div><div class="commit-author"><span>${esc(commit.authorName||commit.authorEmail||'Unknown author')}</span><small>${esc(commit.authorEmail||'')}</small></div><div class="commit-date"><span>${esc(formatDate(commit.date))}</span><small>${esc(formatTime(commit.date))} UTC</small></div></article>`).join('')}</div>`;
    const first=state.historyOffset+1,last=state.historyOffset+payload.commits.length,hasMore=payload.hasMore??last<payload.total;
    $('history-pagination').innerHTML=`<span>${number(first)}–${number(last)} of ${number(payload.total)} commits</span><div><button class="quiet-button" data-history-page="${Math.max(0,state.historyOffset-commitList.limit)}" ${state.historyOffset===0?'disabled':''}>← Previous</button><button class="quiet-button" data-history-page="${state.historyOffset+commitList.limit}" ${hasMore?'':'disabled'}>Next →</button></div>`;
  }
  function changeCommitFilters(delay=0) {
    state.historyOffset=0;historyMode='replace';saveState();clearTimeout(commitList.timer);commitList.controller?.abort();++commitList.request;
    commitList.status='loading';renderHistory();commitList.timer=setTimeout(loadCommits,delay);
  }

  function renderShell() {
    $('workspace').dataset.view=state.view;document.body.dataset.view=state.view;
    const fileScope=state.scope&&data.snapshots.some(snapshot=>(snapshot.files||[]).some(file=>file.path===state.scope));
    const directSource=state.view==='codebase'&&!!fileScope&&canQuery()&&completeIndex();
    document.body.classList.toggle('file-view',directSource);
    $('codebase-screen').classList.toggle('is-file',directSource);
    const evidencePanel=$('evidence-panel'),evidenceContainer=state.view==='codebase'?(fileScope?$('codebase-screen'):document.querySelector('.domain-panel')):$('evolution-screen');
    if(evidencePanel.parentElement!==evidenceContainer)evidenceContainer.appendChild(evidencePanel);
    if(directSource){if($('warnings').parentElement!==$('codebase-screen'))$('codebase-screen').appendChild($('warnings'));}
    else if($('warnings').parentElement!==$('workspace'))$('workspace').insertBefore($('warnings'),document.querySelector('.scope-navigation'));
    $('topbar-page').textContent=state.view==='history'?'Commits':state.view==='overview'?'Overview':state.view==='codebase'?'Codebase':'Evolution';
    document.querySelectorAll('[data-views]').forEach(section=>section.hidden=!section.dataset.views.split(' ').includes(state.view));
    document.querySelectorAll('[data-view]').forEach(button=>{
      if(button.tagName==='A')button.href=AtlasRoutes.href({...state,view:button.dataset.view},data.snapshots,actors);
      if(button.closest('nav')){if(button.dataset.view===state.view)button.setAttribute('aria-current','page');else button.removeAttribute('aria-current');}
    });
    const scope=state.scope||'the entire repository', current=formatDate(data.snapshots[state.current].date);
    $('page-title').textContent=state.view==='history'?'Explore every commit':state.view==='overview'?'Repository overview':state.view==='codebase'?(state.scope?state.scope.split('/').pop():'Explore the codebase'):'Ownership over time';
    $('page-title').setAttribute('tabindex','-1');
    $('page-description').textContent=state.view==='history'?'Explore exact code states across all reachable branches. Follow parents to see how work came together.':state.view==='overview'?`See who contributes to ${scope}, and choose where to look next.`:state.view==='codebase'?`Explore the people and organizations behind ${scope}.`: `Compare attribution in ${scope} and inspect what changed.`;
    $('page-context-action').innerHTML=state.view==='history'?'<button class="quiet-button" data-view="evolution">Explore ownership over time ↗</button>':state.view==='evolution'?'<button class="quiet-button" data-view="codebase">Explore this code <span aria-hidden="true">↗</span></button>':state.view==='codebase'?'<button class="primary-button" data-view="evolution">Compare over time <span aria-hidden="true">↗</span></button>':'<button class="primary-button" data-view="codebase">Explore codebase <span aria-hidden="true">↗</span></button>';
    $('snapshot-control').hidden=state.view==='evolution'||state.view==='history';
    document.querySelector('.view-toolbar').hidden=state.view==='history';document.querySelector('.filter-toolbar').hidden=state.view==='history';$('metric-explanation').hidden=state.view==='history';
    $('history-context').hidden=state.view==='history'||directSource&&!state.tip;
    $('history-context').innerHTML=`<span><span class="history-branch-icon" aria-hidden="true">⑂</span> ${state.tip?'History ending at':'HEAD history'} <strong>${esc(String(temporal.initial?.timeline?.tip||data.repository.head||'').slice(0,8))}</strong><span class="history-chain-label"> · First-parent chain${temporal.initial?.timeline?.revisions?' · '+number(temporal.initial.timeline.revisions)+' revisions':''}</span></span>${state.tip?`<a class="quiet-button" href="${esc(historyHref(''))}">Return to HEAD ↗</a>`:'<button class="quiet-button" data-view="history">Explore all commits ↗</button>'}`;
    $('search-wrapper').hidden=state.view==='overview'||directSource;
    $('breadcrumbs').parentElement.hidden=state.view==='history'||state.view==='overview'&&!state.scope&&!state.actor;
    $('search').placeholder=state.view==='evolution'?'Find a person or organization…':state.actor?'Find an attributed path…':'Find a path or contributor…';
    $('search').setAttribute('aria-label',state.view==='evolution'?'Find an actor in the ownership changes':state.actor?'Find a path attributed to the selected actor':'Find a path or actor in the codebase');
    if($('scope-note'))$('scope-note').textContent=`${state.scope||'Entire repository'} · ${current}`;
    $('snapshot-meta').textContent=indexedCount()||`${historyLabel()} · ${new Date(temporal.bounds.from*DAY).getUTCFullYear()}–${new Date(temporal.bounds.to*DAY).getUTCFullYear()}`;
  }

  function renderRepositoryOverview(beforeFiles,afterFiles,before,after,comparisonActors) {
    const currentActors=comparisonActors.filter(actor=>actor.after>0), visibleActors=currentActors.slice(0,6);
    for(const id of [state.actor,'unknown']){const actor=currentActors.find(item=>item.id===id);if(actor&&!visibleActors.includes(actor))visibleActors.push(actor);}
    const other=currentActors.filter(actor=>!visibleActors.includes(actor)),otherLines=other.reduce((sum,actor)=>sum+actor.after,0);
    const files=afterFiles.filter(file=>(file.contributions||[]).some(contribution=>valueOf(contribution)>0)).length;
    const actorLabel=state.group==='people'?'Contributors':'Affiliations';
    const domains=groupedDomains(beforeFiles,afterFiles);
    const date=formatDate(data.snapshots[state.current].date), baseline=formatDate(data.snapshots[state.baseline].date);
    const distribution=visibleActors.map(actor=>`<button class="ownership-segment" data-overview-actor="${esc(actor.id)}" style="width:${actor.afterPct}%;background:${color(actor.id)}" aria-label="Explore ${esc(name(actor.id))}: ${percent(actor.afterPct)}, ${number(actor.after)} ${metricUnit()}" title="${esc(name(actor.id))} · ${percent(actor.afterPct)}"></button>`).join('')+(otherLines?`<span class="ownership-segment" style="width:${pct(otherLines,after.total)}%;background:#e5e5e5" role="img" aria-label="Other contributors: ${percent(pct(otherLines,after.total))}"></span>`:'');
    const legend=visibleActors.map(actor=>`<button class="summary-legend-item" data-overview-actor="${esc(actor.id)}"><span class="swatch" style="background:${color(actor.id)}"></span><span>${esc(name(actor.id))}</span><strong>${percent(actor.afterPct)}</strong></button>`).join('')+(otherLines?`<span class="summary-legend-item"><span class="swatch" style="background:#e5e5e5"></span><span>${number(other.length)} others</span><strong>${percent(pct(otherLines,after.total))}</strong></span>`:'');
    const domainRows=domains.slice(0,5).map(domain=>{
      const lead=comparison(domain.a,domain.b).find(actor=>actor.after>0);
      return `<button class="overview-path" data-scope="${esc(domain.path)}"><span class="overview-path-icon">${folderIcon(domain.folder)}</span><span class="overview-path-copy"><strong>${esc(domain.label)}${domain.folder?'/':''}</strong><small>${lead?`${esc(name(lead.id))} · ${percent(lead.afterPct)} of this ${domain.folder?'domain':'file'}`:'No attribution at this snapshot'}</small></span><span class="overview-path-share"><strong>${percent(pct(domain.b.total,after.total))}</strong><small>${number(domain.b.total)} ${metricUnit()}</small></span><span class="row-arrow" aria-hidden="true">↗</span></button>`;
    }).join('');
    const shiftScope=domains.length>12?'the 12 largest paths':'paths in this scope';
    const shifts=domains.slice(0,12).map(domain=>{
      const actor=comparison(domain.a,domain.b).sort((a,b)=>Math.abs(b.delta)-Math.abs(a.delta))[0];
      return {domain,actor};
    }).filter(item=>item.actor&&Math.abs(item.actor.delta)>=.05).sort((a,b)=>Math.abs(b.actor.delta)-Math.abs(a.actor.delta)||b.domain.b.total-a.domain.b.total).slice(0,4);
    const insights=shifts.map(({domain,actor})=>{
      const status=!domain.a.total?'New in this comparison':!domain.b.total?'Absent at the selected date':`${number(actor.before)} → ${number(actor.after)} ${metricUnit()}`;
      return `<button class="insight-row" data-explore-shift="${esc(domain.path)}" data-shift-actor="${esc(actor.id)}"><span class="insight-copy"><strong>${esc(domain.label)}${domain.folder?'/':''}</strong><span>${esc(name(actor.id))} · ${percent(actor.beforePct)} → ${percent(actor.afterPct)}</span><small>${status}</small></span><span class="insight-delta">${deltaHTML(actor.delta)}<span aria-hidden="true">↗</span></span></button>`;
    }).join('');
    $('overview-content').innerHTML=`<section class="overview-summary"><div class="summary-heading"><div><h2>Ownership at a glance</h2><p>${esc(state.scope||'Entire repository')} · ${esc(date)}</p></div><span class="summary-measurement">${state.metric==='lines'?'Surviving code':`Recent ${number(data.method.windowDays)} days`}</span></div><div class="summary-metrics"><div class="summary-metric"><span>${state.metric==='lines'?'Attributed lines':'Changed lines'}</span><strong>${number(after.total)}</strong></div><div class="summary-metric"><span>Included files</span><strong>${number(files)}</strong></div><div class="summary-metric"><span>${actorLabel}</span><strong>${number(currentActors.length)}</strong></div>${state.group==='organizations'?`<div class="summary-metric"><span>Unknown affiliation</span><strong>${percent(pct(after.counts.get('unknown')||0,after.total))}</strong></div>`:''}</div>${after.total?`<div class="ownership-strip" aria-label="Share of ${metricName()} in the selected scope">${distribution}</div><div class="summary-legend">${legend}</div>`:empty('No attribution at this snapshot','Choose another snapshot or switch measurement to explore the repository.')}</section><div class="overview-columns"><section class="overview-section"><div class="section-heading"><div><h2>Largest parts of the codebase</h2><p>Share of ${metricName()} in this scope.</p></div></div>${domainRows||empty('No paths in this selection','Clear the actor filter or choose another snapshot.')}<button class="overview-section-action" data-view="codebase">Explore all paths <span aria-hidden="true">→</span></button></section><section class="overview-section"><div class="section-heading"><div><h2>Worth a closer look</h2><p>Share changes across ${shiftScope}.<br>${esc(baseline)} → ${esc(date)}</p></div></div>${insights||empty('No share changes in this comparison',state.baseline===state.current?'Choose two different dates in Evolution to see what moved.':'Attribution shares are unchanged at the selected dates.')}<button class="overview-section-action" data-view="evolution">Compare ownership over time <span aria-hidden="true">→</span></button></section></div>`;
  }

  function renderMovements(comparisonActors,before,after) {
    const query=state.search.toLowerCase();
    const movers=comparisonActors.filter(actor=>!query||matchesActor(actor.id,query)).sort((a,b)=>Math.abs(b.delta)-Math.abs(a.delta)||b.after-a.after||name(a.id).localeCompare(name(b.id)));
    const visible=state.allMovers?movers:movers.slice(0,10);
    if(state.actor&&!visible.some(actor=>actor.id===state.actor)){const selected=movers.find(actor=>actor.id===state.actor);if(selected)visible.push(selected);}
    const rows=visible.map(actor=>`<tr class="${state.actor===actor.id?'selected':''}"><th scope="row"><button class="movement-person" data-actor="${esc(actor.id)}" aria-pressed="${state.actor===actor.id}" title="Trace ${esc(name(actor.id))}"><span class="swatch" style="background:${color(actor.id)}"></span><span class="movement-name">${esc(actors[state.group].get(actor.id)?.name||name(actor.id))}${state.group==='people'&&duplicateNames.has(actors.people.get(actor.id)?.name)?`<small>${esc(actors.people.get(actor.id)?.email||actor.id)}</small>`:''}<small class="movement-mobile-counts">${number(actor.before)} → ${number(actor.after)} ${metricUnit()}</small></span></button></th><td>${percent(actor.beforePct)}</td><td>${percent(actor.afterPct)}</td><td class="movement-counts">${number(actor.before)} <span aria-hidden="true">→</span> ${number(actor.after)}</td><td>${deltaHTML(actor.delta)}</td><td><button class="quiet-button movement-explore" data-overview-actor="${esc(actor.id)}" aria-label="Explore code attributed to ${esc(name(actor.id))}">Explore <span aria-hidden="true">↗</span></button></td></tr>`).join('');
    $('evolution-content').innerHTML=`<div class="movement-header"><div><h2>What changed</h2><p>Ranked by absolute share change · ${esc(state.scope||'entire repository')}${state.actor?` · tracing ${esc(name(state.actor))}`:''}</p></div><span class="count-label">${number(movers.length)} ${state.group==='people'?'people':'affiliations'}</span></div>${rows?`<div class="movement-table-scroll"><table class="movement-table"><thead><tr><th scope="col">${state.group==='people'?'Person':'Organization'}</th><th scope="col">Share at start</th><th scope="col">Share at end</th><th scope="col">${state.metric==='lines'?'Lines':'Changed lines'} · start → end</th><th scope="col">Change</th><th scope="col"><span class="sr-only">Explore attribution</span></th></tr></thead><tbody>${rows}</tbody></table></div>${movers.length>10?`<button class="more-button" data-more-movers>${state.allMovers?'Show fewer changes':`Show all ${number(movers.length)} changes`} <span aria-hidden="true">↓</span></button>`:''}`:empty('No matching contributors',query?'Try another name or clear the search.':'Choose another scope, snapshot, or measurement.')}<p class="movement-footnote">Share uses all ${metricUnit()} in this scope: ${number(before.total)} at the start and ${number(after.total)} at the end. Select an actor to trace their share and inspect supporting commits.</p>`;
  }

  function renderTimeline() {
    const from=Date.parse(state.rangeFrom),to=Date.parse(/^\d{4}-\d{2}-\d{2}$/.test(state.rangeTo)?state.rangeTo+'T23:59:59.999Z':state.rangeTo);
    const chart=$('timeline-chart'),style=getComputedStyle(chart);
    // Keep the last labelled plot while the user drags or the selected query is
    // pending. Rebuilding thousands of exact states on every pointer event blocks input.
    if((temporal.status==='preview'||temporal.status==='loading')&&temporal.activeKey!==rangeKey()&&chart.firstElementChild){chart.setAttribute('aria-busy','true');return;}
    chart.removeAttribute('aria-busy');
    // A frozen export only carries aggregate series for its original scope. Its full-file
    // boundary snapshots can still describe other paths without implying complete scoped history.
    const indexedSeries=Array.isArray(temporal.active?.series)&&(canQuery()||!state.scope);
    const source=temporal.activeScope===state.scope?(indexedSeries?temporal.active.series:temporal.active?.snapshots||[]):[];
    const projectionKey=JSON.stringify([from,to,state.scope,state.group,state.metric,state.actor,indexedSeries,state.timelineAll]);
    const projectionChanged=timelineProjectionPayload!==temporal.active||timelineProjectionKey!==projectionKey;
    if(projectionChanged){
      const points=indexedSeries?source:source.map(point=>({...point,contributions:(point.files||[]).filter(file=>inScope(file.path)).flatMap(file=>file.contributions||[])}));
      timelineProjection=AtlasTimeline.project({points,encoding:indexedSeries?temporal.active?.seriesEncoding:undefined,group:state.group,metric:state.metric,from,to,selectedActor:state.actor,includeAll:state.timelineAll});
      timelineProjectionPayload=temporal.active;timelineProjectionKey=projectionKey;
    }
    const totalHeight=window.matchMedia('(max-width: 550px)').matches?190:228;
    const width=Math.max(280,chart.clientWidth-parseFloat(style.paddingLeft)-parseFloat(style.paddingRight));
    const renderKey=JSON.stringify([projectionKey,width,totalHeight,temporal.status,temporal.error]);
    if(!projectionChanged&&timelineRenderKey===renderKey)return;
    timelineRenderKey=renderKey;
    // Projection retains each exact point (including timestamp collisions),
    // but only the visible actor buckets, not a dense actor map for every state.
    const {points:snapshots,ranked,shown,hasOthers}=timelineProjection;
    const previousRevision=revisionChoices[revisionCursor]?.revision;
    timelinePoints=snapshots;
    revisionChoices=snapshots.filter(point=>point.revision&&(point.kind==='revision'||!point.kind));
    if(!revisionChoices.length)revisionChoices=[...new Map(snapshots.filter(point=>point.revision).map(point=>[point.revision,point])).values()];
    revisionCursor=Math.max(0,revisionChoices.findIndex(point=>point.revision===previousRevision));
    if(!previousRevision)revisionCursor=Math.max(0,revisionChoices.length-1);
    renderRevisionBrowser();
    const revisionCount=indexedSeries?snapshots.filter(point=>point.kind==='revision'||!point.kind).length:snapshots.length;
    const period=dayNumber(state.rangeFrom)===dayNumber(state.rangeTo)?formatDate(state.rangeTo):`${formatDate(state.rangeFrom)} – ${formatDate(state.rangeTo)}`;
    const actorPresent=!!state.actor&&ranked.includes(state.actor);
    $('timeline-caption').textContent=`Share of ${metricName()} · ${period} · ${state.scope||'entire repository'}`;
    $('timeline-note').hidden=!state.actor||actorPresent||!ranked.length;
    $('timeline-note').textContent=state.actor&&!actorPresent?`${state.metric==='lines'?'No surviving code is':'No recent changes are'} attributed to ${name(state.actor)} in this period.`:'';
    if(!ranked.length){chart.innerHTML=empty(temporal.status==='error'?'Could not read this period':temporal.status==='loading'||temporal.status==='preview'?'Reading this period':'No attribution in this period',temporal.status==='error'?'Try again using the button above, or choose another period.':temporal.status==='loading'||temporal.status==='preview'?'The chart will appear when ownership at these dates is ready.':'Widen the period, choose another path, or switch measurement.');$('timeline-legend').innerHTML='';return;}
    const left=32,right=9,plotWidth=width-left-right,height=totalHeight-52,topY=12;
    const single=snapshots.length===1||from===to,x=time=>single?left+plotWidth/2:left+Math.max(0,Math.min(1,(time-from)/Math.max(1,to-from)))*plotWidth;
    const buckets=shown.map(id=>({id,fill:color(id)}));if(hasOthers)buckets.push({id:'__other',fill:'#e5e5e5'});
    const layers=buckets.map(()=>[]);
    snapshots.forEach(snapshot=>{let cumulative=0;buckets.forEach((bucket,index)=>{const value=bucket.id==='__other'?snapshot.other:snapshot.counts.get(bucket.id)||0;const bottom=topY+height-height*cumulative/100;cumulative+=pct(value,snapshot.total);layers[index].push({x:x(Date.parse(snapshot.date)),top:topY+height-height*cumulative/100,bottom});});});
    timelineGeometry={x,left,topY,height,width,from,to,plotWidth};
    let svg=`<svg data-point-count="${snapshots.length}" data-revision-count="${revisionCount}" viewBox="0 0 ${width} ${totalHeight}" preserveAspectRatio="none" role="img" aria-label="${esc(historyLabel())}: ${esc(metricName())} from ${esc(period)}. ${number(revisionCount)} revisions, ${number(snapshots.length)} indexed states including period boundaries and activity expirations. Every state is plotted on the calendar axis.">`;
    for(const level of [0,50,100]){const y=topY+height-height*level/100;svg+=`<line x1="${left}" y1="${y}" x2="${width-right}" y2="${y}" stroke="#ededed"/><text x="0" y="${y+3}" style="font-size:9px">${level}%</text>`;}
    layers.forEach((points,index)=>{
      const bucket=buckets[index],opacity=actorPresent&&bucket.id!==state.actor?'.18':'.9';
      if(single){const point=points[points.length-1];svg+=`<rect x="${point.x-24}" y="${point.top}" width="48" height="${point.bottom-point.top}" fill="${bucket.fill}" opacity="${opacity}"/>`;return;}
      let path=`M ${points[0].x} ${points[0].top}`;
      for(let i=1;i<points.length;i++)if(points[i].top!==points[i-1].top)path+=` H ${points[i].x} V ${points[i].top}`;
      path+=` H ${points[points.length-1].x} V ${points[points.length-1].bottom}`;
      for(let i=points.length-2;i>=0;i--)if(points[i].bottom!==points[i+1].bottom)path+=` H ${points[i+1].x} V ${points[i].bottom}`;
      path+=` H ${points[0].x}`;
      svg+=`<path d="${path} Z" fill="${bucket.fill}" opacity="${opacity}"/>`;
    });
    const spanDays=(to-from)/DAY,intraday=dayNumber(state.rangeFrom)===dayNumber(state.rangeTo),ticks=[from,to],maxTicks=Math.max(2,Math.floor(plotWidth/90));
    if(!single){
      const cursor=new Date(from);
      if(spanDays>730){const step=Math.max(1,Math.ceil((spanDays/365)/maxTicks));cursor.setUTCMonth(0,1);cursor.setUTCHours(0,0,0,0);cursor.setUTCFullYear(cursor.getUTCFullYear()+1);while(cursor.valueOf()<to){ticks.push(cursor.valueOf());cursor.setUTCFullYear(cursor.getUTCFullYear()+step);}}
      else if(spanDays>90){cursor.setUTCDate(1);cursor.setUTCHours(0,0,0,0);cursor.setUTCMonth(cursor.getUTCMonth()+1);const step=Math.max(1,Math.ceil((spanDays/30)/maxTicks));while(cursor.valueOf()<to){ticks.push(cursor.valueOf());cursor.setUTCMonth(cursor.getUTCMonth()+step);}}
      else {const raw=Math.max(1,Math.ceil(spanDays/maxTicks)),step=raw<=2?raw:raw<=7?7:14;let tick=(Math.floor(from/DAY)+step)*DAY;while(tick<to){ticks.push(tick);tick+=step*DAY;}}
    }
    const selectedTicks=[];
    for(const tick of ticks){if(!selectedTicks.some(value=>Math.abs(x(value)-x(tick))<Math.min(78,plotWidth/2)))selectedTicks.push(tick);}
    selectedTicks.sort((a,b)=>a-b).forEach(tick=>{
      const tickDate=new Date(tick),boundary=tick===from||tick===to;
      const label=single?formatDate(snapshots[0].date):intraday?formatTime(tick)+' UTC':spanDays>730?(boundary&&(tickDate.getUTCMonth()!==0||tickDate.getUTCDate()!==1)?new Intl.DateTimeFormat('en-US',{month:'short',year:'numeric',timeZone:'UTC'}).format(tick):String(tickDate.getUTCFullYear())):spanDays>90?shortDate(tick):new Intl.DateTimeFormat('en-US',{month:'short',day:'numeric',timeZone:'UTC'}).format(tick);
      const anchor=single?'middle':tick===from?'start':tick===to?'end':'middle';
      svg+=`<text x="${x(tick)}" y="${totalHeight-19}" text-anchor="${anchor}">${esc(label)}</text>`;
    });
    // One hit surface for every exact point. Avoid tens of thousands of SVG
    // groups, titles and accessibility nodes on each zoom.
    svg+=`<line id="timeline-cursor" x1="${left}" y1="${topY}" x2="${left}" y2="${topY+height}" stroke="var(--ink)" stroke-width="1" pointer-events="none"/><rect id="timeline-hit" class="snapshot-hit" role="button" tabindex="0" aria-label="Inspect a commit on the chart. Use left and right arrows to move between exact revisions." x="${left}" y="${topY}" width="${plotWidth}" height="${height}" fill="transparent"/>`;
    chart.innerHTML=svg+'</svg><div id="timeline-tooltip" class="timeline-tooltip" hidden></div>';
    renderRevisionBrowser();
    $('timeline-legend').innerHTML=shown.map(id=>`<button class="legend-item ${state.actor===id?'is-selected':''}" data-actor="${esc(id)}" title="Trace ${esc(name(id))}"><span class="swatch" style="background:${color(id)}"></span>${esc(name(id))}</button>`).join('')+(hasOthers?`<span class="legend-item"><span class="swatch" style="background:#e5e5e5"></span>${number(ranked.length-shown.length)} other ${state.group==='people'?'contributors':'organizations'}</span>`:'')+`<div class="timeline-category-controls"><span>${state.timelineAll?'All contributors shown separately.':'Includes leaders across the period, at its end, and anyone reaching 10%.'}</span><button class="quiet-button" data-timeline-all aria-pressed="${state.timelineAll}">${state.timelineAll?'Show key contributors':`Show all ${number(ranked.length)} ${state.group==='people'?'contributors':'organizations'}`}</button></div>`;
  }

  function timelinePointAt(clientX) {
    const svg=$('timeline-chart').querySelector('svg');if(!svg||!timelineGeometry||!timelinePoints.length)return -1;
    const rect=svg.getBoundingClientRect(),g=timelineGeometry;
    const x=(clientX-rect.left)*g.width/rect.width;
    const time=g.from+Math.max(0,Math.min(1,(x-g.left)/g.plotWidth))*(g.to-g.from);
    return AtlasTimeline.nearestPoint(timelinePoints,time);
  }
  function showTimelineHover(event) {
    if(temporal.status!=='idle'||event.target.id!=='timeline-hit')return;
    const index=timelinePointAt(event.clientX),point=timelinePoints[index],tooltip=$('timeline-tooltip');if(!point||!tooltip)return;
    const entries=[...point.counts].sort((a,b)=>b[1]-a[1]),visible=entries.slice(0,10);
    const remaining=point.other+entries.slice(10).reduce((sum,[,value])=>sum+value,0);
    const summary=visible.map(([id,value])=>`${name(id)}  ${percent(pct(value,point.total))}`).join('\n')+(remaining?'\nOther '+(state.group==='people'?'contributors':'organizations')+'  '+percent(pct(remaining,point.total)):'');
    tooltip.textContent=`${formatDate(point.date)} · ${formatTime(point.date)} UTC\n${String(point.revision||'').slice(0,10)} · ${number(point.total)} ${metricUnit()}\n${summary}`;
    const rect=$('timeline-chart').getBoundingClientRect();tooltip.style.left=`${Math.max(12,Math.min(rect.width-290,event.clientX-rect.left+14))}px`;tooltip.hidden=false;
    const x=timelineGeometry.x(Date.parse(point.date)),cursor=$('timeline-cursor');cursor.setAttribute('x1',x);cursor.setAttribute('x2',x);
  }

  function renderDates(before,after) {
    $('snapshot-date').value=dayString(dayNumber(state.rangeTo));$('snapshot-date').min=state.rangeDefaultFrom;$('snapshot-date').max=state.rangeDefaultTo;
    const first=temporal.bounds.from,last=temporal.bounds.to,from=dayNumber(state.rangeFrom),to=dayNumber(state.rangeTo),start=periodPosition(from),end=periodPosition(to);
    for(const [id,day,min,max] of [['period-start-date',from,first,to],['period-end-date',to,from,last]]){const input=$(id);input.value=dayString(day);input.min=dayString(min);input.max=dayString(max);}
    for(const [id,day,min,max] of [['period-start',from,first,to],['period-end',to,from,last]]){
      const handle=$(id);handle.style.left=`${periodPosition(day)*100}%`;
      handle.setAttribute('aria-valuemin',min);handle.setAttribute('aria-valuemax',max);handle.setAttribute('aria-valuenow',day);handle.setAttribute('aria-valuetext',formatDate(dayString(day)));
      handle.setAttribute('aria-disabled',first===last);handle.tabIndex=first===last?-1:0;
    }
    $('period-selection').style.left=`${start*100}%`;$('period-selection').style.width=`${(end-start)*100}%`;
    $('period-slider').classList.toggle('handles-close',(end-start)*$('period-slider').clientWidth<44);
    $('period-slider').setAttribute('aria-disabled',first===last);
    const tickCount=Math.max(1,Math.min(12,Math.floor($('period-slider').clientWidth/60)));
    $('period-ticks').innerHTML=Array.from({length:tickCount+1},(_,index)=>{const fraction=index/tickCount;return `<i style="left:${fraction*100}%" class="${fraction>=start&&fraction<=end?'in-period':''}"></i>`;}).join('');
    $('period-global-start').textContent=String(new Date(first*DAY).getUTCFullYear());$('period-global-end').textContent=String(new Date(last*DAY).getUTCFullYear());
    $('period-reset').setAttribute('aria-disabled',from===first&&to===last);
    $('period-help').textContent=first===last?'One calendar day in this analysis.':from===to?'One day · midnight to end of day (UTC)':canQuery()?'Drag to zoom · arrow keys move one day':'Fixed analysis · select an available date';
    if(before&&after)$('comparison-summary').innerHTML=`<strong>${number(before.total)}</strong> → <strong>${number(after.total)}</strong> ${metricUnit()} in scope`;
    renderTemporalStatus();
  }
  function renderBreadcrumbs() {
    let accumulated='';
    $('breadcrumbs').innerHTML=`<button data-scope="">${esc(repoLabel.split('/').pop())}</button>`+state.scope.split('/').filter(Boolean).map(part=>{accumulated+=(accumulated?'/':'')+part;return `<span class="breadcrumb-divider" aria-hidden="true">/</span><button data-scope="${esc(accumulated)}">${esc(part)}</button>`;}).join('');
    $('actor-filter').hidden=!state.actor;
    $('actor-filter').innerHTML=state.actor?`<span class="swatch" style="background:${color(state.actor)}"></span>${esc(name(state.actor))}<span aria-hidden="true">×</span>`:'';
    $('actor-filter').setAttribute('aria-label',`Clear actor filter ${state.actor?name(state.actor):''}`);
  }
  function renderOverview(before,after,comparisonActors,afterFiles) {
    const actorTotal=comparisonActors.filter(a=>a.after>0).length;
    const files=afterFiles.filter(f=>(f.contributions||[]).some(c=>valueOf(c)>0)).length;
    $('scope-overview').innerHTML=`<span class="scope-total">${number(after.total)}<small>${metricUnit()}</small></span><span class="overview-divider" aria-hidden="true"></span><span>${number(files)} ${files===1?'file':'files'}</span><span>·</span><span>${number(actorTotal)} ${state.group==='people'?(actorTotal===1?'person':'people'):(actorTotal===1?'affiliation':'affiliations')}</span><span class="scope-date">${esc(formatDate(data.snapshots[state.current].date))}</span>`;
  }

  function groupedDomains(beforeFiles,afterFiles) {
    const groups=new Map(), query=state.search.toLowerCase();
    const actorMatches=query&&[...actors[state.group]].filter(([id,a])=>matchesActor(id,query)).map(([id])=>id);
    for(const [side,files] of [['before',beforeFiles],['after',afterFiles]])for(const file of files){
      if(query&&!file.path.toLowerCase().includes(query)&&!(file.contributions||[]).some(c=>actorMatches.includes(actorID(c))&&valueOf(c)>0))continue;
      const relative=state.scope&&file.path.startsWith(state.scope+'/')?file.path.slice(state.scope.length+1):file.path===state.scope?file.path.split('/').pop():file.path;
      const part=query?relative:relative.split('/')[0];
      const path=file.path===state.scope?file.path:(state.scope?state.scope+'/':'')+part;
      if(!groups.has(path))groups.set(path,{path,label:part,folder:!query&&relative.includes('/'),before:[],after:[]});
      groups.get(path)[side].push(file);
    }
    return [...groups.values()].map(domain=>({...domain,a:aggregate(domain.before),b:aggregate(domain.after)})).filter(d=>d.a.total||d.b.total).filter(d=>!state.actor||(d.a.counts.get(state.actor)||0)+(d.b.counts.get(state.actor)||0)>0).sort((a,b)=>b.b.total-a.b.total||b.a.total-a.a.total||a.path.localeCompare(b.path));
  }
  function folderIcon(folder) {return folder?'<svg class="path-icon" viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="M2.5 5.5h5l1.8 2H17v8H2.5z" fill="currentColor" fill-opacity=".13" stroke="currentColor" stroke-width="1.2"/><path d="M2.5 7.5H17" stroke="currentColor" stroke-width="1.2"/></svg>':'<svg class="path-icon" viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="M5 2.5h6l4 4v11H5z" stroke="currentColor" stroke-width="1.2"/><path d="M11 2.5v4h4M8 10h4M8 13h4" stroke="currentColor" stroke-width="1.2"/></svg>';}
  function renderDomains(beforeFiles,afterFiles,before,after) {
    const domains=groupedDomains(beforeFiles,afterFiles), isFile=data.snapshots.some(s=>(s.files||[]).some(f=>f.path===state.scope));
    $('domains-title').textContent=isFile?'File attribution':state.search?'Matching paths':'Explore the codebase';
    $('domain-subtitle').textContent=state.actor?`Paths attributed to ${name(state.actor)} at this snapshot.`:'Open a folder or file to see its attribution.';
    $('domain-count').textContent=`${number(domains.length)} ${domains.length===1?'item':'items'}`;
    $('domain-quantity-label').textContent=state.actor?(state.metric==='lines'?'Attributed lines':'Attributed changes'):(state.metric==='lines'?'Total lines':'Total changes');
    if(!domains.length){$('domain-list').innerHTML=empty('No matching attribution',state.search?'Try a different path or actor name.':state.actor?'This actor has no attributed lines in this scope at this snapshot.':'Try another snapshot, widen the scope, or switch measurement.',state.actor?'<button class="quiet-button" data-clear-actor>Clear actor filter →</button>':'');return;}
    const visible=state.allDomains?domains:domains.slice(0,12);
    $('domain-list').innerHTML=visible.map(domain=>{
      const attribution=comparison(domain.a,domain.b), lead=state.actor?attribution.find(a=>a.id===state.actor):attribution[0];
      const current=state.actor?domain.b.counts.get(state.actor)||0:domain.b.total;
      const currentActors=attribution.filter(a=>a.after>0), visibleActors=currentActors.slice(0,7);
      for (const id of [state.actor,'unknown']) { const actor=currentActors.find(a=>a.id===id);if(actor&&!visibleActors.some(a=>a.id===id))visibleActors.push(actor); }
      const otherActors=currentActors.filter(a=>!visibleActors.includes(a)), otherLines=otherActors.reduce((total,a)=>total+a.after,0);
      const bar=visibleActors.map(a=>`<button class="distribution-segment ${state.actor&&a.id!==state.actor?'dimmed':''}" style="width:${a.afterPct}%;background:${color(a.id)};flex-shrink:1" data-actor="${esc(a.id)}" aria-label="Trace ${esc(name(a.id))}: ${number(a.after)} ${metricUnit()}, ${percent(a.afterPct)} of ${esc(domain.label)}" title="${esc(name(a.id))} · ${percent(a.afterPct)} · ${number(a.after)} ${metricUnit()}"></button>`).join('')+(otherLines?`<span class="distribution-segment ${state.actor?'dimmed':''}" style="width:${pct(otherLines,domain.b.total)}%;background:#e5e5e5;flex-shrink:1" role="img" aria-label="${number(otherActors.length)} other actors: ${number(otherLines)} ${metricUnit()}, ${percent(pct(otherLines,domain.b.total))}. Find every actor in the actor panel." title="${number(otherActors.length)} other actors · ${percent(pct(otherLines,domain.b.total))} · all actors available in the actor panel"></span>`:'');
      const fileCount=new Set([...domain.before,...domain.after].map(f=>f.path)).size;
      return `<div class="domain-row"><button class="domain-name ${domain.folder?'folder':''}" data-scope="${esc(domain.path)}" title="${esc(domain.path)}"><span>${folderIcon(domain.folder)}</span><span class="path-text"><span class="path-label">${esc(domain.label)}${domain.folder?'/':''}</span><span class="path-detail">${domain.folder?`${number(fileCount)} files · `:''}${!domain.b.total?'Absent at this snapshot':`${number(domain.b.total)} ${metricUnit()}`}</span></span></button><div><div class="distribution-bar ${!domain.b.total?'is-empty':''}" aria-label="Attribution within ${esc(domain.label)}">${bar}</div><div class="distribution-label">${lead&&lead.after?`<span class="actor-name" title="${esc(name(lead.id))}">${esc(name(lead.id))}</span><span title="${number(lead.after)} of ${number(domain.b.total)} ${metricUnit()} in ${esc(domain.label)}">${percent(lead.afterPct)}</span>`:'No attribution at this snapshot'}</div></div><div class="domain-share" title="${state.actor?esc(name(state.actor))+' · ':''}${number(current)} ${metricUnit()} in ${esc(domain.label)}">${number(current)}<small>${state.actor?'attributed':'total'}</small></div></div>`;
    }).join('')+(domains.length>12?`<button class="more-button" data-more-domains>${state.allDomains?'Show fewer paths':`Show all ${number(domains.length)} paths`} <span aria-hidden="true">↓</span></button>`:'');
  }
  function stopSourceRequest() {
    sourceCode.controller?.abort();sourceCode.controller=null;++sourceCode.request;
    if(sourceCode.status==='loading'){sourceCode.status='idle';sourceCode.key='';}
  }
  async function loadSource(snapshot,path,key) {
    stopSourceRequest();const request=sourceCode.request,controller=new AbortController();sourceCode.controller=controller;
    sourceCode.key=key;sourceCode.data=null;sourceCode.error='';sourceCode.status='loading';sourceCode.renderKey='';renderSource();
    try {
      const response=await AtlasRoutes.fetch('/api/source?'+new URLSearchParams({sha:snapshot.revision,path}),{signal:controller.signal});
      if(response.status!==200){let message='Could not load the code at this revision.';try{message=(await response.json()).error||message;}catch(_){}throw new Error(message);}
      const file=await response.json();if(request!==sourceCode.request||key!==sourceCode.key)return;
      if(file.revision!==snapshot.revision||file.path!==path||!Array.isArray(file.lines)||!Array.isArray(file.blocks))throw new Error('The source does not match the selected file and revision.');
      sourceCode.cache.set(key,file);while(sourceCode.cache.size>4)sourceCode.cache.delete(sourceCode.cache.keys().next().value);
      sourceCode.data=file;sourceCode.status='ready';sourceCode.controller=null;renderSource();
    } catch(error){if(error.name==='AbortError'||request!==sourceCode.request)return;sourceCode.status='error';sourceCode.error=error.message;sourceCode.controller=null;renderSource();}
  }
  function renderSource() {
    const panel=$('source-panel'),snapshot=data.snapshots[state.current];
    const isFile=state.scope&&data.snapshots.some(s=>(s.files||[]).some(file=>file.path===state.scope));
    if(state.view!=='codebase'||!isFile){panel.hidden=true;stopSourceRequest();sourceCode.renderKey='';return;}
    panel.hidden=false;panel.setAttribute('aria-busy','false');
    const pending=temporal.status==='loading'||temporal.status==='preview';
    const exists=(snapshot?.files||[]).some(file=>file.path===state.scope&&file.lines>0);
    const heading='<h2 id="source-title">Source code</h2>';
    if(pending){stopSourceRequest();sourceCode.renderKey='';panel.setAttribute('aria-busy','true');panel.innerHTML=`<div class="section-heading">${heading}</div><p class="source-status" role="status"><span class="source-spinner" aria-hidden="true"></span>Loading the selected version…</p>`;return;}
    if(!exists||!snapshot?.revision){stopSourceRequest();sourceCode.renderKey='';panel.innerHTML=`<div class="section-heading">${heading}</div>${empty('No surviving code at this date','This file is empty, absent or excluded at the selected snapshot. Choose another date to inspect its source.')}`;return;}
    if(!canQuery()||!completeIndex()){panel.innerHTML=`<div class="section-heading">${heading}</div>${empty('Source needs the local repository','Open this repository with the indexed explorer to inspect exact lines.')}`;return;}
    const key=JSON.stringify([snapshot.revision,state.scope]),filter=JSON.stringify([key,state.group,state.actor]);
    if(sourceCode.filter!==filter){sourceCode.filter=filter;sourceCode.passages=!!state.actor;sourceCode.limit=250;sourceCode.renderKey='';}
    if(sourceCode.key!==key){
      stopSourceRequest();sourceCode.key=key;sourceCode.renderKey='';sourceCode.data=sourceCode.cache.get(key)||null;sourceCode.status=sourceCode.data?'ready':'idle';
    }
    if(sourceCode.status==='idle'){loadSource(snapshot,state.scope,key);return;}
    const renderKey=JSON.stringify([filter,sourceCode.status,sourceCode.passages,sourceCode.limit,snapshot.date,state.metric]);
    if(sourceCode.renderKey===renderKey)return;sourceCode.renderKey=renderKey;
    panel.style.setProperty('--source-owner',state.actor?color(state.actor):'var(--teal)');
    const version=`${formatDate(snapshot.date)} · ${snapshot.revision.slice(0,8)}`;
    if(sourceCode.status==='loading'){panel.setAttribute('aria-busy','true');panel.innerHTML=`<div class="section-heading"><div>${heading}<p>${esc(version)}</p></div></div><p class="source-status" role="status"><span class="source-spinner" aria-hidden="true"></span>Reading source and line attribution…</p>`;return;}
    panel.setAttribute('aria-busy','false');
    if(sourceCode.status==='error'){panel.innerHTML=`<div class="section-heading">${heading}</div>${empty('Code could not be loaded',esc(sourceCode.error),'<button class="quiet-button" data-source-retry>Try again →</button>')}`;return;}
    const file=sourceCode.data,projection=AtlasSource.project(file,state.group,state.actor,sourceCode.passages),rows=projection.rows.slice(0,sourceCode.limit);
    const summary=state.actor?`${number(projection.attributed)} of ${number(projection.total)} lines · ${percent(pct(projection.attributed,projection.total))}`:`${number(projection.total)} lines`;
    const ownerCounts=new Map();for(const block of file.blocks){const id=state.group==='people'?block.personId:block.organizationId;ownerCounts.set(id,(ownerCounts.get(id)||0)+block.end-block.start+1);}
    if(state.actor&&!ownerCounts.has(state.actor))ownerCounts.set(state.actor,0);
    const ownerOptions=[...ownerCounts].sort((a,b)=>b[1]-a[1]||name(a[0]).localeCompare(name(b[0])));
    const ownerPicker=`<label class="source-owner-picker"><span>Highlight</span><select id="source-owner" aria-label="${state.group==='people'?'Person':'Organization'} to highlight"><option value="">All ${state.group==='people'?'contributors':'organizations'}</option>${ownerOptions.map(([id,lines])=>`<option value="${esc(id)}" ${id===state.actor?'selected':''}>${esc(name(id))} · ${number(lines)} lines</option>`).join('')}</select></label>`;
    const controls=state.actor?`<div class="segmented" role="group" aria-label="Code display"><button data-source-mode="passages" aria-pressed="${sourceCode.passages}">Attributed passages</button><button data-source-mode="full" aria-pressed="${!sourceCode.passages}">Full file</button></div>`:'';
    let previous=0,previousBlock=-1;
    const body=rows.map(row=>{
      const block=file.blocks[row.blockIndex],gap=row.number-previous-1;
      const gapRow=gap?`<tr class="source-gap"><td colspan="3"><button data-source-mode="full">${number(gap)} ${gap===1?'line':'lines'} folded · show full file</button></td></tr>`:'';
      const showAuthor=row.blockIndex!==previousBlock||gap>0;previous=row.number;previousBlock=row.blockIndex;
      const attribution=block?`${block.author} · ${actors.organizations.get(block.organizationId)?.name||block.organizationId} · ${formatDate(block.date)} · ${block.sha.slice(0,8)} · ${block.summary}`:'';
      const author=showAuthor&&block?`<button data-source-commit="${row.blockIndex}" title="${esc(attribution)}" aria-label="Inspect attribution commit ${esc(attribution)}"><span>${esc(block.author)}</span><small>${block.sha.slice(0,8)} ↗</small>${aiBadges(block.ai)}</button>`:'';
      return gapRow+`<tr class="source-line ${state.actor&&row.matched?'is-attributed':''}" data-line="${row.number}"><td class="source-number" title="${row.matched&&state.actor?'Attributed to '+esc(name(state.actor)):'Line '+row.number}">${row.number}</td><td class="source-text"><code>${esc(row.text)||' '}</code></td><td class="source-author">${author}</td></tr>`;
    }).join('');
    const trailing=projection.rows.length===rows.length&&previous<projection.total?`<tr class="source-gap"><td colspan="3"><button data-source-mode="full">${number(projection.total-previous)} lines folded · show full file</button></td></tr>`:'';
    panel.innerHTML=`<div class="section-heading"><div>${heading}<p>${esc(summary)} <span class="source-version">${esc(version)}</span></p></div></div><div class="source-toolbar">${ownerPicker}${controls}<p>${state.actor?'<span class="source-key" aria-hidden="true"></span> '+(sourceCode.passages?'Includes 3 lines of context.':'Attributed lines are highlighted.'):'Choose whose lines to highlight.'}</p></div>${state.metric==='changedLines'?'<p class="source-metric-note">This reader shows surviving code at the selected date. Added and deleted lines in the recent-change window are in Supporting commits below.</p>':''}${projection.attributed===0&&state.actor?'<p class="source-status">This actor has no surviving lines in this version. <button class="quiet-button" data-source-mode="full">Read the full file →</button></p>':''}${rows.length?`<div class="source-scroll" tabindex="0" role="region" aria-label="Source code at ${esc(version)}"><table class="source-table"><thead class="sr-only"><tr><th>Line</th><th>Code</th><th>Attribution commit</th></tr></thead><tbody>${body}${trailing}</tbody></table></div>`:''}${rows.length<projection.rows.length?`<button class="source-more" data-source-more>Show more code · ${number(rows.length)} of ${number(projection.rows.length)} ${sourceCode.passages?'matching and context ':''}lines shown ↓</button>`:''}`;
  }
  function aiBadges(signals) {
    return [...new Set((signals||[]).map(signal=>signal.tool))].map(tool=>`<span class="ai-badge" title="This commit declares AI assistance; individual lines may be human-written.">${esc(tool)} · AI declared</span>`).join('');
  }
  function actorInitials(actor) {
    if(actor.id==='unknown')return '?';
    const words=String(actor.name||name(actor.id)).trim().split(/\s+/).filter(Boolean);
    return words.length>1?words[0].slice(0,1)+words[words.length-1].slice(0,1):String(words[0]||'?').slice(0,2);
  }
  function renderActorProfile(after,afterFiles) {
    const actor=actors[state.group].get(state.actor)||{id:state.actor,name:name(state.actor)};
    const attributed=after.counts.get(state.actor)||0;
    const includedFiles=afterFiles.filter(file=>(file.contributions||[]).some(contribution=>actorID(contribution)===state.actor&&valueOf(contribution)>0)).length;
    const identity=state.group==='people'?(actorEmails(actor).join('\n')||'No email recorded'):(actor.basis||'No affiliation rule supplied');
    const scope=state.scope||'the entire repository';
    $('actors-title').textContent=state.group==='people'?'Contributor':'Organization';
    $('actors-subtitle').textContent=`At ${formatDate(data.snapshots[state.current].date)}`;
    $('actor-count').hidden=true;
    $('actor-list').innerHTML=`<section class="actor-profile"><div class="profile-heading"><span class="profile-avatar" aria-hidden="true">${esc(actorInitials(actor).toUpperCase())}</span><div class="profile-identity"><h3 tabindex="-1">${esc(actor.name||name(state.actor))}</h3><p>${esc(identity)}</p>${actor.identityBasis==='matching-name'?'<small class="identity-basis">Grouped by matching name</small>':''}</div></div><div class="profile-metrics"><div class="profile-metric"><span>${state.metric==='lines'?'Attributed lines':'Changed lines'}</span><strong>${number(attributed)}</strong><small>${after.total?`${percent(pct(attributed,after.total))} of this scope`:'No included lines at this date'}</small></div><div class="profile-metric"><span>Included files</span><strong>${number(includedFiles)}</strong><small>With ${state.metric==='lines'?'surviving code':'recent changes'}</small></div></div><div class="profile-context"><span>In this scope</span><p>${esc(scope)}</p><small>Share uses all ${number(after.total)} ${metricUnit()} included here.${state.group==='organizations'&&state.actor==='unknown'?' Unknown affiliation means no configured rule matched.':''}</small></div><div class="profile-actions"><button class="primary-button" data-view="evolution">Trace over time <span aria-hidden="true">↗</span></button><button class="quiet-button" data-open-evidence>View supporting commits <span aria-hidden="true">↓</span></button>${state.group==='people'?`<a class="quiet-button profile-ai-link" href="${esc(AtlasRoutes.url('/ai?'+new URLSearchParams({person:actor.email||actor.id,path:state.scope,to:state.rangeTo})))}">Explore AI declarations ↗</a>`:''}<button class="quiet-button" data-clear-actor>All ${state.group==='people'?'contributors':'organizations'} <span aria-hidden="true">←</span></button></div></section>`;
  }
  function renderChangeFocus(before,after) {
    const panel=$('change-focus');if(!panel)return;
    panel.hidden=!state.actor;if(!state.actor){panel.innerHTML='';return;}
    const actor=actors[state.group].get(state.actor)||{id:state.actor,name:name(state.actor)};
    const start=before.counts.get(state.actor)||0,end=after.counts.get(state.actor)||0;
    const atA=data.snapshots[state.baseline].date,atB=data.snapshots[state.current].date,sameDay=dayNumber(atA)===dayNumber(atB)&&Date.parse(atA)!==Date.parse(atB);
    const dateA=formatDate(atA)+(sameDay?' · '+formatTime(atA)+' UTC':''),dateB=formatDate(atB)+(sameDay?' · '+formatTime(atB)+' UTC':'');
    const change=(start,end)=>end===start?'is unchanged':`${end>start?'rose':'fell'} by ${number(Math.abs(end-start))} ${metricUnit()}`;
    const countLabel=state.metric==='lines'?'Attributed lines':'Changed lines';
    const noShare=!before.total&&!after.total?'Neither date has included lines in this scope; shares are unavailable.':!before.total?'The start date has no included lines in this scope; its share is unavailable.':!after.total?'The end date has no included lines in this scope; its share is unavailable.':'';
    const identity=state.group==='people'&&actorEmails(actor).length?` · ${actorEmails(actor).join(' · ')}`:'';
    panel.innerHTML=`<div class="change-focus-heading"><div><span class="eyebrow">Tracing ${state.group==='people'?'a contributor':'an organization'}</span><h2>${esc(actor.name||name(state.actor))}</h2><p>${esc(state.scope||'Entire repository')}${esc(identity)}</p>${actor.identityBasis==='matching-name'?'<small class="identity-basis">Grouped by matching name</small>':''}</div><button class="quiet-button" data-clear-actor>All ${state.group==='people'?'contributors':'organizations'} <span aria-hidden="true">×</span></button></div><div class="change-focus-values"><div class="change-focus-value"><span>Share of scope</span><strong>${before.total?percent(pct(start,before.total)):'—'} <span aria-hidden="true">→</span> ${after.total?percent(pct(end,after.total)):'—'}</strong><small class="focus-date">${esc(dateA)} → ${esc(dateB)}</small></div><div class="change-focus-value"><span>${countLabel}</span><strong>${number(start)} <span aria-hidden="true">→</span> ${number(end)}</strong><small class="focus-date">${esc(dateA)} → ${esc(dateB)}</small></div><div class="change-focus-value"><span>Full scope · ${metricUnit()}</span><strong>${number(before.total)} <span aria-hidden="true">→</span> ${number(after.total)}</strong><small class="focus-date">${esc(dateA)} → ${esc(dateB)}</small></div></div><p class="change-focus-reading">The attributed count ${change(start,end)}; the scope total ${change(before.total,after.total)}. ${noShare||'Shares use the full scope at each date.'}${state.metric==='changedLines'?` Each date covers its preceding ${number(data.method.windowDays)} days.`:''}</p><div class="profile-actions"><button class="quiet-button" data-overview-actor="${esc(state.actor)}">Explore attributed code <span aria-hidden="true">↗</span></button><button class="quiet-button" data-open-evidence>View supporting commits <span aria-hidden="true">↓</span></button></div>`;
  }
  function renderActors(comparisonActors,before,after,afterFiles) {
    const footnote=document.querySelector('.actor-footnote');if(footnote)footnote.hidden=!!state.actor;
    if(state.actor){renderActorProfile(after,afterFiles);return;}
    $('actor-count').hidden=false;
    const query=state.search.toLowerCase();
    const list=comparisonActors.filter(a=>!query||matchesActor(a.id,query));
    $('actors-title').textContent=state.group==='people'?'Contributors':'Organizations';
    $('actors-subtitle').textContent=`Share of ${metricUnit()} in this scope.`;
    $('actor-count').textContent=number(list.length);
    if(!list.length){$('actor-list').innerHTML=empty(query?`No ${state.group==='people'?'contributor':'organization'} names match`:'No attribution at this snapshot',query?`Search for a name, or open a matching path in the explorer to see its ${state.group==='people'?'contributors':'organizations'}.`:'Choose another date or measurement to explore attribution.');return;}
    const visible=state.allActors?list:list.slice(0,7);
    if(state.actor&&!visible.some(a=>a.id===state.actor)){const selected=list.find(a=>a.id===state.actor);if(selected)visible.push(selected);}
    $('actor-list').innerHTML=visible.map(actor=>`<button class="actor-row ${state.actor===actor.id?'selected':''}" data-actor="${esc(actor.id)}" aria-pressed="${state.actor===actor.id}" title="${esc(name(actor.id))}: ${number(actor.after)} ${metricUnit()} at ${esc(formatDate(data.snapshots[state.current].date))}"><span class="actor-line"><span class="swatch" style="background:${color(actor.id)}"></span><span class="actor-name-label">${esc(actors[state.group].get(actor.id)?.name || name(actor.id))}${state.group==='people'&&duplicateNames.has(actors.people.get(actor.id)?.name)?`<span class="actor-identity">${esc(actors.people.get(actor.id)?.email || actor.id)}</span>`:''}</span><span class="actor-share">${percent(actor.afterPct)}</span></span><span class="actor-meta"><span>${number(actor.after)} ${metricUnit()}</span><span>${actor.after?'':'Present only at the earlier date'}</span></span><span class="actor-spark"><span style="width:${actor.afterPct}%;background:${color(actor.id)}"></span></span></button>`).join('')+(list.length>7?`<button class="more-button" data-more-actors>${state.allActors?'Show fewer actors':`Show all ${number(list.length)} actors`} ↓</button>`:'');
  }

  function collectEvidence(beforeFiles,afterFiles) {
    const found=new Map(), query=state.search.toLowerCase();
    for(const [side,files] of [['A',beforeFiles],['B',afterFiles]])for(const file of files)for(const contribution of file.contributions||[]){
      const id=actorID(contribution);
      if(!valueOf(contribution)||(state.actor&&id!==state.actor))continue;
      if(query&&!file.path.toLowerCase().includes(query)&&!matchesActor(id,query))continue;
      for(const evidence of contribution.evidence||[]){
        // Engine evidence kinds describe which measurement the commit supports.
        const kind=String(evidence.kind||'').toLowerCase();
        if(state.metric==='lines'&&(kind==='activity'||kind==='recent'||kind==='change'))continue;
        if(state.metric==='changedLines'&&(kind==='blame'||kind==='ownership'||kind==='surviving'))continue;
        const path=evidence.path||file.path,key=`${evidence.sha}\0${path}\0${id}`;
        if(found.has(key)){const existing=found.get(key);if(!existing.sides.includes(side))existing.sides.push(side);if(side==='B'){existing.scopePath=file.path;existing.scopeSnapshot=state.current;}}
        else found.set(key,{...evidence,path,scopePath:file.path,scopeSnapshot:side==='B'?state.current:state.baseline,id,sides:[side]});
      }
    }
    return [...found.values()].sort((a,b)=>String(b.date).localeCompare(String(a.date))||a.path.localeCompare(b.path));
  }
  function renderEvidence(beforeFiles,afterFiles) {
    evidenceRows=collectEvidence(beforeFiles,afterFiles);
    $('evidence-subtitle').textContent=`${state.actor?name(state.actor)+' · ':''}${state.scope||'Entire repository'} · supporting commits at ${state.view==='codebase'?formatDate(data.snapshots[state.current].date):'the start and end of the period'}. Open a commit to inspect its patch.`;
    $('evidence-count').textContent=`${number(evidenceRows.length)} ${evidenceRows.length===1?'record':'records'}`;
    if(!evidenceRows.length){$('evidence-list').innerHTML=empty('No supporting commits in this selection','Try a different actor, path, date, or measurement. An empty list for this selection is not proof of inactivity elsewhere in the repository.');return;}
    $('evidence-list').innerHTML=`<table class="evidence-table"><thead><tr><th scope="col">COMMIT / SOURCE</th><th scope="col">PATH</th><th scope="col">${state.group==='people'?'PERSON':'ORGANIZATION'}</th><th scope="col">COMMIT DATE</th></tr></thead><tbody>${evidenceRows.slice(0,state.evidenceLimit).map((e,index)=>`<tr><td><button class="commit-proof" data-proof="${index}" title="Inspect patch: ${esc(e.summary)}"><span class="commit-title">${esc(e.summary||'Untitled commit')}</span><span class="commit-meta"><span class="commit-sha">${esc(String(e.sha).slice(0,8))} ↗</span><span class="revision-badge">${state.view==='codebase'?'At selected date':e.sides.map(side=>side==='A'?'Start':'End').join(' + ')}</span><span>${esc(e.kind||'Git evidence')}</span></span></button></td><td><button class="evidence-path path-button" data-scope="${esc(e.scopePath)}" data-scope-at="${e.scopeSnapshot}" title="Explore ${esc(e.scopePath)} at ${esc(formatDate(data.snapshots[e.scopeSnapshot].date))}${e.path!==e.scopePath?`; patch originally at ${esc(e.path)}`:''}">${esc(e.scopePath)}</button>${e.path!==e.scopePath?`<small class="evidence-original-path">Patch: ${esc(e.path)}</small>`:''}</td><td><span class="evidence-actor"><span class="swatch" style="background:${color(e.id)}"></span>${esc(name(e.id))}</span></td><td><span class="evidence-date">${esc(formatDate(e.date))}</span></td></tr>`).join('')}</tbody></table>`+(evidenceRows.length>state.evidenceLimit?`<button class="more-button" data-more-evidence>Show more supporting commits (${number(evidenceRows.length-state.evidenceLimit)} remaining) ↓</button>`:'');
  }
  function renderWarnings() {
    const messages=[...new Set([...(data.warnings||[]),...(state.view==='codebase'?[]:data.snapshots[state.baseline].warnings||[]),...(data.snapshots[state.current].warnings||[])])];
    if(data.repository.shallow&&!messages.some(m=>/shallow/i.test(m)))messages.unshift('This clone is shallow. Missing history can distort attribution and comparisons.');
    $('warnings').innerHTML=messages.length?`<div class="warning-banner"><span aria-hidden="true">△</span><details><summary>${number(messages.length)} ${messages.length===1?'coverage note':'coverage notes'} · ${esc(messages[0])}</summary><ul>${messages.map(m=>`<li>${esc(m)}</li>`).join('')}</ul></details></div>`:'';
  }
  function render() {
    // Moving a handle updates only its controls and loading feedback. The last
    // committed chart stays labelled until the newest exact response arrives.
    if(state.view==='evolution'&&(temporal.status==='preview'||temporal.status==='loading')){renderShell();renderDates();saveState();return;}
    const focused = document.activeElement, focusData = focused?.dataset || {}, focusArea = focused?.closest?.('#actor-list,#timeline-legend,#domain-list,#timeline-chart,#evolution-content')?.id;
    const focusedPath = focused?.closest?.('.domain-row')?.querySelector('[data-scope]')?.dataset.scope;
    const beforeFiles=temporal.empty?[]:filesAt(state.baseline),afterFiles=temporal.empty?[]:filesAt(state.current),before=aggregate(beforeFiles),after=aggregate(afterFiles),comparisonActors=comparison(before,after);
    document.querySelectorAll('[data-group]').forEach(button=>button.setAttribute('aria-pressed',button.dataset.group===state.group));
    document.querySelectorAll('[data-metric]').forEach(button=>button.setAttribute('aria-pressed',button.dataset.metric===state.metric));
    $('metric-explanation').innerHTML=state.metric==='lines'?'Each included line is attributed to its last Git author.':`Added + deleted lines in the ${number(data.method.windowDays)} days ending at each snapshot.`;
    renderShell();renderDates(before,after);renderBreadcrumbs();
    if(state.view==='overview')renderRepositoryOverview(beforeFiles,afterFiles,before,after,comparisonActors);
    if(state.view==='codebase'){const emptyBefore=aggregate([]),snapshotActors=comparison(emptyBefore,after);renderOverview(emptyBefore,after,snapshotActors,afterFiles);renderDomains([],afterFiles,emptyBefore,after);renderActors(snapshotActors,emptyBefore,after,afterFiles);}
    if(state.view==='evolution'){renderChangeFocus(before,after);renderTimeline();renderMovements(comparisonActors,before,after);}
    if(state.view==='history')renderHistory();
    if(state.view==='codebase'||state.view==='evolution')renderEvidence(state.view==='codebase'?[]:beforeFiles,afterFiles);
    renderSource();renderWarnings();saveState();
    if (focused && focused !== document.body && (!focused.isConnected || focused.hidden)) {
      let replacement;
      if (focusData.actor !== undefined) {
        const scope = focusArea ? $(focusArea) : $('actor-list');
        if (focusedPath) replacement = scope.querySelector(`[data-scope="${CSS.escape(focusedPath)}"]`)?.closest('.domain-row')?.querySelector(`[data-actor="${CSS.escape(focusData.actor)}"]`);
        replacement ||= scope.querySelector(`[data-actor="${CSS.escape(focusData.actor)}"]`);
        if(!replacement&&state.actor&&state.view==='codebase')replacement=$('actor-list').querySelector('.profile-identity h3');
      } else if (focusData.scope !== undefined || focused.id === 'actor-filter') replacement = $('breadcrumbs').querySelector('button:last-child');
      else if (focusData.moreActors !== undefined) replacement = $('actor-list').querySelector('[data-more-actors]');
      else if (focusData.moreDomains !== undefined) replacement = $('domain-list').querySelector('[data-more-domains]');
      else if (focusData.moreEvidence !== undefined) replacement = $('evidence-list').querySelector('[data-more-evidence]');
      else if (focusData.moreMovers !== undefined) replacement = $('evolution-content').querySelector('[data-more-movers]');
      else if (focusData.clearActor !== undefined) replacement = state.view==='codebase'?$('actor-list').querySelector('[data-actor]'):$('page-title');
      (replacement || (state.view==='overview'?$('page-title'):$('search'))).focus({preventScroll:true});
    }
  }

  function openMethods() {
    const snapshot=data.snapshots[state.current];
    const exclusions=Object.entries(snapshot.exclusions||{}).sort((a,b)=>b[1]-a[1]);
    $('method-content').innerHTML=`<section class="method-section"><span class="definition-tag">Two complementary signals</span><h3>Surviving code</h3><p>${esc(data.method.ownership)}</p></section><section class="method-section"><h3>Recent changes · ${number(data.method.windowDays)} days</h3><p>${esc(data.method.activity)}</p></section><section class="method-section"><h3>Identity and historical affiliations</h3><p>${esc(data.method.identity)}</p><p>${esc(data.method.affiliation)}</p></section><section class="method-section"><h3>Organization mapping basis</h3><p>These are the supplied mappings, not independently verified employment records.</p><ul>${(data.organizations||[]).map(org=>`<li><strong>${esc(org.name)}</strong> — ${esc(org.basis||'No basis supplied')}</li>`).join('')}</ul></section><section class="method-section"><h3>Reading percentages and change</h3><p>Actor shares use all attributed ${metricUnit()} in the selected path as the denominator. Codebase bars use the lines in each row’s own file or folder as their denominator. The adjacent count shows lines attributed to the selected actor, or all lines when no actor is selected. With an actor selected, domain shares show that actor’s contribution to the scope. Changes are percentage points (pp) from the start to the end of the period; a larger share can result from someone else’s code being deleted. No share proves expertise, responsibility, or decision-making authority.</p></section><section class="method-section"><h3>Time and evidence</h3><p>The timeline contains every indexed first-parent revision of the selected branch, not a subset of dates. Each step reflects the code at that revision; the horizontal axis uses the revision’s effective visibility time. If Git timestamps move backwards, visibility stays monotone along the first-parent chain while the original commit date remains available in the tooltip. Recent-change windows can overlap. The index stores the exact code state and metadata of every reachable commit, including work before it was merged. The Commits screen searches all of these states; parent links let you follow either side of a merge. Explore this history selects the first-parent chain ending at any indexed SHA. Each ownership timeline follows one selected first-parent chain, so its states form a coherent code history. In the HEAD history, merged work becomes visible when integrated; its earlier states remain directly inspectable in Commits or by selecting that history. The indexed commit count covers all branches and can exceed the revision count on this chart. The Start and End badges show which boundary of the selected period supplied the evidence. The slider and date fields query the persistent local index. Zooming does not sample, rebuild, or discard revisions. Start and end snapshots cover the exact requested boundaries. Extra activity entry and expiration points show when changes enter or leave the rolling window, even without a new first-parent revision. Dates are UTC, and the repository HEAD stays fixed for this workspace. A historical path may differ after a rename. Git attribution follows edits; it does not establish that a responsibility was transferred.</p></section><section class="method-section"><h3>Coverage at ${esc(formatDate(snapshot.date))}</h3><p>${number((snapshot.files||[]).length)} included files · ${number(snapshot.excludedFiles)} excluded files · revision <code>${esc(String(snapshot.revision).slice(0,12))}</code></p>${exclusions.length?`<ul>${exclusions.map(([reason,count])=>`<li>${esc(reason)}: ${number(count)} files</li>`).join('')}</ul>`:''}<ul>${(data.method.exclusions||[]).map(item=>`<li>${esc(item)}</li>`).join('')}</ul></section>`;
    previousFocus=document.activeElement;$('method-dialog').showModal();$('close-method').focus();
  }
  function renderRevisionBrowser() {
    const point=revisionChoices[revisionCursor];
    $('revision-browser').hidden=!completeIndex()||!canQuery()||!point;
    if(!point)return;
    $('revision-browser-count').textContent=`Revision ${number(revisionCursor+1)} of ${number(revisionChoices.length)}`;
    $('revision-browser-date').textContent=`${formatDate(point.date)} · ${formatTime(point.date)} UTC`;
    $('revision-inspect').textContent=`Inspect ${String(point.revision).slice(0,8)} ↗`;
    $('revision-inspect').setAttribute('aria-label',`Inspect exact revision ${point.revision}, ${formatDate(point.date)}`);
    const pending=temporal.status==='loading'||temporal.status==='preview';
    $('revision-previous').disabled=pending||revisionCursor===0;$('revision-next').disabled=pending||revisionCursor===revisionChoices.length-1;
    $('revision-inspect').disabled=pending;
    const cursor=$('timeline-cursor');if(cursor&&timelineGeometry){const x=timelineGeometry.x(Date.parse(point.date));cursor.setAttribute('x1',x);cursor.setAttribute('x2',x);}
  }
  function moveRevision(offset,absolute=false) {
    revisionCursor=Math.max(0,Math.min(revisionChoices.length-1,absolute?offset:revisionCursor+offset));
    renderRevisionBrowser();
    const point=revisionChoices[revisionCursor];if(point)announce(`Revision ${number(revisionCursor+1)} of ${number(revisionChoices.length)}, ${String(point.revision).slice(0,8)}, ${formatDate(point.date)}.`);
  }
  function closeRevision() {
    ++revisionRequest;revisionController?.abort();revisionController=null;
    if($('revision-dialog')?.open)$('revision-dialog').close();
  }
  async function openRevision(point) {
    if(!point?.revision)return;
    if(state.view==='history'&&state.historyCommit!==point.revision){state.historyCommit=point.revision;historyMode='push';saveState();}
    inspectedPoint=point;
    revisionController?.abort();revisionController=new AbortController();const request=++revisionRequest,controller=revisionController;
    const scope=state.view==='history'?'':state.scope,group=state.group,selectedActor=state.view==='history'?'':state.actor;
    let dialog=$('revision-dialog');
    if(!dialog){dialog=document.createElement('dialog');dialog.id='revision-dialog';dialog.className='revision-dialog';dialog.setAttribute('aria-labelledby','revision-title');document.body.appendChild(dialog);
      dialog.addEventListener('close',()=>{
        // A Back/Forward restoration may already have reopened this same native dialog.
        if(dialog.open)return;
        ++revisionRequest;revisionController?.abort();
        if(state.view==='history'&&state.historyCommit){state.historyCommit='';historyMode='replace';saveState();}
      });
      dialog.addEventListener('click',event=>{if(event.target===dialog||event.target.closest('[data-close-revision]'))dialog.close();const button=event.target.closest('[data-inspect-sha]');if(button){const next=timelinePoints.find(item=>item.revision===button.dataset.inspectSha&&item.kind==='revision')||{revision:button.dataset.inspectSha};openRevision(next);}if(event.target.closest('[data-retry-revision]'))openRevision(inspectedPoint);});
    }
    if(!dialog.open)previousFocus=document.activeElement;
    dialog.dataset.revision=point.revision;
    const peers=timelinePoints.filter(item=>item.kind==='revision'&&Date.parse(item.date)===Date.parse(point.date));
    dialog.innerHTML=`<div class="dialog-heading"><div><div class="eyebrow">EXACT INDEXED COMMIT</div><h2 id="revision-title">${esc(String(point.revision).slice(0,12))}</h2><p class="proof-meta">${esc(scope||'Entire repository')}${point.date?' · '+esc(formatDate(point.date))+' · '+esc(formatTime(point.date))+' UTC':''}</p><div id="revision-commit-meta"></div></div><button class="icon-button" data-close-revision aria-label="Close revision inspector">×</button></div>${peers.length>1?`<div class="revision-peers"><span>${number(peers.length)} revisions share this visibility time. Each has its own code state.</span><div>${peers.map(item=>`<button class="quiet-button ${item.revision===point.revision?'is-current':''}" data-inspect-sha="${esc(item.revision)}" aria-pressed="${item.revision===point.revision}">${esc(String(item.revision).slice(0,10))}</button>`).join('')}</div></div>`:''}<div class="revision-content" id="revision-content" role="status">Reading this commit from the local index…</div><div class="dialog-footer revision-footer"><span>This is the code at one exact SHA.</span><a class="primary-button" href="${esc(historyHref(point.revision))}">Explore this history ↗</a></div>`;
    if(!dialog.open)dialog.showModal();dialog.querySelector('[data-close-revision]').focus();
    try {
      const response=await AtlasRoutes.fetch('/api/revision?'+new URLSearchParams({sha:point.revision}),{signal:controller.signal});
      if(!response.ok){let message=`Unable to read this revision (${response.status}).`;try{message=(await response.json()).error||message;}catch(_){}throw new Error(message);}
      const payload=await response.json();if(!dialog.open||request!==revisionRequest)return;
      const snapshot=payload.snapshots?.[0];if(!snapshot)throw new Error('The index returned no code state for this revision.');
      dialog.querySelector('.proof-meta').textContent=`${scope||'Entire repository'} · ${formatDate(snapshot.date)} · ${formatTime(snapshot.date)} UTC`;
      const commit=payload.commit||point.commit;
      if(commit)$('revision-commit-meta').innerHTML=`<p class="revision-message">${esc(commit.summary||'Untitled commit')}</p><p class="revision-author">${esc(commit.authorName||commit.authorEmail||'Unknown author')}${commit.authorEmail?' · '+esc(commit.authorEmail):''} · <span class="history-badge ${commit.mainline?'is-mainline':''}">${commit.mainline?'HEAD first-parent':'Side history'}</span></p><div class="revision-parents"><span>${(commit.parents||[]).length?'Parents':'Root commit · no parents'}</span>${(commit.parents||[]).map((sha,index)=>`<button class="quiet-button" data-inspect-sha="${esc(sha)}" aria-label="Inspect parent ${index+1}, ${esc(sha)}">${esc(String(sha).slice(0,10))}${index===0?' · first parent':''} ↗</button>`).join('')}</div>`;
      if(commit?.ai?.length)$('revision-commit-meta').insertAdjacentHTML('beforeend',`<div class="commit-ai-proof"><div>${aiBadges(commit.ai)}</div><p>Declared assistance applies to the commit, not necessarily every line.</p><details><summary>Original AI attribution markers</summary>${commit.ai.map(signal=>`<pre>${esc(signal.evidence)}</pre>`).join('')}</details><a href="${esc(AtlasRoutes.url('/ai?'+new URLSearchParams({person:commit.authorEmail})))}">Explore this author's AI declarations ↗</a></div>`);
      const files=(snapshot.files||[]).filter(file=>!scope||file.path===scope||file.path.startsWith(scope+'/'));
      const names=new Map((payload[group]||[]).map(actor=>[actor.id,actor.name+(group==='people'&&duplicateNames.has(actor.name)?` (${actor.email||actor.id})`:'')]));
      const label=id=>names.get(id)||actors[group].get(id)?.name||id||'Unknown';
      const counts=new Map();let total=0;
      const idOf=contribution=>group==='people'?contribution.personId:contribution.organizationId;
      for(const file of files)for(const contribution of file.contributions||[]){const count=Math.max(0,Number(contribution.lines)||0);counts.set(idOf(contribution),(counts.get(idOf(contribution))||0)+count);total+=count;}
      const people=[...counts].filter(([,count])=>count>0).sort((a,b)=>b[1]-a[1]);
      const actorRow=([id,count])=>`<div class="revision-actor"><span><i class="swatch" style="background:${color(id)}"></i>${esc(label(id))}</span><strong>${percent(pct(count,total))}</strong><span>${number(count)} lines</span></div>`;
      const visibleFiles=files.filter(file=>!selectedActor||(file.contributions||[]).some(contribution=>idOf(contribution)===selectedActor&&contribution.lines>0)).sort((a,b)=>a.path.localeCompare(b.path));
      $('revision-content').innerHTML=`<div class="revision-summary"><strong>${number(total)} surviving lines</strong><span>${number(files.length)} included ${files.length===1?'file':'files'} · ${number(people.length)} ${group==='people'?(people.length===1?'person':'people'):(people.length===1?'organization':'organizations')}</span></div><p class="revision-timestamp">SHA: <code>${esc(point.revision)}</code><br>Revision visibility: <code>${esc(snapshot.date)}</code>${point.date&&Date.parse(point.date)!==Date.parse(snapshot.date)?`<br>Selected timeline state: <code>${esc(point.date)}</code>`:''}${(commit?.date||point.committedDate)?`<br>Original commit date: <code>${esc(commit?.date||point.committedDate)}</code>`:''}</p>${!files.length?empty('No included files at this revision','This path may not exist yet, may have been removed, or may be excluded from attribution.'):`<div class="revision-attribution"><h3>Share of surviving code</h3><p>All ${number(total)} attributed lines in ${esc(scope||'the repository')}.</p><div>${people.slice(0,8).map(actorRow).join('')}${people.length>8?`<details class="revision-more"><summary>Show ${number(people.length-8)} more ${group==='people'?'people':'organizations'}</summary>${people.slice(8).map(actorRow).join('')}</details>`:''}</div></div><div class="revision-files"><h3>Files at this revision${selectedActor?` · ${esc(label(selectedActor))}`:''}</h3><table><thead><tr><th>PATH</th><th>SURVIVING LINES</th></tr></thead><tbody>${visibleFiles.map(file=>`<tr><td>${esc(file.path)}</td><td>${number((file.contributions||[]).reduce((sum,contribution)=>sum+(selectedActor&&idOf(contribution)!==selectedActor?0:Number(contribution.lines)||0),0))}</td></tr>`).join('')}</tbody></table>${!visibleFiles.length?'<p>No files are attributed to the selected actor at this revision.</p>':''}</div>`}`;
    } catch(error) {if(error.name!=='AbortError'&&dialog.open&&request===revisionRequest)$('revision-content').innerHTML=empty('Revision unavailable',error.message,'<button class="quiet-button" data-retry-revision>Try again</button>');}
  }
  async function openProof(index) {
    const evidence=evidenceRows[index];if(!evidence)return;const request=++proofRequest;
    let dialog=$('proof-dialog');
    if(!dialog){dialog=document.createElement('dialog');dialog.id='proof-dialog';dialog.className='proof-dialog';dialog.setAttribute('aria-labelledby','proof-title');document.body.appendChild(dialog);dialog.addEventListener('click',event=>{if(event.target===dialog||event.target.closest('[data-close-proof]'))dialog.close();});}
    previousFocus=document.activeElement;
    dialog.innerHTML=`<div class="dialog-heading"><div><div class="eyebrow">GIT EVIDENCE · ${state.view==='codebase'?'At selected date':evidence.sides.map(side=>side==='A'?'Start':'End').join(' + ')}</div><h2 id="proof-title">${esc(evidence.summary||'Commit evidence')}</h2><p class="proof-meta">${esc(String(evidence.sha).slice(0,12))} · ${esc(formatDate(evidence.date))} · ${esc(evidence.path)}</p></div><button class="icon-button" data-close-proof aria-label="Close commit evidence">✕</button></div><div id="proof-content" class="proof-content" role="status">Loading the local Git patch…</div>`;
    dialog.showModal();dialog.querySelector('[data-close-proof]').focus();
    try {const response=await AtlasRoutes.fetch('/api/commit?'+new URLSearchParams({sha:evidence.sha,path:evidence.path}));if(!response.ok){let message=`Unable to read this patch (${response.status}).`;try{const payload=await response.json();message=payload.error||message;}catch(_){}throw new Error(message);}const proof=await response.json();if(!dialog.open||request!==proofRequest)return;$('proof-content').innerHTML=`${proof.truncated?'<p class="patch-notice">This patch is truncated. Inspect the commit locally for the complete change.</p>':''}${proof.patch?`<pre class="patch-code">${String(proof.patch).split('\n').map(line=>`<span class="${line.startsWith('+')&&!line.startsWith('+++')?'patch-add':line.startsWith('-')&&!line.startsWith('---')?'patch-remove':line.startsWith('@@')?'patch-hunk':''}">${esc(line)}</span>`).join('\n')}</pre>`:'<p>No textual patch is available for this path at this commit. It may be a rename, binary file, or merge.</p>'}`;}
    catch(error){if(dialog.open&&request===proofRequest)$('proof-content').innerHTML=`<div class="empty-state"><h3>Patch unavailable</h3><p>${esc(error.message)}</p><p>Inspect <code>${esc(String(evidence.sha).slice(0,12))}</code> locally to verify the evidence.</p></div>`;}
  }

  function bindEvents() {
    $('source-panel').addEventListener('change',event=>{if(event.target.id==='source-owner'){const id=event.target.value;if(id)setActor(id);else clearActor();$('source-owner')?.focus({preventScroll:true});}});
    document.querySelector('.skip-link')?.addEventListener('click',event=>{event.preventDefault();$('main').focus();});
    $('snapshot-date').addEventListener('change',event=>setSnapshotDate(event.target.value));
    bindPeriod();
    $('history-search').addEventListener('input',event=>{state.historyQuery=event.target.value;changeCommitFilters(280);});
    for(const [id,field] of [['history-from','historyFrom'],['history-to','historyTo']])$(id).addEventListener('change',event=>{state[field]=event.target.value;if(state.historyFrom&&state.historyTo&&state.historyFrom>state.historyTo){if(field==='historyFrom')state.historyTo=state.historyFrom;else state.historyFrom=state.historyTo;}changeCommitFilters();});
    $('history-clear').addEventListener('click',()=>{state.historyQuery='';state.historyFrom='';state.historyTo='';changeCommitFilters();$('history-search').focus();});
    $('timeline-chart').addEventListener('pointermove',showTimelineHover);
    $('timeline-chart').addEventListener('pointerleave',()=>{if($('timeline-tooltip'))$('timeline-tooltip').hidden=true;renderRevisionBrowser();});
    $('timeline-chart').addEventListener('click',event=>{if(event.target.id==='timeline-hit'&&completeIndex()&&canQuery()&&temporal.status==='idle'){const index=timelinePointAt(event.clientX);if(index>=0)openRevision(timelinePoints[index]);}});
    $('timeline-chart').addEventListener('keydown',event=>{if(event.target.id!=='timeline-hit'||temporal.status!=='idle')return;if(event.key==='ArrowLeft'||event.key==='ArrowRight'){event.preventDefault();moveRevision(event.key==='ArrowLeft'?-1:1);}else if(event.key==='Enter'||event.key===' '){event.preventDefault();if(revisionChoices[revisionCursor])openRevision(revisionChoices[revisionCursor]);}});
    $('revision-previous').addEventListener('click',()=>moveRevision(-1));
    $('revision-next').addEventListener('click',()=>moveRevision(1));
    $('revision-inspect').addEventListener('click',()=>openRevision(revisionChoices[revisionCursor]));
    $('revision-browser').addEventListener('keydown',event=>{if(event.key==='ArrowLeft'||event.key==='ArrowRight'||event.key==='Home'||event.key==='End'||event.key==='PageUp'||event.key==='PageDown'){event.preventDefault();if(event.key==='Home'||event.key==='End')moveRevision(event.key==='Home'?0:revisionChoices.length-1,true);else moveRevision(event.key==='ArrowLeft'?-1:event.key==='ArrowRight'?1:event.key==='PageUp'?-10:10);}});
    $('actor-filter').addEventListener('click',clearActor);
    $('search').addEventListener('input',event=>{state.search=event.target.value;state.allDomains=false;state.evidenceLimit=12;render();});
    $('method-button').addEventListener('click',()=>data&&openMethods());
    $('coverage-button').addEventListener('click',openMethods);
    $('close-method').addEventListener('click',()=>$('method-dialog').close());
    $('method-dialog').addEventListener('click',event=>{if(event.target===$('method-dialog'))$('method-dialog').close();});
    document.addEventListener('close',event=>{if(!event.target.open&&previousFocus?.isConnected)previousFocus.focus();},true);
    const restoreLocation=async()=>{
      const linkedTip=AtlasRoutes.validTip(new URLSearchParams(location.search).get('tip'));if(linkedTip!==state.tip){location.reload();return;}
      clearTimeout(commitList.timer);commitList.controller?.abort();++commitList.request;
      cancelPeriodRequest();const request=++locationRequest;restoringRoute=true;
      closeRevision();
      ++proofRequest;if($('proof-dialog')?.open)$('proof-dialog').close();
      Object.assign(state,{view:'overview',group:'people',metric:'lines',baseline:0,current:data.snapshots.length-1,scope:'',actor:'',search:'',tip:linkedTip,historyQuery:'',historyFrom:'',historyTo:'',historyOffset:0,historyCommit:'',allActors:false,allDomains:false,allMovers:false,timelineAll:false,evidenceLimit:12});
      $('search').value='';restoreState();render();
      if(state.view!=='history')await loadPeriod();if(request!==locationRequest)return;
      // Resolve a path or identity that only exists in the newly loaded dates.
      const resolved=readLocation(location);state.scope=resolved.scope;state.actor=resolved.actor;
      restoringRoute=false;render();$('page-title').focus({preventScroll:true});if(state.view==='history'){loadCommits();if(state.historyCommit)openRevision({revision:state.historyCommit});}announce('Restored the linked ownership comparison.');
    };
    window.addEventListener('popstate',restoreLocation);
    // Previously shared hash links are converted immediately to the new URLs.
    window.addEventListener('hashchange',()=>{if(location.hash)restoreLocation();});
    document.addEventListener('click',event=>{
      const target=event.target.closest('button,a[data-view]');if(!target)return;
      if(target.matches('a[data-view]')){if(event.metaKey||event.ctrlKey||event.shiftKey||event.altKey||event.button!==0)return;event.preventDefault();}
      if(target.hasAttribute('data-view'))navigate(target.dataset.view);
      else if(target.hasAttribute('data-timeline-all')){state.timelineAll=!state.timelineAll;renderTimeline();$('timeline-legend').querySelector('[data-timeline-all]')?.focus({preventScroll:true});}
      else if(target.hasAttribute('data-source-mode')){sourceCode.passages=target.dataset.sourceMode==='passages';sourceCode.limit=250;renderSource();$('source-panel').querySelector('[data-source-mode="'+target.dataset.sourceMode+'"]')?.focus({preventScroll:true});}
      else if(target.hasAttribute('data-source-more')){const scroll=$('source-panel').querySelector('.source-scroll'),top=scroll?.scrollTop||0;sourceCode.limit+=500;renderSource();const next=$('source-panel').querySelector('.source-scroll');if(next){next.scrollTop=top;next.focus({preventScroll:true});}}
      else if(target.hasAttribute('data-source-retry')){sourceCode.status='idle';sourceCode.renderKey='';renderSource();}
      else if(target.hasAttribute('data-source-commit')){const block=sourceCode.data?.blocks[Number(target.dataset.sourceCommit)];if(block)openRevision({revision:block.sha,date:block.date});}
      else if(target.hasAttribute('data-open-commit')){const commit=commitList.data?.commits.find(item=>item.sha===target.dataset.openCommit);if(commit)openRevision({revision:commit.sha,date:commit.date,committedDate:commit.date,commit});}
      else if(target.hasAttribute('data-retry-commits'))loadCommits();
      else if(target.hasAttribute('data-history-page')){state.historyOffset=Number(target.dataset.historyPage)||0;historyMode='push';saveState();loadCommits();$('page-title').focus({preventScroll:true});window.scrollTo({top:0,behavior:'instant'});}
      else if(target.hasAttribute('data-overview-actor'))navigate('codebase',{actor:target.dataset.overviewActor});
      else if(target.hasAttribute('data-explore-shift'))navigate('evolution',{scope:target.dataset.exploreShift,actor:target.dataset.shiftActor});
      else if(target.hasAttribute('data-group')){++locationRequest;restoringRoute=false;state.group=target.dataset.group;state.actor='';state.allActors=false;state.allMovers=false;render();}
      else if(target.hasAttribute('data-metric')){++locationRequest;restoringRoute=false;state.metric=target.dataset.metric;state.evidenceLimit=12;render();}
      else if(target.hasAttribute('data-scope')){if(target.closest('#breadcrumbs'))navigate(state.view,{scope:target.dataset.scope});else if(target.hasAttribute('data-scope-at'))navigate('codebase',{scope:target.dataset.scope,snapshot:target.dataset.scopeAt});else setScope(target.dataset.scope);}
      else if(target.hasAttribute('data-actor'))setActor(target.dataset.actor);
      else if(target.hasAttribute('data-more-actors')){state.allActors=!state.allActors;render();}
      else if(target.hasAttribute('data-more-domains')){state.allDomains=!state.allDomains;render();}
      else if(target.hasAttribute('data-more-evidence')){state.evidenceLimit+=20;render();}
      else if(target.hasAttribute('data-more-movers')){state.allMovers=!state.allMovers;render();}
      else if(target.hasAttribute('data-clear-actor'))clearActor();
      else if(target.hasAttribute('data-open-evidence')){const disclosure=$('evidence-disclosure');disclosure.open=true;disclosure.scrollIntoView({block:'start',behavior:'smooth'});disclosure.querySelector('summary')?.focus({preventScroll:true});}
      else if(target.hasAttribute('data-proof'))openProof(Number(target.dataset.proof));
    });
    document.addEventListener('keydown',event=>{if(event.key==='/'&&!event.metaKey&&!event.ctrlKey&&!['INPUT','TEXTAREA','SELECT'].includes(document.activeElement.tagName)&&!document.querySelector('dialog[open]')){event.preventDefault();if(state.view==='overview')navigate('codebase');(document.body.classList.contains('file-view')?$('source-owner'):$(state.view==='history'?'history-search':'search'))?.focus();}});
  }
  async function start() {
    try {
      await waitForIndex();
      state.tip=AtlasRoutes.validTip(new URLSearchParams(location.search).get('tip'));
      data=await readOwnership(state.tip?new URLSearchParams({tip:state.tip}):undefined);
      if(!data.repository||!Array.isArray(data.snapshots))throw new Error('The server returned an invalid ownership dataset.');
      if(!data.snapshots.length){$('loading').innerHTML='<h1>No snapshots to explore yet.</h1><p>Analyze a Git repository with tracked source files, or widen the analysis dates, then reload this page.</p><button class="primary-button" id="reload-analysis">Reload analysis</button>';$('reload-analysis').addEventListener('click',()=>location.reload());return;}
      data.snapshots.sort((a,b)=>new Date(a.date)-new Date(b.date));
      data.method=data.method||{};
      actors={people:new Map((data.people||[]).map(actor=>[actor.id,actor])),organizations:new Map((data.organizations||[]).map(actor=>[actor.id,actor]))};
      colors={people:new Map(),organizations:new Map()};
      for (const group of ['people','organizations']) {
        [...actors[group].keys()].sort().forEach((id,index)=>colors[group].set(id,palette[index%palette.length]));
      }
      const names = new Map(); for (const person of data.people||[]) names.set(person.name,(names.get(person.name)||0)+1); duplicateNames=new Set([...names].filter(([,count])=>count>1).map(([name])=>name));
      repoLabel=data.repository.name;
      const remote=String(data.repository.remote||''); const match=remote.match(/^(?:https?:\/\/|git@)(?:github\.com|gitlab\.com|bitbucket\.org)[/:]([^?#]+?)(?:\.git)?$/); if(match)repoLabel=match[1];
      temporal.bounds={from:dayNumber(data.timeline?.since||data.snapshots[0].date),to:dayNumber(data.timeline?.until||data.snapshots[data.snapshots.length-1].date)};
      state.rangeDefaultFrom=dayString(temporal.bounds.from);state.rangeDefaultTo=dayString(temporal.bounds.to);
      state.rangeFrom=state.rangeDefaultFrom;state.rangeTo=state.rangeDefaultTo;
      const initial={...data,snapshots:[...data.snapshots]};temporal.initial=initial;temporal.active=initial;temporal.activeScope='';temporal.activeKey=rangeKey();temporal.initialKey=rangeKey();temporal.cache.set(rangeKey(),initial);
      state.current=data.snapshots.length-1;restoringRoute=true;restoreState();
      if(rangeKey()!==temporal.activeKey)await loadPeriod();
      const resolved=readLocation(location);state.scope=resolved.scope;state.actor=resolved.actor;restoringRoute=false;
      document.title=`${repoLabel} · Ownership atlas`;
      $('repo-label').textContent=repoLabel;
      $('repo-context').innerHTML=`<strong>${esc(repoLabel)}</strong> · ${esc(historyLabel())}<br>${esc(data.repository.path)}`;
      bindEvents();initialized=true;$('loading').hidden=true;$('workspace').hidden=false;render();if(state.view==='history'){loadCommits();if(state.historyCommit)openRevision({revision:state.historyCommit});}
      let resizeTimer; window.addEventListener('resize',()=>{clearTimeout(resizeTimer);resizeTimer=setTimeout(()=>{if(state.view==='evolution'){renderTimeline();$('period-slider').classList.toggle('handles-close',(periodPosition(dayNumber(state.rangeTo))-periodPosition(dayNumber(state.rangeFrom)))*$('period-slider').clientWidth<44);}},100);});
    } catch(error) {
      if(error.index)return;
      $('loading').classList.add('error');$('loading').innerHTML=`<span class="loading-mark"></span><h1>We couldn’t load this repository.</h1><p>${esc(error.message)} Check that the local explorer is running, then try again.</p><button class="primary-button" id="retry-load">Try again</button>`;$('retry-load').addEventListener('click',()=>location.reload());
    }
  }
  start();
})();
