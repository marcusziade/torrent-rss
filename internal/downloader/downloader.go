package downloader

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

type Downloader struct {
	client      *http.Client
	downloadDir string
	baseURL     string
	authCookie  string
}

func extractAuthFromRSS(rssURL string) map[string]string {
	parsedURL, err := url.Parse(rssURL)
	if err != nil {
		return nil
	}

	// Split the query string on semicolons since it's not standard URL formatting
	params := strings.Split(parsedURL.RawQuery, ";")
	auth := make(map[string]string)

	for _, param := range params {
		if strings.Contains(param, "=") {
			parts := strings.SplitN(param, "=", 2)
			auth[parts[0]] = parts[1]
		} else {
			// Handle params without = like "private"
			auth[param] = ""
		}
	}

	return auth
}

func NewDownloader(downloadDir, baseURL, cookieAuth string) (*Downloader, error) {
	jar, err := cookiejar.New(&cookiejar.Options{
		PublicSuffixList: publicsuffix.List,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}

	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create download directory: %w", err)
	}

	return &Downloader{
		client:      client,
		downloadDir: downloadDir,
		baseURL:     baseURL,
		authCookie:  cookieAuth,
	}, nil
}

func (d *Downloader) findDownloadLink(pageURL string) (string, error) {
	torrentID := filepath.Base(pageURL)
	authenticatedURL := fmt.Sprintf("%s/torrent.php?id=%s", d.baseURL, torrentID)

	req, err := http.NewRequest("GET", authenticatedURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("cache-control", "max-age=0")
	req.Header.Set("cookie", d.authCookie)
	req.Header.Set("user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch torrent page: %w", err)
	}
	defer resp.Body.Close()

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
	}

	var downloadLink string
	var crawler func(*html.Node)
	crawler = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {

			for _, attr := range node.Attr {
				if attr.Key == "class" && attr.Val == "dl_Btn" {
					for _, href := range node.Attr {
						if href.Key == "href" {
							// Add base URL to relative path
							downloadLink = fmt.Sprintf("https://www.torrentday.com/%s", strings.TrimPrefix(href.Val, "/"))
							return
						}
					}
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			crawler(c)
		}
	}
	crawler(doc)

	if downloadLink == "" {
		return "", fmt.Errorf("download link not found in HTML")
	}

	return downloadLink, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (d *Downloader) DownloadTorrent(pageURL string) error {
	downloadLink, err := d.findDownloadLink(pageURL)
	if err != nil {
		return fmt.Errorf("failed to find download link: %w", err)
	}

	// Use same auth for download request
	cookieValue := "uid=2550949; pass=8f645a7b1785f3b624c7a151456953c8"

	req, err := http.NewRequest("GET", downloadLink, nil)
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}

	// Use same headers for download
	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("cookie", cookieValue)
	req.Header.Set("user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download torrent: %w", err)
	}
	defer resp.Body.Close()

	// Get original filename and clean it
	origFilename := filepath.Base(downloadLink)
	cleanedFilename := cleanTorrentName(origFilename)

	filepath := filepath.Join(d.downloadDir, cleanedFilename)
	fmt.Printf("Saving as: %s\n", cleanedFilename)

	out, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// cleanTorrentName removes unwanted tags and normalizes the filename format
func cleanTorrentName(filename string) string {
	// First, URL decode the name to handle encoded characters
	decoded, err := url.QueryUnescape(filename)
	if err != nil {
		return filename // fallback to the original name if decoding fails
	}

	// Remove common suffixes, streaming service tags, and redundant info
	cleaned := strings.TrimSuffix(decoded, ".torrent")
	cleaned = strings.ReplaceAll(cleaned, " NF", "")
	cleaned = strings.ReplaceAll(cleaned, " WEB-DL", "")
	cleaned = strings.ReplaceAll(cleaned, " DD 5 1", "")
	cleaned = strings.ReplaceAll(cleaned, " DD 2 0", "")
	cleaned = strings.ReplaceAll(cleaned, " H 264", "")
	cleaned = strings.ReplaceAll(cleaned, "-playWEB", "")
	cleaned = strings.ReplaceAll(cleaned, " 1080p", "")
	cleaned = strings.TrimSpace(cleaned)

	// Use regex to remove unnecessary tokens like quality tags or unwanted extra info
	cleaned = regexp.MustCompile(`\b(1080p|720p|x264|BluRay|HDRip)\b`).ReplaceAllString(cleaned, "")
	cleaned = regexp.MustCompile(`\s+`).ReplaceAllString(cleaned, " ") // replace multiple spaces with a single space

	// Add .torrent extension back
	return cleaned + ".torrent"
}
