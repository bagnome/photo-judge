# Changelog

All notable changes to Photo Judge are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows the versioning scheme in the
[README](README.md#versioning): `MAJOR.MINOR.PATCH`, where MINOR bumps on each
`development → main` release and PATCH bumps on each `feature → development` merge.
Tagged releases are the MINOR versions on `main`; the **[Unreleased]** section below
collects changes already merged into `development` but not yet released.

## [Unreleased]

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

[Unreleased]: https://github.com/bagnome/photo-judge/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/bagnome/photo-judge/releases/tag/v1.0.0
