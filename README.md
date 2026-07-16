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
| `-o, --output-dir DIR` | Download directory (default: `./downloads/creator-name/`) |
| `-c, --concurrency N` | Parallel downloads (default: 8) |
| `--filename-length N` | Maximum filename length including extension (default: 100) |
| `-v, --version` | Print version |
| `-h, --help` | Show help |

## Filename cleanup

Only letters, digits, spaces, and `-_.()[]` are retained; all other title characters are removed and spaces are normalized. Each filename ends with ` - POST_ID` before its extension (for example, `This is the title - 12345678.mp4`). Existing downloads are identified by this post ID. Use `--filename-length` to override the 100-character filename limit.
