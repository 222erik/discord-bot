package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

func main() {
	loadEnvFile(".env")

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN is not set — put it in .env or export it (see .env.example)")
	}
	// Optional: set to your server's ID for instant slash-command registration.
	// Leave empty to register commands globally (propagation can take up to an hour).
	guildID := os.Getenv("GUILD_ID")

	bot, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("failed to create Discord session: %v", err)
	}
	bot.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages

	bot.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		switch i.ApplicationCommandData().Name {
		case "ping":
			handlePing(s, i)
		}
	})

	if err := bot.Open(); err != nil {
		log.Fatalf("failed to open connection: %v", err)
	}
	defer bot.Close()

	registerPingCommand(bot, guildID)

	log.Println("Bot is running. Press Ctrl+C to exit.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("Shutting down.")
}

// handlePing replies to a /ping interaction with the gateway latency.
func handlePing(s *discordgo.Session, i *discordgo.InteractionCreate) {
	latency := s.HeartbeatLatency().Round(time.Millisecond)
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("pong! 🏓 (latency: %s)", latency),
		},
	})
	if err != nil {
		log.Printf("failed to respond to /ping: %v", err)
	}
}

// registerPingCommand creates the /ping slash command, or updates it if a
// command with that name already exists (avoids duplicate-name errors on restart).
func registerPingCommand(s *discordgo.Session, guildID string) {
	cmd := &discordgo.ApplicationCommand{
		Name:        "ping",
		Description: "Replies with pong!",
	}

	existing, err := s.ApplicationCommands(s.State.User.ID, guildID)
	if err != nil {
		log.Printf("warning: could not fetch existing commands: %v", err)
		existing = nil
	}
	for _, c := range existing {
		if c.Name == cmd.Name {
			if _, err := s.ApplicationCommandEdit(s.State.User.ID, guildID, c.ID, cmd); err != nil {
				log.Printf("warning: could not update /%s command: %v", cmd.Name, err)
				return
			}
			log.Printf("Updated /%s command", cmd.Name)
			return
		}
	}

	if _, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, cmd); err != nil {
		log.Printf("warning: could not register /%s command: %v", cmd.Name, err)
		return
	}
	log.Printf("Registered /%s command", cmd.Name)
}

// loadEnvFile reads KEY=VALUE pairs from path into the environment.
// Existing environment variables take precedence and real env vars are never
// clobbered. The file is optional — everything can come from the environment.
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // .env is optional
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
