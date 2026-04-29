package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	Chat Chat   `json:"chat"`
	Text string `json:"text"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type GetUpdatesResponse struct {
	Ok     bool     `json:"ok"`
	Result []Update `json:"result"`
}

var (
	telegramToken string
	ghToken       string
	repoOwner     string
	repoName      string
	offsetFile    = "offset.txt"
	downloadDir   = "downloads"
	baseURL       = "https://tapi.bale.ai/bot"
	githubAPIBase = "https://api.github.com"
	httpClient    = &http.Client{Timeout: 60 * time.Second}
	maxParallel   = 5
	maxChunks     = 4
)

func main() {
	telegramToken = os.Getenv("TELEGRAM_TOKEN")
	ghToken = os.Getenv("GITHUB_TOKEN")
	repoOwner = os.Getenv("REPO_OWNER")
	repoName = os.Getenv("REPO_NAME")

	if telegramToken == "" || ghToken == "" || repoOwner == "" || repoName == "" {
		log.Fatal("env missing")
	}

	offset := getOffset()

	resp, err := httpClient.Get(fmt.Sprintf(
		"%s%s/getUpdates?offset=%d&timeout=5",
		baseURL, telegramToken, offset,
	))
	if err != nil {
		log.Println("getUpdates error:", err)
		return
	}
	defer resp.Body.Close()

	var updatesResp GetUpdatesResponse
	json.NewDecoder(resp.Body).Decode(&updatesResp)

	if !updatesResp.Ok || len(updatesResp.Result) == 0 {
		log.Println("No new messages.")
		return
	}

	cancelChats := map[int64]bool{}
	for _, upd := range updatesResp.Result {
		if upd.Message != nil && strings.TrimSpace(upd.Message.Text) == "/cancel" {
			cancelChats[upd.Message.Chat.ID] = true
		}
	}

	needCommit := false
	for _, upd := range updatesResp.Result {
		offset = upd.UpdateID + 1
		saveOffset(offset)
		needCommit = true

		if upd.Message == nil || upd.Message.Text == "" {
			continue
		}
		chatID := upd.Message.Chat.ID
		text := upd.Message.Text

		if strings.TrimSpace(text) == "/cancel" {
			continue
		}

		if cancelChats[chatID] {
			sendMessage(chatID, "درخواست دانلود شما لغو شد.")
			continue
		}

		handleMessage(chatID, text)
	}

	if needCommit {
		commitOffsetFile(offset)
	}
}

func handleMessage(chatID int64, text string) {
	switch {
	case strings.HasPrefix(text, "/start"):
		sendMessage(chatID, "سلام! لینک بفرستید تا دانلود کنم.\nبرای لغو دانلود قبل از شروع، /cancel را بزنید.")
		return
	case strings.HasPrefix(text, "/help"):
		sendMessage(chatID, `/simple + لینک‌ها = ساده
