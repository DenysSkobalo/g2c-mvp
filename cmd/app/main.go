package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/DenysSkobalo/g2c-mvp/config"
	"github.com/DenysSkobalo/g2c-mvp/internal/client/github"
	"github.com/DenysSkobalo/g2c-mvp/internal/client/groq"
	httpDelivery "github.com/DenysSkobalo/g2c-mvp/internal/delivery/http"
	"github.com/DenysSkobalo/g2c-mvp/internal/delivery/telegram"
	"github.com/DenysSkobalo/g2c-mvp/internal/domain"
	"github.com/DenysSkobalo/g2c-mvp/internal/service"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("WARNING: .env file not found, using system environment variables")
	}

	cfg := config.LoadConfig()
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobQueue := make(chan domain.Job, 100)

	ghClient := github.NewClient(cfg.GitHubToken)
	aiClient := groq.NewClient(cfg.GroqKey, cfg.GroqModel, cfg.Temperature)

	tgBot, err := telegram.NewBot(cfg.TelegramBotToken, cfg.TelegramUserID, aiClient)
	if err != nil {
		log.Fatalf("CRITICAL: Telegram Bot init failed: %v", err)
	}
	log.Printf("Initialized G2C MVP on @%s", tgBot.Username())

	pipelineSvc := service.NewPipelineService(cfg, ghClient, aiClient, tgBot)

	var wg sync.WaitGroup
	go tgBot.StartListener(ctx)
	pipelineSvc.StartWorkers(ctx, 5, jobQueue, &wg)

	httpHandler := httpDelivery.NewHandler(cfg, jobQueue)
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", httpHandler.GithubWebhookHandler)

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

	go func() {
		log.Printf("Server listening on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("CRITICAL: HTTP server crashed: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Gracefully shutting down server...")

	cancel()
	
	ctxShutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(ctxShutdown); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	wg.Wait()
	log.Println("Server exiting")
}
