package domain

type Job struct {
	EventType string `json:"event_type"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	DiffURL   string `json:"diff_url"`
}

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
