package groq

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/DenysSkobalo/g2c-mvp/internal/domain"
)

//go:embed prompt.txt
var basePrompt string

type Client struct {
	apiKey      string
	model       string
	temperature float32
}

func NewClient(apiKey, model string, temp float32) *Client {
	return &Client{
		apiKey:      apiKey,
		model:       model,
		temperature: temp,
	}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type groqRequest struct {
	Temperature float32   `json:"temperature"`
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
}

func (c *Client) GeneratePost(diff string, job domain.Job) (string, error) {
	contextMarker := "Це завершений Pull Request. Опиши фінальний результат."
	if job.EventType == "push" {
		contextMarker = "Це поточний робочий push розробника (Trunk-Based Development). Сформуй контент як мікро-оновлення в процесі (Build in Public). Фокус на тому, над чим розробник працює прямо зараз."
	}

	sysPrompt := fmt.Sprintf("%s\n\n[Системний контекст події]: %s", basePrompt, contextMarker)
	userPrompt := fmt.Sprintf("Назва/Повідомлення: %s\n\nКод (Diff):\n%s", job.Title, diff)

	payload := groqRequest{
		Temperature: c.temperature,
		Model:       c.model,
		Messages: []message{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if len(result.Choices) > 0 {
		return result.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("empty response from Groq")
}
