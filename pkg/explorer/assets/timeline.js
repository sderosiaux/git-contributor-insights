/* Exact sparse-series projection. Every input state stays available; only visible actor buckets are retained. */
((root) => {
  'use strict';
  function project({points,encoding,group,metric,from=-Infinity,to=Infinity,selectedActor='',limit=5,includeAll=false}) {
    if(encoding&&encoding!=='changes-v1')throw new Error('Unsupported ownership series encoding: '+encoding);
    const sparse=encoding==='changes-v1',actorOf=value=>group==='people'?value.personId:value.organizationId;
    const amount=value=>Math.max(0,Number(value[metric])||0);
    // Reconstruct just one current state. Sparse rows are absolute replacements,
    // including explicit zeroes; actors on different organizations remain separate pairs.
    function visit(callback) {
      const pairs=new Map(),counts=new Map(),changed=new Set();let total=0;
      for(const point of points){
        const time=Date.parse(point.date);if(time>to)break;
        changed.clear();
        if(!sparse){for(const id of counts.keys())changed.add(id);counts.clear();total=0;}
        for(const contribution of point.contributions||[]){
          const id=actorOf(contribution),value=amount(contribution);
          let difference=value;
          if(sparse){const key=contribution.personId+'\0'+contribution.organizationId;difference-=pairs.get(key)||0;if(value)pairs.set(key,value);else pairs.delete(key);}
          if(!difference)continue;
          const next=(counts.get(id)||0)+difference;if(next)counts.set(id,next);else counts.delete(id);
          total+=difference;changed.add(id);
        }
        if(time>=from)callback(point,counts,total,changed);
      }
    }
    // Overall volume alone hides late arrivals, even a leader at the end. Keep
    // the overall and endpoint leaders, plus anyone reaching 10% in any state.
    // This selects visible categories only; no history points are sampled.
    const totals=new Map(),previous=new Map(),last=new Map(),significant=new Set();let position=0,lastTotal=0,endCounts;
    visit((point,counts,total,changed)=>{
      const ids=position===0?counts.keys():changed;
      for(const id of ids){totals.set(id,(totals.get(id)||0)+(position-(last.get(id)||0))*(previous.get(id)||0));last.set(id,position);previous.set(id,counts.get(id)||0);}
      // When the denominator shrinks, unchanged actors can cross the threshold.
      // Otherwise only actors whose amounts changed can gain share.
      const candidates=position===0||total<lastTotal?counts.keys():changed;
      if(total>0)for(const id of candidates)if((counts.get(id)||0)/total>=.1)significant.add(id);
      lastTotal=total;endCounts=counts;
      ++position;
    });
    for(const [id,value] of previous)totals.set(id,(totals.get(id)||0)+(position-(last.get(id)||0))*value);
    const ranked=[...totals].filter(([,value])=>value>0).sort((a,b)=>b[1]-a[1]||String(a[0]).localeCompare(String(b[0]))).map(([id])=>id);
    const endpoint=[...(endCounts||[])].filter(([,value])=>value>0).sort((a,b)=>b[1]-a[1]||String(a[0]).localeCompare(String(b[0]))).slice(0,limit).map(([id])=>id);
    const endpointShares=[...(endCounts||[])].filter(([,value])=>lastTotal>0&&value/lastTotal>=.05).map(([id])=>id);
    const included=new Set([...ranked.slice(0,limit),...endpoint,...endpointShares,...significant]);
    const shown=includeAll?[...ranked]:ranked.filter(id=>included.has(id));
    if(ranked.includes('unknown')&&!shown.includes('unknown'))shown.push('unknown');
    if(selectedActor&&ranked.includes(selectedActor)&&!shown.includes(selectedActor))shown.push(selectedActor);
    const projected=[];
    visit((point,counts,total)=>{
      const visible=new Map();let visibleTotal=0;
      for(const id of shown){const value=counts.get(id)||0;if(value)visible.set(id,value);visibleTotal+=value;}
      projected.push({date:point.date,revision:point.revision,kind:point.kind,committedDate:point.committedDate,lines:point.lines,counts:visible,total,other:Math.max(0,total-visibleTotal)});
    });
    return {points:projected,ranked,shown,hasOthers:ranked.some(id=>!shown.includes(id))};
  }
  // Upper-bound lookup preserves the last exact state at a colliding timestamp.
  // Earlier states at that timestamp remain accessible in the revision browser.
  function nearestPoint(points,time) {
    if(!points.length)return -1;
    let lo=0,hi=points.length;
    while(lo<hi){const mid=(lo+hi)>>>1;if(Date.parse(points[mid].date)<=time)lo=mid+1;else hi=mid;}
    if(!lo)return 0;if(lo===points.length)return lo-1;
    return time-Date.parse(points[lo-1].date)<=Date.parse(points[lo].date)-time?lo-1:lo;
  }
  const api={project,nearestPoint};
  if(typeof module!=='undefined'&&module.exports)module.exports=api;else root.AtlasTimeline=api;
})(globalThis);
