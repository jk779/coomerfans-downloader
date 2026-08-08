# coomerfans-downloader

Download videos from coomerfans.com creator pages.

## Download

Pre-built binaries are available on the [releases page](https://github.com/jk779/coomerfans-downloader/releases).

### Unverified Downloads

These binaries are **not code-signed**, but are built and released by a Github Action. You will see warnings from your OS when first opening them.

**macOS**: Gatekeeper will block the binary. Run this command to disable Gatekeeper for this file.
```
xattr -d com.apple.quarantine ~/Downloads/coomerfans-downloader-darwin-arm64
```

**Windows**: If SmartScreen blocks the executable, click "More info" → "Run anyway".

## Build from source

```sh
make                     # default: macOS Apple Silicon, Linux x86-64, Windows x86-64
make VERSION=1.2.0
make TARGETS="darwin/arm64 windows/arm64" VERSION=1.2.0
```

Binaries are written to `dist/`. Run `make help` for the available options.

## Usage

```
coomerfans-downloader [creator_name_or_url] [options]
```

## Examples

```
coomerfans-downloader hotbabe96
coomerfans-downloader https://coomerfans.com/u/onlyfans/1234567/hotbabe96
coomerfans-downloader hotbabe96 -o ~/Videos -c 4
```

## Options

| Flag | Description |
|------|-------------|
| `-o, --output-dir DIR` | Download directory (default: `./creator-name/`) |
| `-c, --concurrency N` | Parallel downloads (default: 8) |
| `--filename-length N` | Maximum filename length including extension (default: 100) |
| `--failed-only` | Retry only the saved failed downloads; does not scrape creator indexes |
| `-v, --version` | Print version |
| `-h, --help` | Show help |

## Filename cleanup

Only letters, digits, spaces, and `-_.()[]` are retained; all other title characters are removed and spaces are normalized. Each filename starts with `CREATOR_NAME - ` and ends with ` - POST_ID` before its extension (for example, `creator-name - This is the title - 12345678.mp4`). Existing downloads are identified by this post ID. Interrupted downloads keep an additional `.part` suffix and are resumed on the next run; only completed files are treated as existing downloads. Use `--filename-length` to override the 100-character filename limit.

## Failed downloads

Failures are saved per output directory in `.failed-downloads.json`. On the next normal run, the program offers to retry those items and exit, ignore them for the normal scrape, or delete the list. A retry reopens each saved post detail page to obtain a fresh media URL before downloading. Use `--failed-only` to skip the prompt and retry the queue directly; with `-o DIR` it can run without specifying the creator again.
