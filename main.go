package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/cavaliergopher/grab/v3"

	"golang.org/x/net/html"
)

var version = "dev" // overridden at build time via -ldflags "-X main.version=x.y.z"

// config holds values parsed from CLI args and interactive prompts.
type config struct {
	creatorURL string
	outputDir  string
	failedOnly bool
}

// ANSI color/style codes
const (
	reset      = "\033[0m"
	bold       = "\033[1m"
	colCyan    = "\033[36m"      // downloading
	colGreen   = "\033[32m"      // done (green-yellow)
	colTeal    = "\033[38;5;28m" // skip (darker green)
	colYellow  = "\033[33m"      // wait
	colRed     = "\033[31m"      // error
	colDefault = "\033[39m"      // normal text
)

func tag(color, label string) string {
	return bold + color + "[" + label + "]" + reset
}

const (
	scrapeDelayBase   = 500 * time.Millisecond
	scrapeDelayJitter = 250 * time.Millisecond
	maxRateLimitRetry = 10
)

var (
	maxDownloads = 8

	videoExt     = regexp.MustCompile(`(?i)\.(mp4|m3u8|webm|mov)`)
	scriptURLRe  = regexp.MustCompile(`(?i)https?://[^\s"'<>]+\.(?:mp4|m3u8|webm|mov)[^\s"'<>]*`)
	postPathRe   = regexp.MustCompile(`^/p/\d+/`)
	postIDRe     = regexp.MustCompile(`^\d+$`)
	pageNumRe    = regexp.MustCompile(`[?&]page=(\d+)`)
	prefixRe     = regexp.MustCompile(`(?i)^[^/\-]+[/\-]\s*`)
	multiSpaceRe = regexp.MustCompile(`\s+`)
	totalRe      = regexp.MustCompile(`Total\s+(\d+)`)

	httpHeaders = map[string]string{
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
		"Referer":         "https://coomerfans.com/",
	}

	scrapeClient = &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			applyHeaders(req)
			return nil
		},
	}
	downloadClient = &http.Client{
		// No overall Timeout – ResponseHeaderTimeout only limits waiting
		// for the first response byte; body transfer runs until completion.
		Transport: &http.Transport{
			ResponseHeaderTimeout: 60 * time.Second,
		},
	}
)

// filterFilenameCharacters retains only ordinary, filesystem-safe title characters.
func filterFilenameCharacters(title string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		switch r {
		case ' ', '-', '_', '.', '(', ')', '[', ']':
			return r
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return -1
	}, title)
}

