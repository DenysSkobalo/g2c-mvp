package github

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	token string
}

func NewClient(token string) *Client {
	return &Client{token: token}
}

func (c *Client) FetchDiff(diffURL string) (string, error) {
	req, err := http.NewRequest("GET", diffURL, nil)
	if err != nil {
		return "", err
	}
	
	req.Header.Set("Authorization", "Bearer "+c.token)
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
