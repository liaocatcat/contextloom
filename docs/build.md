# Building ContextLoom from Source (Full Platform Matrix)

This page is the complete, copy-paste build reference for ContextLoom. It covers the
**TUI** (pure Go, zero CGO) and the **GUI** desktop shell (Wails/WebView, CGO required)
across every target we support — no shortcuts, no omitted flags.

> Companion pages: [../README.md](../README.md) (project overview) ·
> [architecture.md](architecture.md) (layers and runtime) ·
> [toolchain.md](toolchain.md) (employees/scenes/chains/gates).
> Chinese edition: [build.zh-CN.md](build.zh-CN.md).

## 0. TL;DR

```bash
# TUI — current platform, one command:
make build

# TUI — any target (pure Go, no toolchain extras):
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/contextloom-linux-arm64 ./cmd/contextloom

# GUI — current platform (needs a C toolchain + WebView dev libs):
CGO_ENABLED=1 go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui

# GUI — Windows from any host (needs mingw-w64; -H windowsgui is mandatory):
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o dist/contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui
```

Two build units, two rules:

| Unit | Source | CGO | Cross-compiles? |
|---|---|---|---|
| TUI `contextloom` | `./cmd/contextloom` | **off** (`CGO_ENABLED=0`) | Yes, to every Go target |
| GUI `contextloom-gui` | `./cmd/contextloom-gui` | **on** (`CGO_ENABLED=1`) | Only with a matching cross C toolchain (mingw-w64 for Windows) |

## 1. Prerequisites

- **Go 1.22+** (`go version`). The module is `github.com/contextloom/contextloom-agent` (see `go.mod`).
- **TUI**: nothing else. Pure Go, no C toolchain, no system libraries.
- **GUI**: a C compiler plus the platform WebView headers:
  - macOS: Xcode Command Line Tools (`xcode-select --install`).
  - Windows: mingw-w64 (native, or for cross-compiling from macOS/Linux).
  - Linux: GTK 3 + WebKitGTK 2 dev packages (see §3.3).

Fetch dependencies once:

```bash
go mod download
```

## 2. TUI Core (Pure Go — `CGO_ENABLED=0` on every platform)

The TUI (`./cmd/contextloom`) is pure Go and cross-compiles natively. Every command below
follows the same shape:

```
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -ldflags="-s -w" -o dist/contextloom-<os>-<arch>[.exe] ./cmd/contextloom
```

### 2.1 Full cross-compile matrix

**macOS**

```bash
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o dist/contextloom-darwin-arm64 ./cmd/contextloom
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-darwin-amd64 ./cmd/contextloom
```

**Linux — x86 and ARM family**

```bash
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-linux-amd64   ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w" -o dist/contextloom-linux-arm64   ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=386   go build -ldflags="-s -w" -o dist/contextloom-linux-386     ./cmd/contextloom

# 32-bit ARM: pick the ARM revision with GOARM (5 = ARMv5, 6 = ARMv6, 7 = ARMv7)
CGO_ENABLED=0 GOOS=linux   GOARCH=arm GOARM=5 go build -ldflags="-s -w" -o dist/contextloom-linux-armv5 ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=arm GOARM=6 go build -ldflags="-s -w" -o dist/contextloom-linux-armv6 ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o dist/contextloom-linux-armv7 ./cmd/contextloom
```

**Linux — other architectures**

```bash
CGO_ENABLED=0 GOOS=linux   GOARCH=ppc64le  go build -ldflags="-s -w" -o dist/contextloom-linux-ppc64le  ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=s390x    go build -ldflags="-s -w" -o dist/contextloom-linux-s390x    ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=riscv64  go build -ldflags="-s -w" -o dist/contextloom-linux-riscv64  ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=loong64  go build -ldflags="-s -w" -o dist/contextloom-linux-loong64  ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=mips64le go build -ldflags="-s -w" -o dist/contextloom-linux-mips64le ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=mipsle   go build -ldflags="-s -w" -o dist/contextloom-linux-mipsle   ./cmd/contextloom
```

