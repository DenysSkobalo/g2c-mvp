package service

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/DenysSkobalo/g2c-mvp/config"
	"github.com/DenysSkobalo/g2c-mvp/internal/client/github"
	"github.com/DenysSkobalo/g2c-mvp/internal/client/groq"
	"github.com/DenysSkobalo/g2c-mvp/internal/delivery/telegram"
	"github.com/DenysSkobalo/g2c-mvp/internal/domain"
)

type PipelineService struct {
	cfg   config.AppConfig
	gh    *github.Client
	ai    *groq.Client
	tg    *telegram.Bot
}

func NewPipelineService(cfg config.AppConfig, gh *github.Client, ai *groq.Client, tg *telegram.Bot) *PipelineService {
	return &PipelineService{cfg: cfg, gh: gh, ai: ai, tg: tg}
}

func (p *PipelineService) StartWorkers(ctx context.Context, numWorkers int, jobs <-chan domain.Job, wg *sync.WaitGroup) {
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					log.Printf("Worker %d shutting down", workerID)
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					p.processJob(job)
				}
			}
		}(i)
	}
}

func (p *PipelineService) processJob(job domain.Job) {
	rawDiff, err := p.gh.FetchDiff(job.DiffURL)
	if err != nil {
		log.Printf("[Error] Fetching diff failed for %s: %v", job.URL, err)
		return
	}

	cleanDiff, entropy := p.sanitizeDiff(rawDiff)
	if entropy < p.cfg.EntropyThreshold {
		log.Printf("[Info] Skipped job due to low entropy (Changes: %d). URL: %s", entropy, job.URL)
		return
	}

	postText, err := p.ai.GeneratePost(cleanDiff, job)
	if err != nil {
		log.Printf("[Error] AI Generation failed: %v", err)
		return
	}

	finalText := fmt.Sprintf("%s\n\n🔗 [Дивитись зміни на GitHub](%s)", postText, job.URL)
	p.tg.SendPost(finalText, cleanDiff, job)
}

func (p *PipelineService) sanitizeDiff(rawDiff string) (string, int) {
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
