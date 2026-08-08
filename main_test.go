package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	got := filenameFor("creator-name", strings.Repeat("a", 120), "https://example.com/video.mp4", "12345678", 1, 100)
	if !strings.HasPrefix(got, "creator-name - ") {
		t.Fatalf("filename %q does not start with creator name", got)
	}
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

func TestPostAlreadyDownloadedIgnoresPartialFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "creator - An old title - 12345678.mp4.part"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if postAlreadyDownloaded(dir, "12345678") {
		t.Fatal("partial file must not be treated as a completed download")
	}
}

func TestFailedDownloadTrackerPersistsAndRemovesRecords(t *testing.T) {
	dir := t.TempDir()
	tracker, err := loadFailedDownloadTracker(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := failedDownload{PostURL: "https://coomerfans.com/p/123/1/x", VideoIndex: 0}
	second := failedDownload{PostURL: "https://coomerfans.com/p/123/1/x", VideoIndex: 1}
	if err := tracker.add(first); err != nil {
		t.Fatal(err)
	}
	if err := tracker.add(second); err != nil {
		t.Fatal(err)
	}

	reloaded, err := loadFailedDownloadTracker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reloaded.list()); got != 2 {
		t.Fatalf("saved failures = %d, want 2", got)
	}
	if err := reloaded.remove(first); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.list(); len(got) != 1 || got[0] != second {
		t.Fatalf("remaining failures = %#v, want %#v", got, []failedDownload{second})
	}
	if err := reloaded.clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(failedDownloadsPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("failure file still exists: %v", err)
	}
}

func TestRateLimitBackoff(t *testing.T) {
	for attempt, want := range map[int]time.Duration{
		1: 20 * time.Second,
		2: 40 * time.Second,
		3: 80 * time.Second,
		4: 160 * time.Second,
		5: 300 * time.Second,
	} {
		if got := rateLimitBackoff(attempt); got != want {
			t.Errorf("rateLimitBackoff(%d) = %s, want %s", attempt, got, want)
		}
	}
}

func TestScrapeDelayStaysWithinJitterRange(t *testing.T) {
	for range 100 {
		got := scrapeDelay()
		minDelay := scrapeDelayBase - scrapeDelayJitter
		maxDelay := scrapeDelayBase + scrapeDelayJitter
		if got < minDelay || got > maxDelay {
			t.Fatalf("scrapeDelay() = %s, want between %s and %s", got, minDelay, maxDelay)
		}
	}
}