**Windows**

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-windows-amd64.exe ./cmd/contextloom
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags="-s -w" -o dist/contextloom-windows-arm64.exe ./cmd/contextloom
CGO_ENABLED=0 GOOS=windows GOARCH=386   go build -ldflags="-s -w" -o dist/contextloom-windows-386.exe   ./cmd/contextloom
```

> ⚠️ **Do NOT add `-H windowsgui` to a Windows TUI build.** The TUI is a *console*
> program. `-H windowsgui` switches the PE to the GUI subsystem (Subsystem=2), which
> detaches the console and breaks all terminal input/output. Only the GUI binary gets
> that flag (§3.2).

**BSD family**

```bash
CGO_ENABLED=0 GOOS=freebsd   GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-freebsd-amd64   ./cmd/contextloom
CGO_ENABLED=0 GOOS=freebsd   GOARCH=arm64 go build -ldflags="-s -w" -o dist/contextloom-freebsd-arm64   ./cmd/contextloom
CGO_ENABLED=0 GOOS=netbsd    GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-netbsd-amd64    ./cmd/contextloom
CGO_ENABLED=0 GOOS=openbsd   GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-openbsd-amd64   ./cmd/contextloom
CGO_ENABLED=0 GOOS=dragonfly GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-dragonfly-amd64 ./cmd/contextloom
```

**Solaris, AIX, Plan 9**

```bash
CGO_ENABLED=0 GOOS=solaris GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-solaris-amd64 ./cmd/contextloom
CGO_ENABLED=0 GOOS=aix     GOARCH=ppc64 go build -ldflags="-s -w" -o dist/contextloom-aix-ppc64     ./cmd/contextloom
CGO_ENABLED=0 GOOS=plan9   GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-plan9-amd64   ./cmd/contextloom
```

### 2.2 One-shot batch build (all TUI targets)

Iterate a `GOOS/GOARCH` table in a shell loop. The `.exe` suffix is applied only to Windows.

```bash
#!/usr/bin/env bash
# build-all-tui.sh — build the TUI for every supported target into dist/
set -euo pipefail
mkdir -p dist

# "GOOS/GOARCH[/GOARM]" entries
targets=(
  darwin/amd64   darwin/arm64
  linux/amd64    linux/arm64   linux/386
  linux/arm/5    linux/arm/6   linux/arm/7
  linux/ppc64le  linux/s390x   linux/riscv64   linux/loong64
  linux/mips64le linux/mipsle
  windows/amd64  windows/arm64 windows/386
  freebsd/amd64  freebsd/arm64
  netbsd/amd64   openbsd/amd64 dragonfly/amd64
  solaris/amd64  aix/ppc64     plan9/amd64
)

for t in "${targets[@]}"; do
  IFS='/' read -r os arch arm <<< "$t"
  out="dist/contextloom-${os}-${arch}"
  [ "$arch" = "arm" ] && [ -n "${arm:-}" ] && out="dist/contextloom-${os}-armv${arm}"
  [ "$os" = "windows" ] && out="${out}.exe"

  echo "==> ${os}/${arch}${arm:+/GOARM=$arm} -> ${out}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" ${arm:+GOARM="$arm"} \
    go build -ldflags="-s -w" -o "$out" ./cmd/contextloom
done
echo "✅ TUI builds complete."
```

### 2.3 Makefile wrappers

```bash
make build   # TUI binary for the current platform -> ./contextloom
make run     # go run ./cmd/contextloom
make test    # go test -race -cover ./...
make lint    # golangci-lint run ./...
make clean   # rm -f contextloom
```

## 3. GUI Desktop Shell (Wails/WebView — CGO required)

The GUI (`./cmd/contextloom-gui`) is a Wails v2 desktop shell. It is **not** a standalone
app: at launch it locates the `contextloom` backend (see §6), starts it headless if needed,
and renders the frontend. Because Wails binds to the platform WebView, it needs CGO and a
per-platform C toolchain — you cannot produce a macOS/Linux GUI binary without building on
that platform (or with its toolchain).

The production build tag is `-tags production` (strips the Wails dev server).

### 3.1 macOS (native build)

```bash
# GUI binary for the current macOS architecture
CGO_ENABLED=1 go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui

