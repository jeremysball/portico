function displayName(s) {
  return s.nameOverride || s.title || `${s.host}:${s.port}`;
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

function renderTile(s) {
  const a = document.createElement("a");
  a.className = "tile" + (s.online ? "" : " offline");
  a.href = s.url;
  a.target = "_blank";
  a.rel = "noopener noreferrer";

  const dot = document.createElement("span");
  dot.className = "dot";
  a.appendChild(dot);

  const icon = document.createElement("div");
  icon.className = "icon";
  if (s.icon) {
    const img = document.createElement("img");
    img.src = s.icon;
    img.loading = "lazy";
    img.alt = "";
    img.onerror = () => {
      img.remove();
      icon.textContent = initials(displayName(s));
    };
    icon.appendChild(img);
  } else {
    icon.textContent = initials(displayName(s));
  }
  a.appendChild(icon);

  const name = document.createElement("div");
  name.className = "name";
  name.textContent = displayName(s);
  a.appendChild(name);

  const meta = document.createElement("div");
  meta.className = "meta";
  meta.textContent = `${s.host}:${s.port}`;
  a.appendChild(meta);

  const edit = document.createElement("button");
  edit.className = "edit-btn";
  edit.textContent = "⋯";
  edit.title = "Rename / recategorize / hide";
  edit.onclick = (e) => {
    e.preventDefault();
    e.stopPropagation();
    openEditor(s);
  };
  a.appendChild(edit);

  return a;
}

function render(services) {
  const app = document.getElementById("app");
  app.innerHTML = "";

  if (services.length === 0) {
    app.innerHTML = '<p class="empty">No services discovered yet…</p>';
    return;
  }

  const groups = groupBy(services);
  if (groups.size === 0) {
    app.innerHTML = '<p class="empty">Everything is hidden.</p>';
    return;
  }

  const sortedCats = [...groups.keys()].sort();
  for (const cat of sortedCats) {
    const section = document.createElement("section");

    const h2 = document.createElement("h2");
    h2.textContent = cat;
    section.appendChild(h2);

    const grid = document.createElement("div");
    grid.className = "grid";
    const items = groups
      .get(cat)
      .sort((a, b) => displayName(a).localeCompare(displayName(b)));
    for (const s of items) grid.appendChild(renderTile(s));
    section.appendChild(grid);

    app.appendChild(section);
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
