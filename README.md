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
- **Upload & reorder page** — drag-and-drop upload with lazy folder creation,
  filename-collision auto-renaming (`sunset.jpg` → `sunset (2).jpg`), non-images
  skipped; drag thumbnails to set display order (saved to `order.json`); remove a
  photo with an × (recoverable soft-delete).
- **Named output windows ("screens")** — create as many as you like; each is
  persisted and restored on relaunch (with a blank category, so a photo can never
  appear before you choose one). Auto-placed on a chosen monitor via the Window
  Management API; fullscreen with a click or **F**.
- **Operator control table** — per screen: choose category + orientation (resets to
  the title card), **Prev/Next**, **Black/Reveal**, **Make live** (show this, black
  the rest), plus a global **Black all**.
- **Optional title-card logo** — drop one image in `logo\` and it appears above the
  category name on every title card.
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
         Landscape\  ( + order.json )
         Portrait\   ( + order.json )
       Wildlife\ …
   screens.json          ← saved output-window definitions (state)
   ```

The port can be overridden with the `PHOTOJUDGE_PORT` environment variable
(default `8753`).

### Typical run, start to finish

1. **Create / pick a session** (by date) on the console.
2. **Upload photos** — *Upload / Reorder* page → choose session, category, and
   Landscape/Portrait → drag images in → drag thumbnails to set the order.
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

Versions use the `MAJOR.MINOR.PATCH` format (e.g. `1.0.0`), with project-specific
meaning tied to the branching workflow — each slot maps to a git event:

| Slot | Bumps when… | Example |
|------|-------------|---------|
| **MAJOR** | a major overhaul / rewrite (rare) | `1.x.x` → `2.0.0` |
| **MINOR** | `development` is merged into `main` (a release) | `1.0.x` → `1.1.0` |
| **PATCH** | an **app change** (`feature/` or `fix/` branch) is merged into `development` | `1.1.0` → `1.1.1` |

Lower numbers **reset** when a higher one bumps: a MINOR bump sends PATCH back to
`0`, and a MAJOR bump resets both.

**The version tracks the app, not the repo.** Only changes to the application itself
move the number. Repo-only work does **not** bump `VERSION`, and is kept on separate
branch types:

| Branch | For | Bumps version? |
|--------|-----|----------------|
| `feature/*` | new app functionality | ✅ PATCH |
| `fix/*` | app bug fixes | ✅ PATCH |
| `docs/*` | documentation (README, CHANGELOG, guides) | ❌ no |
| `chore/*` | repo upkeep (`.gitignore`, CI, build config, tooling) | ❌ no |

The current version is stored in the [`VERSION`](VERSION) file — the single source
of truth. It's embedded into the executable at build time (via `//go:embed`), shown
on the operator console, and logged at startup. Release history is kept in
[`CHANGELOG.md`](CHANGELOG.md).

**Bumping it (manual):**

- **App change → `development`:** in a `feature/` or `fix/` PR, bump the PATCH digit
  in `VERSION` (e.g. `1.1.0` → `1.1.1`). `docs/` and `chore/` PRs leave it unchanged.
- **`development` → `main` (release):** in the release PR, bump MINOR and reset PATCH
  (e.g. `1.1.3` → `1.2.0`). After it merges, tag `main`:

  ```sh
  git checkout main && git pull
  git tag -a v1.2.0 -m "Release 1.2.0"
  git push origin v1.2.0
  ```

  Then optionally draft a GitHub Release from that tag and attach `photo-judge.exe`.

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