# Explicit architecture (build on the matching host, or with a C cross toolchain)
CGO_ENABLED=1 GOARCH=arm64 go build -tags production -ldflags="-s -w" -o dist/contextloom-gui-darwin-arm64 ./cmd/contextloom-gui
CGO_ENABLED=1 GOARCH=amd64 go build -tags production -ldflags="-s -w" -o dist/contextloom-gui-darwin-amd64 ./cmd/contextloom-gui

# Universal binary (merge both slices with lipo)
lipo -create -output dist/contextloom-gui-darwin-universal \
  dist/contextloom-gui-darwin-arm64 dist/contextloom-gui-darwin-amd64

# Package a double-clickable .app bundle -> dist/ContextLoom.app
make build-gui-macos          # runs scripts/build-macos-app.sh
```

`scripts/build-macos-app.sh` builds both `contextloom` and `contextloom-gui` into
`dist/ContextLoom.app/Contents/MacOS/`, writes `Info.plist`, and marks them executable. On
macOS, double-clicking a **bare** binary makes LaunchServices open it with Terminal.app; the
`.app` bundle launches it as a proper GUI app with no console window.

### 3.2 Windows (native, or cross-compiled — both need mingw-w64)

```bash
# Native on Windows (mingw-w64 / MSYS2 installed), amd64:
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o dist/contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui

# Cross-compile the same from macOS/Linux (install mingw-w64 first):
#   Debian/Ubuntu:  sudo apt install gcc-mingw-w64-x86-64
#   macOS (brew):   brew install mingw-w64
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o dist/contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui

# Windows on ARM64 (aarch64 mingw-w64):
CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc GOOS=windows GOARCH=arm64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o dist/contextloom-gui-windows-arm64.exe ./cmd/contextloom-gui

# Makefile shortcut (cross-compile amd64 + PE check):
make build-gui-win
```

> ⚠️ **`-H windowsgui` is a make-or-break flag.** A Windows GUI build without it produces a
> console-subsystem executable (PE `Subsystem=3`); double-clicking it always pops up a black
> command-line window. The flag is applied by the Go **linker based on the target
> `GOOS=windows`**, regardless of the build host — so a cross-compile from Linux/macOS needs
> it exactly the same as a native Windows build.
>
> Historical note: an earlier Makefile gated this flag behind `ifeq ($(OS),Windows_NT)`, which
> silently dropped it whenever the Windows exe was produced by cross-compilation or a plain
> `go build`. The current Makefile applies `GUI_LDFLAGS := -H windowsgui` unconditionally.

### 3.3 Linux (native build — WebKitGTK dev libraries required)

```bash
# Prerequisites — Debian / Ubuntu:
sudo apt update
sudo apt install libgtk-3-dev libwebkit2gtk-4.0-dev gcc pkg-config

# Prerequisites — Fedora / RHEL:
sudo dnf install gtk3-devel webkit2gtk4.0-devel gcc pkgconf-pkg-config

# Prerequisites — Arch:
sudo pacman -S gtk3 webkit2gtk base-devel pkgconf