// truncateTitle truncates title to maxLen runes.
func truncateTitle(title string, maxLen int) string {
	if maxLen <= 0 || len([]rune(title)) <= maxLen {
		return title
	}
	runes := []rune(title)[:maxLen]
	for len(runes) > 0 && runes[len(runes)-1] == ' ' {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

var (
	filenameLengthFlag = 100
)

func applyHeaders(req *http.Request) {
	for k, v := range httpHeaders {
		req.Header.Set(k, v)
	}
}

func scrapeDelay() time.Duration {
	return scrapeDelayBase + time.Duration(rand.Intn(2*int(scrapeDelayJitter/time.Millisecond)+1))*time.Millisecond - scrapeDelayJitter
}

// rateLimitBackoff is shared by scraping and downloads. Attempts start at one:
// 20s, 40s, 80s, 160s, then cap at 300s.
func rateLimitBackoff(attempt int) time.Duration {
	return time.Duration(min((1<<attempt)*10, 300)) * time.Second
}

// ── HTTP ──────────────────────────────────────────────────────────────────────

func fetch(rawURL string, retries ...int) (string, error) {
	return fetchWithWarning(rawURL, nil, retries...)
}

// fetchWithWarning reports retryable HTTP responses to onWarning when provided.
// This lets post scraping keep warnings on the same line format as its post.
func fetchWithWarning(rawURL string, onWarning func(status int, wait time.Duration), retries ...int) (string, error) {
	maxRetries := 3
	if len(retries) > 0 {
		maxRetries = retries[0]
	}
	lastStatus := 0
	rateLimitRetries := 0
	for attempt := 0; attempt < maxRetries; attempt++ {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return "", err
		}
		applyHeaders(req)

		resp, err := scrapeClient.Do(req)
		if err != nil {
			return "", err
		}

		switch resp.StatusCode {
		case 200:
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return "", err
			}
			return string(body), nil
		case 429:
			resp.Body.Close()
			if rateLimitRetries >= maxRateLimitRetry {
				return "", fmt.Errorf("HTTP 429 after %d retries", maxRateLimitRetry)
			}
			rateLimitRetries++
			wait := rateLimitBackoff(rateLimitRetries)
			if onWarning != nil {
				onWarning(resp.StatusCode, wait)
			} else {
				fmt.Printf("\n  "+tag(colRed, "429")+" scraping rate limited, waiting %ds (attempt %d/%d)...\n", int(wait.Seconds()), rateLimitRetries, maxRateLimitRetry)
			}
			time.Sleep(wait)
			attempt-- // 429 retries do not consume ordinary transient-error retries.
			continue
		case 500, 502, 503, 504:
			resp.Body.Close()
			lastStatus = resp.StatusCode
			wait := time.Duration(attempt+1) * 5 * time.Second
			if onWarning != nil {
				onWarning(resp.StatusCode, wait)
			} else {
				fmt.Printf("\n  "+tag(colYellow, "warn")+" HTTP %d, retrying in %ds...\n", resp.StatusCode, int(wait.Seconds()))
			}
			time.Sleep(wait)
		default:
			resp.Body.Close()
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
	}
	return "", fmt.Errorf("HTTP %d after %d retries", lastStatus, maxRetries)
}

func absURL(base, href string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	h, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return b.ResolveReference(h).String()
}

// ── HTML helpers ──────────────────────────────────────────────────────────────

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func findAll(n *html.Node, tag string, fn func(*html.Node)) {
	if n.Type == html.ElementNode && n.Data == tag {
		fn(n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		findAll(c, tag, fn)
	}
}

func findFirst(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirst(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func textContent(n *html.Node) string {
	if n == nil {
		return ""
	}
	var sb strings.Builder
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
	return sb.String()
}

func metaContent(doc *html.Node, attrKey, attrVal string) string {
	var result string
	findAll(doc, "meta", func(n *html.Node) {
		if result != "" {
			return
		}
		if attr(n, attrKey) == attrVal {
			result = attr(n, "content")
		}
	})
	return result
}

// ── Title extraction ──────────────────────────────────────────────────────────

func sanitizeTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "untitled"
	}
	title = filterFilenameCharacters(title)
	title = multiSpaceRe.ReplaceAllString(title, " ")
	title = strings.TrimSpace(title)
	if title == "" {
		return "untitled"
	}
	return title
}

func extractTitle(doc *html.Node) string {
	raw := metaContent(doc, "name", "description")
	if raw == "" {
		raw = metaContent(doc, "property", "og:description")
	}
	if raw == "" {
		if h1 := findFirst(doc, "h1"); h1 != nil {
			raw = textContent(h1)
		}
	}
	return strings.TrimSpace(prefixRe.ReplaceAllString(raw, ""))
}

// ── Creator URL resolution ────────────────────────────────────────────────────

func isURL(input string) bool {
	return strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://")
}

func creatorNameFromURL(url string) string {
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func resolveCreatorURL(creatorName string) (string, error) {
	searchURL := "https://coomerfans.com/?q=" + url.QueryEscape(creatorName)
	body, err := fetch(searchURL)
	if err != nil {
		return "", err
	}

	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("could not parse search results")
	}

	// Walk the tree manually so we can scope the search to the results section.
	// The results are in a <section> with <h2>Names of Models - <name>. Total N</h2>.
	var creatorURL string
	var foundHeader bool

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if creatorURL != "" {
			return
		}
		if n.Type == html.ElementNode {
			if n.Data == "h2" && !foundHeader {
				text := textContent(n)
				if !strings.HasPrefix(text, "Names of Models - ") {
					return
				}
				// Parse "Total N" to check there are actual results
				if m := totalRe.FindStringSubmatch(text); m != nil {
					var total int
					fmt.Sscanf(m[1], "%d", &total)
					if total == 0 {
						return // no results — skip this section
					}
				}
				foundHeader = true
			}
			if foundHeader && n.Data == "section" {
				// We've left the results section — stop walking.
				return
			}
			if foundHeader && n.Data == "div" && attr(n, "class") == "thumb" {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if creatorURL != "" {
						return
					}
					if c.Type == html.ElementNode && c.Data == "a" {
						href := attr(c, "href")
						if strings.HasPrefix(href, "/u/") {
							creatorURL = absURL(searchURL, href)
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(doc)

	if creatorURL == "" {
		return "", fmt.Errorf("no creator found for %q (try entering the full URL)", creatorName)
	}
	return creatorURL, nil
}

// ── Video extraction ──────────────────────────────────────────────────────────

type postResult struct {
	videos      []string
	title       string
	hadWarnings bool
	err         error
}

func extractVideos(postURL string, onWarning func(status int, wait time.Duration)) postResult {
	hadWarnings := false
	body, err := fetchWithWarning(postURL, func(status int, wait time.Duration) {
		hadWarnings = true
		if onWarning != nil {
			onWarning(status, wait)
		}
	})
	if err != nil {
		return postResult{hadWarnings: true, err: err}
	}

	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return postResult{}
	}

	title := sanitizeTitle(extractTitle(doc))
	seen := map[string]bool{}
	var videos []string

	addVideo := func(u string) {
		if u != "" && videoExt.MatchString(u) && !seen[u] {
			seen[u] = true
			videos = append(videos, u)
		}
	}

	// <video src> and <video><source src> <- primary pattern
	findAll(doc, "video", func(n *html.Node) {
		addVideo(attr(n, "src"))
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "source" {
				addVideo(attr(c, "src"))
			}
		}
	})

	// bare URLs in <script> blocks
	findAll(doc, "script", func(n *html.Node) {
		if n.FirstChild != nil {
			for _, match := range scriptURLRe.FindAllString(n.FirstChild.Data, -1) {
				addVideo(match)
			}
		}
	})

	return postResult{videos: videos, title: title, hadWarnings: hadWarnings}
}

// ── Pagination + post link collection ────────────────────────────────────────

func collectPostLinks(creatorURL string) []string {
	var allPosts []string
	seenPages := map[string]bool{}
	pageURL := creatorURL
	seen := map[string]bool{}

	for {
		if seenPages[pageURL] {
			break
		}
		seenPages[pageURL] = true
		fmt.Printf("  "+tag(colDefault, "page")+" %s\n", pageURL)

		body, err := fetch(pageURL)
		if err != nil {
			fmt.Printf("  "+tag(colYellow, "warn")+" %v\n", err)
			break
		}

		doc, err := html.Parse(strings.NewReader(body))
		if err != nil {
			break
		}

		var nextLink string

		findAll(doc, "a", func(n *html.Node) {
			href := attr(n, "href")
			if href == "" {
				return
			}

			// collect post links
			if postPathRe.MatchString(href) {
				full := absURL(pageURL, href)
				if full != "" && !seen[full] {
					seen[full] = true
					allPosts = append(allPosts, full)
				}
			}

			// next page: rel="next"
			if attr(n, "rel") == "next" && nextLink == "" {
				nextLink = href
			}

			// next page: text next/›/»
			text := strings.TrimSpace(textContent(n))
			if nextLink == "" && href != "#" &&
				(strings.EqualFold(text, "next") || text == "›" || text == "»") {
				nextLink = href
			}
		})

		// next page: ?page=N+1
		if nextLink == "" {
			curPage := 1
			if m := pageNumRe.FindStringSubmatch(pageURL); m != nil {
				fmt.Sscanf(m[1], "%d", &curPage)
			}
			nextPageRe := fmt.Sprintf(`[?&]page=%d(&|$)`, curPage+1)
			findAll(doc, "a", func(n *html.Node) {
				if nextLink == "" {
					href := attr(n, "href")
					if matched, _ := regexp.MatchString(nextPageRe, href); matched {
						nextLink = href
					}
				}
			})
		}

		if nextLink == "" {
			break
		}
		pageURL = absURL(pageURL, nextLink)
		time.Sleep(scrapeDelay())
	}

	return allPosts
}

// ── Downloader ────────────────────────────────────────────────────────────────

func postIDFromURL(postURL string) string {
	parsed, err := url.Parse(postURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "p" && postIDRe.MatchString(parts[1]) {
		return parts[1]
	}
	return ""
}

func filenameFor(creatorName, title, rawURL, postID string, index, maxLen int) string {
	ext := ".mp4"
	if u, err := url.Parse(rawURL); err == nil {
		if e := filepath.Ext(u.Path); e != "" {
			ext = strings.ToLower(e)
		}
	}
	base := title
	if base == "" {
		base = fmt.Sprintf("video_%d", index)
	}
	if creatorName != "" {
		base = sanitizeTitle(creatorName) + " - " + base
	}
	suffix := ext
	if postID != "" {
		suffix = " - " + postID + ext
	}
	if maxLen > 0 {
		avail := maxLen - len(suffix)
		if avail > 0 && len([]rune(base)) > avail {
			base = truncateTitle(base, avail)
		}
	}
	return base + suffix
}

// postAlreadyDownloaded deliberately identifies a completed post by its ID,
// regardless of title changes between runs.
func postAlreadyDownloaded(outputDir, postID string) bool {
	if postID == "" {
		return false
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Partial files are intentionally kept as *.part so an interrupted
		// download can be resumed on the next run without being treated as done.
		if strings.HasSuffix(name, ".part") {
			continue
		}
		ext := filepath.Ext(name)
		if ext != "" && strings.HasSuffix(strings.TrimSuffix(name, ext), " - "+postID) {
			return true
		}
	}
	return false
}

type dlStats struct {
	downloaded *atomic.Int64
	active     *atomic.Int64
	found      *atomic.Int64
	totalBytes *atomic.Int64
	failed     *atomic.Int64
}

// failedDownload identifies a video by information that remains valid when a
// signed media URL expires. videoIndex is its zero-based position on the post.
type failedDownload struct {
	PostURL    string `json:"post_url"`
	VideoIndex int    `json:"video_index"`
}

// failedDownloadTracker keeps a small, per-creator retry queue next to the
// downloaded files. All its methods are safe to call from download workers.
type failedDownloadTracker struct {
	mu      sync.Mutex
	path    string
	records map[string]failedDownload
}

func failedDownloadsPath(outputDir string) string {
	return filepath.Join(outputDir, ".failed-downloads.json")
}

func failedDownloadKey(record failedDownload) string {
	return fmt.Sprintf("%s#%d", record.PostURL, record.VideoIndex)
}

func loadFailedDownloadTracker(outputDir string) (*failedDownloadTracker, error) {
	tracker := &failedDownloadTracker{
		path:    failedDownloadsPath(outputDir),
		records: make(map[string]failedDownload),
	}
	body, err := os.ReadFile(tracker.path)
	if os.IsNotExist(err) {
		return tracker, nil
	}
	if err != nil {
		return nil, err
	}
	var records []failedDownload
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, fmt.Errorf("could not read failed-downloads file: %w", err)
	}
	for _, record := range records {
		if record.PostURL != "" && record.VideoIndex >= 0 {
			tracker.records[failedDownloadKey(record)] = record
		}
	}
	return tracker, nil
}

func (t *failedDownloadTracker) list() []failedDownload {
	t.mu.Lock()
	defer t.mu.Unlock()
	records := make([]failedDownload, 0, len(t.records))
	for _, record := range t.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].PostURL == records[j].PostURL {
			return records[i].VideoIndex < records[j].VideoIndex
		}
		return records[i].PostURL < records[j].PostURL
	})
	return records
}

func (t *failedDownloadTracker) saveLocked() error {
	if len(t.records) == 0 {
		if err := os.Remove(t.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	records := make([]failedDownload, 0, len(t.records))
	for _, record := range t.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return failedDownloadKey(records[i]) < failedDownloadKey(records[j]) })
	body, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}

func (t *failedDownloadTracker) add(record failedDownload) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records[failedDownloadKey(record)] = record
	return t.saveLocked()
}

