package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cavaliergopher/grab/v3"

	"golang.org/x/net/html"
)

var version = "dev" // overridden at build time via -ldflags "-X main.version=x.y.z"

// config holds values parsed from CLI args and interactive prompts.
type config struct {
	creatorURL string
	outputDir  string
}

// ANSI color/style codes
const (
	reset      = "\033[0m"
	bold       = "\033[1m"
	colCyan    = "\033[36m"      // enqueued
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
	scrapeDelay = 1500 * time.Millisecond
)

var (
	maxDownloads = 8
	queueMax     = maxDownloads + maxDownloads/2

	videoExt     = regexp.MustCompile(`(?i)\.(mp4|m3u8|webm|mov)`)
	scriptURLRe  = regexp.MustCompile(`(?i)https?://[^\s"'<>]+\.(?:mp4|m3u8|webm|mov)[^\s"'<>]*`)
	postPathRe   = regexp.MustCompile(`^/p/\d+/`)
	pageNumRe    = regexp.MustCompile(`[?&]page=(\d+)`)
	prefixRe     = regexp.MustCompile(`(?i)^[^/\-]+[/\-]\s*`)
	illegalRe    = regexp.MustCompile(`[/\\:*?"<>|]`)
	multiSpaceRe    = regexp.MustCompile(`\s+`)
	totalRe         = regexp.MustCompile(`Total\s+(\d+)`)

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

// commonEmojis maps frequently seen emojis to word replacements.
var commonEmojis = map[string]string{
	"🔥": "fire", "😈": "smilingdevil", "😛": "tongue", "🍌": "banana",
	"🎥": "camera", "😋": "yum", "🥵": "hot", "😍": "hearteyes",
	"❤️": "heart", "💕": "twohearts", "💋": "kiss", "👅": "tongue2",
	"🍑": "peach", "💦": "sweatdroplets", "✨": "sparkles", "🎵": "note",
	"💪": "muscle", "😏": "smirk", "👀": "eyes", "🐻": "bear",
	"🐰": "rabbit", "🦋": "butterfly", "🌸": "blossom", "☀️": "sunny",
	"🌙": "moon", "⭐": "star", "💀": "skull", "🤤": "drooling",
	"😘": "blowkiss", "🫦": "bitinglip", "🍒": "cherry", "🫣": "peeking",
	"😳": "flushed", "🤭": "handmouth", "😩": "tired", "🥺": "pleading",
	"💅": "nailpolish", "🧚": "fairy", "🎀": "ribbon", "👑": "crown",
	"💖": "sparklingheart", "💗": "growingheart", "💝": "giftheart",
	"🔞": "18", "⚡": "lightning", "🎶": "notes", "🎤": "microphone",
	"🎧": "headphone", "📸": "cameraflash", "👙": "bikini",
	"🎬": "clapper", "🌟": "glowingstar", "💫": "dizzy",
	"🎈": "balloon", "🎉": "party", "🎊": "confetti", "🎁": "gift",
	"🏆": "trophy", "🥇": "medal", "👍": "thumbsup", "👎": "thumbsdown",
	"👏": "clap", "🙌": "raisinghands", "🤝": "handshake",
	"💃": "dancer", "🕺": "dancer2", "📝": "memo", "📌": "pushpin",
	"📎": "paperclip", "✂️": "scissors", "🔒": "locked", "🔓": "unlocked",
	"🔑": "key", "💎": "gem", "📱": "phone", "💻": "laptop",
	"📹": "cam", "🎙️": "mic", "🎭": "masks", "🎨": "palette",
	"📚": "books", "📖": "openbook", "📰": "newspaper", "📛": "badge",
	"🔰": "japan", "⭕": "o", "✅": "check", "❌": "cross",
	"❓": "question", "❗": "exclaim", "💯": "hundred",
	"🔴": "redcircle", "🟢": "greencircle", "🔵": "bluecircle",
	"🔺": "redtriangle", "🔻": "bluetriangle",
	"🏁": "checkered", "🚩": "triangular", "🎌": "crossedflags",
	"🏳️": "whitflag", "🏴": "blackflag", "🏳️‍🌈": "rainbow",
}

// replaceEmojis replaces known emojis with word equivalents.
// Unmapped multi-byte emoji sequences become [emoji].
func replaceEmojis(title string) string {
	var sb strings.Builder
	runes := []rune(title)
	for i := 0; i < len(runes); {
		if word, ok := commonEmojis[string(runes[i])]; ok {
			sb.WriteString(word)
			i++
			continue
		}
		found := false
		for end := i + 2; end <= len(runes); end++ {
			s := string(runes[i:end])
			if word, ok := commonEmojis[s]; ok {
				sb.WriteString(word)
				i = end
				found = true
				break
			}
		}
		if !found {
			sb.WriteRune(runes[i])
			i++
		}
	}
		return sb.String()
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

// Package-level flags set by parseArgs for use by sanitizeTitle.
var (
	sanitizeEmojisFlag  bool
	filenameLengthFlag int
)

func applyHeaders(req *http.Request) {
	for k, v := range httpHeaders {
		req.Header.Set(k, v)
	}
}

// ── HTTP ──────────────────────────────────────────────────────────────────────

func fetch(rawURL string, retries ...int) (string, error) {
	maxRetries := 3
	if len(retries) > 0 {
		maxRetries = retries[0]
	}
	lastStatus := 0
	for attempt := range maxRetries {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return "", err
		}
		applyHeaders(req)

		resp, err := scrapeClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		switch resp.StatusCode {
		case 200:
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return "", err
			}
			return string(body), nil
		case 500, 502, 503, 504:
			lastStatus = resp.StatusCode
			wait := time.Duration(attempt+1) * 5 * time.Second
			fmt.Printf("\n  "+tag(colYellow, "warn")+" HTTP %d, retrying in %ds...\n", resp.StatusCode, int(wait.Seconds()))
			time.Sleep(wait)
		default:
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

func sanitizeTitle(title string, doReplaceEmojis bool, maxLen int) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "untitled"
	}
	title = illegalRe.ReplaceAllString(title, "")
	title = multiSpaceRe.ReplaceAllString(title, " ")
	title = strings.TrimSpace(title)
	if doReplaceEmojis {
		title = replaceEmojis(title)
	}
	if maxLen > 0 {
		title = truncateTitle(title, maxLen-len(".mp4"))
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
	videos []string
	title  string
}

func extractVideos(postURL string) postResult {
	body, err := fetch(postURL)
	if err != nil {
		fmt.Printf("  "+tag(colYellow, "warn")+" fetch error for %s: %v\n", postURL, err)
		return postResult{}
	}

	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return postResult{}
	}

	title := sanitizeTitle(extractTitle(doc), sanitizeEmojisFlag, filenameLengthFlag)
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

	return postResult{videos: videos, title: title}
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
		time.Sleep(scrapeDelay)
	}

	return allPosts
}

