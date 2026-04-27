// download.go
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
)

func main() {
	data, err := os.ReadFile(triggerFile)
	if err != nil {
		log.Fatal("cannot read trigger file")
	}
	lines := strings.Split(string(data), "\n")

	// جداکننده‌های بخش‌ها
	simpleComment := "# اگر لینک های خود را در زیر این کامنت وارد کنید فایل ها بدون تغییر به صورت ساده ذخیره میشوند"
	zipAllComment := "# اگر لینک هارا زیر این کامنت وارد کنید همه فایل ها در یک فایل فشرده زیپ ذخیره میشوند"
	zipEachComment := "# اگر لینک هارا زیر این کامنت وارد کنید هر فایل به صورت یک فایل فشرده زیپ ذخیره میشوند"

	// حالت‌ها
	var (
		simpleUrls []string
		zipAllUrls []string
		zipEachUrls []string
		currentMode string
	)

	urlRegex := regexp.MustCompile(`https?://[^\s]+`)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			// تشخیص کامنت‌های مود
			if strings.Contains(trimmed, "بدون تغییر") || trimmed == simpleComment {
				currentMode = "simple"
			} else if strings.Contains(trimmed, "همه فایل ها در یک فایل") || trimmed == zipAllComment {
				currentMode = "zipall"
			} else if strings.Contains(trimmed, "هر فایل به صورت یک فایل") || trimmed == zipEachComment {
				currentMode = "zipeach"
			}
			continue
		}
		// استخراج لینک‌ها از خط
		urls := urlRegex.FindAllString(trimmed, -1)
		switch currentMode {
		case "simple":
			simpleUrls = append(simpleUrls, urls...)
		case "zipall":
			zipAllUrls = append(zipAllUrls, urls...)
		case "zipeach":
			zipEachUrls = append(zipEachUrls, urls...)
		default:
			// بدون حالت مشخص، پیش‌فرض ساده
			simpleUrls = append(simpleUrls, urls...)
		}
	}

	os.MkdirAll(downloadDir, 0755)

	// پردازش هر گروه
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

func downloadFile(urlStr string) ([]byte, error) {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func guessFileName(rawURL string) string {
	parsed, _ := url.Parse(rawURL)
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
		go func(dlURL string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := downloadFile(dlURL)
			if err != nil {
				log.Printf("Error downloading %s: %v", dlURL, err)
				return
			}
			fname := guessFileName(dlURL)
			mu.Lock()
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