func (t *failedDownloadTracker) remove(record failedDownload) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.records, failedDownloadKey(record))
	return t.saveLocked()
}

func (t *failedDownloadTracker) clear() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = make(map[string]failedDownload)
	return t.saveLocked()
}

func (s dlStats) format() string {
	return fmt.Sprintf("[active: %d, done: %d]",
		s.active.Load(), s.downloaded.Load())
}

// summary uses a sliding window: intervalBytes/intervalSecs = speed over last tick period
func (s dlStats) summary(intervalBytes int64, intervalSecs float64) string {
	totalMB := float64(s.totalBytes.Load()) / 1024 / 1024
	intervalMB := float64(intervalBytes) / 1024 / 1024
	mbps := intervalMB / intervalSecs
	return fmt.Sprintf("active: %d, done: %d, failed: %d, %.1f MB total @ %.1f MB/s",
		s.active.Load(), s.downloaded.Load(), s.failed.Load(), totalMB, mbps)
}

// downloadProgress tracks only bytes transferred during this run. In
// particular, bytes already present in a resumed .part file are excluded so
// they cannot inflate the displayed transfer rate.
type downloadProgress struct {
	mu        sync.Mutex
	active    map[*grab.Response]int64
	completed int64
}

func newDownloadProgress() *downloadProgress {
	return &downloadProgress{active: make(map[*grab.Response]int64)}
}

