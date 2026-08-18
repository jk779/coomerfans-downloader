# coomerfans-downloader

Download videos from coomerfans.com creator pages.

## Installation

### macOS / Linux

Install the latest release with:

```sh
curl -fL https://get.coomerfans.download/bash | bash
```

The installer:

- detects your operating system and architecture
- downloads the latest matching release
- installs it as `~/.local/bin/coomerfans-downloader`
- makes the binary executable
- checks whether `~/.local/bin` is already in your `PATH`
- asks before modifying your shell profile if a `PATH` update is needed

No `sudo` or system-wide installation is used.

Supported installer targets:

- macOS Apple Silicon (`darwin/arm64`)
- Linux x86-64 (`linux/amd64`)
- Linux ARM64 (`linux/arm64`)

### Windows

Download the latest Windows executable from the [releases page](https://github.com/jk779/coomerfans-downloader/releases).

If SmartScreen blocks the executable, click **More info** → **Run anyway**.

### Manual downloads

Pre-built binaries for all supported platforms are available on the [releases page](https://github.com/jk779/coomerfans-downloader/releases).

The binaries are built automatically by GitHub Actions and are currently not code-signed.

## Build from source

```sh
make                     # default: macOS Apple Silicon, Linux x86-64/arm64, Windows x86-64
make VERSION=dev
make TARGETS="darwin/arm64 windows/arm64" VERSION=1.2.3
```

Binaries are written to `dist/`. Run `make help` for the available options.

## Usage

```text
coomerfans-downloader [creator_name_or_url] [options]
```

## Examples

```sh
coomerfans-downloader hotbabe96
coomerfans-downloader https://coomerfans.com/u/onlyfans/1234567/hotbabe96
coomerfans-downloader hotbabe96 -o ~/Videos -c 4
```

## Options

| Flag | Description |
|------|-------------|
| `-o, --output-dir DIR` | Download directory (default: `./creator-name/`) |
| `-c, --concurrency N` | Parallel downloads (default: 8, coomerfans limits to 20 concurrent connections) |
| `--filename-length N` | Maximum filename length including extension (default: 100) |
| `--failed-only` | Retry only the saved failed downloads; does not scrape creator |
| `-v, --version` | Print version |
| `-h, --help` | Show help |

## Filename cleanup

Only letters, digits, spaces, and `-_.()[]` are retained; all other title characters are removed and spaces are normalized. Each filename starts with `CREATOR_NAME - ` and ends with ` - POST_ID` before its extension (for example, `creator-name - This is the title - 12345678.mp4`). Existing downloads are identified by this post ID. Interrupted downloads keep an additional `.part` suffix and are resumed on the next run; only completed files are treated as existing downloads. Use `--filename-length` to override the 100-character filename limit.

## Failed downloads

Failures are saved per output directory in `.failed-downloads.json`. On the next normal run, the program offers to retry those items and exit, ignore them for the normal scrape, or delete the list. A retry reopens each saved post detail page to obtain a fresh media URL before downloading. Use `--failed-only` to skip the prompt and retry the queue directly; with `-o DIR` it can run without specifying the creator again.