package main

import (
	"log/slog"
	"os"
	"os/signal"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	conf, err := LoadConfig("./.env")
	if err != nil {
		slog.Error("Error occured when load config file", "error", err)
		return
	}
	bot, err := InitBot(conf)
	if err != nil {
		slog.Error("Error occured when turn on the bot", "error", err)
		return
	}
	botID := ""
	if bot.State != nil && bot.State.User != nil {
		botID = bot.State.User.ID
	}
	slog.Info("Bot is started...", "ID", botID)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	slog.Info("Process is interrupted... exit")
	KillBot(bot, conf)
}