func (p *downloadProgress) add(resp *grab.Response) {
	p.mu.Lock()
	p.active[resp] = resp.BytesComplete()
	p.mu.Unlock()
}

func (p *downloadProgress) remove(resp *grab.Response) {
	p.mu.Lock()
	if start, ok := p.active[resp]; ok {
		p.completed += max(0, resp.BytesComplete()-start)
		delete(p.active, resp)
	}
	p.mu.Unlock()
}

func (p *downloadProgress) snapshot() (bytes int64, active int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	bytes = p.completed
	for resp, start := range p.active {
		bytes += max(0, resp.BytesComplete()-start)
	}
	return bytes, len(p.active)
}

// statusReporter maintains one sticky terminal line at the bottom of an
// interactive terminal, without moving the cursor used for normal log output.
type statusReporter struct {
	stats     dlStats
	progress  *downloadProgress
	maxActive int
	outputMu  *sync.Mutex
	enabled   bool
	rows      int
	stop      chan struct{}
	done      chan struct{}
}

func newStatusReporter(stats dlStats, progress *downloadProgress, maxActive int, outputMu *sync.Mutex) *statusReporter {
	info, err := os.Stdout.Stat()
	return &statusReporter{
		stats:     stats,
		progress:  progress,
		maxActive: maxActive,
		outputMu:  outputMu,
		enabled:   err == nil && info.Mode()&os.ModeCharDevice != 0,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

func (r *statusReporter) Printf(format string, args ...any) {
	r.outputMu.Lock()
	fmt.Printf(format, args...)
	r.outputMu.Unlock()
}

func (r *statusReporter) Start() {
	if !r.enabled {
		close(r.done)
		return
	}
	rows, err := terminalRows()
	if err != nil || rows < 2 {
		r.enabled = false
		close(r.done)
		return
	}
	r.outputMu.Lock()
	// Start the dedicated layout with a clean screen so existing shell output
	// is not gradually overwritten by the scrolling log region.
	fmt.Print("\033[2J\033[H")
	r.setLayoutLocked(rows)
	r.outputMu.Unlock()
	go r.run()
}

func (r *statusReporter) Stop() {
	if r.enabled {
		close(r.stop)
	}
	<-r.done
}

func (r *statusReporter) run() {
	defer close(r.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	lastTime := time.Now()
	lastBytes, _ := r.progress.snapshot()
	for {
		select {
		case now := <-ticker.C:
			bytes, active := r.progress.snapshot()
			seconds := now.Sub(lastTime).Seconds()
			rate := float64(bytes-lastBytes) / seconds / 1024 / 1024
			r.outputMu.Lock()
			// The scroll region reserves the final row for progress.
			// The log cursor is restored immediately after each redraw.
			fmt.Printf("\0337\033[%d;1H\033[2K"+tag(colCyan, "progress")+" downloading: %d/%d | completed: %d/%d | failed: %d | %.1f MB/s\0338",
				r.rows,
				active, r.maxActive, r.stats.downloaded.Load(), r.stats.found.Load(), r.stats.failed.Load(), rate)
			r.outputMu.Unlock()
			lastTime, lastBytes = now, bytes
		case <-r.stop:
			r.outputMu.Lock()
			fmt.Printf("\0337\033[%d;1H\033[2K\033[r\0338", r.rows)
			r.outputMu.Unlock()
			return
		}
	}
}

func (r *statusReporter) setLayoutLocked(rows int) {
	// Restrict normal output to every row except the final progress row.
	fmt.Printf("\033[1;%dr", rows-1)
	r.rows = rows
}

func terminalRows() (int, error) {
	if runtime.GOOS == "windows" {
		return 0, fmt.Errorf("terminal layout is not supported on Windows")
	}
	cmd := exec.Command("stty", "size")
	cmd.Stdin = os.Stdin
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	var rows, columns int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(output)), "%d %d", &rows, &columns); err != nil || rows < 2 {
		return 0, fmt.Errorf("could not determine terminal size")
	}
	return rows, nil
}

func buildDownloadRequest(rawURL string) (*http.Request, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Use video-specific headers for signed URLs (e= and hash= params),
	// normal scrape headers for plain URLs (e.g. /storager/ without params)
	parsedURL, _ := url.Parse(rawURL)
	q := parsedURL.Query()
	if q.Get("e") != "" && q.Get("hash") != "" {
		req.Header.Set("User-Agent", httpHeaders["User-Agent"])
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Accept-Encoding", "identity;q=1, *;q=0")
		req.Header.Set("Accept-Language", "en-US,en;q=0.6")
		req.Header.Set("Referer", rawURL)
		req.Header.Set("Sec-Ch-Ua", `"Brave";v="149", "Chromium";v="149", "Not)A;Brand";v="24"`)
		req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
		req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
		req.Header.Set("Sec-Fetch-Dest", "video")
		req.Header.Set("Sec-Fetch-Mode", "no-cors")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Sec-Gpc", "1")
		req.Header.Set("Cache-Control", "no-cache")
		req.Header.Set("Pragma", "no-cache")
	} else {
		for k, v := range httpHeaders {
			req.Header.Set(k, v)
		}
	}
	return req, nil
}

func downloadVideo(rawURL, title, postURL, creatorName string, index, videoIndex int, outputDir string, stats dlStats, progress *downloadProgress, reporter *statusReporter, failedTracker *failedDownloadTracker) {
	stats.active.Add(1)
	defer stats.active.Add(-1)

	filename := filenameFor(creatorName, title, rawURL, postIDFromURL(postURL), index, filenameLengthFlag)
	finalDest := filepath.Join(outputDir, filename)
	partialDest := finalDest + ".part"

	grabClient := grab.NewClient()
	grabClient.HTTPClient = downloadClient

	// errorf prints a concise error line followed by file context for debugging.
	errorf := func(soFar int64, reason string) {
		stats.failed.Add(1)
		if err := failedTracker.add(failedDownload{PostURL: postURL, VideoIndex: videoIndex}); err != nil {
			reporter.Printf("  "+tag(colYellow, "warn")+" could not save failed download: %v\n", err)
		}
		reporter.Printf("\n  "+tag(colRed, "error")+" %s\n", stats.format())
		reporter.Printf("  download of video %q failed because %s\n", title, reason)
		reporter.Printf("  -> post:  %s\n", postURL)
		reporter.Printf("  -> video: %s\n", rawURL)
		reporter.Printf("  -> downloaded so far: %.1f MB\n", float64(soFar)/1024/1024)
	}

	retries := 0
	for {
		// grab resumes an existing destination automatically. Keeping that
		// destination as *.part prevents an interrupted file from looking done.
		req, err := grab.NewRequest(partialDest, rawURL)
		if err != nil {
			errorf(0, err.Error())
			return
		}

		// Apply correct headers via a pre-built http.Request
		httpReq, err := buildDownloadRequest(rawURL)
		if err != nil {
			errorf(0, err.Error())
			return
		}
		req.HTTPRequest.Header = httpReq.Header

		resp := grabClient.Do(req)
		// A response with a successful HTTP status has an initialized transfer
		// that can safely be sampled by the live progress reporter.
		trackProgress := resp.HTTPResponse != nil && resp.HTTPResponse.StatusCode >= 200 && resp.HTTPResponse.StatusCode < 300
		if trackProgress {
			progress.add(resp)
		}
		err = resp.Err()
		if trackProgress {
			progress.remove(resp)
		}
		if err != nil {
			soFar := resp.BytesComplete()
			if resp.HTTPResponse != nil && resp.HTTPResponse.StatusCode == 429 {
				if retries >= maxRateLimitRetry {
					errorf(soFar, fmt.Sprintf("gave up after %d retries for %q", maxRateLimitRetry, title))
					return
				}
				retries++
				wait := rateLimitBackoff(retries)
				reporter.Printf("\n  "+tag(colRed, "429")+" rate limited, waiting %ds (attempt %d/%d) for %q...\n",
					int(wait.Seconds()), retries, maxRateLimitRetry, title)
				time.Sleep(wait)
				continue
			}
			errorf(soFar, err.Error())
			return
		}

		if err := os.Rename(partialDest, finalDest); err != nil {
			errorf(resp.BytesComplete(), fmt.Sprintf("could not finalize download: %v", err))
			return
		}

		stats.downloaded.Add(1)
		if err := failedTracker.remove(failedDownload{PostURL: postURL, VideoIndex: videoIndex}); err != nil {
			reporter.Printf("  "+tag(colYellow, "warn")+" could not update failed-downloads file: %v\n", err)
		}
		// Use actual file size on disk – resp.Size() only counts bytes transferred
		// in this session, missing already-downloaded bytes from a previous partial run
		if fi, err := os.Stat(finalDest); err == nil {
			stats.totalBytes.Add(fi.Size())
		} else {
			stats.totalBytes.Add(resp.Size())
		}
		reporter.Printf("\n  "+tag(colGreen, "done")+" %s (%.1f MB) %s\n",
			filename, float64(resp.Size())/1024/1024, stats.format())
		return
	}
}

// ── Input helper ──────────────────────────────────────────────────────────────

func prompt(label string) string {
	fmt.Print(label)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// ── Input ─────────────────────────────────────────────────────────────────────

func parseArgs() *config {
	cfg := &config{}
	var creatorURL, outputDir string

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			fmt.Printf(`coomerfans-downloader %s – download videos from coomerfans.com creator pages

Usage:
  coomerfans-downloader [creator_name_or_url] [options]

Arguments:
  creator_name_or_url     Creator name or URL.  When a name is given,
                            the site search is used to resolve the full URL.
                            e.g. https://coomerfans.com/u/onlyfans/1234567/hotbabe96
                            or simply: hotbabe96

Options:
  -o, --output-dir DIR   Directory for downloaded videos
                           (default: ./creator-name/)
  -c, --concurrency N    Number of parallel downloads (default: 8)
  --filename-length N    Maximum filename length including extension
                           (default: 100)
  --failed-only          Retry saved failed downloads only; do not scrape indexes
  -v, --version          Print version and exit
  -h, --help             Show this help

Filename cleanup:
  Only letters, digits, spaces, and -_.()[] are retained.
  Multiple spaces are collapsed and leading/trailing spaces are trimmed.
  Each filename starts with "CREATOR_NAME - " and ends with " - POST_ID"
  before its extension. Interrupted downloads use an additional .part suffix.

Examples:
  coomerfans hotbabe96
  coomerfans https://coomerfans.com/u/onlyfans/1234567/hotbabe96
  coomerfans hotbabe96 -o ~/Videos/hotbabe86 -c 4
  coomerfans hotbabe96 -o ~/Videos/hotbabe86 -c 12 --filename-length 64

`, version)
			os.Exit(0)
		case "--version", "-v":
			fmt.Println(version)
			os.Exit(0)
		case "-o", "--output-dir":
			if i+1 < len(args) {
				outputDir = args[i+1]
				i++
			}
		case "-c", "--concurrency":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &maxDownloads)
				i++
			}
		case "--filename-length":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &filenameLengthFlag)
				i++
			}
		case "--failed-only":
			cfg.failedOnly = true
		default:
			if !strings.HasPrefix(args[i], "-") {
				if isURL(args[i]) {
					creatorURL = args[i]
				} else {
					resolved, err := resolveCreatorURL(args[i])
					if err != nil {
						fmt.Printf("  "+tag(colRed, "error")+" %v\n", err)
						os.Exit(1)
					}
					creatorURL = resolved
				}
			}
		}
	}

	cfg.creatorURL = creatorURL
	cfg.outputDir = outputDir
	return cfg
}

