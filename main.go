package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	_ "embed" 
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

//go:embed prompt.txt
var basePrompt string

type AppConfig struct {
	GitHubSecret     string
	GitHubToken      string
	GroqModel        string
	GroqKey          string
	TelegramBotToken string
	TelegramUserID   int64
	Port             string
}

type Job struct {
	EventType string
	Title     string
	URL       string
	DiffURL   string
}

var (
	bot       *tgbotapi.BotAPI
	cfg       AppConfig
	postState sync.Map
	jobQueue  = make(chan Job, 100) 
)

type PullRequestPayload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Merged  bool   `json:"merged"`
		APIURL  string `json:"url"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
	} `json:"pull_request"`
}

type PushPayload struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	HeadCommit struct {
		Message string `json:"message"`
		URL     string `json:"url"`
	} `json:"head_commit"`
}

type GroqMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type GroqRequest struct {
	Model    string        `json:"model"`
	Messages []GroqMessage `json:"messages"`
}

type GroqResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("WARNING: .env file not found, using system environment variables")
	}

	cfg = loadConfig()

	var err error
	bot, err = tgbotapi.NewBotAPI(cfg.TelegramBotToken)
	if err != nil {
		log.Fatalf("CRITICAL: Telegram Bot init failed: %v", err)
	}

	log.Printf("Initialized G2C MVP on @%s", bot.Self.UserName)

	go telegramCallbackListener()
	initWorkerPool(5) 

	http.HandleFunc("/webhook", githubWebhookHandler)

	log.Printf("Server listening on :%s", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, nil); err != nil {
		log.Fatalf("CRITICAL: HTTP server crashed: %v", err)
	}
}

func loadConfig() AppConfig {
	userID, err := strconv.ParseInt(os.Getenv("TELEGRAM_USER_ID"), 10, 64)
	if err != nil {
		log.Fatalf("CRITICAL: Invalid TELEGRAM_USER_ID: %v", err)
	}

	model := os.Getenv("GROQ_MODEL")
	if model == "" {
		model = "llama-3.3-70b-versatile"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	return AppConfig{
		GitHubSecret:     os.Getenv("GITHUB_WEBHOOK_SECRET"),
		GitHubToken:      os.Getenv("GITHUB_PERSONAL_TOKEN"),
		GroqKey:          os.Getenv("GROQ_API_KEY"),
		GroqModel:        model,
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramUserID:   userID,
		Port:             port,
	}
}

// --- MODULE 1: Webhook Router & Listener ---
func githubWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Can't read body", http.StatusBadRequest)
		return
	}

	signature := r.Header.Get("X-Hub-Signature-256")
	if !validateGitHubSignature(signature, body, cfg.GitHubSecret) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")

	switch eventType {
	case "pull_request":
		var payload PullRequestPayload
		if err := json.Unmarshal(body, &payload); err == nil {
			if payload.Action == "closed" && payload.PullRequest.Merged {
				jobQueue <- Job{
					EventType: "pull_request",
					Title:     payload.PullRequest.Title,
					DiffURL:   payload.PullRequest.APIURL,
					URL:       payload.PullRequest.HTMLURL,
				}
			}
		}

	case "push":
		var payload PushPayload
		if err := json.Unmarshal(body, &payload); err == nil {
			if payload.Before != "0000000000000000000000000000000000000000" {
				diffURL := fmt.Sprintf("https://api.github.com/repos/%s/compare/%s...%s", payload.Repository.FullName, payload.Before, payload.After)
				jobQueue <- Job{
					EventType: "push",
					Title:     payload.HeadCommit.Message,
					DiffURL:   diffURL,
					URL:       payload.HeadCommit.URL,
				}
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}

func validateGitHubSignature(signature string, payload []byte, secret string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature[7:]), []byte(expectedMAC))
}

// --- MODULE 2: Worker Pool & Pipeline ---
func initWorkerPool(numWorkers int) {
	for i := 0; i < numWorkers; i++ {
		go func(workerID int) {
			for job := range jobQueue {
				processPipeline(job)
			}
		}(i)
	}
}

func processPipeline(job Job) {
	rawDiff, err := fetchDiff(job.DiffURL)
	if err != nil {
		log.Printf("[Error] Fetching diff failed for %s: %v", job.URL, err)
		return
	}

	cleanDiff, entropy := sanitizeDiff(rawDiff)
	
	if entropy < 10 {
		log.Printf("[Info] Skipped job due to low entropy (Changes: %d). URL: %s", entropy, job.URL)
		return
	}

	generateAndSendPost(cleanDiff, job)
}

func fetchDiff(diffURL string) (string, error) {
	req, _ := http.NewRequest("GET", diffURL, nil)
	req.Header.Set("Authorization", "Bearer "+cfg.GitHubToken)
	req.Header.Set("Accept", "application/vnd.github.v3.diff")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

func sanitizeDiff(rawDiff string) (string, int) {
	var cleaned bytes.Buffer
	scanner := bufio.NewScanner(strings.NewReader(rawDiff))
	skip := false
	ignored := []string{"go.mod", "go.sum", ".json", ".yaml", ".yml", "_test.go", ".md"}
	entropy := 0

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "diff --git") {
			skip = false
			for _, ext := range ignored {
				if strings.HasSuffix(strings.TrimSpace(line), ext) {
					skip = true
					break
				}
			}
		}
		if !skip {
			cleaned.WriteString(line + "\n")
			if (strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")) ||
			   (strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")) {
				entropy++
			}
		}
	}
	return cleaned.String(), entropy
}

// --- MODULE 3: AI Connector ---
func generateAndSendPost(diff string, job Job) {
	contextMarker := "Це завершений Pull Request. Опиши фінальний результат."
	if job.EventType == "push" {
		contextMarker = "Це поточний робочий push розробника (Trunk-Based Development). Сформуй контент як мікро-оновлення в процесі (Build in Public). Фокус на тому, над чим розробник працює прямо зараз."
	}

	sysPrompt := fmt.Sprintf("%s\n\n[Системний контекст події]: %s", basePrompt, contextMarker)
	userPrompt := fmt.Sprintf("Назва/Повідомлення: %s\n\nКод (Diff):\n%s", job.Title, diff)

	payload := GroqRequest{
		Model: cfg.GroqModel,
		Messages: []GroqMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	jsonData, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+cfg.GroqKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Groq API error: %v", err)
		return
	}
	defer resp.Body.Close()

	var result GroqResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if len(result.Choices) > 0 {
		text := fmt.Sprintf("%s\n\n🔗 [Дивитись зміни на GitHub](%s)", result.Choices[0].Message.Content, job.URL)
		sendToTelegram(text, diff, job)
	}
}

// --- MODULE 4: Telegram ---
func sendToTelegram(text, originalDiff string, job Job) {
	msg := tgbotapi.NewMessage(cfg.TelegramUserID, text)
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 Регенерувати", "regen_post"),
			tgbotapi.NewInlineKeyboardButtonData("🎯 Взяв у роботу", "done_post"),
		),
	)
	msg.ParseMode = "Markdown"

	sentMsg, err := bot.Send(msg)
	if err != nil {
		log.Printf("Telegram error: %v", err)
		return
	}

	jobData, _ := json.Marshal(job)
	postState.Store(sentMsg.MessageID, map[string]string{"diff": originalDiff, "job": string(jobData)})
}

func telegramCallbackListener() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.CallbackQuery == nil {
			continue
		}

		msgID := update.CallbackQuery.Message.MessageID

		switch update.CallbackQuery.Data {
		case "regen_post":
			if val, ok := postState.Load(msgID); ok {
				d := val.(map[string]string)
				var job Job
				json.Unmarshal([]byte(d["job"]), &job)
				go generateAndSendPost(d["diff"], job)
			}
		case "done_post":
			bot.Send(tgbotapi.NewEditMessageReplyMarkup(cfg.TelegramUserID, msgID, tgbotapi.InlineKeyboardMarkup{}))
			postState.Delete(msgID)
		}
		bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
	}
}
