package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/DenysSkobalo/g2c-mvp/internal/client/groq"
	"github.com/DenysSkobalo/g2c-mvp/internal/domain"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Bot struct {
	api       *tgbotapi.BotAPI
	userID    int64
	ai        *groq.Client
	postState sync.Map
}

func NewBot(token string, userID int64, ai *groq.Client) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}
	return &Bot{
		api:    api,
		userID: userID,
		ai:     ai,
	}, nil
}

func (b *Bot) Username() string {
	return b.api.Self.UserName
}

func (b *Bot) SendPost(text, originalDiff string, job domain.Job) {
	msg := tgbotapi.NewMessage(b.userID, text)
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 Регенерувати", "regen_post"),
			tgbotapi.NewInlineKeyboardButtonData("🎯 Взяв у роботу", "done_post"),
		),
	)
	msg.ParseMode = "Markdown"

	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[Telegram] Send error: %v", err)
		return
	}

	jobData, _ := json.Marshal(job)
	b.postState.Store(sentMsg.MessageID, map[string]string{"diff": originalDiff, "job": string(jobData)})
}

func (b *Bot) StartListener(ctx context.Context) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			log.Println("[Telegram] Listener stopped")
			return
		case update := <-updates:
			if update.CallbackQuery == nil {
				continue
			}

			msgID := update.CallbackQuery.Message.MessageID

			switch update.CallbackQuery.Data {
			case "regen_post":
				if val, ok := b.postState.Load(msgID); ok {
					data := val.(map[string]string)
					var job domain.Job
					json.Unmarshal([]byte(data["job"]), &job)
					
					go func() {
						newText, err := b.ai.GeneratePost(data["diff"], job)
						if err == nil {
							finalText := fmt.Sprintf("%s\n\n🔗 [Дивитись зміни на GitHub](%s)", newText, job.URL)
							edit := tgbotapi.NewEditMessageText(b.userID, msgID, finalText)
							edit.ParseMode = "Markdown"
							edit.ReplyMarkup = update.CallbackQuery.Message.ReplyMarkup
							b.api.Send(edit)
						}
					}()
				}
			case "done_post":
				b.api.Send(tgbotapi.NewEditMessageReplyMarkup(b.userID, msgID, tgbotapi.InlineKeyboardMarkup{}))
				b.postState.Delete(msgID)
			}
			b.api.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
		}
	}
}