func resolveInteractive() (string, string) {
	fmt.Printf("coomerfans-downloader %s – download videos from coomerfans.com\n", version)
	fmt.Println("Run with --help for usage information.")
	fmt.Println()
	if maxDownloads > 20 {
		fmt.Printf("\033[1;31mWarning: concurrency set to %d, which may cause server overload. This is bad for everyone. Consider using a lower value.\033[0m\n\n", maxDownloads)
	}

	creatorURL := prompt("Enter creator name or URL (e.g. slikd or https://coomerfans.com/u/onlyfans/1234567/hotbabe96): ")
	if creatorURL == "" {
		fmt.Println("No input given, exiting.")
		os.Exit(1)
	}
	if !isURL(creatorURL) {
		resolved, err := resolveCreatorURL(creatorURL)
		if err != nil {
			fmt.Printf("  "+tag(colRed, "error")+" %v\n", err)
			os.Exit(1)
		}
		creatorURL = resolved
	}

	name := creatorNameFromURL(creatorURL)
	if name == "" {
		name = "unknown"
	}
	outputDir := prompt(fmt.Sprintf("Download folder [./%s]: ", name))
	if outputDir == "" {
		outputDir = filepath.Join(".", name)
	}
	return creatorURL, outputDir
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	if runtime.GOOS == "windows" {
		defer func() {
			fmt.Print("\nPress Enter to exit...")
			bufio.NewReader(os.Stdin).ReadString('\n')
		}()
	}

	cfg := parseArgs()
	// Retrying a saved queue needs only its output directory; it deliberately
	// avoids resolving a creator or scraping an index page.
	if cfg.creatorURL == "" && !(cfg.failedOnly && cfg.outputDir != "") {
		cfg.creatorURL, cfg.outputDir = resolveInteractive()
	}

	if cfg.outputDir == "" {
		name := creatorNameFromURL(cfg.creatorURL)
		if name == "" {
			name = "unknown"
		}
		cfg.outputDir = filepath.Join(".", name)
	}

	fmt.Println()
	fmt.Println("=== coomerfans-downloader ===")
	fmt.Printf("Version:      %s\n", version)
	fmt.Printf("Creator URL:  %s\n", cfg.creatorURL)
	creatorName := creatorNameFromURL(cfg.creatorURL)
	if creatorName != "" {
		fmt.Printf("Creator name: %s\n", creatorName)
	}
	fmt.Printf("Output dir:   %s\n", cfg.outputDir)
	fmt.Printf("Concurrency:  %d\n", maxDownloads)
	fmt.Println()

	if err := os.MkdirAll(cfg.outputDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create output dir: %v\n", err)
		return
	}

	failedTracker, err := loadFailedDownloadTracker(cfg.outputDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load failed-downloads file: %v\n", err)
		return
	}
	if cfg.failedOnly {
		runFailedDownloads(cfg.outputDir, failedTracker)
		return
	}
	if failures := failedTracker.list(); len(failures) > 0 {
		fmt.Printf("Found %d saved failed download(s).\n", len(failures))
		choice := prompt("[1] Retry and exit  [2] Ignore and scrape normally  [3] Delete list and exit: ")
		switch choice {
		case "1":
			runFailedDownloads(cfg.outputDir, failedTracker)
			return
		case "3":
			if err := failedTracker.clear(); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to delete failed-downloads file: %v\n", err)
				return
			}
			fmt.Println("Failed-downloads list deleted.")
			return
		case "", "2":
			// Continue. Newly failed downloads are merged into this list.
		default:
			fmt.Println("Unknown selection; continuing with the normal scrape.")
		}
	}

	runScrapeAndDownload(cfg.creatorURL, cfg.outputDir, failedTracker)
}

