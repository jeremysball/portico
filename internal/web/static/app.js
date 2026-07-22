function displayName(s) {
  return s.nameOverride || s.title || "Not Found";
}

function effectiveCategory(s) {
  return s.categoryOverride || s.category || "Other";
}

function initials(name) {
  return name.slice(0, 2).toUpperCase();
}

// Uptime bar: a fixed-width row of buckets spanning the same 7-day window
// the backend retains history for (see historyRetention in registry.go),
// always rendered on the tile — no click needed to see it.
const UPTIME_BUCKETS = 28;
const UPTIME_WINDOW_MS = 7 * 24 * 60 * 60 * 1000;
const UPTIME_BUCKET_MS = UPTIME_WINDOW_MS / UPTIME_BUCKETS;

// Turns a service's history (transition timestamps, already sorted
// ascending by the backend) into one state per bucket: false if any offline
// moment touched the bucket (outages shorter than a bucket must still show,
// so offline always wins ties within a bucket), true if only online moments
// touched it, null if the window predates any recorded history.
function computeUptimeBars(history, now) {
  const windowStart = now - UPTIME_WINDOW_MS;
  const bars = new Array(UPTIME_BUCKETS).fill(null);
  if (history.length === 0) return bars;

  for (let i = 0; i < history.length; i++) {
    const segStart = new Date(history[i].time).getTime();
    const segEnd = i + 1 < history.length ? new Date(history[i + 1].time).getTime() : now;
    const online = history[i].online;

    const start = Math.max(segStart, windowStart);
    const end = Math.min(segEnd, now);
    if (end <= start) continue;

    const firstBucket = Math.max(0, Math.floor((start - windowStart) / UPTIME_BUCKET_MS));
    const lastBucket = Math.min(UPTIME_BUCKETS - 1, Math.ceil((end - windowStart) / UPTIME_BUCKET_MS) - 1);
    for (let b = firstBucket; b <= lastBucket; b++) {
      if (bars[b] === false) continue; // offline already wins this bucket
      bars[b] = online === false ? false : true;
    }
  }
  return bars;
}

function uptimeSummary(bars) {
  const known = bars.filter((b) => b !== null);
  if (known.length === 0) return "No history yet";
  const onlinePct = Math.round((known.filter(Boolean).length / known.length) * 100);
  return `${onlinePct}% uptime (7d)`;
}

function groupBy(services) {
  const groups = new Map();
  for (const s of services) {
    if (s.hidden) continue;
    const cat = effectiveCategory(s);
    if (!groups.has(cat)) groups.set(cat, []);
    groups.get(cat).push(s);
  }
  return groups;
}

// Rendered elements are kept alive across refreshes and patched in place
// instead of being torn down and rebuilt, so an update to one service
// doesn't flicker or reshuffle every other tile.
const sectionEls = new Map(); // category -> { section, h2, grid }
const tileEls = new Map(); // service id -> tile refs

function buildTile() {
  const a = document.createElement("a");
  a.className = "tile";
  a.target = "_blank";
  a.rel = "noopener noreferrer";

  const dot = document.createElement("span");
  dot.className = "dot";
  a.appendChild(dot);

  const icon = document.createElement("div");
  icon.className = "icon";
  a.appendChild(icon);

  const name = document.createElement("div");
  name.className = "name";
  a.appendChild(name);

  const meta = document.createElement("div");
  meta.className = "meta";
  a.appendChild(meta);

  const detected = document.createElement("div");
  detected.className = "detected";
  a.appendChild(detected);

  const uptime = document.createElement("div");
  uptime.className = "uptime";
  const uptimeBars = [];
  for (let i = 0; i < UPTIME_BUCKETS; i++) {
    const bar = document.createElement("span");
    bar.className = "uptime-bar";
    uptime.appendChild(bar);
    uptimeBars.push(bar);
  }
  a.appendChild(uptime);

  const edit = document.createElement("button");
  edit.className = "edit-btn";
  edit.textContent = "⋯";
  edit.title = "Rename / recategorize / hide";
  a.appendChild(edit);

  return { el: a, icon, name, meta, detected, uptime, uptimeBars, edit };
}

function updateTile(tile, s) {
  const { el, icon, name, meta, detected, uptime, uptimeBars, edit } = tile;

  const wantOffline = !s.online;
  el.classList.toggle("offline", wantOffline);
  if (el.href !== s.url) el.href = s.url;

  const dn = displayName(s);
  const wantsImg = !!s.icon;
  const hasImg = icon.firstChild && icon.firstChild.tagName === "IMG";
  if (wantsImg) {
    if (!hasImg || icon.firstChild.getAttribute("src") !== s.icon) {
      icon.innerHTML = "";
      const img = document.createElement("img");
      img.src = s.icon;
      img.loading = "lazy";
      img.alt = "";
      img.onerror = () => {
        icon.innerHTML = "";
        icon.textContent = initials(dn);
      };
      icon.appendChild(img);
    }
  } else if (hasImg || icon.textContent !== initials(dn)) {
    icon.innerHTML = "";
    icon.textContent = initials(dn);
  }

  if (name.textContent !== dn) name.textContent = dn;

  const metaText = `${s.host}:${s.port}`;
  if (meta.textContent !== metaText) meta.textContent = metaText;

  const detectedText = s.detected || "";
  if (detected.textContent !== detectedText) detected.textContent = detectedText;
  detected.hidden = !detectedText;

  const bars = computeUptimeBars(s.history || [], Date.now());
  uptime.title = uptimeSummary(bars);
  bars.forEach((state, i) => {
    const bar = uptimeBars[i];
    const wantClass = state === null ? "uptime-bar" : state ? "uptime-bar online" : "uptime-bar offline";
    if (bar.className !== wantClass) bar.className = wantClass;
  });

  // Rebind each render so the handlers always close over the latest service.
  edit.onclick = (e) => {
    e.preventDefault();
    e.stopPropagation();
    openEditor(s);
  };
}