/zipall + لینک‌ها = همه در یک zip
/zipeach + لینک‌ها = هر فایل zip جدا
/list = لیست فایل‌ها
/cancel = لغو آخرین درخواست دانلود (در همان چرخه)`)
		return
	case strings.HasPrefix(text, "/list"):
		handleList(chatID)
		return
	}

	mode := ""
	t := text
	if strings.HasPrefix(t, "/simple") {
		mode = "simple"
		t = strings.TrimPrefix(t, "/simple")
	} else if strings.HasPrefix(t, "/zipall") {
		mode = "zipall"
		t = strings.TrimPrefix(t, "/zipall")
	} else if strings.HasPrefix(t, "/zipeach") {
		mode = "zipeach"
		t = strings.TrimPrefix(t, "/zipeach")
	}

	urls := extractURLs(t)
	if len(urls) == 0 {
		sendMessage(chatID, "❌ لینکی پیدا نشد.")
		return
	}

	if mode == "" {
		if len(urls) == 1 {
			mode = "simple"
		} else {
			mode = "zipall"
		}
	}

	sendMessage(chatID, "⏳ در حال دانلود (اسم فایل از سرور گرفته می‌شود) ...")
	filesMap := downloadAll(urls)
	if len(filesMap) == 0 {
		sendMessage(chatID, "❌ دانلود ناموفق.")
		return
	}

	switch mode {
	case "simple":
		commitSimple(chatID, filesMap)
	case "zipall":
		commitZipAll(chatID, filesMap)
	case "zipeach":
		commitZipEach(chatID, filesMap)
	}
}

func commitFileToRepo(path string, content []byte) error {
	apiURL := fmt.Sprintf("%s/repos/%s/%s/contents/%s", githubAPIBase, repoOwner, repoName, path)
	sha := ""
	resp, _ := httpClient.Get(apiURL)
	if resp != nil && resp.StatusCode == 200 {
		var e struct{ SHA string }
		json.NewDecoder(resp.Body).Decode(&e)
		sha = e.SHA
		resp.Body.Close()
	}
	payload := map[string]interface{}{
		"message": fmt.Sprintf("Add %s", path),
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  "main",
	}
	if sha != "" {
		payload["sha"] = sha
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("PUT", apiURL, bytes.NewReader(body))
	req.Header.Set("Authorization", "token "+ghToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func commitSimple(chatID int64, files map[string][]byte) {
	for name, data := range files {
		p := downloadDir + "/" + name
		err := commitFileToRepo(p, data)
		if err != nil {
			sendMessage(chatID, "❌ خطا: "+err.Error())
			continue
		}
		raw := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s", repoOwner, repoName, p)
		sendMessage(chatID, "✅ "+raw)
	}
}

func commitZipAll(chatID int64, files map[string][]byte) {
	data, err := createZipArchive(files)
	if err != nil {
		sendMessage(chatID, "❌ ساخت zip")
		return
	}
	name := fmt.Sprintf("archive_%d.zip", time.Now().Unix())
	p := downloadDir + "/" + name
	if err = commitFileToRepo(p, data); err != nil {
		sendMessage(chatID, "❌ "+err.Error())
		return
	}
	raw := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s", repoOwner, repoName, p)
	sendMessage(chatID, "✅ "+raw)
}

func commitZipEach(chatID int64, files map[string][]byte) {
	suf := time.Now().Unix()
	for name, data := range files {
		a := map[string][]byte{name: data}
		d, _ := createZipArchive(a)
		zipName := fmt.Sprintf("%s_%d.zip", strings.TrimSuffix(name, path.Ext(name)), suf)
		p := downloadDir + "/" + zipName
		if err := commitFileToRepo(p, d); err != nil {
			sendMessage(chatID, "❌ "+err.Error())
			continue
		}
		raw := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s", repoOwner, repoName, p)
		sendMessage(chatID, "✅ "+raw)
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
	cdName := ""
	if contentDisposition != "" {
		re := regexp.MustCompile(`filename\*?=(?:UTF-8'')?["']?([^"'; \s]+)`)
		match := re.FindStringSubmatch(contentDisposition)
		if len(match) > 1 {
			cdName = match[1]
		}
	}
	parsed, _ := url.Parse(urlStr)
	urlName := path.Base(parsed.Path)
	if urlName == "" || urlName == "." {
		urlName = ""
	}
	uuidRegex := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	if cdName != "" && (uuidRegex.MatchString(cdName) || !strings.Contains(cdName, ".")) {
		if urlName != "" && !uuidRegex.MatchString(urlName) {
			return urlName
		}
		return cdName
	}
	if cdName != "" {
		return cdName
	}
	if urlName != "" {
		return urlName
	}
	return "downloaded_file"
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

func createZipArchive(files map[string][]byte) ([]byte, error) {
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	for name, data := range files {
		f, err := w.Create(name)
		if err != nil {
			return nil, err
		}
		f.Write(data)
	}
	w.Close()
	return buf.Bytes(), nil
}

func getOffset() int {
	data, _ := os.ReadFile(offsetFile)
	off, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return off
}

func saveOffset(offset int) {
	os.WriteFile(offsetFile, []byte(strconv.Itoa(offset)), 0644)
}

func commitOffsetFile(off int) {
	saveOffset(off)
}

func sendMessage(chatID int64, text string) {
	d := map[string]interface{}{"chat_id": chatID, "text": text}
	b, _ := json.Marshal(d)
	http.Post(baseURL+telegramToken+"/sendMessage", "application/json", bytes.NewReader(b))
}

func extractURLs(text string) []string {
	re := regexp.MustCompile(`https?://[^\s]+`)
	return re.FindAllString(text, -1)
}

func handleList(chatID int64) {
	u := fmt.Sprintf("%s/repos/%s/%s/contents/%s", githubAPIBase, repoOwner, repoName, downloadDir)
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Authorization", "token "+ghToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		sendMessage(chatID, "❌ خطا.")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		sendMessage(chatID, "پوشه خالی یا خطا.")
		return
	}
	var items []struct {
		Name        string `json:"name"`
		DownloadURL string `json:"download_url"`
		Type        string `json:"type"`
	}
	json.NewDecoder(resp.Body).Decode(&items)
	if len(items) == 0 {
		sendMessage(chatID, "هنوز فایلی نیست.")
		return
	}
	var b strings.Builder
	b.WriteString("📂 فایل‌ها:\n")
	for _, it := range items {
		if it.Type == "file" {
			b.WriteString(fmt.Sprintf("• [%s](%s)\n", it.Name, it.DownloadURL))
		}
	}
	sendMessage(chatID, b.String())
}