# Build
CGO_ENABLED=1 go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui
```

> On a headless Linux host (no display / no WebView), the GUI shell automatically falls back
> to **browser mode** — it starts the backend and serves the UI over HTTP, opening it in the
> system browser instead of a desktop window.

### 3.4 PE subsystem verification (regression guard)

Any Windows GUI build should be checked: the PE `Subsystem` must be **2 (GUI)**, not 3 (CONSOLE).

```bash
go run ./scripts/pecheck dist/contextloom-gui-windows-amd64.exe        # expect: subsystem=2(GUI) OK
go run ./scripts/pecheck dist/contextloom-gui-windows-amd64.exe 2      # explicit expected value
```

`scripts/pecheck` reads the PE Optional Header `Subsystem` field directly (offset 68) and
exits non-zero on mismatch, so a dropped `-H windowsgui` fails the build instead of shipping a
windowed app that spawns a console.

The Makefile runs `pecheck` automatically:

```bash
make build-gui       # builds contextloom-gui (host) then verifies PE subsystem
make build-gui-win   # cross-compiles the Windows GUI then verifies PE subsystem
make verify-gui      # verifies an already-built contextloom-gui.exe
```

## 4. Build Matrix Cheat Sheet

| Target | CGO | Extra dependencies | Key flags |
|---|---|---|---|
| TUI, any platform | `CGO_ENABLED=0` | none | `-ldflags="-s -w"` |
| GUI, macOS (arm64/amd64) | `CGO_ENABLED=1` | Xcode CLT | `-tags production` |
| GUI, macOS universal | `CGO_ENABLED=1` | Xcode CLT + `lipo` | `-tags production` + `lipo -create` |
| GUI, Windows amd64 | `CGO_ENABLED=1` | mingw-w64 (`x86_64-w64-mingw32-gcc`) | `-tags production -ldflags="… -H windowsgui"` |
| GUI, Windows arm64 | `CGO_ENABLED=1` | aarch64 mingw-w64 (`aarch64-w64-mingw32-gcc`) | same as above |
| GUI, Linux | `CGO_ENABLED=1` | `libgtk-3-dev`, `libwebkit2gtk-4.0-dev`, `pkg-config` | `-tags production` |

## 5. Makefile Targets Cheat Sheet

| Target | What it does |
|---|---|
| `make build` | Build the TUI for the current platform (`./contextloom`). |
| `make run` | `go run ./cmd/contextloom`. |
| `make test` | `go test -race -cover ./...`. |
| `make lint` | `golangci-lint run ./...`. |
| `make clean` | Remove the local `contextloom` binary. |
| `make build-gui` | Build the GUI for the host, then run `pecheck` on the produced exe. |
| `make build-gui-win` | Cross-compile the Windows GUI (amd64, mingw-w64) + `pecheck`. |
| `make verify-gui` | Run `pecheck` on an already-built `contextloom-gui.exe`. |
| `make build-gui-macos` | Package `dist/ContextLoom.app` via `scripts/build-macos-app.sh`. |

## 6. Runtime Layout and Environment Variables

```
deploy/
  contextloom-gui        # GUI launcher (Wails desktop shell)
  contextloom            # backend the GUI starts on demand
  config.yaml            # optional; copied from config.example.yaml
