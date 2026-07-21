package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/DenysSkobalo/g2c-mvp/config"
	"github.com/DenysSkobalo/g2c-mvp/internal/domain"
)

type Handler struct {
	cfg      config.AppConfig
	jobQueue chan<- domain.Job
}

func NewHandler(cfg config.AppConfig, jobQueue chan<- domain.Job) *Handler {
	return &Handler{cfg: cfg, jobQueue: jobQueue}
}

func (h *Handler) GithubWebhookHandler(w http.ResponseWriter, r *http.Request) {
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
	if !h.validateGitHubSignature(signature, body) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")

	switch eventType {
	case "pull_request":
		var payload domain.PullRequestPayload
		if err := json.Unmarshal(body, &payload); err == nil {
			if payload.Action == "closed" && payload.PullRequest.Merged {
				h.jobQueue <- domain.Job{
					EventType: "pull_request",
					Title:     payload.PullRequest.Title,
					DiffURL:   payload.PullRequest.APIURL,
					URL:       payload.PullRequest.HTMLURL,
				}
			}
		}

	case "push":
		var payload domain.PushPayload
		if err := json.Unmarshal(body, &payload); err == nil {
			if payload.Before != "0000000000000000000000000000000000000000" {
				diffURL := fmt.Sprintf("https://api.github.com/repos/%s/compare/%s...%s", payload.Repository.FullName, payload.Before, payload.After)
				h.jobQueue <- domain.Job{
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

func (h *Handler) validateGitHubSignature(signature string, payload []byte) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.cfg.GitHubSecret))
	mac.Write(payload)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature[7:]), []byte(expectedMAC))
}
