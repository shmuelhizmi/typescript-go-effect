package report

// reportCSS is the inline stylesheet for the HTML report. It is moved from the
// former perf/report_assets.go, with two additions for multi-page documents:
// the .page show/hide rules and the .navgrp sidebar group header. The treemap
// container is selected by class (.treemap) rather than id, since a document
// can contain several treemaps.
const reportCSS = `
*{box-sizing:border-box;margin:0;padding:0}
:root{
  --bg:#0a0c10;--bg2:#0e1219;--panel:#12161f;--panel2:#171c27;--line:#232b39;--line2:#2e3849;
  --fg:#eaeef6;--fg2:#c2cad8;--muted:#8893a6;--dim:#5a6577;
  --c-accent:#6ea8fe;--c-accent2:#9b87ff;
  --c-parse:#3fb950;--c-bind:#bc8cff;--c-check:#6ea8fe;--c-emit:#e3a008;
  --c-hot:#f0883e;--c-bad:#ff5c57;--c-ok:#3fb950;
  --mono:ui-monospace,"SF Mono",SFMono-Regular,Menlo,Consolas,monospace;
  --sans:-apple-system,BlinkMacSystemFont,"Segoe UI",Inter,Roboto,Helvetica,sans-serif;
  --r:12px;
}
html{-webkit-font-smoothing:antialiased;text-rendering:optimizeLegibility;scroll-behavior:smooth}
body{background:
  radial-gradient(1000px 520px at 100% -8%,rgba(155,135,255,.10),transparent 60%),
  radial-gradient(1000px 520px at -8% -4%,rgba(110,168,254,.09),transparent 58%),var(--bg);
  color:var(--fg);font-family:var(--sans);font-size:13.5px;line-height:1.5;letter-spacing:.1px;
  font-variant-numeric:tabular-nums}

/* shell: sticky sidebar + main */
.shell{display:flex;gap:30px;max-width:1260px;margin:0 auto;padding:30px 30px 72px}
.nav{position:sticky;top:30px;align-self:flex-start;width:210px;flex:none}
.main{flex:1;min-width:0}
section{scroll-margin-top:24px}
.brand{display:flex;align-items:center;gap:11px;padding:2px 4px 16px;border-bottom:1px solid var(--line)}
.logo{width:28px;height:28px;border-radius:8px;flex:none;
  background:linear-gradient(135deg,var(--c-accent),var(--c-accent2));box-shadow:0 4px 18px rgba(110,168,254,.35)}
.title{font-size:15px;font-weight:700;letter-spacing:-.2px}
.proj{font-family:var(--mono);font-size:10.5px;color:var(--muted);margin-top:1px}
.navlist{list-style:none;margin-top:14px;display:flex;flex-direction:column;gap:2px}
.navgrp{margin:12px 0 3px;padding:0 11px;color:var(--dim);font-size:9.5px;font-weight:700;
  text-transform:uppercase;letter-spacing:.08em}
.navlist a{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:7px 11px;
  border-radius:8px;color:var(--muted);text-decoration:none;font-size:13px;font-weight:500;
  border-left:2px solid transparent;transition:background .12s,color .12s}
.navlist a:hover{background:var(--panel);color:var(--fg2)}
.navlist a.active{background:var(--panel2);color:var(--fg);border-left-color:var(--c-accent)}
.navlist .badge{font-family:var(--mono);font-size:10px;font-weight:700;background:rgba(255,92,87,.16);
  color:var(--c-bad);border-radius:5px;padding:1px 6px}
@media(max-width:880px){.shell{flex-direction:column;gap:18px;padding:22px 18px 60px}
  .nav{position:static;width:auto}.brand{border:none;padding-bottom:8px}
  .navlist{flex-direction:row;flex-wrap:wrap;margin-top:6px}.navlist a{border-left:none}
  .navgrp{width:100%;margin:6px 0 0}
  .navlist a.active{border-left:none;box-shadow:inset 0 -2px 0 var(--c-accent)}}

/* pages: one visible at a time */
.page{display:none}
.page.active{display:block}

/* hero */
.hero{display:flex;align-items:center;justify-content:space-between;gap:20px;flex-wrap:wrap;margin-bottom:18px}
.eyebrow{color:var(--dim);font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.08em}
.hero-time{display:flex;align-items:baseline;gap:5px;margin-top:2px}
.hero-time .num{font-family:var(--mono);font-size:44px;font-weight:700;letter-spacing:-2px;
  background:linear-gradient(135deg,#fff,#9fb4d6);-webkit-background-clip:text;background-clip:text;-webkit-text-fill-color:transparent}
.hero-time .unit{font-size:17px;color:var(--muted);font-weight:600}
.verdict{display:flex;align-items:center;gap:11px;background:var(--panel);border:1px solid var(--line);
  border-radius:var(--r);padding:12px 15px;font-size:13.5px;color:var(--fg2);max-width:460px}
.tag{font-family:var(--mono);font-size:10.5px;font-weight:700;text-transform:uppercase;letter-spacing:.06em;
  border-radius:6px;padding:3px 9px;color:#0a0c10;flex:none}
.tag-ok{background:var(--c-ok)}.tag-warn{background:var(--c-emit)}

/* kpis */
.kpis{display:grid;grid-template-columns:repeat(5,1fr);gap:11px;margin-bottom:18px}
@media(max-width:820px){.kpis{grid-template-columns:repeat(3,1fr)}}
.kpi{background:linear-gradient(180deg,var(--panel2),var(--panel));border:1px solid var(--line);
  border-radius:var(--r);padding:13px 15px;position:relative;overflow:hidden}
.kpi::before{content:"";position:absolute;inset:0 0 auto 0;height:2px;
  background:linear-gradient(90deg,transparent,var(--c-accent),transparent);opacity:.5}
.kpi.warn::before{background:linear-gradient(90deg,transparent,var(--c-emit),transparent);opacity:.8}
.kk{color:var(--dim);font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.07em}
.kv{font-family:var(--mono);font-size:22px;font-weight:600;margin-top:4px;letter-spacing:-.5px}
.kpi.warn .kv{color:var(--c-emit)}
.ku{font-size:12px;color:var(--muted);font-weight:400}

/* phase bar */
.phase{margin-bottom:26px}
.bar{display:flex;height:28px;border-radius:9px;overflow:hidden;border:1px solid var(--line);
  background:var(--bg2);box-shadow:inset 0 1px 3px rgba(0,0,0,.45)}
.bar span{display:flex;align-items:center;justify-content:center;font-family:var(--mono);font-size:11px;
  color:rgba(10,12,16,.92);font-weight:700;min-width:0;overflow:hidden;white-space:nowrap}
.legend{display:flex;flex-wrap:wrap;gap:16px;margin-top:9px;color:var(--muted);font-family:var(--mono);font-size:11.5px}
.legend .li i{display:inline-block;width:9px;height:9px;border-radius:3px;margin-right:6px;vertical-align:middle}
.legend b{color:var(--fg)}

/* blocks */
.block{margin-top:30px}
.head{margin-bottom:13px}
h2{font-size:16px;font-weight:700;letter-spacing:-.1px;display:flex;align-items:center;gap:10px}
h2 .cnt{font-family:var(--mono);font-size:11px;font-weight:600;color:var(--dim);
  background:var(--panel);border:1px solid var(--line);border-radius:6px;padding:1px 8px}
h2.crit{color:var(--c-bad)}
.sub{color:var(--muted);font-size:12.5px;margin-top:4px}

/* issues */
.issues{background:linear-gradient(180deg,rgba(255,92,87,.06),rgba(255,92,87,.02));
  border:1px solid rgba(255,92,87,.25);border-radius:16px;padding:20px 22px}
.issues .sub{max-width:680px}
.issue-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:11px;margin-top:6px}
.issue{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:12px 14px}
.issue-n{font-family:var(--mono);font-size:24px;font-weight:700;color:var(--c-bad);line-height:1}
.issue-name{font-family:var(--mono);font-size:12px;color:var(--fg);margin:6px 0 4px;word-break:break-word}
.issue-why{font-size:11.5px;color:var(--muted);line-height:1.45}

/* treemap */
.treemap{position:relative;width:100%;height:560px;background:var(--bg2);border:1px solid var(--line);
  border-radius:var(--r);overflow:hidden}
.tm-cell{position:absolute;overflow:hidden;border:1px solid rgba(10,12,16,.55);border-radius:3px;
  cursor:default;transition:filter .1s,outline-color .1s;outline:1px solid transparent}
.tm-cell:hover{filter:brightness(1.18);outline-color:rgba(255,255,255,.5);z-index:5}
.tm-lbl{position:absolute;left:5px;top:3px;right:4px;font-family:var(--mono);font-size:10.5px;
  font-weight:600;color:rgba(255,255,255,.95);text-shadow:0 1px 2px rgba(0,0,0,.6);
  white-space:nowrap;overflow:hidden;text-overflow:ellipsis;pointer-events:none}
.tm-grp{position:absolute;border:1px solid var(--line2);border-radius:5px;background:rgba(255,255,255,.015)}
.tm-grp>.tm-hdr{position:absolute;left:0;top:0;right:0;height:15px;line-height:15px;padding:0 6px;
  font-family:var(--mono);font-size:9.5px;font-weight:700;letter-spacing:.02em;color:var(--fg2);
  background:rgba(255,255,255,.04);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;
  border-bottom:1px solid var(--line);pointer-events:none}
.tm-scale{display:flex;align-items:center;gap:8px;margin-top:9px;color:var(--muted);font-size:11px;
  font-family:var(--mono);justify-content:flex-end}
.tm-scale .grad{width:160px;height:8px;border-radius:5px;
  background:linear-gradient(90deg,rgb(35,55,84),rgb(31,111,235),rgb(227,160,8),rgb(248,81,73))}
#tt{position:fixed;z-index:50;pointer-events:none;display:none;max-width:340px;
  background:#05070b;border:1px solid var(--line2);border-radius:9px;padding:9px 12px;
  box-shadow:0 10px 34px rgba(0,0,0,.6);font-size:12px}
#tt .p{font-family:var(--mono);font-size:11px;color:var(--c-accent);word-break:break-all;margin-bottom:5px}
#tt .row{display:flex;justify-content:space-between;gap:20px;color:var(--fg2)}
#tt .row b{font-family:var(--mono);color:var(--fg)}

/* tables */
.tbl{background:var(--panel);border:1px solid var(--line);border-radius:var(--r);overflow:hidden}
table{width:100%;border-collapse:collapse}
th{text-align:left;padding:9px 14px;font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.07em;
  color:var(--dim);background:var(--bg2);border-bottom:1px solid var(--line)}
td{padding:8px 14px;border-bottom:1px solid rgba(35,43,57,.55);font-size:12.5px}
tr:last-child td{border-bottom:none}
tbody tr{transition:background .1s}tbody tr:hover{background:rgba(110,168,254,.06)}
.r{text-align:right}.mono{font-family:var(--mono)}.dim{color:var(--muted)}
td.path{font-family:var(--mono);font-size:12px}
td.sym{font-family:var(--mono);color:var(--c-accent)}
.meter{position:relative;display:inline-block;min-width:108px;text-align:right}
.meter .fill{position:absolute;inset:0 0 0 auto;border-radius:5px;opacity:.20}
.meter .mt{position:relative;font-family:var(--mono);padding:0 6px}
.empty{background:var(--panel);border:1px solid var(--line);border-radius:var(--r);padding:14px 16px;
  color:var(--muted);font-size:12.5px}.empty .ok{color:var(--c-ok);font-weight:700}
footer{margin-top:44px;padding-top:18px;border-top:1px solid var(--line);color:var(--dim);font-size:11.5px}
footer b{color:var(--muted)}
`

