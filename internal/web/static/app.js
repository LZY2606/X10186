"use strict";
let VM = null;
let currentDoc = null;
let selected = null; // {num,gen}
let selectedCand = null;

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c]));
const originLabel = {declared:"按声明", heuristic:"启发式", objstm:"对象流", free:"free"};
const originCls = {declared:"declared", heuristic:"heuristic", objstm:"objstm", free:"free"};

async function api(path, opts){
  const res = await fetch(path, opts);
  if(!res.ok){
    let msg = res.statusText;
    try{ const j = await res.json(); msg = j.error || msg; }catch(e){}
    throw new Error(msg);
  }
  return res.json();
}

async function loadDocs(){
  const docs = await api("/api/documents");
  const el = $("docList");
  if(!docs.length){ el.innerHTML = '<div class="muted">尚无文档。</div>'; return; }
  el.innerHTML = docs.map(d => `
    <div class="docitem ${currentDoc===d.id?'active':''}" data-id="${esc(d.id)}">
      <div class="n">${esc(d.name)}</div>
      <div class="m">${d.size} B · ${esc(d.sha256.slice(0,12))} · ${esc((d.importedAt||'').replace('T',' ').slice(0,19))}</div>
    </div>`).join("");
  el.querySelectorAll(".docitem").forEach(n => n.onclick = () => openDoc(n.dataset.id));
}

async function openDoc(id){
  currentDoc = id;
  VM = await api(`/api/documents/${id}`);
  selected = null; selectedCand = null;
  $("docmeta").textContent = `${VM.name} · ${VM.size} 字节 · ${VM.revisions.length} 个修订`;
  $("exportBtn").disabled = false;
  renderAll();
  loadDocs();
}

function renderAll(){ renderRevisions(); renderDiagnostics(); renderTrailing(); renderObjects(); renderDetailEmpty(); }

function renderRevisions(){
  const el = $("revisions");
  if(!VM.revisions.length){ el.innerHTML = '<div class="bad">没有可解析的 xref 链（见诊断）。</div>'; return; }
  el.innerHTML = VM.revisions.map(r => {
    const root = r.trailer.Root || "";
    const prev = r.hasPrev ? r.Prev : "—（链首）";
    return `<div class="rev">
      <span class="rid">修订 #${r.index}</span>
      <span class="kind">${r.xrefKind==='stream'?'xref stream':'xref table'}</span>
      <table>
        <tr><td>xref 偏移</td><td>${r.xrefOffset}</td></tr>
        <tr><td>xref 字节</td><td>${r.xrefStart}..${r.xrefEnd}</td></tr>
        <tr><td>段落字节</td><td>${r.segmentStart}..${r.segmentEnd}</td></tr>
        <tr><td>条目数</td><td>${r.entries.length}</td></tr>
        <tr><td>/Prev</td><td>${prev}</td></tr>
        <tr><td>/Root</td><td>${esc(root)}</td></tr>
      </table>
    </div>`;
  }).join("");
}

function sevName(s){ return {error:"错误",warning:"警告",info:"信息"}[s] || s; }
function renderDiagnostics(){
  const el = $("diagnostics");
  if(!VM.diagnostics.length){ el.innerHTML = '<div class="ok">未发现异常。</div>'; return; }
  el.innerHTML = VM.diagnostics.map(d => `
    <div class="diag ${d.severity}">
      <div><b>${sevName(d.severity)}</b> <span class="code">${esc(d.code)}${d.revision>=0?' · 修订#'+d.revision:''}${d.offset>=0?' · 偏移 '+d.offset:''}</span></div>
      <div>${esc(d.message)}</div>
      <div class="muted">证据：${esc(d.evidence)}</div>
    </div>`).join("");
}

function renderTrailing(){
  const el = $("trailing");
  if(!VM.trailingData.length){ el.innerHTML = '<div class="ok">无尾随数据。</div>'; return; }
  el.innerHTML = VM.trailingData.map(t => `
    <div class="trail">
      <div><b>${t.start}..${t.end}</b>（${t.end-t.start} 字节）</div>
      <div>ASCII：${esc(t.ascii)}</div>
      <pre style="max-height:110px">${esc(t.hex)}</pre>
    </div>`).join("");
}