func runScrapeAndDownload(creatorURL, outputDir string, failedTracker *failedDownloadTracker) {
	creatorName := creatorNameFromURL(creatorURL)
	if creatorName == "" {
		creatorName = "unknown"
	}
	fmt.Println("Step 1: collecting post links...")
	postLinks := collectPostLinks(creatorURL)
	fmt.Printf("  -> %d posts found\n\n", len(postLinks))

	runDownloads(postLinks, creatorName, outputDir, failedTracker, nil)
}

func runFailedDownloads(outputDir string, failedTracker *failedDownloadTracker) {
	failures := failedTracker.list()
	if len(failures) == 0 {
		fmt.Println("No saved failed downloads.")
		return
	}
	creatorName := filepath.Base(filepath.Clean(outputDir))
	posts := make([]string, 0, len(failures))
	selected := make(map[string]map[int]bool)
	for _, failure := range failures {
		if selected[failure.PostURL] == nil {
			selected[failure.PostURL] = make(map[int]bool)
			posts = append(posts, failure.PostURL)
		}
		selected[failure.PostURL][failure.VideoIndex] = true
	}
	fmt.Printf("Retrying %d saved failed download(s) from %d post(s)...\n\n", len(failures), len(posts))
	runDownloads(posts, creatorName, outputDir, failedTracker, selected)
}

