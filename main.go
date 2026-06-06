package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"
)

const (
	scrapeDelay    = 1500 * time.Millisecond
	maxDownloads   = 8
	queueMax       = int(maxDownloads * 1.5)
	titleMaxLength = 200
)

var (
	videoExt = regexp.MustCompile(`(?i)\.(mp4|m3u8|webm|mov)`)
	headers  = map[string]string{
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
		"Referer":         "https://coomerfans.com/",
	}
)

// ── HTTP ──────────────────────────────────────────────────────────────────────

func fetch(rawURL string) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			return nil
		},
	}

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
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

// find <meta> by attribute selector, e.g. name="description"
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

var prefixRe = regexp.MustCompile(`(?i)^[^/\-]+[/\-]\s*`)
var illegalChars = regexp.MustCompile(`[/\\:*?"<>|]`)
var multiSpace = regexp.MustCompile(`\s+`)

func sanitizeTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "untitled"
	}
	title = illegalChars.ReplaceAllString(title, "")
	title = multiSpace.ReplaceAllString(title, " ")
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
		fmt.Fprintf(os.Stderr, "  [warn] fetch error for %s: %v\n", postURL, err)
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

	// <video src> and <video><source src> ← primary pattern
	findAll(doc, "video", func(n *html.Node) {
		addVideo(attr(n, "src"))
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "source" {
				addVideo(attr(c, "src"))
			}
		}
	})

	// bare URLs in <script> blocks
	scriptURLRe := regexp.MustCompile(`(?i)https?://[^\s"'<>]+\.(?:mp4|m3u8|webm|mov)[^\s"'<>]*`)
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

var postPathRe = regexp.MustCompile(`^/p/\d+/`)
var pageNumRe = regexp.MustCompile(`[?&]page=(\d+)`)

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
		fmt.Printf("  [page] %s\n", pageURL)

		body, err := fetch(pageURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [warn] %v\n", err)
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

func downloadVideo(rawURL, title string, index int, outputDir string, downloaded *atomic.Int64, mu *sync.Mutex) {
	filename := filenameFor(title, rawURL, index)
	dest := filepath.Join(outputDir, filename)
	part := dest + ".part"

	if _, err := os.Stat(dest); err == nil {
		mu.Lock()
		fmt.Printf("  [skip] %s (already exists)\n", filename)
		mu.Unlock()
		return
	}

	client := &http.Client{Timeout: 120 * time.Second}

	retries := 0
	for {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			mu.Lock()
			fmt.Fprintf(os.Stderr, "  [error] %v\n", err)
			mu.Unlock()
			return
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			mu.Lock()
			fmt.Fprintf(os.Stderr, "  [error] %v\n", err)
			mu.Unlock()
			return
		}

		switch resp.StatusCode {
		case 200:
			f, err := os.Create(part)
			if err != nil {
				resp.Body.Close()
				mu.Lock()
				fmt.Fprintf(os.Stderr, "  [error] %v\n", err)
				mu.Unlock()
				return
			}
			size, err := io.Copy(f, resp.Body)
			f.Close()
			resp.Body.Close()
			if err != nil {
				os.Remove(part)
				mu.Lock()
				fmt.Fprintf(os.Stderr, "  [error] %v\n", err)
				mu.Unlock()
				return
			}
			os.Rename(part, dest)
			downloaded.Add(1)
			mu.Lock()
			fmt.Printf("  [done] %s (%.1f MB)\n", filename, float64(size)/1024/1024)
			mu.Unlock()
			return

		case 429:
			resp.Body.Close()
			if retries >= 10 {
				mu.Lock()
				fmt.Fprintf(os.Stderr, "  [error] gave up after 10 retries for %q\n", title)
				mu.Unlock()
				os.Remove(part)
				return
			}
			retries++
			wait := time.Duration(min(1<<retries*10, 300)) * time.Second
			mu.Lock()
			fmt.Fprintf(os.Stderr, "\n  [429] rate limited, waiting %ds (attempt %d/10) for %q...\n", int(wait.Seconds()), retries, title)
			mu.Unlock()
			time.Sleep(wait)

		default:
			resp.Body.Close()
			mu.Lock()
			fmt.Fprintf(os.Stderr, "  [error] HTTP %d downloading %s\n", resp.StatusCode, filename)
			mu.Unlock()
			return
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	// On Windows, keep terminal open after finish
	defer func() {
		fmt.Print("\nPress Enter to exit...")
		fmt.Scanln()
	}()

	var creatorURL, outputDir string

	// Check CLI args first
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--output-dir":
			if i+1 < len(args) {
				outputDir = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				creatorURL = args[i]
			}
		}
	}

	if creatorURL == "" {
		fmt.Print("Enter creator URL (e.g. https://coomerfans.com/u/onlyfans/1234567/hotbabe96): ")
		fmt.Scanln(&creatorURL)
		creatorURL = strings.TrimSpace(creatorURL)
	}
	if creatorURL == "" {
		fmt.Println("No URL given, exiting.")
		return
	}

	if outputDir == "" {
		fmt.Print("Download folder [./downloads]: ")
		fmt.Scanln(&outputDir)
		outputDir = strings.TrimSpace(outputDir)
		if outputDir == "" {
			outputDir = "./downloads"
		}
	}

	fmt.Println()
	fmt.Println("=== coomerfans crawler ===")
	fmt.Printf("Creator:    %s\n", creatorURL)
	fmt.Printf("Output dir: %s\n", outputDir)
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
	var downloaded atomic.Int64
	var totalVideos int

	// Spin up download workers
	var wg sync.WaitGroup
	for range maxDownloads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range queue {
				downloadVideo(item.url, item.title, item.index, outputDir, &downloaded, &mu)
			}
		}()
	}

	spinner := []string{"|", "/", "-", `\`}
	spinIdx := 0

	// Scrape + enqueue
	for i, postURL := range postLinks {
		mu.Lock()
		fmt.Printf("  [%d/%d] scraping %s ... ", i+1, len(postLinks), postURL)
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

			// Non-blocking send attempt; if full, show spinner and block
			for {
				select {
				case queue <- item:
					goto nextVideo
				default:
					mu.Lock()
					fmt.Printf("\r  [wait] queue full, be patient... %s", spinner[spinIdx%4])
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
	fmt.Printf("Videos downloaded: %d\n", downloaded.Load())
}