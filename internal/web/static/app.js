function displayName(s) {
  return s.nameOverride || s.title || "Not Found";
}

function effectiveCategory(s) {
  return s.categoryOverride || s.category || "Other";
}

function initials(name) {
  return name.slice(0, 2).toUpperCase();
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

  const history = document.createElement("button");
  history.className = "history-btn";
  history.textContent = "\u{1F550}";
  history.title = "View history";
  a.appendChild(history);

  const edit = document.createElement("button");
  edit.className = "edit-btn";
  edit.textContent = "⋯";
  edit.title = "Rename / recategorize / hide";
  a.appendChild(edit);

  return { el: a, icon, name, meta, history, edit };
}

function updateTile(tile, s) {
  const { el, icon, name, meta, detected, history, edit } = tile;

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

  // Rebind each render so the handlers always close over the latest service.
  history.onclick = (e) => {
    e.preventDefault();
    e.stopPropagation();
    showHistory(s);
  };
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
      const h2 = document.createElement("h2");
      h2.textContent = cat;
      section.appendChild(h2);
      const grid = document.createElement("div");
      grid.className = "grid";
      section.appendChild(grid);
      sec = { section, h2, grid };
      sectionEls.set(cat, sec);
    }
    app.appendChild(sec.section); // no-op if already last, else moves into place

    const items = groups
      .get(cat)
      .sort((a, b) => displayName(a).localeCompare(displayName(b)));

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

function showHistory(s) {
  const dn = displayName(s);
  if (!s.history || s.history.length === 0) {
    alert(`${dn}\n\nNo history recorded yet.`);
    return;
  }
  const lines = s.history
    .slice()
    .reverse()
    .map((h) => `${new Date(h.time).toLocaleString()} — ${h.online ? "online" : "offline"}`);
  alert(`${dn} — history (last 7 days)\n\n${lines.join("\n")}`);
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