function statusPill(c){
  return `<span class="pill ${c.status}">${ {confirmed:'已确认',rejected:'已拒绝',pending:'待确认'}[c.status] || c.status }</span>`;
}

function renderObjects(){
  const el = $("objectList");
  if(!VM.objects.length){ el.innerHTML = '<div class="muted">无对象。</div>'; return; }
  el.innerHTML = VM.objects.map(o => {
    const cls = selected && selected.num===o.num && selected.gen===o.gen ? "objrow sel" : "objrow";
    const vs = o.versions.filter(v=>v.origin!=='free');
    const origins = [...new Set(o.versions.map(v=>v.origin))].map(o2 =>
      `<span class="pill ${originCls[o2]||''}">${originLabel[o2]||o2}</span>`).join(" ");
    const revs = [...new Set(o.versions.map(v=>v.revision))].sort((a,b)=>a-b).map(r=>"#"+r).join(",");
    const flag = o.contested ? '<span class="wn" title="候选竞争，需人工裁决">⚔ 竞争</span>' : "";
    return `<div class="${cls}" data-num="${o.num}" data-gen="${o.gen}">
      <div><b>${o.num}</b></div><div class="muted">g${o.gen}</div>
      <div>${origins} ${flag}<div class="muted" style="font-size:10px">修订 ${revs} · ${vs.length} 个版本</div></div>
      <div style="text-align:right">${o.contested?'':''}</div>
    </div>`;
  }).join("");
  el.querySelectorAll(".objrow").forEach(n => n.onclick = () => {
    selected = {num:+n.dataset.num, gen:+n.dataset.gen};
    renderObjects(); renderDetail();
  });
}

function renderDetailEmpty(){
  $("detail").style.display = "none";
  $("detailEmpty").style.display = "block";
}

async function renderDetail(){
  if(!selected) return;
  const d = await api(`/api/documents/${currentDoc}/objects/${selected.num}/${selected.gen}`);
  $("detailEmpty").style.display = "none";
  $("detail").style.display = "block";
  $("detailTitle").textContent = `对象 ${d.num} 代次 ${d.gen}${d.contested ? " · ⚔ 候选竞争" : ""}`;
  renderBranchNotice(d);
  renderRefBy(d);
  renderCandidates(d);
  drawGraph(d);
  renderHexTabs(d);
}

function decisionMap(){
  const m = {};
  (VM.decisions||[]).forEach(x => m[x.candidateId] = x.action);
  return m;
}

function renderBranchNotice(d){
  const n = $("branchNote");
  if(!d.contested){ n.style.display="none"; return; }
  const dec = decisionMap();
  const chosen = d.versions.find(v=>dec[v.id]==='confirmed');
  if(chosen){
    n.style.display="block";
    n.innerHTML = `该对象存在竞争候选；当前采用你确认的版本 <b>${chosen.id}</b>（修订 #${chosen.revision}，${originLabel[chosen.origin]}）。其他旧分支仍保留并可复现，不会按最晚偏移自动改判。`;
  }else{
    n.style.display="block";
    n.textContent = "两个候选竞争同一对象版本：系统不会自动选择，请在下方核对证据后人工确认，确认将形成解释分支。";
  }
}

function renderRefBy(d){
  const el = $("refBy");
  if(!d.referencedBy.length){ el.innerHTML = '<div class="muted">没有对象引用它。</div>'; return; }
  el.innerHTML = '<h2 style="font-size:12px;color:var(--muted);margin:0 0 4px">被哪些对象引用</h2>' +
    d.referencedBy.map(r=>`<div class="edge">← 对象 ${r.fromNum}（修订 #${r.fromRev}）· 键 <b>${esc(r.context)}</b> · 偏移 ${r.fromOffset}</div>`).join("");
}

function kv(k,v){ return `<tr><td class="k">${esc(k)}</td><td>${esc(v)}</td></tr>`; }

