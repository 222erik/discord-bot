package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
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

	// Claude needs either an API key or an auth token. NewClient() also honors
	// ANTHROPIC_BASE_URL for proxies/gateways, and reads all of these from .env
	// via loadEnvFile.
	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" {
		log.Fatal("no Anthropic credentials set — put ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN in .env (see .env.example)")
	}
	claude := anthropic.NewClient()

	systemPrompt := loadSystemPrompt()

	bot, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("failed to create Discord session: %v", err)
	}
	// IntentsMessageContent is required to read the text of @mention messages.
	// It's a privileged intent — also enable it in the Developer Portal (see README).
	bot.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

	bot.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		switch i.ApplicationCommandData().Name {
		case "ping":
			handlePing(s, i)
		}
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		handleMessage(s, m, &claude, systemPrompt)
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

// handleMessage answers @mentions in guild channels and all DMs with Claude.
func handleMessage(s *discordgo.Session, m *discordgo.MessageCreate, client *anthropic.Client, systemPrompt string) {
	if m.Author.Bot {
		return
	}

	isDM := m.GuildID == ""
	isMentioned := false
	for _, u := range m.Mentions {
		if u.ID == s.State.User.ID {
			isMentioned = true
			break
		}
	}
	if !isDM && !isMentioned {
		return
	}

	// Strip the mentions so Claude sees just the user's words.
	content := m.Content
	for _, u := range m.Mentions {
		content = strings.ReplaceAll(content, fmt.Sprintf("<@%s>", u.ID), "")
		content = strings.ReplaceAll(content, fmt.Sprintf("<@!%s>", u.ID), "")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return // mention-only or empty message
	}

	// Let Discord show a "typing…" indicator while we wait on the API.
	s.ChannelTyping(m.ChannelID)

	resp, err := client.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("auto"),
		MaxTokens: 4096,
		// Disabled for snappy chat. To let Claude reason before answering (slower,
		// pricier), swap in: Thinking: anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
		Thinking: anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
		System: []anthropic.TextBlockParam{{
			Text: systemPrompt,
		}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(content)),
		},
	})
	if err != nil {
		log.Printf("Claude API error: %v", err)
		s.ChannelMessageSend(m.ChannelID, "⚠️ I hit an error talking to Claude. Try again in a moment.")
		return
	}

	var text string
	for _, block := range resp.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text += b.Text
		}
	}
	if text == "" {
		return
	}

	sendLongMessage(s, m.ChannelID, text)
}

// sendLongMessage sends text to Discord, splitting it at the 2000-character
// per-message limit (preferring newline boundaries).
func sendLongMessage(s *discordgo.Session, channelID, text string) {
	const maxLen = 2000
	for len(text) > 0 {
		chunk := text
		if len(chunk) > maxLen {
			idx := strings.LastIndex(chunk[:maxLen], "\n")
			if idx == 0 {
				idx = maxLen
			}
			chunk = chunk[:idx]
		}
		if _, err := s.ChannelMessageSend(channelID, chunk); err != nil {
			log.Printf("failed to send message: %v", err)
		}
		text = strings.TrimLeft(text[len(chunk):], "\n")
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

// systemPromptPath is where the bot's personality lives. Edit this file to
// change how Claude behaves in chat; it is read once at startup.
const systemPromptPath = "system-prompt.txt"

// loadSystemPrompt reads the bot's personality from systemPromptPath. If the
// file is missing or unreadable it logs a warning and falls back to a generic
// default so the bot still runs.
func loadSystemPrompt() string {
	data, err := os.ReadFile(systemPromptPath)
	if err != nil {
		log.Printf("warning: could not read %s (%v) — using a generic default; create this file to personalize the bot", systemPromptPath, err)
		return "You are a helpful assistant in a Discord server. Keep your responses concise and natural for chat."
	}
	return strings.TrimSpace(string(data))
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
