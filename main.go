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

	"golang.org/x/net/html"
)

var version = "dev" // overridden at build time via -ldflags "-X main.version=x.y.z"

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
	scrapeDelay    = 1500 * time.Millisecond
	titleMaxLength = 200
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
	multiSpaceRe = regexp.MustCompile(`\s+`)

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
	downloadClient = &http.Client{Timeout: 30 * time.Second}
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

func sanitizeTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "untitled"
	}
	title = illegalRe.ReplaceAllString(title, "")
	title = multiSpaceRe.ReplaceAllString(title, " ")
	title = strings.TrimSpace(title)
	runes := []rune(title)
	if len(runes) > titleMaxLength {
		runes = runes[:titleMaxLength]
	}
	return strings.TrimSpace(string(runes))
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
			nextPageRe := regexp.MustCompile(fmt.Sprintf(`[?&]page=%d(&|$)`, curPage+1))
			findAll(doc, "a", func(n *html.Node) {
				if nextLink == "" {
					href := attr(n, "href")
					if nextPageRe.MatchString(href) {
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

func filenameFor(title, rawURL string, index int) string {
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
	return base + ext
}

type dlStats struct {
	downloaded *atomic.Int64
	active     *atomic.Int64
	queued     func() int // returns current queue length
}

func (s dlStats) format() string {
	return fmt.Sprintf("[queued: %d, active: %d, done: %d]",
		s.queued(), s.active.Load(), s.downloaded.Load())
}

func downloadVideo(rawURL, title string, index int, outputDir string, stats dlStats, mu *sync.Mutex) {
	stats.active.Add(1)
	defer stats.active.Add(-1)

	filename := filenameFor(title, rawURL, index)
	dest := filepath.Join(outputDir, filename)
	part := dest + ".part"

	retries := 0
	for {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			mu.Lock()
			fmt.Printf("  "+tag(colRed, "error")+" %v %s\n", err, stats.format())
			mu.Unlock()
			return
		}
		applyHeaders(req)

		resp, err := downloadClient.Do(req)
		if err != nil {
			mu.Lock()
			fmt.Printf("  "+tag(colRed, "error")+" %v %s\n", err, stats.format())
			mu.Unlock()
			return
		}

		switch resp.StatusCode {
		case 200:
			f, err := os.Create(part)
			if err != nil {
				resp.Body.Close()
				mu.Lock()
				fmt.Printf("  "+tag(colRed, "error")+" %v %s\n", err, stats.format())
				mu.Unlock()
				return
			}
			size, err := io.Copy(f, resp.Body)
			f.Close()
			resp.Body.Close()
			if err != nil {
				os.Remove(part)
				mu.Lock()
				fmt.Printf("  "+tag(colRed, "error")+" %v %s\n", err, stats.format())
				mu.Unlock()
				return
			}
			os.Rename(part, dest)
			stats.downloaded.Add(1)
			mu.Lock()
			fmt.Printf("  "+tag(colGreen, "done")+" %s (%.1f MB) %s\n", filename, float64(size)/1024/1024, stats.format())
			mu.Unlock()
			return

		case 429:
			resp.Body.Close()
			if retries >= 10 {
				mu.Lock()
				fmt.Printf("  "+tag(colRed, "error")+" gave up after 10 retries for %q %s\n", title, stats.format())
				mu.Unlock()
				os.Remove(part)
				return
			}
			retries++
			wait := time.Duration(min(1<<retries*10, 300)) * time.Second
			mu.Lock()
			fmt.Printf("\n  "+tag(colRed, "429")+" rate limited, waiting %ds (attempt %d/10) for %q...\n",
				int(wait.Seconds()), retries, title)
			mu.Unlock()
			time.Sleep(wait)

		default:
			resp.Body.Close()
			mu.Lock()
			fmt.Printf("  "+tag(colRed, "error")+" HTTP %d downloading %s %s\n", resp.StatusCode, filename, stats.format())
			mu.Unlock()
			return
		}
	}
}

// ── Input helper ──────────────────────────────────────────────────────────────

func prompt(label string) string {
	fmt.Print(label)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	// On Windows, keep terminal open after finish
	if runtime.GOOS == "windows" {
		defer func() {
			fmt.Print("\nPress Enter to exit...")
			bufio.NewReader(os.Stdin).ReadString('\n')
		}()
	}

	var creatorURL, outputDir string

	// Parse CLI args
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
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
		default:
			if !strings.HasPrefix(args[i], "-") {
				creatorURL = args[i]
			}
		}
	}

	// Recalculate queueMax after potential -c override
	queueMax = maxDownloads + maxDownloads/2

	if creatorURL == "" {
		creatorURL = prompt("Enter creator URL (e.g. https://coomerfans.com/u/onlyfans/1234567/hotbabe96): ")
	}
	if creatorURL == "" {
		fmt.Println("No URL given, exiting.")
		return
	}

	if outputDir == "" {
		outputDir = prompt("Download folder [./downloads]: ")
		if outputDir == "" {
			outputDir = "./downloads"
		}
	}

	fmt.Println()
	fmt.Println("=== coomerfans crawler ===")
	fmt.Printf("Version:      %s\n", version)
	fmt.Printf("Creator:      %s\n", creatorURL)
	fmt.Printf("Output dir:   %s\n", outputDir)
	fmt.Printf("Concurrency:  %d\n", maxDownloads)
	fmt.Println()

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create output dir: %v\n", err)
		return
	}

	fmt.Println("Step 1: collecting post links...")
	postLinks := collectPostLinks(creatorURL)
	fmt.Printf("  -> %d posts found\n\n", len(postLinks))

	fmt.Printf("Step 2: scraping + downloading (max %d concurrent downloads)...\n\n", maxDownloads)

	type queueItem struct {
		url   string
		title string
		index int
	}
	queue := make(chan queueItem, queueMax)

	var mu sync.Mutex
	stats := dlStats{
		downloaded: &atomic.Int64{},
		active:     &atomic.Int64{},
		queued:     func() int { return len(queue) },
	}
	var totalVideos int

	// Spin up download workers
	var wg sync.WaitGroup
	for range maxDownloads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range queue {
				downloadVideo(item.url, item.title, item.index, outputDir, stats, &mu)
			}
		}()
	}

	spinner := []string{"|", "/", "-", `\`}
	spinIdx := 0

	// Scrape + enqueue
	for i, postURL := range postLinks {
		mu.Lock()
		fmt.Printf("  "+bold+colDefault+"[%d/%d]"+reset+" scraping %s ... ", i+1, len(postLinks), postURL)
		mu.Unlock()

		result := extractVideos(postURL)

		mu.Lock()
		if len(result.videos) > 0 {
			fmt.Printf("%d video(s) %q\n", len(result.videos), result.title)
		} else {
			fmt.Println("no video")
		}
		mu.Unlock()

		for _, u := range result.videos {
			totalVideos++
			item := queueItem{url: u, title: result.title, index: totalVideos}

			// Skip early if file already exists
			dest := filepath.Join(outputDir, filenameFor(item.title, item.url, item.index))
			if _, err := os.Stat(dest); err == nil {
				mu.Lock()
				fmt.Printf("  "+tag(colTeal, "skip")+" %s (already exists)\n", item.title)
				mu.Unlock()
				continue
			}

			// Non-blocking send; if full, spin until a slot opens
			for {
				select {
				case queue <- item:
					mu.Lock()
					fmt.Printf("  "+tag(colCyan, "enqueued")+" %s %s\n", item.title, stats.format())
					mu.Unlock()
					goto nextVideo
				default:
					mu.Lock()
					fmt.Printf("\r  "+tag(colYellow, "wait")+" queue full, be patient... %s", spinner[spinIdx%4])
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
	fmt.Println("All posts scraped - waiting for the last downloads to finish...")
	close(queue)
	wg.Wait()

	fmt.Println()
	fmt.Println("=== Done ===")
	fmt.Printf("Posts scraped:     %d\n", len(postLinks))
	fmt.Printf("Videos found:      %d\n", totalVideos)
	fmt.Printf("Videos downloaded: %d\n", stats.downloaded.Load())
}
