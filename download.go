package main

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	triggerFile = "trigger_download.txt"
	downloadDir = "downloads"
	httpClient  = &http.Client{Timeout: 60 * time.Second}
	maxParallel = 5
	maxChunks   = 4
)

func main() {
	data, err := os.ReadFile(triggerFile)
	if err != nil {
		log.Fatal("cannot read trigger file")
	}
	lines := strings.Split(string(data), "\n")

	simpleComment := "# اگر لینک های خود را در زیر این کامنت وارد کنید فایل ها بدون تغییر به صورت ساده ذخیره میشوند"
	zipAllComment := "# اگر لینک هارا زیر این کامنت وارد کنید همه فایل ها در یک فایل فشرده زیپ ذخیره میشوند"
	zipEachComment := "# اگر لینک هارا زیر این کامنت وارد کنید هر فایل به صورت یک فایل فشرده زیپ ذخیره میشوند"

	var (
		simpleUrls  []string
		zipAllUrls  []string
		zipEachUrls []string
		currentMode string
	)

	urlRegex := regexp.MustCompile(`https?://[^\s]+`)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if strings.Contains(trimmed, "بدون تغییر") || trimmed == simpleComment {
				currentMode = "simple"
			} else if strings.Contains(trimmed, "همه فایل ها در یک فایل") || trimmed == zipAllComment {
				currentMode = "zipall"
			} else if strings.Contains(trimmed, "هر فایل به صورت یک فایل") || trimmed == zipEachComment {
				currentMode = "zipeach"
			}
			continue
		}
		urls := urlRegex.FindAllString(trimmed, -1)
		switch currentMode {
		case "simple":
			simpleUrls = append(simpleUrls, urls...)
		case "zipall":
			zipAllUrls = append(zipAllUrls, urls...)
		case "zipeach":
			zipEachUrls = append(zipEachUrls, urls...)
		default:
			simpleUrls = append(simpleUrls, urls...)
		}
	}

	os.MkdirAll(downloadDir, 0755)

	if len(simpleUrls) > 0 {
		processSimple(simpleUrls)
	}
	if len(zipAllUrls) > 0 {
		processZipAll(zipAllUrls)
	}
	if len(zipEachUrls) > 0 {
		processZipEach(zipEachUrls)
	}
}

func downloadFileWithChunks(rawURL string) ([]byte, string, error) {
	headReq, _ := http.NewRequest("HEAD", rawURL, nil)
	headResp, err := httpClient.Do(headReq)
	if err != nil {
		return nil, "", err
	}
	headResp.Body.Close()

	finalURL := headResp.Request.URL.String()
	fileName := extractFileName(finalURL, headResp.Header.Get("Content-Disposition"))

	acceptRanges := headResp.Header.Get("Accept-Ranges")
	contentLength := headResp.ContentLength
	if acceptRanges == "bytes" && contentLength > 0 {
		return downloadChunked(rawURL, contentLength, finalURL)
	}

	req, _ := http.NewRequest("GET", rawURL, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	fileName = extractFileName(resp.Request.URL.String(), resp.Header.Get("Content-Disposition"))
	return data, fileName, nil
}

func downloadChunked(rawURL string, totalSize int64, finalURL string) ([]byte, string, error) {
	chunkSize := totalSize / int64(maxChunks)
	if chunkSize < 1024 {
		req, _ := http.NewRequest("GET", rawURL, nil)
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		fname := extractFileName(resp.Request.URL.String(), resp.Header.Get("Content-Disposition"))
		return data, fname, nil
	}

	chunks := make([][]byte, maxChunks)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, maxChunks)

	for i := 0; i < maxChunks; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			start := int64(idx) * chunkSize
			end := start + chunkSize - 1
			if idx == maxChunks-1 {
				end = totalSize - 1
			}
			req, _ := http.NewRequest("GET", rawURL, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
			resp, err := httpClient.Do(req)
			if err != nil {
				errs[idx] = err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusPartialContent {
				errs[idx] = fmt.Errorf("range not supported, status %d", resp.StatusCode)
				return
			}
			chunk, err := io.ReadAll(resp.Body)
			if err != nil {
				errs[idx] = err
				return
			}
			mu.Lock()
			chunks[idx] = chunk
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	for _, e := range errs {
		if e != nil {
			return downloadSingle(rawURL, finalURL)
		}
	}

	buf := make([]byte, 0, totalSize)
	for _, chunk := range chunks {
		buf = append(buf, chunk...)
	}
	fname := extractFileName(finalURL, "")
	return buf, fname, nil
}

func downloadSingle(rawURL, finalURL string) ([]byte, string, error) {
	req, _ := http.NewRequest("GET", rawURL, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	fname := extractFileName(resp.Request.URL.String(), resp.Header.Get("Content-Disposition"))
	return data, fname, nil
}

func extractFileName(urlStr, contentDisposition string) string {
	if contentDisposition != "" {
		re := regexp.MustCompile(`filename\*?=(?:UTF-8'')?["']?([^"'; \s]+)`)
		match := re.FindStringSubmatch(contentDisposition)
		if len(match) > 1 {
			return match[1]
		}
	}
	parsed, _ := url.Parse(urlStr)
	fname := path.Base(parsed.Path)
	if fname == "" || fname == "." {
		fname = "downloaded_file"
	}
	return fname
}

func downloadAll(urls []string) map[string][]byte {
	files := make(map[string][]byte)
	var mu sync.Mutex
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for _, u := range urls {
		wg.Add(1)
		go func(link string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, fname, err := downloadFileWithChunks(link)
			if err != nil {
				log.Printf("download error %s: %v", link, err)
				return
			}
			mu.Lock()
			if _, exists := files[fname]; exists {
				fname = fmt.Sprintf("%d_%s", time.Now().UnixNano(), fname)
			}
			files[fname] = data
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	return files
}

func processSimple(urls []string) {
	files := downloadAll(urls)
	for name, data := range files {
		path := filepath.Join(downloadDir, name)
		if err := os.WriteFile(path, data, 0644); err != nil {
			log.Printf("Error writing file %s: %v", name, err)
		}
	}
	log.Println("Simple download completed.")
}

func processZipAll(urls []string) {
	files := downloadAll(urls)
	if len(files) == 0 {
		return
	}
	zipName := fmt.Sprintf("archive_%d.zip", time.Now().Unix())
	zipPath := filepath.Join(downloadDir, zipName)
	zipFile, err := os.Create(zipPath)
	if err != nil {
		log.Fatal(err)
	}
	defer zipFile.Close()
	w := zip.NewWriter(zipFile)
	for name, data := range files {
		f, err := w.Create(name)
		if err != nil {
			log.Printf("Error creating entry %s: %v", name, err)
			continue
		}
		f.Write(data)
	}
	w.Close()
	log.Println("ZipAll archive created:", zipName)
}

func processZipEach(urls []string) {
	files := downloadAll(urls)
	suffix := time.Now().Unix()
	for name, data := range files {
		zipName := fmt.Sprintf("%s_%d.zip", strings.TrimSuffix(name, filepath.Ext(name)), suffix)
		zipPath := filepath.Join(downloadDir, zipName)
		zipFile, err := os.Create(zipPath)
		if err != nil {
			log.Printf("Error creating zip for %s: %v", name, err)
			continue
		}
		w := zip.NewWriter(zipFile)
		f, err := w.Create(name)
		if err != nil {
			zipFile.Close()
			log.Printf("Error adding %s to zip: %v", name, err)
			continue
		}
		f.Write(data)
		w.Close()
		zipFile.Close()
	}
	log.Println("ZipEach completed.")
}