function renderCandidates(d){
  const dec = decisionMap();
  const el = $("candidates");
  el.innerHTML = d.versions.map(c => {
    const st = dec[c.id] || c.status;
    const win = d.contested && st==='confirmed';
    const lose = d.contested && st==='rejected';
    const value = c.value || {};
    let dictRows = "";
    if(value.dict){ dictRows = Object.entries(value.dict).map(([k,v])=>kv("/"+k,v)).join(""); }
    const range = c.origin==='objstm'
      ? `宿主对象 ${c.hostNum}，文件字节 ${c.hostStart}..${c.hostEnd}；展开内偏移 ${c.byteStart}..${c.byteEnd}`
      : (c.byteStart>=0 ? `${c.byteStart}..${c.byteEnd}` : "无（偏移未能定位）");
    const verify = c.origin==='free' ? "" :
      (c.verified ? '<span class="ok">✓ 长度/filter/边界已核验</span>' : '<span class="wn">未核验（不展开）</span>');
    const actions = c.origin==='free' ? "" : `
      <div style="margin-top:8px;display:flex;gap:6px;flex-wrap:wrap">
        <button data-act="confirmed" data-cid="${c.id}">确认此版本</button>
        <button data-act="rejected" data-cid="${c.id}">拒绝</button>
        ${d.contested ? `<button class="primary" data-branch="1" data-cid="${c.id}">据此形成解释分支</button>` : ""}
      </div>`;
    return `<div class="cand ${win?'win':''} ${lose?'lose':''}" data-cid="${c.id}">
      <h3><span class="pill ${originCls[c.origin]}">${originLabel[c.origin]||c.origin}</span>
        ${statusPill({status:st})}
        修订 #${c.revision} ${c.contested?'· ⚔':''} ${win?'· ✅ 采用':''}
      </h3>
      <table class="kvs">
        ${kv("候选 ID", c.id)}
        ${kv("原始字节范围", range)}
        ${c.origin!=='objstm' && c.origin!=='free' ? kv("声明偏移", c.offset + (c.headerOK ? "（头部吻合）" : "（头部不吻合/空白）")) : ""}
        ${kv("校验", verify)}
        ${kv("解析类型", value.type || "—")}
        ${dictRows}
        ${value.streamLen!=null ? kv("流长度", value.streamLen + " 字节 · filter: "+((value.filters||[]).join(",")||"无")) : ""}
        ${value.preview ? kv("内容预览", value.preview.slice(0,300)) : ""}
        ${kv("证据", c.evidence)}
      </table>
      ${actions}
    </div>`;
  }).join("");

  el.querySelectorAll("button[data-act]").forEach(b => b.onclick = async () => {
    try{
      await api(`/api/documents/${currentDoc}/decide`, {method:"POST",headers:{"Content-Type":"application/json"},
        body: JSON.stringify({candidateId:b.dataset.cid, action:b.dataset.act, note:"人工确认"})});
      await openDoc(currentDoc); await renderDetail();
    }catch(e){ alert(e.message); }
  });
  el.querySelectorAll("button[data-branch]").forEach(b => b.onclick = async () => {
    const label = prompt("给这个解释分支命名：", `分支-obj${d.num}-g${d.gen}`);
    if(label===null) return;
    try{
      await api(`/api/documents/${currentDoc}/branches`, {method:"POST",headers:{"Content-Type":"application/json"},
        body: JSON.stringify({num:d.num, gen:d.gen, chosenCandidateId:b.dataset.cid, label})});
      await openDoc(currentDoc); await renderDetail();
    }catch(e){ alert(e.message); }
  });
}

