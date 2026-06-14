# Photo Judge

A small, **fully offline, portable Windows app** for running a photography club's
"digital print" judging night. It presents competition photos to judges on one or
more monitors while you — the operator — line up the next photo privately on the
laptop. Judges only ever see clean photos on black, announced by a per-category
title card; they never see the desktop, a file dialog, a cursor, or the queue.

It ships as a **single self-contained executable** (no installer, no runtime, no
admin rights, no internet) that runs a tiny local web server and drives the
displays through your preinstalled browser.

---

## Contents

- [Why it exists](#why-it-exists)
- [How it works](#how-it-works)
- [Features](#features)
- [Building the executable](#building-the-executable)
- [Running it](#running-it)
  - [Typical run, start to finish](#typical-run-start-to-finish)
- [Moving it to another computer](#moving-it-to-another-computer)
- [Project layout](#project-layout)
- [Versioning](#versioning)
- [Roadmap](#roadmap)
- [Built with Claude Code](#built-with-claude-code)
- [License](#license)

---

## Why it exists

A typical club setup is a laptop with two external monitors fixed in orientation —
one **landscape**, one **portrait** — because prints are judged in their natural
aspect. The operator needs to:

- queue the next photo **without the judges seeing it**,
- keep the **unused** monitor black,
- bookend each category with a **title card** and black,
- step **back and forth** through a category at the judges' pace,

…all from a private console on the laptop. Photo Judge is built around exactly
that workflow.

---

## How it works

```
            ┌──────────────────────────────┐
            │  photo-judge.exe             │   one static binary
            │  ├─ local web server         │   http://127.0.0.1 (port 80)
            │  └─ embedded HTML/JS/CSS     │   (go:embed)
            └──────────────┬───────────────┘
                           │  Server-Sent Events (live)
        ┌──────────────────┼─────────────────────┐
        ▼                  ▼                       ▼
  Operator console   Output window           Output window
  (laptop screen)    (landscape monitor)     (portrait monitor)
  private control    black-by-default        black-by-default
```

- **One executable** = a localhost web server with all UI assets embedded in the
  binary via `//go:embed`. Standard library only — no third-party dependencies, so
  it builds and runs entirely offline.
- Served over `http://localhost`, which is a **secure context** — that's what
  unlocks the browser Window Management API used to place output windows on the
  right monitor.
- **Server-Sent Events** push live state from the console to each output window, so
  pressing *Next* updates the judge's screen instantly. (SSE rather than WebSockets,
  to stay dependency-free.)
- **Everything is resolved relative to the executable's own folder** (via
  `os.Executable()`), so the whole thing is portable — copy the folder anywhere
  and run.

---

## Features

- **Sessions** — organize each competition night. Keyed by a stable sequential ID
  (the folder name, e.g. `001`) with the **date as an editable label**. Create,
  relabel, or soft-delete (recoverable) sessions.
- **Categories, per session** — manage each session's categories in the **Manage
  categories** page: two panes (**Inactive** / **Active**) where you move categories
  between them, reorder the active ones, add new ones, and delete unused ones (a category
  with photos can only be deactivated, which keeps its photos). The Active order drives
  that session's dropdowns and score sheet. A category name is the folder name *and* the
  title-card text. Each session owns its slate; a **new session inherits the latest
  session's setup**, and `categories.txt` seeds only the very first session.
- **Per-orientation photo groups** — each category has a **Landscape** and a
  **Portrait** group, presented separately (all of one orientation, then the other).
  Orientation is assigned **automatically** from each photo's shape, so you upload to
  a category and the app files every photo for you.
- **Upload & reorder page** — drag-and-drop upload (**JPG/PNG**) with lazy folder
  creation; each photo is **auto-sorted into Landscape or Portrait** by its shape
  (taller than wide = Portrait; squares go to Landscape; a JPG's EXIF rotation is
  honored), shown in two on-page sections; filename-collision auto-renaming
  (`sunset.jpg` → `sunset (2).jpg`), other files skipped; drag thumbnails within a
  section to set display order (saved to `order.json`); remove a photo with an ×
  (recoverable soft-delete); and a per-photo text box to record the
  **photographer's name** (saved to `names.json`).
- **Named output windows ("screens")** — create as many as you like; each is
  persisted and restored on relaunch (with a blank category, so a photo can never
  appear before you choose one). Auto-placed on a chosen monitor via the Window
  Management API; fullscreen with a click or **F**.
- **Operator control table** — per screen: choose category + orientation (resets to
  the title card), **Prev/Next**, **Black/Reveal**, **Make live** (show this, black
  the rest), plus a global **Black all**.
- **Optional title-card logo** — drop one image in `logo\` and it appears above the
  category name on every title card.
- **Scorekeeping** — a **🏆 Scoring** page (opened from the console) lets a
  scorekeeper follow any screen and record a score per photo. They flip through the
  category independently of what the operator is presenting (Prev/Next or ←/→), with a
  live indicator of which photo the operator is on, a highlight when both are on the
  same photo, and a **Jump to operator's photo** shortcut. Scores are saved per photo
  and surface as a badge on the Upload / Reorder grid and pre-filled in the score-sheet
  PDF. A separate **Physical prints** mode records printed entries that have no image
  file — a spreadsheet-style table of title / photographer / score per category that
  auto-extends as you type (Tab between fields, Enter for the next row).
- **Printable score sheet (PDF)** — one click downloads a single-column scoring form
  for the selected session: a section per category (in the session's category order),
  Landscape before Portrait, one row per photo in display order with the photo name
  and blank spaces for the photographer name and a score. Any photographer names and
  judge scores already recorded are pre-filled. Generated in-process with the standard
  library only — no PDF dependency.
- **Getting Started guide** — an in-app, illustrated walkthrough (a **Getting Started**
  button on the console) that takes a new operator from creating a session through to
  presenting, with a screenshot for each step. Its images are embedded in the exe, so it
  works offline on a copied build.
- **LAN remote control** — the console shows the computer's LAN address so the show can
  be driven from a second laptop, phone, or tablet on the same network, and a **Show QR
  code** button renders a scannable code (a small standard-library-only QR encoder, no
  third-party dependency) to open the control page on a phone instantly. The judges'
  output windows stay on the host machine.
- **Session archiving** — when a competition night is over, archive its session: the
  photo metadata (titles, photographers, scores, categories, orientations, dates) is saved
  to `archives\<id>.json` and the **photo image files are permanently deleted** to reclaim
  space. The session becomes read-only and leaves the console; archived sessions are
  browsable on the **Archived Sessions** page (search by date range / session ID /
  photographer / title) and downloadable as JSON.
- **Mobile-friendly** — every control page (console, Upload/Reorder, Manage categories,
  Scoring, Getting Started) is responsive and touch-friendly, so it works from a phone or
  tablet: the console's screen table becomes one card per screen, panes stack, tap targets
  grow, and photo reordering works by touch via a drag grip (pointer events). Pairs with
  LAN remote control above.
- **Settings page** — one place for app-wide options: lock the scorekeeper to the
  operator, allow only one live screen at a time, a logo library (upload/choose/delete the
  title-card logo), a custom PDF header, LAN access, and per-field photo-metadata import.
  Saved to `settings.json`; the LAN and metadata toggles are gated by `photo-judge.properties`
  (a `false` there wins).
- **Consistent in-app dialogs** — confirmations, prompts, and messages use the app's own
  dark modals (matching the QR-code dialog) instead of the browser's plain pop-ups, with a
  red action button for destructive choices; **Enter** confirms and **Esc** cancels.
- **Close App** button — stops the server cleanly so nothing lingers in memory.

---

## Building the executable

**Prerequisites:** [Go](https://go.dev/dl/) **1.26 or newer**. No other tooling.

```sh
git clone https://github.com/bagnome/photo-judge.git
cd photo-judge
go build -o photo-judge.exe .
```

That produces a single ~9 MB `photo-judge.exe` with the web UI embedded — nothing
else needs to be copied alongside it for a fresh start. Because it's pure standard
library, the build works with no network access once the toolchain is installed.

Cross-compiling for Windows from macOS/Linux:

```sh
GOOS=windows GOARCH=amd64 go build -o photo-judge.exe .
```

> The build artifact (`*.exe`) is intentionally **git-ignored** — distribute it via
> a [GitHub Release](https://github.com/bagnome/photo-judge/releases) rather than
> committing the binary.

---

## Running it

1. Double-click **`photo-judge.exe`**.
   - First run on a new PC may trip Windows SmartScreen ("unknown publisher")
     because the exe isn't code-signed → **More info → Run anyway** (one time).
2. A small command window opens **and** your browser opens to the operator console
   automatically. Leave the command window open while you use the app (closing it
   stops the server). If the tab didn't open, browse to **http://127.0.0.1**.
3. On first run the app creates, next to itself:

   ```
   photo-judge.exe
   categories.txt          ← the editable category list (seeded with defaults)
   photo-judge.properties  ← startup settings (port / autoPort / gates), seeded
   settings.json           ← app settings from the Settings page
   logo\                   ← title-card logo library (managed on the Settings page)
   photos\                 ← all session photos live here
     001\                ← a session (stable sequential ID)
       session.json      ← { id, date, created, categories[] }
       Pictorial\
         Landscape\  ( + order.json, names.json )
         Portrait\   ( + order.json, names.json )
       Wildlife\ …
   screens.json          ← saved output-window definitions (state)
   ```

By default the server listens on **port 80** (so the console is just
`http://127.0.0.1`). Edit `photo-judge.properties` to change it — set `port=…` for a
fixed port, or `autoPort=true` to let the OS assign any free one (the command window
prints the chosen address). For development, the `PHOTOJUDGE_PORT` environment
variable overrides the file.

By default the server binds to all interfaces so other devices on the LAN can reach
the console (see the **LAN remote control** feature); the operator's own browser is
still opened at `http://127.0.0.1`, a secure context that keeps the Window Management
API working. Set `lanAccess=false` in `photo-judge.properties` to bind to loopback
only (this computer only).

Set `importMetadata=true` in `photo-judge.properties` to pull the **photographer**
(EXIF/PNG artist/author) and **title** (used as the photo's filename) from each
uploaded photo's embedded metadata; it's off by default.

Double-clicking the exe again while it's already running won't start a second copy
— it detects the running instance and just reopens the console pointing at it. If
the copy you launch is **newer** than the one running, the console shows a banner
suggesting you close and reopen Photo Judge to update.

### Typical run, start to finish

1. **Create / pick a session** (by date) on the console.
2. **Set up categories** *(optional)* — *Manage categories* → activate/deactivate,
   reorder, or add this session's categories. A new session already inherits the last
   one's setup, so you can usually skip this.
3. **Upload photos** — *Upload / Reorder* page → choose session and category →
   drag images in (each is auto-filed as Landscape or Portrait by its shape) → drag
   thumbnails within a section to set the order.
4. **Create your screens** — *Create Screen*, name each (e.g. "Landscape monitor").
5. **Open each screen's window** — *Open window*, move it to the correct monitor,
   click it (or press **F**) for fullscreen. It starts black.
6. **Present** — on a screen's row pick a category + orientation → *Load* (shows the
   title card) → *Next* to reveal photo 1, *Next/Prev* to move through, *Black* or
   switch categories any time.

> The default categories (seeded into the first session) are: Pictorial, Wildlife,
> Altered Reality, Portraiture, Macro, "Landscapes, Cityscapes, and Travel", Black and
> White. After that, manage each session's categories in **Manage categories** (no
> restart needed); `categories.txt` only seeds the very first session.

A full end-user walkthrough (for the operator who runs the show, not the developer)
ships in [`User Guide.txt`](User%20Guide.txt).

---

## Moving it to another computer

Copy just `photo-judge.exe` to start fresh — it rebuilds everything it needs.
To bring your content along, copy the exe **and** keep these beside it:
`photos\`, `categories.txt`, `logo\`, `screens.json`.

Requirements on the target machine: 64-bit Windows 10/11, Microsoft Edge (already
present), and a **writable** folder (a write-protected USB stick blocks uploads and
new sessions — copy to the local drive for an event).

---

## Project layout

| Path | What it is |
|------|------------|
| `main.go` | The entire backend — HTTP server, sessions, screens, SSE, uploads (standard library only) |
| `config.go` | Reads `photo-judge.properties` (port / autoPort) at startup |
| `archive.go` | Session archiving — write metadata JSON, delete photos, search archives |
| `metadata.go` | Reads photographer / title from JPEG EXIF & PNG text chunks on upload |
| `physical.go` | Physical-print scoring (no image file) — store/search per session |
| `settings.go` | App settings (`settings.json`) + logo library; gated by properties |
| `web/settings.html` | Settings page (toggles, logo library, PDF header) |
| `qr.go` | Standard-library QR-code encoder for the "connect over LAN" code |
| `web/console.html` | Operator console (private control surface) |
| `web/nav.js` | Shared right-side navigation menu injected into every control page |
| `web/modal.js` | Shared in-app dialogs (alert/confirm/prompt replacements) for every page |
| `web/output.html` | Judge-facing output window (black-by-default display) |
| `web/admin.html` | Upload / reorder page |
| `web/categories.html` | Category manager (per-session) page |
| `web/score.html` | Scoring page (follow a screen, record scores) |
| `web/archived.html` | Archived Sessions page (search / view / download archives) |
| `web/getting-started.html` | Illustrated Getting Started walkthrough |
| `getting-started-images/` | Screenshots for the Getting Started page (embedded into the exe) |
| `categories.txt` | First-session category seed (one per line) |
| `photo-judge.properties` | Runtime settings (port / autoPort), seeded on first run |
| `User Guide.txt` | End-user (operator) guide |
| `CHANGELOG.md` | Release history |
| `VERSION` | Current version (single source of truth, embedded into the exe) |

Runtime data (`photos\`, `logo\`, `screens.json`, the built `*.exe`) is
git-ignored — see [`.gitignore`](.gitignore).

---

## Versioning

Versions use a four-number format, **`MAJOR.RELEASE.FEATURES.PATCH`** (e.g.
`1.2.3.1`), with project-specific meaning tied to the branching workflow. Two
long-lived branches each carry a version: **`main` is the current release** and
**`development` is the next release**, with `development` always exactly one RELEASE
ahead of `main`.

| Slot | Meaning | Moves when… |
|------|---------|-------------|
| **MAJOR** | a major overhaul / rewrite (rare) | almost never (`1.x.x.x` → `2.0.0.0`) |
| **RELEASE** | which release this is; `development` sits one ahead of `main` | `development` is merged into `main` (a release): its number bubbles up and replaces main's |
| **FEATURES** | how many changes are queued in the upcoming release | a `feature/` (or dev-side `fix/`) branch merges into `development`; **resets to `0`** at each release |
| **PATCH** | lifetime patch count for the shipped release | a `patch/` branch merges into `main`; **never resets** |

**The two lines, and how the numbers move:**

- **Features go to the next release (`development`).** Each `feature/` (or dev-side
  `fix/`) merge bumps the **3rd** digit — the count of changes queued for the next
  release. It resets to `0` when a new release cycle begins.
- **Patches go to the current release (`main`).** A *patch* is anything small enough
  to ship onto the already-released version without waiting for the next one — a bug
  fix, or a small addition. A `patch/` branch off `main` bumps `main`'s **4th** digit
  (e.g. `1.1.0.0` → `1.1.0.1`), and `development`'s 4th digit is bumped to match. The
  PATCH digit is a **lifetime counter** shared by both lines — it only ever climbs.
- **A release** is a `development` → `main` PR. `development`'s number replaces
  `main`'s (the RELEASE digit bumps on `main`, the FEATURES count comes along, and the
  PATCH digit is already in sync). `development` then rolls to the next RELEASE with
  FEATURES back to `0` and PATCH carried over.

A worked timeline:

```
main 1.1.0.0   development 1.2.0.0
  feature  → development 1.2.1.0
  feature  → development 1.2.2.0
  patch    → main 1.1.0.1   development 1.2.2.1   (4th digit synced)
  feature  → development 1.2.3.1
  patch    → main 1.1.0.2   development 1.2.3.2
  RELEASE  → main 1.2.3.2   development 1.3.0.2   (FEATURES reset, PATCH carries)
```

**Branch types:**

| Branch | Branches off | Merges into | Version effect |
|--------|--------------|-------------|----------------|
| `feature/*` | `development` | `development` | +1 **FEATURES** (3rd) |
| `fix/*` | `development` | `development` | +1 **FEATURES** (3rd) — a dev-side fix is a change in the next release |
| `patch/*` | `main` | `main` | +1 **PATCH** (4th) on `main`; bump `development`'s 4th to match |
| `docs/*` | `development` | `development` | none |
| `chore/*` | `development` | `development` | none |

The current version is stored in the [`VERSION`](VERSION) file — the single source
of truth. It's embedded into the executable at build time (via `//go:embed`), shown
on the operator console, and logged at startup. Release history is kept in
[`CHANGELOG.md`](CHANGELOG.md).

**Bumping it (manual):**

- **Feature → `development`:** in a `feature/` or `fix/` PR, bump the **FEATURES**
  (3rd) digit in `VERSION` (e.g. `1.2.0.0` → `1.2.1.0`). `docs/` and `chore/` PRs leave
  it unchanged.
- **Patch → `main`:** in a `patch/` PR off `main`, bump the **PATCH** (4th) digit of
  `main`'s `VERSION` (e.g. `1.1.0.0` → `1.1.0.1`), then bump `development`'s 4th digit
  to match. After it merges, tag `main` with the patched version:

  ```sh
  git checkout main && git pull
  git tag -a v1.1.0.1 -m "Patch 1.1.0.1"
  git push origin v1.1.0.1
  ```

- **`development` → `main` (release):** the release PR carries `development`'s number
  onto `main`. After it merges, tag `main` and roll `development` to the next RELEASE
  (`VERSION` → `1.<RELEASE+1>.0.<PATCH>`):

  ```sh
  git checkout main && git pull
  git tag -a v1.2.3.2 -m "Release 1.2.3.2"
  git push origin v1.2.3.2
  ```

  Then optionally draft a GitHub Release from that tag and attach `photo-judge.exe`.

**Release codenames.** Each release (a RELEASE-digit bump — a new `vX.Y.*.*` line on
`main`) is given a codename: a **famously photographed landmark**, chosen
alphabetically (A, B, C…), fitting for a photography-club tool. The letter advances
once per release, independent of the numbers; a patch keeps its release's codename.
`1.1.0` was **"Antelope Canyon"** (the "A" release); the next release takes a "B"
landmark. The codename goes in the GitHub Release title and the `CHANGELOG.md` heading
for that version.

---

## Roadmap

Planned directions for the project (not yet implemented):

- **Judge scoring from phones** — let judges connect to the web app from their own
  phones over the local network and submit scores for each photo, so scoring is
  captured digitally instead of on paper. The console would collect and tally results
  live.
- **Scoring physical prints** — extend the same scoring system to physical print
  competitions (not just on-screen digital entries), so a single tool can handle both
  formats of a club night.

These are exploratory goals; details and timing may change. Ideas and contributions
toward them are welcome.

---

## Built with Claude Code

This web app is being developed with the help of
[Claude Code](https://claude.com/claude-code), Anthropic's agentic coding tool,
used as a pair-programming assistant throughout the build.

---

## License

Released under the [MIT License](LICENSE) — free to use, modify, and distribute.
Contributions are welcome.
