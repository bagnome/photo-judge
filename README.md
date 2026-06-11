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
            │  ├─ local web server         │   http://127.0.0.1:8753
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
- **Categories** from a plain-text `categories.txt` (one per line). The name is the
  single source of truth: it's the folder name *and* the text on the title card.
  Creating a session snapshots the current category list, so past sessions are frozen.
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
- **Printable score sheet (PDF)** — one click downloads a single-column scoring form
  for the selected session: a section per category (in `categories.txt` order),
  Landscape before Portrait, one row per photo in display order with the photo name
  and blank spaces for the photographer name and a score. Any photographer names
  recorded on the upload page are pre-filled. Generated in-process with the standard
  library only — no PDF dependency.
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
   stops the server). If the tab didn't open, browse to **http://127.0.0.1:8753**.
3. On first run the app creates, next to itself:

   ```
   photo-judge.exe
   categories.txt        ← the editable category list (seeded with defaults)
   logo\                 ← optional: drop one image here for the title cards
   photos\               ← all session photos live here
     001\                ← a session (stable sequential ID)
       session.json      ← { id, date, created, categories[] }
       Pictorial\
         Landscape\  ( + order.json, names.json )
         Portrait\   ( + order.json, names.json )
       Wildlife\ …
   screens.json          ← saved output-window definitions (state)
   ```

The port can be overridden with the `PHOTOJUDGE_PORT` environment variable
(default `8753`).

Double-clicking the exe again while it's already running won't start a second copy
— it detects the running instance and just reopens the console pointing at it. If
the copy you launch is **newer** than the one running, the console shows a banner
suggesting you close and reopen Photo Judge to update.

### Typical run, start to finish

1. **Create / pick a session** (by date) on the console.
2. **Upload photos** — *Upload / Reorder* page → choose session and category →
   drag images in (each is auto-filed as Landscape or Portrait by its shape) → drag
   thumbnails within a section to set the order.
3. **Create your screens** — *Create Screen*, name each (e.g. "Landscape monitor").
4. **Open each screen's window** — *Open window*, move it to the correct monitor,
   click it (or press **F**) for fullscreen. It starts black.
5. **Present** — on a screen's row pick a category + orientation → *Load* (shows the
   title card) → *Next* to reveal photo 1, *Next/Prev* to move through, *Black* or
   switch categories any time.

> The default categories are: Pictorial, Wildlife, Altered Reality, Portraiture,
> Macro, "Landscapes, Cityscapes, and Travel", Black and White. Edit
> `categories.txt` (one per line; `#` lines are comments) and restart to change them
> — this only affects **new** sessions.

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
| `web/console.html` | Operator console (private control surface) |
| `web/output.html` | Judge-facing output window (black-by-default display) |
| `web/admin.html` | Upload / reorder page |
| `categories.txt` | Default category slate (one per line) |
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