// treemapLibJS defines tsRenderTreemap(rootId, TREE): a squarified treemap with
// a heat scale and hover tooltip, callable per container. Refactored from the
// former perf treemapJS single-page IIFE so a document can host several
// treemaps and so a treemap on a hidden page is (re)drawn when its page becomes
// visible (it reads clientWidth/Height, which is 0 while hidden). Cells are
// sized by node.v and colored by node.h (0..1) when present, else node.v scaled
// to the largest leaf. Node text/tooltips use textContent (no innerHTML), so
// untrusted paths/labels cannot inject markup.
const treemapLibJS = `
window.__tsTreemaps = window.__tsTreemaps || [];
function tsEnsureTT(){
  if(window.__tsTT) return window.__tsTT;
  var tt=document.createElement('div'); tt.id='tt';
  (document.body||document.documentElement).appendChild(tt);
  window.__tsTT=tt; return tt;
}
function tsRenderTreemap(rootId, TREE){
  var MINW=48,MINH=26,HDR=15,PAD=2,MAXD=7;
  var maxLeaf=0.0001;
  (function fm(n){ if(!n.ch||!n.ch.length){ if(n.v>maxLeaf)maxLeaf=n.v; } else n.ch.forEach(fm); })(TREE);
  function val(n){ return Math.max(n.v,0.0001); }
  function heatOf(n){ return (n.h!=null)? n.h : (maxLeaf>0? n.v/maxLeaf : 0); }
  function heat(t){
    t=Math.max(0,Math.min(1,t));
    var stops=[[35,55,84],[31,111,235],[227,160,8],[248,81,73]], pos=[0,.45,.78,1];
    for(var i=1;i<pos.length;i++){ if(t<=pos[i]){
      var u=(t-pos[i-1])/(pos[i]-pos[i-1]),a=stops[i-1],c=stops[i];
      return 'rgb('+Math.round(a[0]+(c[0]-a[0])*u)+','+Math.round(a[1]+(c[1]-a[1])*u)+','+Math.round(a[2]+(c[2]-a[2])*u)+')';
    }}
    return 'rgb(248,81,73)';
  }
  function worst(row,len){
    var s=0,mx=-1e18,mn=1e18;
    for(var i=0;i<row.length;i++){var v=row[i].v;s+=v;if(v>mx)mx=v;if(v<mn)mn=v;}
    var l2=len*len,s2=s*s; return Math.max(l2*mx/s2, s2/(l2*mn));
  }
  function squarify(items,x,y,w,h,out){
    var rect={x:x,y:y,w:w,h:h},row=[],i=0;
    function lay(row,rc){
      var s=0;for(var k=0;k<row.length;k++)s+=row[k].v;
      if(rc.w>=rc.h){var dw=s/rc.h,cy=rc.y;
        for(var k=0;k<row.length;k++){var dh=row[k].v/dw;out.push({n:row[k].n,x:rc.x,y:cy,w:dw,h:dh});cy+=dh;}
        return {x:rc.x+dw,y:rc.y,w:rc.w-dw,h:rc.h};}
      var dh=s/rc.w,cx=rc.x;
      for(var k=0;k<row.length;k++){var dw2=row[k].v/dh;out.push({n:row[k].n,x:cx,y:rc.y,w:dw2,h:dh});cx+=dw2;}
      return {x:rc.x,y:rc.y+dh,w:rc.w,h:rc.h-dh};
    }
    while(i<items.length){
      var len=Math.min(rect.w,rect.h);
      if(!row.length){row.push(items[i]);i++;continue;}
      if(worst(row,len)>=worst(row.concat([items[i]]),len)){row.push(items[i]);i++;}
      else{rect=lay(row,rect);row=[];}
    }
    if(row.length)lay(row,rect);
  }
  function el(cls,x,y,w,h){var d=document.createElement('div');d.className=cls;
    d.style.left=x+'px';d.style.top=y+'px';d.style.width=Math.max(0,w)+'px';d.style.height=Math.max(0,h)+'px';return d;}
  function render(node,x,y,w,h,depth,parent){
    var leaf=!node.ch||!node.ch.length;
    if(leaf||w<MINW||h<MINH||depth>MAXD){
      var c=el('tm-cell',x,y,w,h); c.style.background=heat(heatOf(node));
      c._node=node; c._agg=!leaf;
      if(w>40&&h>15){var l=document.createElement('div');l.className='tm-lbl';l.textContent=node.n;c.appendChild(l);}
      parent.appendChild(c); return;
    }
    var g=el('tm-grp',x,y,w,h); parent.appendChild(g);
    var oy=y;
    if(depth>0&&h>HDR+12&&w>54){var hd=document.createElement('div');hd.className='tm-hdr';hd.textContent=node.n;g.appendChild(hd);oy=HDR;}
    else oy=0;
    var ix=PAD,iy=oy+PAD,iw=w-2*PAD,ih=h-oy-2*PAD;
    if(iw<MINW||ih<MINH){var c2=el('tm-cell',x,y,w,h);c2.style.background=heat(heatOf(node));c2._node=node;c2._agg=true;
      if(w>40&&h>15){var l2=document.createElement('div');l2.className='tm-lbl';l2.textContent=node.n;c2.appendChild(l2);}
      parent.appendChild(c2);g.remove();return;}
    var sum=0;node.ch.forEach(function(c){sum+=val(c);});
    var scale=(iw*ih)/sum;
    var items=node.ch.map(function(c){return {n:c,v:val(c)*scale};});
    var rects=[];squarify(items,ix,iy,iw,ih,rects);
    rects.forEach(function(rc){render(rc.n,rc.x,rc.y,rc.w,rc.h,depth+1,g);});
  }
  var rootEl=document.getElementById(rootId);
  if(!rootEl) return;
  function draw(){
    var W=rootEl.clientWidth,H=rootEl.clientHeight;
    if(!W||!H) return; // hidden page — will be drawn when the page is shown
    rootEl.innerHTML='';
    if(!TREE.ch||!TREE.ch.length){rootEl.innerHTML='<div style="padding:18px;color:#8893a6">No data.</div>';return;}
    var sum=0;TREE.ch.forEach(function(c){sum+=val(c);});
    var scale=(W*H)/sum;
    var items=TREE.ch.map(function(c){return {n:c,v:val(c)*scale};});
    var rects=[];squarify(items,0,0,W,H,rects);
    rects.forEach(function(rc){render(rc.n,rc.x,rc.y,rc.w,rc.h,1,rootEl);});
  }
  rootEl.addEventListener('mousemove',function(e){
    var tt=tsEnsureTT();
    var t=e.target; while(t&&t!==rootEl&&!t._node)t=t.parentNode;
    if(!t||!t._node){tt.style.display='none';return;}
    var n=t._node;
    tt.innerHTML='';
    var pEl=document.createElement('div'); pEl.className='p'; pEl.textContent=(n.path||n.n); tt.appendChild(pEl);
    (n.tip||[]).forEach(function(kv){
      var row=document.createElement('div'); row.className='row';
      var s=document.createElement('span'); s.textContent=kv.k;
      var bEl=document.createElement('b'); bEl.textContent=kv.v;
      row.appendChild(s); row.appendChild(bEl); tt.appendChild(row);
    });
    if(t._agg){var ar=document.createElement('div');ar.className='row';ar.style.color='#8893a6';ar.style.marginTop='4px';ar.textContent='folder aggregate';tt.appendChild(ar);}
    tt.style.display='block';
    var px=e.clientX+14,py=e.clientY+14;
    if(px+tt.offsetWidth>innerWidth)px=e.clientX-tt.offsetWidth-14;
    if(py+tt.offsetHeight>innerHeight)py=e.clientY-tt.offsetHeight-14;
    tt.style.left=px+'px';tt.style.top=py+'px';
  });
  rootEl.addEventListener('mouseleave',function(){ if(window.__tsTT) window.__tsTT.style.display='none'; });
  window.__tsTreemaps.push({rootId:rootId, draw:draw});
  draw();
}
`