```

- **Same directory:** the GUI looks for the `contextloom` backend next to its own executable
  first (`os.Executable()`), so keep both binaries in the same folder.
- **`CONTEXTLOOM_BIN`:** if the backend is elsewhere, point the launcher at it explicitly:

  ```bash
  CONTEXTLOOM_BIN=/opt/contextloom/contextloom ./contextloom-gui
  ```

  Lookup order is: **same directory → `CONTEXTLOOM_BIN` → `PATH`**.
- **Headless Linux fallback:** with no display/WebView, the shell serves the UI over HTTP and
  opens the system browser (or run it explicitly with `--browser`).
- **Config discovery:** the GUI resolves `config.yaml` next to the real (symlink-resolved)
  executable, then the CWD, then built-in defaults. Copy `config.example.yaml` to
  `config.yaml` and fill in your model/API keys.

## 7. Common Pitfalls and Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Double-clicking the Windows GUI pops a black console window | `-H windowsgui` was dropped → PE `Subsystem=3` | Rebuild with `-H windowsgui`; verify with `go run ./scripts/pecheck <exe>` (expect Subsystem=2). |
| `gcc: command not found` / `cgo: C compiler not found` | CGO disabled or no C toolchain | Set `CGO_ENABLED=1` and install gcc/Clang/mingw-w64. |
| `Package webkit2gtk-4.0 was not found` (Linux) | WebKitGTK dev headers missing | Install `libwebkit2gtk-4.0-dev` (Debian) / `webkit2gtk4.0-devel` (Fedora). |
| Cross-compiling the GUI for macOS/Linux fails | CGO cross-compilation needs a matching cross toolchain | Build the GUI on its target OS, or use a proper cross C toolchain. Only the **Windows** GUI cross-compiles cleanly (via mingw-w64). |
| TUI won't interact on Windows after building | `-H windowsgui` was added to the TUI | Remove it — the TUI is a console program. |
| `contextloom` backend not found at GUI launch | Binaries are in different directories | Put both in the same directory, or set `CONTEXTLOOM_BIN`. |
| Wrong ARM variant runs on an old board | `GOARM` mismatch | Use `GOARM=5` for ARMv5, `6` for ARMv6, `7` for ARMv7. |
| GUI reads the wrong `config.yaml` after launching via a symlink | Symlink resolution | The launcher resolves the real executable path first; keep `config.yaml` next to the real binary or the CWD. |

## 8. Docker Build Example

The repository ships no `Dockerfile`, so here is a multi-stage example. The TUI stage needs
only Go (pure Go, `CGO_ENABLED=0`); the GUI stage additionally needs the WebKitGTK dev
libraries.

```dockerfile
# syntax=docker/dockerfile:1

# ---- Stage 1: build the TUI (pure Go, no CGO) ----
FROM golang:1.22-bookworm AS tui
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /out/contextloom ./cmd/contextloom

# ---- Stage 2: build the GUI (needs GTK3 + WebKitGTK) ----
FROM golang:1.22-bookworm AS gui
RUN apt-get update && apt-get install -y --no-install-recommends \
      libgtk-3-dev libwebkit2gtk-4.0-dev gcc pkg-config \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -tags production -ldflags="-s -w" \
    -o /out/contextloom-gui ./cmd/contextloom-gui

# ---- Stage 3: runtime (TUI + GUI launcher) ----
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      libgtk-3-0 libwebkit2gtk-4.0-37 ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=tui /out/contextloom       /app/contextloom
COPY --from=gui /out/contextloom-gui   /app/contextloom-gui
WORKDIR /app
# Headless hosts fall back to browser mode; the TUI can be run directly:
#   docker run -it --rm <image> /app/contextloom
ENTRYPOINT ["/app/contextloom-gui"]
```

Build and run:

```bash
docker build -t contextloom:local .
docker run -it --rm contextloom:local /app/contextloom   # run the TUI instead
```

## 9. Related Documents

- [../README.md](../README.md) — project overview and the short "Building on All Platforms" section.
- [architecture.md](architecture.md) — layer overview, runtime core, UI shells.
- [toolchain.md](toolchain.md) — employees, scenes, toolchains, and quality gates.
- [plugins.md](plugins.md) — plugin file locations and packaging.
- [build.zh-CN.md](build.zh-CN.md) — Chinese edition of this page.

## Key Takeaways

- **TUI**: `CGO_ENABLED=0`, pure Go, cross-compiles to every target in §2.1 — never add `-H windowsgui`.
- **GUI**: `CGO_ENABLED=1` + `-tags production`; Windows needs mingw-w64 and the mandatory `-H windowsgui` (verify with `scripts/pecheck`, Subsystem=2).
- **Keep `contextloom` and `contextloom-gui` in the same directory** (or set `CONTEXTLOOM_BIN`).
- **Headless Linux** falls back to browser mode automatically.