function renderHexTabs(d){
  const tabs = $("hexTabs");
  tabs.innerHTML = d.versions.filter(c=>c.origin!=='free').map((c,i)=>
    `<button data-i="${i}" class="${i===0?'on':''}">${originLabel[c.origin]} #${c.revision} ${c.id.slice(0,8)}</button>`).join("");
  const list = d.versions.filter(c=>c.origin!=='free');
  const show = async (i) => {
    const c = list[i];
    tabs.querySelectorAll("button").forEach((b,j)=>b.classList.toggle("on",j===i));
    try{
      const hb = await api(`/api/documents/${currentDoc}/candidates/${c.id}/bytes`);
      $("hexView").textContent =
        `# ${hb.note||""}\n# 原始文件字节（长度 ${hb.length}）\n` + hb.originalHex +
        (hb.expandedHex ? `\n\n# 展开后的对象流内容（${hb.expandedLen} 字节，受资源限额保护）\n`+hb.expandedHex : "");
    }catch(e){ $("hexView").textContent = "无法取字节："+e.message; }
  };
  tabs.querySelectorAll("button").forEach(b => b.onclick = ()=>show(+b.dataset.i));
  if(list.length) show(0); else $("hexView").textContent = "free 条目无字节。";
}

function drawGraph(d){
  const svg = $("graph");
  svg.innerHTML = "";
  const W = svg.clientWidth || 340, H = 260;
  const cx = W/2, cy = H/2;
  const refs = d.referencedBy;
  const nodes = [{id:`obj ${d.num}\ng${d.gen}`, x:cx, y:cy, me:true}];
  const n = Math.max(refs.length,1);
  refs.forEach((r,i)=>{
    const ang = -Math.PI/2 + (i*2*Math.PI)/n;
    nodes.push({id:`obj ${r.fromNum}\n#${r.fromRev}`, x:cx+Math.cos(ang)*92, y:cy+Math.sin(ang)*92, me:false});
  });
  const NS = "http://www.w3.org/2000/svg";
  nodes.slice(1).forEach((node,i)=>{
    const ln = document.createElementNS(NS,"line");
    ln.setAttribute("x1",node.x); ln.setAttribute("y1",node.y);
    ln.setAttribute("x2",cx); ln.setAttribute("y2",cy);
    ln.setAttribute("stroke","#2d4a63"); ln.setAttribute("marker-end","url(#arr)");
    svg.appendChild(ln);
  });
  let defs = document.createElementNS(NS,"defs");
  defs.innerHTML = `<marker id="arr" markerWidth="8" markerHeight="8" refX="6" refY="3" orient="auto">
    <path d="M0,0 L6,3 L0,6 Z" fill="#4cc2ff"/></marker>`;
  svg.appendChild(defs);
  nodes.forEach((node,i)=>{
    const g = document.createElementNS(NS,"g");
    const c = document.createElementNS(NS,"circle");
    c.setAttribute("cx",node.x); c.setAttribute("cy",node.y); c.setAttribute("r",node.me?26:18);
    c.setAttribute("fill", node.me ? "#17324a" : "#1b222d");
    c.setAttribute("stroke", node.me ? "#4cc2ff" : "#39536b");
    g.appendChild(c);
    const t = document.createElementNS(NS,"text");
    t.setAttribute("x",node.x); t.setAttribute("y",node.y+4);
    t.setAttribute("text-anchor","middle"); t.setAttribute("fill","#e6edf3"); t.setAttribute("font-size","9");
    t.textContent = node.me ? `${d.num}:${d.gen}` : refs[i-1]?.fromNum;
    g.appendChild(t);
    svg.appendChild(g);
  });
  if(!refs.length){
    const t = document.createElementNS(NS,"text");
    t.setAttribute("x",12); t.setAttribute("y",H-12); t.setAttribute("fill","#8b98a8"); t.setAttribute("font-size","11");
    t.textContent = "该对象未被引用（孤立/根候选）。";
    svg.appendChild(t);
  }
}

$("uploadBtn").onclick = async () => {
  const f = $("file").files[0];
  if(!f){ alert("先选择一个 PDF 文件"); return; }
  const fd = new FormData(); fd.append("file", f);
  try{
    const r = await fetch("/api/documents",{method:"POST",body:fd}).then(x=>x.json());
    if(r.error) throw new Error(r.error);
    await loadDocs(); await openDoc(r.id);
  }catch(e){ alert("导入失败："+e.message); }
};

$("exportBtn").onclick = () => {
  if(!currentDoc) return;
  window.location = `/api/documents/${currentDoc}/export`;
};

loadDocs();
