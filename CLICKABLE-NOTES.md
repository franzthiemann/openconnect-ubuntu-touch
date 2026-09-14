# Clickable / Ubuntu Touch development notes

Hard-won lessons from building remoteImage (rimg) — an SSH media gallery with
vendored Python deps — on Ubuntu Touch 24.04, clickable 8.9, arm64 device.
Written as a bootstrap for the next app.

## Project setup (clickable.yaml)

- **Set `framework` explicitly.** No `framework` key defaults via `qt` to
  ubuntu-sdk-20.04 (Qt 5.12 / Python 3.8). `framework: ubuntu-touch-24.04-1.x`
  gives Qt 5.15 / Python 3.12 and drives `$ENV{CLICK_FRAMEWORK}` and
  `$ENV{APPARMOR_POLICY}` (→ policy 2404.1) in the manifest/apparmor templates.
  There's also a `...-2.x` channel — must match the device's OTA channel.
- **Builder choice matters for arch:** `pure-qml-cmake` forces arch `all` and
  errors on `--arch arm64`. If the click bundles any arch-specific binaries,
  use `builder: cmake` (even if CMake compiles nothing and only installs
  qml/src/assets trees).
- Useful keys: `prebuild`, `build`, `postbuild`, `dependencies_host` (apt pkgs
  in the build container), `dependencies_target` (apt pkgs bundled for the
  device), `env_vars`, `kill` (process to kill on relaunch, e.g. `qmlscene`).
  `${ROOT}` = project root in hooks.
- `clickable build` runs click-review automatically at the end ("pass"/FAIL).

## Vendoring Python deps (the big one)

- To ship pip packages: `dependencies_host: [python3-pip]` + a `prebuild` that
  `pip3 install --target ${ROOT}/src/vendor ...`; install `src/` via CMake so
  vendor ships inside the click. Add `src/vendor/` to .gitignore.
- **CRITICAL: the clickable "arm64" image is an amd64 host that
  cross-compiles.** C/C++ gets a cross-toolchain, but pip runs the container's
  native amd64 Python and silently fetches **amd64 wheels**. Force the target
  tags:
  ```
  pip3 install --target ${ROOT}/src/vendor --no-compile --only-binary=:all: \
    --platform manylinux2014_aarch64 --python-version 312 \
    --implementation cp --abi cp312 <pinned packages>
  ```
  `--abi cp312` still matches abi3 wheels (cryptography, bcrypt, pynacl).
  `rm -rf src/vendor` first — it persists between builds; stale/mixed-arch
  files survive otherwise.
- **Wrong-arch .so surfaces as a misleading error:** dlopen of an x86_64 .so
  on the device reports `ImportError: ... cannot open shared object file: No
  such file or directory` — looks like a missing file, isn't. Diagnose with
  `file src/vendor/**/*.so` (names literally contain x86_64 vs aarch64).
- Verify wheel availability up front on any host:
  `pip download --only-binary=:all: --platform manylinux2014_aarch64
  --python-version 312 --implementation cp --abi cp312 <pkgs>`.
