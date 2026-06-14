# Changelog

All notable changes to Photo Judge are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows the versioning scheme in the
[README](README.md#versioning): `MAJOR.RELEASE.FEATURES.PATCH`, where the RELEASE
digit bumps on each `development → main` release, the FEATURES digit counts changes
queued in `development`, and the PATCH digit counts fixes/small additions shipped onto
the current release on `main` (a lifetime count that never resets). The
**[Unreleased]** section below collects changes already merged into `development` but
not yet released.

## [Unreleased]

### Added
- **Session import / export** — a new **⇄ Import / Export** button on the console opens a
  wizard for moving whole sessions (photos and all) between computers. **Export** lists
  every session (live and archived) with filters (active/archived, photographer, photo
  title, date range), sorting and pagination; pick sessions into a basket and download
  them as a portable file — **`.pjs`** for one session or **`.pjss`** for several (both are
  ordinary zip bundles that include the images, metadata, scores, physical prints and, for
  past nights, the archive record). **Import** reads one or more `.pjs`/`.pjss` files, shows
  a preview of the sessions inside, and lets you choose which to bring in and (optionally)
  one to make the active session. Imported sessions always get a fresh ID so they can never
  collide with existing ones; the wizard shows the old→new ID mapping. The page size of the
  export list is configurable via `exportPageSize` in `photo-judge.properties` (default 50).
- **Session description** — a session can carry an optional free-text note (e.g. "April
  club competition — judged by Jane") to tell sessions apart. Sessions are now created and
  edited through a small modal (date + description) from the console, the description shows
  below the action bar and on the Archived Sessions page, and it prints on the score-sheet
  and archive PDFs.
- **In-app dialogs** — the browser's plain alert/confirm/prompt pop-ups have been replaced
  throughout with dark, centered modals styled to match the QR-code dialog, so every
  prompt, confirmation, and message now looks consistent with the rest of the app.
  Destructive actions (delete session, archive, remove photo, delete screen/logo/category,
  close app) show a red action button; pressing **Enter** confirms and **Esc** cancels.
- **Settings page** — a new **⚙️ Settings** page (in the menu) collects app-wide options
  in one place: lock the scorekeeper to the operator's photo; allow only one live screen
  at a time (revealing one blacks out the rest); a **logo library** (upload several, pick
  the active title-card logo, delete the rest); a custom **PDF header** for the score-sheet
  and archive PDFs (capped to two lines); LAN access; and metadata import split into two
  toggles (photographer and title). Saved to `settings.json`. The LAN and metadata
  toggles are gated by `photo-judge.properties` — a `false` there takes priority and greys
  the toggle out (the effective value is the properties value AND the setting). LAN changes
  apply on the next restart; the rest apply immediately.
- **Physical print scoring** — the **🏆 Scoring** page now has a **📋 Physical prints**
  mode (alongside the existing on-screen mode) for judging printed entries that have no
  image file in the app. Pick a session and category, then fill a spreadsheet-style table
  of **title / photographer / score**: a fresh row appears as you start the last one, Tab
  moves between fields and Enter jumps to the next row, and rows save automatically. The
  prints are stored per session and are included when the session is archived (shown on
  the Archived Sessions page, searchable by photographer/title, and in both the score-sheet
  and archive PDFs, where the uploaded photos are now grouped under a **Digital Prints**
  heading to match). The Scoring page also accepts a `?mode=physical` deep link.
- **Sort archived sessions** — the Archived Sessions page can sort the list of sessions by
  **session date**, **created date**, or **session ID** (ascending or descending). The
  session's creation date is now saved in the archive and shown on each card.
- **Import photographer / title from photo metadata** — with `importMetadata=true` in
  `photo-judge.properties` (off by default), uploading a photo reads its embedded
  metadata: an EXIF/PNG **artist/author** value is filled in as the photographer, and a
  **title** value is used as the photo's on-screen name (its filename) instead of the
  original filename. Anything missing falls back to the usual behavior, and the
  photographer can still be edited by hand. JPEG (EXIF `Artist`/`ImageDescription` and the
  Windows `XPAuthor`/`XPTitle` tags) and PNG (`tEXt`/`zTXt`/`iTXt` `Author`/`Title`)
  are read with the standard library only. (On startup the app now also appends any
  newly-added settings to an existing `photo-judge.properties`, so upgrading picks up new
  options without deleting the file or losing your edits.)
- **Session archiving** — once a competition night is over, an **🗄 Archive Session**
  button (shown on the console only for sessions whose date is in the past) saves that
  session's record — photo titles, photographers, scores, categories, orientations, the
  date, and the archive date — to a single JSON file under `archives\`, then
  **permanently deletes the photo image files** to reclaim disk space. The session leaves
  the console (it's read-only afterward). A confirmation dialog spells out that the photos
  will be deleted and the session can't be changed; the action can't be undone. A new
  **Archived Sessions** page (in the menu) lists every archived session and lets you
  search by date range, session ID, photographer, or photo title, expand a session to see
  its photos (sortable by any column), and download any session's archive as a printable
  **PDF** report or as **JSON**. IDs are never reused, even after the photo folder is gone.
- **Navigation side menu** — page-to-page links now live in a single collapsible menu
  pinned to the right edge of every control page, instead of being scattered across each
  page's top bar. Collapsed it's a slim rail of clickable page icons; expanded it shows
  the page names and, at the top, the app's network address plus the **Show QR code**
  button for sharing access. On phones the rail is hidden and a floating hamburger opens
  the menu as a full drawer. The console toolbar now carries only session/screen actions,
  and the per-page "← Console" links and the console's old LAN bar are gone (folded into
  the menu). Served from a shared `nav.js` so every page stays consistent.
- **Mobile-friendly control pages** — the operator console and the Upload/Reorder,
  Manage categories, Scoring, and Getting Started pages are now responsive, so the show
  can be run from a phone or tablet (handy with the LAN remote-control address). Each page
  declares a mobile viewport; on a narrow screen the console's screen table reflows into
  one labelled card per screen, the category panes and the scoring stage/panel stack
  vertically, tap targets grow, and text inputs use a 16px font so phones don't zoom on
  focus. Photo reordering on the Upload page was reworked to use pointer events: grab a
  tile anywhere with a mouse, or use the new **⠿ drag** grip on a touchscreen (so swiping
  elsewhere still scrolls). Controls that only work on the host computer (opening a judge
  window) are hidden on touch devices. The judge **output** display is unchanged.
- **Scoring page** — a new **🏆 Scoring** page (opened from the console) lets a
  scorekeeper follow any screen and record a score per photo. Pick a screen to follow,
  then flip through that category's photos independently of what the operator is
  presenting (Prev / Next or ← / → arrows). A live indicator shows which photo the
  operator is currently on, highlights when you're viewing that same photo, and a
  **Jump to operator's photo** button snaps to it — or tick **Lock to the operator's
  photo** to follow along automatically. The photo's name is shown alongside the
  photographer. The score you type is saved per photo (one total score for now) and
  shows up as a badge on the Upload / Reorder grid and pre-filled in the **Score**
  column of the downloaded score-sheet PDF.
- **LAN remote control + QR code** — the operator console now shows the computer's
  network address at the top, so the show can be driven from a second laptop, phone, or
  tablet on the same network. A **Show QR code** button opens a modal with a scannable
  code (generated by a small standard-library-only QR encoder, `qr.go`, with no
  third-party dependency) that opens the control page on a phone instantly; when the
  machine has multiple addresses, the modal lets you pick which one. The User Guide gains
  a section on the one-time Windows Firewall / network setup needed to allow remote
  devices to connect. The judges' output windows stay on the host machine. Remote
  access can be turned off with `lanAccess=false` in `photo-judge.properties`, which
  binds the server to this computer only and hides the address bar and QR button.

### Changed
- **Default port is now 80, with a settings file** — Photo Judge listens on port `80`
  by default, so the control page is simply `http://127.0.0.1` (no port number to
  remember). A new `photo-judge.properties` file is created next to the exe on first run
  where the port can be changed (`port=…`) or handed to the app to choose automatically
  (`autoPort=true` finds any free port). The file is plain text with comments; a leading
  byte-order mark from editors like Notepad is tolerated. (`PHOTOJUDGE_PORT` still
  overrides for development.)
- **Output screens fill the window** — the judge-facing photo now scales up to the
  largest size that fits the window while keeping its aspect ratio (letterboxed with
  black bars), instead of being capped at the image's native size. Small photos no
  longer appear undersized in the middle of the screen.

## [1.2.3.0] - 2026-06-11 — "Bryce Canyon"

### Added
- **Getting Started page** — a new illustrated, step-by-step walkthrough (create a
  session → manage categories → upload photos → create screens → load → present),
  opened from a **Getting Started** button on the console. Its screenshots live in
  `getting-started-images/` and are embedded into the exe, so the guide works on a
  copied, offline build.
- **Category manager (per session)** — a new **Manage categories** page (linked from the
  console and the Upload page) replaces hand-editing `categories.txt`. Two panes —
  **Inactive** (alphabetical) and **Active** (ordered) — let you move categories between
  them, reorder the active ones (select + ↑/↓), add new categories, and delete ones with
  no photos (used categories can only be deactivated, which preserves their photos).
  Categories are now **per session**: each session owns its slate, a new session inherits
  the latest session's active order + inactive set, and the active order drives that
  session's console/Upload dropdowns and its score-sheet PDF. `categories.txt` now only
  seeds the very first session.

### Changed
- **Version-number format** — versions are now four numbers,
  `MAJOR.RELEASE.FEATURES.PATCH` (e.g. `1.2.0.0`): the 3rd digit counts changes queued
  in the upcoming release and the 4th is a lifetime patch counter for the shipped
  release. See [Versioning](README.md#versioning). The version comparison behind the
  "newer version launched" banner now understands the four-number form (and still
  reads older three-number builds, treating `1.1.0` as `1.1.0.0`).

## [1.1.0] - 2026-06-10 — "Antelope Canyon"

### Added
- This changelog.
- **Downloadable score sheet (PDF)** — a "⤓ Score sheet" button on the console
  generates a printable, single-column scoring form for the selected session:
  one section per category (in `categories.txt` order), Landscape before Portrait,
  one row per photo (in display order) with the photo name and blanks for the
  photographer name and a score. Generated with the standard library only (no
  third-party PDF dependency).
- **Photographer names** — a text box under each photo on the Upload / Reorder
  page associates a photographer with that photo (saved per folder in
  `names.json`). When a name is set it's pre-filled into the Photographer column
  of the score-sheet PDF; photos left blank keep an empty space to write in.
- **"Newer version launched" banner** — if a newer build of the exe is started
  while an older one is running, the running console shows a banner recommending a
  restart to update (only when the launched copy is strictly newer).
- **Getting-started guidance on the Upload / Reorder page** — when no session
  exists, the upload area is replaced with step-by-step instructions for creating
  one; when sessions exist but none is selected, a short prompt explains how to
  pick or create one.

### Changed
- **Re-launching the app is friendly** — double-clicking the exe while it's already
  running no longer fails with a raw "address in use" error. The second launch
  detects the running instance, opens the console pointing at it, and exits cleanly
  (so you never get two servers fighting over the same `photos\` folder).
- **Automatic orientation on upload** — the operator no longer picks Landscape or
  Portrait when uploading. Each photo is filed automatically by its shape (taller
  than wide = Portrait; squares go to Landscape), and the Upload / Reorder page now
  shows both orientations as separate on-page sections. A JPG's EXIF orientation
  (rotation) is honored, so a photo rotated in-camera still lands in the right
  section. Uploads are now limited to **JPG and PNG**.

## [1.0.0] - 2026-06-10

Initial release.

### Added
- Private operator console and judge-facing, black-by-default output windows, with
  live updates over Server-Sent Events.
- Sessions keyed by a stable sequential ID with an editable date label; create,
  relabel, and recoverable (soft) delete.
- Categories loaded from an external `categories.txt`; new sessions snapshot the list
  so past sessions stay frozen.
- Separate Landscape and Portrait photo groups per category, presented one
  orientation at a time.
- Upload & reorder page: drag-and-drop upload with lazy folder creation,
  filename-collision auto-rename, drag-to-reorder, and recoverable photo removal.
- Persistent named "screens" restored on relaunch (with a blank category) and
  auto-placed on a chosen monitor via the Window Management API.
- Per-screen operator controls (category/orientation, Prev/Next, Black/Reveal, Make
  live) plus a global Black all.
- Optional title-card logo from a `logo\` folder.
- Close App button for a clean server shutdown.
- Single self-contained executable (Go, standard library only) with the web UI
  embedded; fully portable, with all paths resolved next to the exe.
- `1.x.x` version scheme: a `VERSION` file embedded into the exe, shown on the
  operator console and logged at startup.

[Unreleased]: https://github.com/bagnome/photo-judge/compare/v1.2.3.0...HEAD
[1.2.3.0]: https://github.com/bagnome/photo-judge/compare/v1.1.0...v1.2.3.0
[1.1.0]: https://github.com/bagnome/photo-judge/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/bagnome/photo-judge/releases/tag/v1.0.0