// pageNavJS routes the visible page from location.hash and toggles the active
// sidebar link, replacing the former single-page IntersectionObserver
// scroll-spy. When a page becomes active it (re)draws any treemaps inside it
// (their containers had zero size while hidden), and it redraws visible
// treemaps on resize.
const pageNavJS = `
(function(){
  var links=[].slice.call(document.querySelectorAll('.navlist a[data-page]'));
  var pages=[].slice.call(document.querySelectorAll('main section.page'));
  function redrawVisible(){
    (window.__tsTreemaps||[]).forEach(function(t){
      var el=document.getElementById(t.rootId);
      if(el && el.offsetParent!==null) t.draw();
    });
  }
  function show(id){
    var found=false;
    pages.forEach(function(p){ var on=p.id===id; p.classList.toggle('active',on); if(on)found=true; });
    if(!found && pages.length){ pages[0].classList.add('active'); id=pages[0].id; }
    links.forEach(function(a){ a.classList.toggle('active', a.getAttribute('data-page')===id); });
    redrawVisible();
  }
  function fromHash(){ return (location.hash||'').replace(/^#/,''); }
  addEventListener('hashchange',function(){ show(fromHash()); });
  var rt; addEventListener('resize',function(){ clearTimeout(rt); rt=setTimeout(redrawVisible,120); });
  show(fromHash());
})();
`