- Wheels that worked on 24.04/py312/aarch64: paramiko 3.5.1, pillow 10.4.0,
  av 14.2.0 (PyAV bundles a full FFmpeg incl. GPL x264/x265 — ~38 MB, and it
  makes the click's distribution effectively GPL).

## Build / install / launch pitfalls

- `clickable install` pushes the **already-built** click; `launch` only starts
  it. Neither rebuilds. And building without installing leaves the OLD click
  on the device. Version numbers don't change during dev, so nothing warns
  you — verify with file mtimes: `adb shell ls -la /opt/click.ubuntu.com/<app>/<ver>/...`.
- Chaining needs the subcommand: `clickable chain build install launch`
  (bare `clickable build install launch` is a usage error).
- **With no device on ADB, `clickable build` silently builds for the host
  arch (amd64).** When the device is disconnected, always pass `--arch arm64`.
- The app's runtime logs:
  `adb shell journalctl --user -u 'lomiri-app-launch--application-click--<name>_<app>_<ver>--.service'`.
  PyOtherSide prints full Python tracebacks there; QML errors too.

## Device & app lifecycle (source of mysterious hangs)

- **Lomiri SIGSTOPs unfocused apps** (ps state `T`). Consequences:
  - A plain `kill` stays pending forever → use `kill -9`.
  - `clickable launch` (and chain's launch step) can hang for minutes waiting
    for Lomiri to resume/focus the app — typically because the screen is off.
    Remedy: `kill -9` the old process, wake the screen, relaunch.
  - An app that "keeps failing after the fix" may be the old *suspended*
    process resuming with old code.
- Wake the screen over adb:
  `gdbus call --system --dest com.canonical.Unity.Screen
  --object-path /com/canonical/Unity/Screen
  --method com.canonical.Unity.Screen.setScreenPowerMode on 42`
- `adb shell` has no session bus; raw `lomiri-app-launch` fails with D-Bus
  errors. Either use `clickable launch` or
  `export DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/32011/bus` first
  (32011 = phablet's uid).
- adb shell is unconfined: you can run the app's vendored Python directly for
  headless backend tests on-device:
  `cd /opt/click.ubuntu.com/<app>/<ver>/src && python3 -c "import sys; sys.path[:0]=['vendor','.']; import backend; ..."`.

## AppArmor / confinement

- Policy groups live at `/usr/share/apparmor/easyprof/policygroups/ubuntu/2404.1/`;
  each file states `# Usage: common|reserved`. The generated profile is at
  `/var/lib/apparmor/profiles/click_<fullname>` — grep it to answer "can the
  app access X?" definitively.
- **Common** (fine for OpenStore): networking, audio, video, content_exchange,
  connectivity, webview, keep-display-on, ...
- **Reserved** (click-review FAILs, needs manual OpenStore vetting):
  ALL `*_files` groups — picture_files, music_files, video_files, and (new in
  2404.1) document_files, plus `_read` variants. There is no other way to
  write removable media: SD access is only
  `/media/<user>/<label>/{Pictures,Documents,Music,Videos}/**` via those
  groups. `skip_review: true` only bypasses the check for sideloading.
- Clipboard: `TextArea.copy()` does NOTHING under confined Wayland. Use
  `Clipboard.push(text)` from Lomiri.Components (common capability, works).
- Content Hub export was flaky on-device (peers listed, hand-off dies);
  writing to a user-visible folder or clipboard was more reliable.

## QML / Lomiri specifics

- `LomiriShape.source` re-renders its image with `sourceFillMode` that
  defaults to **Stretch** — the inner `Image.fillMode` is irrelevant. Set
  `sourceFillMode: LomiriShape.PreserveAspectCrop` for thumbnails.
- Pinch zoom that centres between the fingers: don't scale the Image; use the
  canonical Flickable pattern — imperative `contentWidth/Height`, Image sized
  to content, `flick.resizeContent(w, h, pinch.center)` in `onPinchUpdated`,
  pan with `pinch.previousCenter - pinch.center`, `returnToBounds()` on
  finish. Disable outer pagers (`ListView.interactive: false`) while zoomed.
- Image pager: horizontal ListView + `snapMode: SnapOneItem` +
  `highlightRangeMode: StrictlyEnforceRange`. Keep download state at page
  level (maps keyed by id), not in delegates — recycling destroys delegates
  mid-callback.
- Qt's pixmap cache is keyed by the full URL: bust it after rewriting a file
  at the same path with a `#fragment` (`"file://" + path + "#" + mtime`);
  the fragment never reaches the filesystem.
- PyOtherSide: `addImportPath(Qt.resolvedUrl('../src/vendor'))` before your
  own module; guard against the **import race** — pages instantiated before
  `importModule`'s callback fire "backend is not defined" — gate initial
  calls on a `ready` flag and re-run on `onReadyChanged`. All `py.call`s
  share ONE worker thread: calls serialise in order, which you can exploit
  (current image downloads before prefetch neighbours).
- QtMultimedia (`Video`) exists on UT 24.04 and works via media-hub with the
  common `audio`+`video` policy groups.
- suru icon names: check `/usr/share/icons/suru/actions/scalable/` on device
  (`rotate-left`, `rotate-right`, `media-playback-start` all exist).
- Lint QML on the host: `qmllint-qt5` (Manjaro pkg qt5-declarative) catches
  syntax errors; the device's /usr/bin/qmllint wrapper is broken.
- App icon: plain SVG with the rounded square drawn in (no system masking);
  `Icon=assets/logo.svg` in the .desktop.

## Testing strategy that worked

- Host-side backend tests against an in-process paramiko SFTP server
  (subclass `SFTPServerInterface`; note: paramiko's `SFTPHandle` needs
  `writefile` set for uploads, and implement `rename`/`posix_rename`/`remove`
  if the code uses them). Run in a venv pinned to the same package versions
  as the prebuild.
- What can't be automated on-device: touch input. Verify via journal
  (zero tracebacks), headless backend calls over adb, and ask the human for
  the visual/gesture checks.

## Screenshots & publishing

- `clickable screenshots` pulls the ENTIRE `~/Pictures/Screenshots/` from the
  device — including personal ones, into the repo directory. Move them out
  before any `git add`, and check shots for private data (hostnames, ports,
  usernames) before publishing; redact with solid overwrites, not blur.
- Manifest: `title` = launcher/store name, `description` = store tagline,
  `maintainer` is public. Click versions must strictly increase for updates.
- OpenStore: app name must match manifest `name` exactly; click-review "pass"
  locally predicts a clean automated review (if only common policy groups).
