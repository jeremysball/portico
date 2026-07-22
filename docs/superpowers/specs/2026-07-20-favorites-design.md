# Favorites

## Purpose

Let a user pin services they check often to the top of the dashboard, instead
of hunting for them inside their category section.

## Behavior

- Each tile gets a star toggle button (☆ / ★), placed alongside the existing
  history and edit buttons, revealed on hover the same way those are.
- Clicking the star toggles favorite status immediately — no modal, no
  confirmation.
- A favorited service is pulled out of its normal category section and shown
  only in a new "Favorites" section, pinned above every other category
  regardless of alphabetical order. It does not also appear in its original
  category (mirrors how `hidden` already removes a tile from its normal
  spot).
- If no services are favorited, no "Favorites" section is rendered.

## Data model

`internal/registry/registry.go`:

- `Service` gains `Favorite bool `json:"favorite,omitempty"`` alongside the
  existing `Hidden`/`NameOverride`/`CategoryOverride` fields.
- Preserved across re-discovery the same way those fields are today
  (registry.go:160-162 copies overrides from the existing record onto the
  freshly discovered one).
- `Update(id string, name, category *string, hidden *bool)` gains a fourth
  parameter: `favorite *bool`.

## API

`internal/web/server.go`:

- `updateRequest` gains `Favorite *bool `json:"favorite"``.
- `handleUpdate` passes it through to `reg.Update`.
- No new endpoint — this reuses the existing `PATCH /api/services/{id}`
  endpoint that already handles name/category/hidden edits.

## Frontend

`internal/web/static/app.js`:

- `buildTile()`: add a `favorite-btn` button next to `history-btn`/`edit-btn`.
- `updateTile()`: set the star's glyph/title from `s.favorite`; `onclick`
  sends `PATCH {favorite: !s.favorite}` to `/api/services/{id}` and calls
  `refresh()`.
- `groupBy()`: a service with `favorite: true` goes into a synthetic
  `"Favorites"` group instead of its real category (checked before the
  existing `hidden` skip, same style).
- `render()`: after the existing alphabetical sort of category keys, move
  `"Favorites"` to the front of the list if present, so it always renders
  first. Section/tile creation, diffing, and cleanup reuse the existing
  per-category machinery unchanged.

`internal/web/static/style.css`:

- `.tile .favorite-btn` mirrors the existing `.edit-btn`/`.history-btn`
  hover-reveal positioning and styling.

## Out of scope

- No drag-to-reorder within Favorites (services sort alphabetically within
  the section like every other section).
- No dedicated favorites endpoint.
- No limit on number of favorites.