// selected is nil for a normal scrape. Otherwise it limits each freshly read
// post page to the video positions that previously failed.
func runDownloads(postLinks []string, creatorName, outputDir string, failedTracker *failedDownloadTracker, selected map[string]map[int]bool) {
	fmt.Printf("Step 2: reading + downloading (max %d concurrent downloads)...\n\n", maxDownloads)

	var mu sync.Mutex
	stats := dlStats{
		downloaded: &atomic.Int64{},
		active:     &atomic.Int64{},
		found:      &atomic.Int64{},
		totalBytes: &atomic.Int64{},
		failed:     &atomic.Int64{},
	}
	progress := newDownloadProgress()
	reporter := newStatusReporter(stats, progress, maxDownloads, &mu)
	reporter.Start()
	var downloadWG sync.WaitGroup
	downloadSlots := make(chan struct{}, maxDownloads)
	var totalVideos int

	for i, postURL := range postLinks {
		postID := postIDFromURL(postURL)
		postLabel := postID
		if postLabel == "" {
			postLabel = "?"
		}
		logPost := func(color, status, detail string) {
			reporter.Printf("  "+bold+colDefault+"[%d/%d]"+reset+" Post #%s: "+bold+color+"%s"+reset+"%s (%s)\n",
				i+1, len(postLinks), postLabel, status, detail, postURL)
		}
		waitBeforeNextPost := func() {
			if i < len(postLinks)-1 {
				time.Sleep(scrapeDelay())
			}
		}
		result := extractVideos(postURL, func(status int, wait time.Duration) {
			logPost(colYellow, fmt.Sprintf("warning: HTTP %d - retrying in %ds...", status, int(wait.Seconds())), "")
		})

		if result.err != nil {
			logPost(colRed, "warning: "+result.err.Error(), "")
			waitBeforeNextPost()
			continue
		}
		if len(result.videos) == 0 {
			status := "no video"
			if result.hadWarnings {
				status = "warning: no video"
			}
			logPost(colYellow, status, "")
			waitBeforeNextPost()
			continue
		}
		if selected == nil && postAlreadyDownloaded(outputDir, postID) {
			logPost(colTeal, "skip existing", fmt.Sprintf(" (%q)", result.title))
			waitBeforeNextPost()
			continue
		}

		selectedVideos := 0
		for vi := range result.videos {
			if selected == nil || selected[postURL][vi] {
				selectedVideos++
			}
		}
		if selectedVideos == 0 {
			logPost(colYellow, "no matching failed video", "")
			waitBeforeNextPost()
			continue
		}
		status := "downloading"
		if selectedVideos > 1 {
			status = fmt.Sprintf("downloading %d videos", selectedVideos)
		}
		logPost(colCyan, status, fmt.Sprintf(" (%q)", result.title))

		for vi, u := range result.videos {
			if selected != nil && !selected[postURL][vi] {
				continue
			}
			totalVideos++
			stats.found.Add(1)
			itemIndex := totalVideos
			itemTitle := result.title
			if len(result.videos) > 1 {
				itemTitle = fmt.Sprintf("%s (%d)", result.title, vi+1)
			}

			// Wait for a free slot before starting another download.
			downloadSlots <- struct{}{}
			downloadWG.Add(1)
			go func() {
				defer downloadWG.Done()
				defer func() { <-downloadSlots }()
				downloadVideo(u, itemTitle, postURL, creatorName, itemIndex, vi, outputDir, stats, progress, reporter, failedTracker)
			}()
		}

		waitBeforeNextPost()
	}

	reporter.Printf("\nAll posts received - waiting for the remaining downloads to finish...\n")
	downloadWG.Wait()
	reporter.Stop()

	fmt.Println()
	fmt.Println("=== Done ===")
	fmt.Printf("Posts received:    %d\n", len(postLinks))
	fmt.Printf("Videos found:      %d\n", totalVideos)
	fmt.Printf("Videos downloaded: %d\n", stats.downloaded.Load())
	fmt.Printf("Videos failed:     %d\n", stats.failed.Load())
}