// ── Downloader ────────────────────────────────────────────────────────────────

func filenameFor(title, rawURL string, index int, maxLen int) string {
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
	if maxLen > 0 {
		avail := maxLen - len(ext)
		if avail > 0 && len([]rune(base)) > avail {
			base = string([]rune(base)[:avail])
		}
	}
	return base + ext
}

type dlStats struct {
	downloaded *atomic.Int64
	active     *atomic.Int64
	totalBytes *atomic.Int64
	queued     func() int // returns current queue length
}

func (s dlStats) format() string {
	return fmt.Sprintf("[queued: %d, active: %d, done: %d]",
		s.queued(), s.active.Load(), s.downloaded.Load())
}

// summary uses a sliding window: intervalBytes/intervalSecs = speed over last tick period
func (s dlStats) summary(intervalBytes int64, intervalSecs float64) string {
	totalMB := float64(s.totalBytes.Load()) / 1024 / 1024
	intervalMB := float64(intervalBytes) / 1024 / 1024
	mbps := intervalMB / intervalSecs
	return fmt.Sprintf("active: %d, done: %d, %.1f MB total @ %.1f MB/s",
		s.active.Load(), s.downloaded.Load(), totalMB, mbps)
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

func downloadVideo(rawURL, title, postURL string, index int, outputDir string, stats dlStats, mu *sync.Mutex) {
	stats.active.Add(1)
	defer stats.active.Add(-1)

	filename := filenameFor(title, rawURL, index, filenameLengthFlag)
	dest := filepath.Join(outputDir, filename)

	grabClient := grab.NewClient()
	grabClient.HTTPClient = downloadClient

		// errorf prints a concise error line followed by file context for debugging.
		errorf := func(soFar int64, reason string) {
			mu.Lock()
			fmt.Printf("\n  "+tag(colRed, "error")+" %s\n", stats.format())
			fmt.Printf("  download of video %q failed because %s\n", title, reason)
			fmt.Printf("  -> post:  %s\n", postURL)
			fmt.Printf("  -> video: %s\n", rawURL)
			fmt.Printf("  -> downloaded so far: %.1f MB\n", float64(soFar)/1024/1024)
			mu.Unlock()
		}

	retries := 0
	for {
		req, err := grab.NewRequest(dest, rawURL)
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
		if err := resp.Err(); err != nil {
			soFar := resp.BytesComplete()
			if resp.HTTPResponse != nil && resp.HTTPResponse.StatusCode == 429 {
				if retries >= 10 {
					errorf(soFar, fmt.Sprintf("gave up after 10 retries for %q", title))
					return
				}
				retries++
				wait := time.Duration(min(1<<retries*10, 300)) * time.Second
				mu.Lock()
				fmt.Printf("\n  "+tag(colRed, "429")+" rate limited, waiting %ds (attempt %d/10) for %q...\n",
					int(wait.Seconds()), retries, title)
				mu.Unlock()
				time.Sleep(wait)
				continue
			}
			errorf(soFar, err.Error())
			return
		}

		stats.downloaded.Add(1)
		// Use actual file size on disk – resp.Size() only counts bytes transferred
		// in this session, missing already-downloaded bytes from a previous partial run
		if fi, err := os.Stat(dest); err == nil {
			stats.totalBytes.Add(fi.Size())
		} else {
			stats.totalBytes.Add(resp.Size())
		}
		mu.Lock()
		fmt.Printf("\n  "+tag(colGreen, "done")+" %s (%.1f MB) %s\n",
			filename, float64(resp.Size())/1024/1024, stats.format())
		mu.Unlock()
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
			fmt.Printf(`coomerfans-video-downloader %s – download videos from coomerfans.com creator pages

Usage:
  coomerfans [creator_name_or_url] [options]

Arguments:
  creator_name_or_url     Creator name or URL.  When a name is given,
                            the site search is used to resolve the full URL.
                            e.g. https://coomerfans.com/u/onlyfans/1234567/hotbabe96
                            or simply: hotbabe96

Options:
  -o, --output-dir DIR   Directory for downloaded videos
                           (default: ./downloads/creator-name/)
  -c, --concurrency N    Number of parallel downloads (default: 8)
  --replace-emojis       Replace emojis in filenames with words
                           (unmapped emojis become [emoji])
  --filename-length N    Maximum filename length including extension
                           (default: unlimited)
  -v, --version          Print version and exit
  -h, --help             Show this help

Filename cleanup:
  Illegal characters ([\/\\:*?"<>|]) are removed.
  Multiple spaces are collapsed and leading/trailing spaces are trimmed.

Examples:
  coomerfans hotbabe96
  coomerfans https://coomerfans.com/u/onlyfans/1234567/hotbabe96
  coomerfans hotbabe96 -o ~/Videos/hotbabe86 -c 4
  coomerfans hotbabe96 -o ~/Videos/hotbabe86 -c 12 --replace-emojis --filename-length 64
 
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
		case "--sanitize-filenames":
			sanitizeEmojisFlag = true
		case "--filename-length":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &filenameLengthFlag)
				i++
			}
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

	queueMax = maxDownloads + maxDownloads/2
	cfg.creatorURL = creatorURL
	cfg.outputDir = outputDir
	return cfg
}

func resolveInteractive() (string, string) {
	fmt.Printf("coomerfans-video-downloader %s – download videos from coomerfans.com\n", version)
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
	outputDir := prompt(fmt.Sprintf("Download folder [./downloads/%s]: ", name))
	if outputDir == "" {
		outputDir = filepath.Join("./downloads", name)
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
	if cfg.creatorURL == "" {
		cfg.creatorURL, cfg.outputDir = resolveInteractive()
	}

	if cfg.outputDir == "" {
		name := creatorNameFromURL(cfg.creatorURL)
		if name == "" {
			name = "unknown"
		}
		cfg.outputDir = filepath.Join("./downloads", name)
	}

	fmt.Println()
	fmt.Println("=== coomerfans-video-downloader ===")
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

	runScrapeAndDownload(cfg.creatorURL, cfg.outputDir)
}

func runScrapeAndDownload(creatorURL, outputDir string) {
	fmt.Println("Step 1: collecting post links...")
	postLinks := collectPostLinks(creatorURL)
	fmt.Printf("  -> %d posts found\n\n", len(postLinks))

	fmt.Printf("Step 2: reading + downloading (max %d concurrent downloads)...\n\n", maxDownloads)

	type queueItem struct {
		url     string
		title   string
		postURL string
		index   int
	}
	queue := make(chan queueItem, queueMax)

	var mu sync.Mutex
	stats := dlStats{
		downloaded: &atomic.Int64{},
		active:     &atomic.Int64{},
		totalBytes: &atomic.Int64{},
		queued:     func() int { return len(queue) },
	}
	var totalVideos int

	var wg sync.WaitGroup
	for range maxDownloads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range queue {
				downloadVideo(item.url, item.title, item.postURL, item.index, outputDir, stats, &mu)
			}
		}()
	}

	// Background status ticker
	var inWait atomic.Bool
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		var lastBytes int64
		lastTick := time.Now()
		for range ticker.C {
			if inWait.Load() {
				now := time.Now()
				currentBytes := stats.totalBytes.Load()
				intervalBytes := currentBytes - lastBytes
				intervalSecs := now.Sub(lastTick).Seconds()
				lastBytes = currentBytes
				lastTick = now
				mu.Lock()
				fmt.Printf("\n  "+tag(colCyan, "status")+" %s\n", stats.summary(intervalBytes, intervalSecs))
				mu.Unlock()
			}
		}
	}()

	spinner := []string{"|", "/", "-", `\`}
	spinIdx := 0

	for i, postURL := range postLinks {
		inWait.Store(false)
		mu.Lock()
		fmt.Printf("\n  "+bold+colDefault+"[%d/%d]"+reset+" reading %s\n", i+1, len(postLinks), postURL)
		mu.Unlock()

		result := extractVideos(postURL)

		mu.Lock()
		if len(result.videos) > 0 {
			fmt.Printf("  -> %d video(s) %q\n", len(result.videos), result.title)
		} else {
			fmt.Println("  -> no video")
		}
		mu.Unlock()

		for vi, u := range result.videos {
			totalVideos++
			itemTitle := result.title
			if len(result.videos) > 1 {
				itemTitle = fmt.Sprintf("%s (%d)", result.title, vi+1)
			}
			item := queueItem{url: u, title: itemTitle, postURL: postURL, index: totalVideos}

			dest := filepath.Join(outputDir, filenameFor(item.title, item.url, item.index, filenameLengthFlag))
			if _, err := os.Stat(dest); err == nil {
				mu.Lock()
				fmt.Printf("\n  "+tag(colTeal, "skip")+" (already exists) %s\n", item.title)
				mu.Unlock()
				continue
			}

			for {
				select {
				case queue <- item:
					mu.Lock()
					fmt.Printf("\n  "+tag(colCyan, "enqueued")+" %s %s\n", item.title, stats.format())
					mu.Unlock()
					goto nextVideo
				default:
					inWait.Store(true)
					mu.Lock()
					fmt.Printf("\r  "+tag(colYellow, "wait")+" download queue full, be patient... %s", spinner[spinIdx%4])
					mu.Unlock()
					spinIdx++
					time.Sleep(100 * time.Millisecond)
				}
			}
		nextVideo:
		}

		if i < len(postLinks)-1 {
			time.Sleep(scrapeDelay)
		}
	}

	fmt.Println()
	fmt.Println("All posts received - waiting for the last downloads to finish...")
	close(queue)
	wg.Wait()

	fmt.Println()
	fmt.Println("=== Done ===")
	fmt.Printf("Posts received:    %d\n", len(postLinks))
	fmt.Printf("Videos found:      %d\n", totalVideos)
	fmt.Printf("Videos downloaded: %d\n", stats.downloaded.Load())
}

