package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

type AppConfig struct {
	GitHubSecret      string
	GitHubToken       string
	GroqModel		  string
	GroqKey           string
	TelegramBotToken  string
	TelegramUserID    int64
	Port              string
}

var (
	bot       *tgbotapi.BotAPI
	cfg       AppConfig
	postState sync.Map
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

// --- MODULE 1: Webhook Listener ---
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

	if r.Header.Get("X-GitHub-Event") != "pull_request" {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload PullRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if payload.Action == "closed" && payload.PullRequest.Merged {
		go processPipeline(payload.PullRequest.APIURL, payload.PullRequest.Title, payload.PullRequest.HTMLURL)
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

// --- CORE PIPELINE ---
func processPipeline(diffURL, prTitle, prURL string) {
	rawDiff, err := fetchDiff(diffURL)
	if err != nil {
		log.Printf("Error fetching diff: %v", err)
		return
	}

	cleanDiff := sanitizeDiff(rawDiff)
	generateAndSendPost(cleanDiff, prTitle, prURL)
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

func sanitizeDiff(rawDiff string) string {
	var cleaned bytes.Buffer
	scanner := bufio.NewScanner(strings.NewReader(rawDiff))
	skip := false
	ignored := []string{"go.mod", "go.sum", ".json", ".yaml", ".yml", "_test.go", ".md"}

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
		}
	}
	return cleaned.String()
}

// --- MODULE: AI Connector ---
func generateAndSendPost(diff, title, url string) {
	prompt := fmt.Sprintf("Назва PR: %s\n\nКод (Diff):\n%s", title, diff)
	payload := GroqRequest{
		Model: cfg.GroqModel,
		Messages: []GroqMessage{
			{Role: "system", Content: fetchSystemPrompt()},
			{Role: "user", Content: prompt},
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
		text := fmt.Sprintf("%s\n\n🔗 [Дивитись PR на GitHub](%s)", result.Choices[0].Message.Content, url)
		sendToTelegram(text, diff, title, url)
	}
}

func fetchSystemPrompt() string {
	content, err := os.ReadFile("prompt.txt")
	if err != nil {
		return "Ти — Senior Tech Writer. Напиши короткий пост про цей git diff."
	}
	return string(content)
}

// --- MODULE: Telegram ---
func sendToTelegram(text, originalDiff, title, url string) {
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

	postState.Store(sentMsg.MessageID, map[string]string{"diff": originalDiff, "title": title, "url": url})
}

func telegramCallbackListener() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.CallbackQuery == nil { continue }
		
		msgID := update.CallbackQuery.Message.MessageID
		
		switch update.CallbackQuery.Data {
		case "regen_post":
			if val, ok := postState.Load(msgID); ok {
				d := val.(map[string]string)
				go generateAndSendPost(d["diff"], d["title"], d["url"])
			}
		case "done_post":
			bot.Send(tgbotapi.NewEditMessageReplyMarkup(cfg.TelegramUserID, msgID, tgbotapi.InlineKeyboardMarkup{}))
			postState.Delete(msgID)
		}
		bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
	}
}