function render(services) {
  const app = document.getElementById("app");

  if (services.length === 0) {
    app.innerHTML = '<p class="empty">No services discovered yet…</p>';
    sectionEls.clear();
    tileEls.clear();
    return;
  }

  const groups = groupBy(services);
  if (groups.size === 0) {
    app.innerHTML = '<p class="empty">Everything is hidden.</p>';
    sectionEls.clear();
    tileEls.clear();
    return;
  }
  if (app.querySelector(".empty")) app.innerHTML = "";

  const sortedCats = [...groups.keys()].sort();
  const seenCats = new Set(sortedCats);
  const seenIds = new Set();

  for (const cat of sortedCats) {
    let sec = sectionEls.get(cat);
    if (!sec) {
      const section = document.createElement("section");
      const heading = document.createElement("div");
      heading.className = "section-heading";
      const h2 = document.createElement("h2");
      h2.textContent = cat;
      heading.appendChild(h2);
      const refresh = document.createElement("button");
      refresh.className = "refresh-btn";
      refresh.innerHTML = '<span class="refresh-icon">\u{21BB}</span><span class="refresh-label">refresh</span>';
      refresh.onclick = () => refreshHost(refresh.dataset.host, refresh);
      heading.appendChild(refresh);
      section.appendChild(heading);
      const grid = document.createElement("div");
      grid.className = "grid";
      section.appendChild(grid);
      sec = { section, h2, grid, refresh };
      sectionEls.set(cat, sec);
    }
    app.appendChild(sec.section); // no-op if already last, else moves into place

    const items = groups
      .get(cat)
      .sort((a, b) => displayName(a).localeCompare(displayName(b)));

    // Category is user-renameable and can in principle mix services from
    // different hosts; refresh only makes sense when the section maps
    // cleanly onto exactly one real host.
    const hosts = new Set(items.map((s) => s.host));
    const sectionHost = hosts.size === 1 ? items[0].host : null;
    sec.refresh.hidden = !sectionHost;
    sec.refresh.dataset.host = sectionHost || "";
    sec.refresh.title = sectionHost ? `Scan ${sectionHost} for new services now` : "";

    for (const s of items) {
      seenIds.add(s.id);
      let tile = tileEls.get(s.id);
      if (!tile) {
        tile = buildTile();
        tileEls.set(s.id, tile);
      }
      updateTile(tile, s);
      sec.grid.appendChild(tile.el); // no-op if already in place, else moves
    }
  }

  for (const [cat, sec] of sectionEls) {
    if (!seenCats.has(cat)) {
      sec.section.remove();
      sectionEls.delete(cat);
    }
  }
  for (const [id, tile] of tileEls) {
    if (!seenIds.has(id)) {
      tile.el.remove();
      tileEls.delete(id);
    }
  }
}

async function openEditor(s) {
  const name = prompt("Display name", s.nameOverride || s.title || "");
  if (name === null) return;
  const category = prompt("Category", s.categoryOverride || s.category || "");
  if (category === null) return;
  const hidden = confirm("Hide this service?\nOK = hide, Cancel = keep visible");

  await fetch(`/api/services/${encodeURIComponent(s.id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, category, hidden }),
  });
  refresh();
}

async function refreshHost(host, btn) {
  if (!host) return;
  const label = btn.querySelector(".refresh-label");
  const original = label.textContent;
  btn.disabled = true;
  btn.classList.add("spinning");
  label.textContent = "scanning…";
  try {
    const res = await fetch(`/api/hosts/${encodeURIComponent(host)}/refresh`, { method: "POST" });
    if (!res.ok) throw new Error(await res.text());
    await refresh();
  } catch (err) {
    alert(`Couldn't scan ${host}:\n${err.message}`);
  } finally {
    btn.disabled = false;
    btn.classList.remove("spinning");
    label.textContent = original;
  }
}

async function refresh() {
  const res = await fetch("/api/services");
  const services = await res.json();
  render(services);
}

function connectEvents() {
  const statusEl = document.getElementById("status");
  const es = new EventSource("/events");
  es.onopen = () => { statusEl.textContent = "live"; };
  es.onerror = () => { statusEl.textContent = "reconnecting…"; };
  es.addEventListener("update", refresh);
}

refresh();
connectEvents();

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => {});
  });
}
