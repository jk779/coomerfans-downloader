# CLAUDE.md — coomerfans-downloader

## Project Overview
This is a video downloader for coomer.fans (and similar sites). It started as a Ruby prototype and was later rewritten in Go.

## Branch Structure
- **`main`** — Go application (production code)
- **`prototype`** — Ruby prototype (historical, kept for reference)

## Key Files (main branch)
| File | Purpose |
|------|---------|
| `main.go` | Core Go application |
| `go.mod` / `go.sum` | Go module dependencies |
| `compile.sh` | Build script (cross-compilation for darwin/linux/windows) |
| `dist/` | Compiled binaries (`.gitkeep`) |
| `LICENSE` | Project license |
| `.gitignore` | Git ignore rules |

## Key Files (prototype branch)
| File | Purpose |
|------|---------|
| `coomerfans_crawler.rb` | Original Ruby crawler script |
| `Gemfile` / `Gemfile.lock` | Ruby dependencies |
| `.ruby-lsp/` | Ruby Language Server Protocol tooling |
| `.tool-versions` | asdf version specification |

## Constraints
- **Never push changes.** Only the user should decide when to push. Branches and commits can be rewritten freely.
- The `downloads/` directory contains actual downloaded videos and is managed separately.

## Git History
- `main`: 10 commits, starting from "lets go to GO! binaries for windows etc."
- `prototype`: 8 commits, starting from "First version" (Ruby)
