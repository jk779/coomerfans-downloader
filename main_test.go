package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostIDFromURL(t *testing.T) {
	for _, postURL := range []string{
		"https://coomerfans.com/p/12345678/305271/onlyfans",
		"https://coomerfans.com/p/81544523/305271/onlyfans",
	} {
		if got := postIDFromURL(postURL); got != strings.Split(postURL, "/")[4] {
			t.Fatalf("postIDFromURL(%q) = %q", postURL, got)
		}
	}
}

func TestSanitizeTitleOnlyRetainsWhitelistedCharacters(t *testing.T) {
	got := sanitizeTitle("  Grüß!  This/is 🔥 a  title ❤️ [v2].mp4  ")
	if want := "Grüß Thisis a title [v2].mp4"; got != want {
		t.Fatalf("sanitizeTitle() = %q, want %q", got, want)
	}
}

func TestFilenameIncludesPostIDAndRespectsLimit(t *testing.T) {
	got := filenameFor(strings.Repeat("a", 120), "https://example.com/video.mp4", "12345678", 1, 100)
	if !strings.HasSuffix(got, " - 12345678.mp4") {
		t.Fatalf("filename %q does not end with post ID", got)
	}
	if len([]rune(got)) != 100 {
		t.Fatalf("filename length = %d, want 100", len([]rune(got)))
	}
}

func TestPostAlreadyDownloadedMatchesIDOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "An old title - 12345678.mp4"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !postAlreadyDownloaded(dir, "12345678") {
		t.Fatal("expected matching post ID to be found")
	}
	if postAlreadyDownloaded(dir, "81544523") {
		t.Fatal("unexpected match for a different post ID")
	}
}
