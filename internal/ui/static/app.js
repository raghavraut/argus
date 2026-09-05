/* Argus triage UI — vanilla JS, no build step. */
(function () {
  "use strict";
  const S = window.ARGUS;
  let campaign = S.campaign;
  let nodeIndex = {}; // mermaid token -> asset
  let rows = [];
  let sortKey = "score", sortDir = -1;

  const $ = (id) => document.getElementById(id);

  async function jget(path) {
    const r = await fetch(path);
    if (!r.ok) throw new Error(path + ": " + r.status);
    return r.json();
  }

  /* ---- campaigns ---- */
  async function loadCampaigns() {
    const list = await jget("/api/campaigns");
    const sel = $("campaign");
    sel.innerHTML = "";
    for (const c of list) {
      const o = document.createElement("option");
      o.value = c.campaign;
      o.textContent = `${c.campaign} (${c.results} results)`;
      if (c.campaign === campaign) o.selected = true;
      sel.appendChild(o);
    }
    sel.onchange = () => { campaign = sel.value; refresh(); };
  }

  function api(p) { return p + "?campaign=" + encodeURIComponent(campaign); }

  /* ---- graph ---- */
  async function loadGraph() {
    const g = await jget(api("/api/graph"));
    nodeIndex = g.index || {};
    const pre = $("mermaid-src");
    pre.textContent = g.mermaid;
    pre.removeAttribute("data-processed");
    if (window.mermaid) {
      try {
        mermaid.initialize({ securityLevel: "loose", startOnLoad: false });
        await mermaid.run({ nodes: [pre] });
      } catch (e) {
        $("graph-note").hidden = false;
      }
    } else {
      $("graph-note").hidden = false;
    }
    const st = await jget(api("/api/info"));
    $("stats").textContent = `${st.nodes} nodes · ${st.edges} edges · ${st.results} results`;
  }

  // Called by mermaid `click <token> onNodeClick` bindings.
  window.onNodeClick = async function (token) {
    const asset = nodeIndex[token];
    if (!asset) return;
    const d = await jget("/api/asset?campaign=" + encodeURIComponent(campaign) + "&id=" + encodeURIComponent(asset));
    $("panel").hidden = false;
    $("p-asset").textContent = d.result.asset;
    const sc = $("p-score");
    sc.textContent = d.result.score.toFixed(3);
    sc.className = d.result.score >= 0.6 ? "score-hi" : "score-mid";
    $("p-conf").textContent = d.result.confidence;
    $("p-rarity").textContent = d.result.rarity_index;
    $("p-title").textContent = `${d.status} ${d.title}`;
    $("p-tech").textContent = (d.tech || []).join(", ") || "—";
    $("p-rec").textContent = d.result.recommendation;
    $("p-evidence").innerHTML = "";
    for (const e of d.result.evidence || []) {
      const li = document.createElement("li");
      li.textContent = e;
      $("p-evidence").appendChild(li);
    }
    $("p-headers").textContent = Object.entries(d.headers || {})
      .map(([k, v]) => `${k}: ${v}`).join("\n") || "—";
    $("p-findings").innerHTML = "";
    for (const f of d.findings || []) {
      const li = document.createElement("li");
      li.textContent = `[${f.severity}] ${f.template_id} — ${f.matched || f.url}`;
      $("p-findings").appendChild(li);
    }
    if (!(d.findings || []).length) {
      const li = document.createElement("li");
      li.textContent = "none";
      $("p-findings").appendChild(li);
    }
  };
  $("panel-close").onclick = () => { $("panel").hidden = true; };

  /* ---- corpus table ---- */
  function inBand(r) { return r.score >= S.bandLo && r.score < S.bandHi; }

  function renderTable() {
    const q = $("q").value.toLowerCase();
    const bandOnly = $("band-only").checked;
    const tb = document.querySelector("#assets tbody");
    tb.innerHTML = "";
    const list = rows
      .filter((r) => !bandOnly || inBand(r))
      .filter((r) => !q || (r.asset + " " + r.title + " " + (r.tech || []).join(" ")).toLowerCase().includes(q))
      .sort((a, b) => {
        const va = a[sortKey], vb = b[sortKey];
        if (va < vb) return -1 * sortDir;
        if (va > vb) return 1 * sortDir;
        return 0;
      });
    for (const r of list) {
      const tr = document.createElement("tr");
      if (inBand(r)) tr.className = "band";
      const cells = [r.asset, r.score.toFixed(3), r.confidence, r.rarity,
        r.status, r.title, (r.tech || []).join(", "), r.recommendation, r.finding_count || 0];
      for (const c of cells) {
        const td = document.createElement("td");
        td.textContent = c;
        tr.appendChild(td);
      }
      tr.ondblclick = () => {
        document.querySelector("#tab-graph").click();
        window.onNodeClick(assetToToken(r.asset));
      };
      tb.appendChild(tr);
    }
  }

  function assetToToken(asset) {
    for (const [tok, a] of Object.entries(nodeIndex)) if (a === asset) return tok;
    return null;
  }

  async function loadAssets() {
    rows = await jget(api("/api/assets"));
    renderTable();
  }

  document.querySelectorAll("#assets th").forEach((th) => {
    th.onclick = () => {
      const k = th.dataset.k;
      if (sortKey === k) sortDir *= -1;
      else { sortKey = k; sortDir = k === "asset" || k === "title" ? 1 : -1; }
      renderTable();
    };
  });
  $("q").oninput = renderTable;
  $("band-only").onchange = renderTable;

  /* ---- tabs ---- */
  for (const name of ["graph", "corpus"]) {
    $("tab-" + name).onclick = () => {
      document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
      document.querySelectorAll(".view").forEach((v) => v.classList.remove("active"));
      $("tab-" + name).classList.add("active");
      $("view-" + name).classList.add("active");
    };
  }

  async function refresh() {
    await loadCampaigns();
    await Promise.all([loadGraph(), loadAssets()]);
  }
  refresh().catch((e) => { $("stats").textContent = "error: " + e.message; });
})();
