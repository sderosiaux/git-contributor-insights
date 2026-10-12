(function() {
  'use strict';
  const $ = id => document.getElementById(id);
  const esc = value => String(value ?? '').replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
  const number = value => new Intl.NumberFormat('en-US').format(value || 0);
  const percent = (n,d) => d ? (100*n/d).toFixed(1)+'%' : '—';
  const date = value => new Date(value).toLocaleDateString('en-US',{year:'numeric',month:'short',day:'numeric',timeZone:'UTC'});
  const tools = ['Claude','Copilot','Cursor','Codex','ChatGPT','Gemini','Aider','Windsurf'];
  let report, catalog, controller, request = 0, peopleExpanded = false, domainsExpanded = false;
  const cache = new Map();
  const fields = ['from','to','person','path','tool','evidence','offset'];
  const params = () => {
    const source = new URLSearchParams(location.search), result = new URLSearchParams();
    fields.forEach(key => { if(source.get(key))result.set(key,source.get(key)); });
    return result;
  };
  function href(route, values) { const p=new URLSearchParams();for(const [key,value] of Object.entries(values))if(value)p.set(key,value);return AtlasRoutes.url(route+(p.size?'?'+p:'')); }
  function change(values, replace=false) {
    const next=params();for(const [key,value] of Object.entries(values)){if(value)next.set(key,value);else next.delete(key);}
    if(!Object.hasOwn(values,'offset'))next.delete('offset');
    history[replace?'replaceState':'pushState']({},'', AtlasRoutes.url('/ai'+(next.size?'?'+next:'')));
    peopleExpanded=false;domainsExpanded=false;load();
  }
  async function read(query, signal) {
    const key=query.toString();if(cache.has(key))return cache.get(key);
    const response=await AtlasRoutes.fetch('/api/ai'+(key?'?'+key:''),{signal});
    let data;try{data=await response.json();}catch(_){throw new Error('AI provenance needs the updated local explorer. Restart ghca explore and try again.');}
    if(response.status===202){throw new Error(`The repository is still indexing (${number(data.completed)} / ${number(data.total)} commits). Open Overview to follow progress, then retry.`);}
    if(!response.ok)throw new Error(data.error||'Could not read AI provenance.');
    if(cache.size>=12)cache.delete(cache.keys().next().value);cache.set(key,data);return data;
  }
  function syncFilters() {
    const query=params();
    for(const field of ['from','to','person','tool','path'])$('ai-'+field).value=query.get(field)||(['from','to'].includes(field)?report[field]:'');
    for(const field of ['from','to']) { $('ai-'+field).min=catalog.firstDate;$('ai-'+field).max=catalog.lastDate; }
    $('ai-clear').hidden=!query.size;
    $('ai-all-time').disabled=!query.has('from')&&!query.has('to');
  }
  async function load() {
    controller?.abort();controller=new AbortController();const signal=controller.signal,id=++request;
    $('ai-status').hidden=false;$('ai-status').classList.remove('is-error');$('ai-status').innerHTML='<span class="source-spinner" aria-hidden="true"></span> Updating AI contributions…';
    $('ai-results').setAttribute('aria-busy','true');
    try {
      if(!catalog){catalog=await read(new URLSearchParams(),signal);if(id!==request)return;
        $('ai-person').innerHTML='<option value="">Everyone</option>'+catalog.people.slice().sort((a,b)=>a.name.localeCompare(b.name)).map(p=>`<option value="${esc(p.email||p.id)}">${esc(p.name||p.email)}</option>`).join('');
        $('ai-tool').innerHTML='<option value="">Any AI tool</option>'+tools.map(tool=>`<option>${tool}</option>`).join('');
        $('repo-label').textContent=catalog.repository.name;document.title=`AI contributions · ${catalog.repository.name} · Ownership atlas`;
        const status=await AtlasRoutes.fetch('/api/index',{signal}).then(r=>r.json());if(id!==request)return;
        $('snapshot-meta').textContent=`${number(status.completed)} / ${number(status.total)} commits indexed`;
      }
      const data=await read(params(),signal);if(id!==request)return;report=data;
      syncFilters();render();$('ai-status').hidden=true;
    }catch(error){if(error.name==='AbortError'||id!==request)return;$('ai-status').classList.add('is-error');$('ai-status').innerHTML=`${esc(error.message)} <button class="quiet-button" data-ai-retry>Try again</button>`;$('ai-results').innerHTML='';}
    finally{if(id===request)$('ai-results').setAttribute('aria-busy','false');}
  }
  const metric = (label,value,note) => `<div class="summary-metric"><span>${label}</span><strong>${value}</strong><small>${note}</small></div>`;
  function timeline() {
    const months=report.months,tool=params().get('tool'),step=Math.max(1,Math.ceil(months.length/9));
    const bars=months.map((m,i)=>{
      const share=m.commits?100*m.declared/m.commits:0;
      const label=new Date(m.key+'-01T00:00:00Z').toLocaleDateString('en-US',{month:'short',year:'2-digit',timeZone:'UTC'});
      const title=`${label}: ${number(m.declared)} of ${number(m.commits)} commits with ${tool||'AI'} declarations (${percent(m.declared,m.commits)})`;
      return `<button class="ai-month" data-ai-month="${m.key}" aria-label="${esc(title)}. Filter to this month." title="${esc(title)}"><span class="ai-month-column ${m.commits?'':'is-empty'}"><i style="height:${share}%"></i></span><span class="ai-month-label">${i===months.length-1||(i%step===0&&i<months.length-1-Math.ceil(step/2))?label:''}</span></button>`;
    }).join('');
    return `<section class="ai-panel ai-trend"><div class="section-heading"><div><h2>Declared assistance over time</h2><p>${esc(date(report.from))} – ${esc(date(report.to))} · click a month to explore</p></div><div class="ai-legend"><span><i></i>${esc(tool||'AI')} declared</span><span><i class="is-unknown"></i>${tool?'Other / unidentified':'Unidentified'}</span></div></div><div class="ai-chart"><div class="ai-yaxis"><span>100%</span><span>50%</span><span>0%</span></div><div class="ai-months">${bars}</div></div><p class="ai-chart-note">Share of commits each month. Empty months have no commits; hover or focus a bar for counts.</p></section>`;
  }
  function people() {
    const shown=peopleExpanded?report.people:report.people.slice(0,12);
    return `<section class="ai-panel"><div class="section-heading"><div><h2>People & declared usage</h2><p>Each share uses that person’s commits in this period and path.</p></div><span class="count-label">${number(report.people.length)} people</span></div><div class="ai-table-scroll"><table class="ai-people"><thead><tr><th>Person</th><th>Declared / all commits</th><th>Declared share</th><th>Still represented</th></tr></thead><tbody>${shown.map(p=>`<tr><td><button class="ai-person-link" data-ai-person="${esc(p.email||p.id)}"><span class="ai-avatar">${esc((p.name||'?').split(/\s+/).map(w=>w[0]).slice(0,2).join(''))}</span><span>${esc(p.name||p.email)}</span></button></td><td><strong>${number(p.declared)}</strong><span class="ai-muted"> / ${number(p.commits)}</span></td><td><span class="ai-share"><span class="ai-meter"><i style="width:${p.commits?100*p.declared/p.commits:0}%"></i></span>${p.declared?percent(p.declared,p.commits):'<span class="ai-muted">No declaration</span>'}</span></td><td>${p.declared?`${number(p.represented)} <span class="ai-muted">/ ${number(p.declared)}</span>`:'—'}</td></tr>`).join('')||'<tr><td colspan="4">No commits match this period and path. Widen the dates or reset the filters.</td></tr>'}</tbody></table></div>${report.people.length>12?`<button class="overview-section-action" data-ai-people>${peopleExpanded?'Show fewer people':'Show all '+number(report.people.length)+' people'} ↓</button>`:''}</section>`;
  }
  function domains() {
    const domains=report.domains.filter(d=>d.declared>0),shown=domainsExpanded?domains:domains.slice(0,8);
    return `<section class="ai-panel"><div class="section-heading"><div><h2>Where assistance is declared</h2><p>Included files touched by marked commits. Explore a path to go deeper.</p></div><span class="count-label">${number(domains.length)} paths</span></div><div class="ai-domain-list">${shown.map(d=>`<button class="ai-domain" data-ai-path="${esc(d.key)}"><span>${esc(d.key)}<small>${number(d.declared)} declared / ${number(d.commits)} commits touching this path</small></span><strong>${percent(d.declared,d.commits)} <span aria-hidden="true">↗</span></strong></button>`).join('')||'<p class="ai-empty">No marked commits touch an included path in this selection.</p>'}</div>${domains.length>8?`<button class="overview-section-action" data-ai-domains>${domainsExpanded?'Show fewer paths':'Show all '+number(domains.length)+' paths'} ↓</button>`:''}</section>`;
  }
  function evidence() {
    const mode=params().get('evidence')||'declared',tool=params().get('tool'),offset=report.offset,total=report.evidenceTotal;
    return `<section class="ai-panel" id="ai-evidence"><div class="section-heading"><div><h2>Follow the evidence</h2><p>Original declarations and the code still attributed at ${esc(date(report.to))}.</p></div><span class="count-label">${number(total)} commits</span></div><div class="segmented ai-evidence-tabs" role="group" aria-label="Commit evidence"><button data-ai-evidence="declared" aria-pressed="${mode==='declared'}">${esc(tool||'AI')} declared</button><button data-ai-evidence="unidentified" aria-pressed="${mode==='unidentified'}">No AI marker</button><button data-ai-evidence="all" aria-pressed="${mode==='all'}">All commits</button></div><div class="ai-commits">${report.evidence.map(c=>{
      const tools=[...new Set((c.signals||[]).map(s=>s.tool))],surviving=c.survivingPaths||[];
      return `<article class="ai-commit"><div class="ai-commit-top"><a class="ai-commit-title" href="${esc(href('/history',{commit:c.sha}))}">${esc(c.summary||'Untitled commit')} <span aria-hidden="true">↗</span></a><span class="ai-commit-date">${esc(date(c.date))}</span></div><div class="ai-commit-meta"><button data-ai-person="${esc(c.email||c.personId)}">${esc(c.author)}</button><code>${c.sha.slice(0,8)}</code>${c.merge?'<span class="history-badge">Merge</span>':''}${tools.length?tools.map(t=>`<span class="ai-badge">${esc(t)}</span>`).join(''):'<span class="ai-muted">AI assistance unidentified</span>'}<span class="ai-survival ${surviving.length?'has-code':''}">${surviving.length?'Code remains in '+number(surviving.length)+' file'+(surviving.length===1?'':'s'):'No surviving attribution at this date'}</span></div>${c.signals?.length?`<details class="ai-proof"><summary>View ${c.signals.length===1?'original marker':number(c.signals.length)+' original markers'}</summary>${c.signals.map(s=>`<pre>${esc(s.evidence)}</pre>`).join('')}</details>`:''}<details class="ai-proof"><summary>${surviving.length?'Explore surviving code':'View touched paths'} · ${number(surviving.length||c.paths?.length)}</summary><div class="ai-file-links">${(surviving.length?surviving:c.paths||[]).map(path=>surviving.length?`<a href="${esc(href('/codebase',{path,at:report.to,person:c.email}))}">${esc(path)} ↗</a>`:`<span>${esc(path)}</span>`).join('')||'<p>No included text-file changes were recorded.</p>'}</div></details></article>`;
    }).join('')||`<div class="ai-empty"><h3>${mode==='declared'?'No declared assistance in this selection':'No matching commits'}</h3><p>${mode==='declared'?'This does not establish that AI was not used. Explore unmarked commits or widen the period.':'Change the person, path or dates to explore more history.'}</p>${mode==='declared'?'<button class="quiet-button" data-ai-evidence="unidentified">Explore commits without markers →</button>':''}</div>`}</div><div class="history-pagination"><span>${total?number(offset+1)+'–'+number(Math.min(offset+report.limit,total))+' of '+number(total):'0 commits'}</span><div><button class="quiet-button" data-ai-page="${Math.max(0,offset-report.limit)}" ${offset===0?'disabled':''}>← Previous</button><button class="quiet-button" data-ai-page="${offset+report.limit}" ${offset+report.limit>=total?'disabled':''}>Next →</button></div></div></section>`;
  }
  function render() {
    const tool=params().get('tool'),declaredPeople=report.people.filter(p=>p.declared>0).length;
    $('ai-results').innerHTML=`<section class="overview-summary ai-summary"><div class="summary-heading"><div><h2>${esc(tool||'AI')} assistance, as recorded in Git</h2><p>${esc(params().get('path')||'Entire repository')} · ${esc(date(report.from))} – ${esc(date(report.to))}</p></div><a class="ai-method-link" href="#ai-method">About these signals ↗</a></div><div class="summary-metrics">${metric('Commits with declarations',number(report.declared),percent(report.declared,report.commits)+' of '+number(report.commits)+' commits')}${metric('People with declarations',number(declaredPeople),'of '+number(report.people.length)+' contributors in this selection')}${metric('Marked commits still represented',number(report.represented),'of '+number(report.declared)+' · at the selected end date')}</div><p class="ai-caveat">No marker means unidentified. Declared assistance does not establish who wrote each line.</p></section>${timeline()}${people()}${domains()}${evidence()}`;
  }
  $('ai-filters').addEventListener('submit',event=>{event.preventDefault();const values=Object.fromEntries(new FormData(event.target));if(values.from>values.to){$('ai-to').setCustomValidity('End must be on or after start.');$('ai-to').reportValidity();return;}$('ai-to').setCustomValidity('');change(values);});
  for(const id of ['ai-from','ai-to'])$(id).addEventListener('input',()=>$('ai-to').setCustomValidity(''));
  for(const key of ['person','tool'])$('ai-'+key).addEventListener('change',()=>change({[key]:$('ai-'+key).value}));
  $('ai-recent').addEventListener('click',()=>{if(!catalog)return;const end=new Date(catalog.lastDate+'T00:00:00Z'),start=new Date(end);start.setUTCDate(start.getUTCDate()-89);change({from:start.toISOString().slice(0,10)<catalog.firstDate?catalog.firstDate:start.toISOString().slice(0,10),to:catalog.lastDate});});
  $('ai-all-time').addEventListener('click',()=>change({from:'',to:''}));
  $('ai-clear').addEventListener('click',()=>{history.pushState({},'',AtlasRoutes.url('/ai'));load();});
  document.addEventListener('click',event=>{
    const target=event.target.closest('button,a');if(!target)return;
    if(target.matches('a[href="#ai-method"]'))$('ai-method').open=true;
    if(target.hasAttribute('data-ai-retry'))load();
    if(target.hasAttribute('data-ai-person'))change({person:target.dataset.aiPerson});
    if(target.hasAttribute('data-ai-path'))change({path:target.dataset.aiPath});
    if(target.hasAttribute('data-ai-evidence'))change({evidence:target.dataset.aiEvidence});
    if(target.hasAttribute('data-ai-page'))change({offset:target.dataset.aiPage});
    if(target.hasAttribute('data-ai-people')){peopleExpanded=!peopleExpanded;render();}
    if(target.hasAttribute('data-ai-domains')){domainsExpanded=!domainsExpanded;render();}
    if(target.hasAttribute('data-ai-month')){
      const from=target.dataset.aiMonth+'-01',last=new Date(from+'T00:00:00Z');last.setUTCMonth(last.getUTCMonth()+1);last.setUTCDate(0);
      change({from:from<report.from?report.from:from,to:last.toISOString().slice(0,10)>report.to?report.to:last.toISOString().slice(0,10)});
    }
  });
  window.addEventListener('popstate',load);
  load();
})();
