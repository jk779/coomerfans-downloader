# coomerfans-downloader

Download videos from coomerfans.com creator pages.

## Download

Pre-built binaries are available on the [releases page](https://github.com/jk779/coomerfans-downloader/releases).

### Unverified Downloads

These binaries are **not code-signed**, so you may see warnings from your OS.

**macOS** — If Gatekeeper blocks the binary, run:
```
xattr -d com.apple.quarantine ./coomerfans-downloader-darwin-arm64
```

**Windows** — If SmartScreen blocks the executable, click "More info" → "Run anyway".

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
| `--replace-emojis` | Replace emojis in filenames with words |
| `--filename-length N` | Maximum filename length including extension |
| `-v, --version` | Print version |
| `-h, --help` | Show help |

## Filename cleanup

Illegal characters are removed, spaces are normalized, and emojis are replaced with word equivalents. Use `--replace-emojis` to enable emoji replacement and `--filename-length` to limit filename size.
