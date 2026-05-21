package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

var AppConfig struct {
	OllamaURL       string
	DiscordBotToken string
	SearxngURL      string
	ModelVersion    string
}

func init() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("Error loading .env files")
	}

	AppConfig.OllamaURL = os.Getenv("OLLAMA_URL")
	AppConfig.DiscordBotToken = os.Getenv("DISCORD_BOT_TOKEN")
	AppConfig.SearxngURL = os.Getenv("SEARXNG_BASE_URL")
	AppConfig.ModelVersion = os.Getenv("MODEL_VERSION")
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OllamaChatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream"`
	KeepAlive string    `json:"keep_alive"`
}

type OllamaChatResponse struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

type StartingPrompt struct {
	Content string
}

// and pass it into callOllama
var chatHistory map[string][]Message
const MaxHistory = 10
var sp = StartingPrompt{}

// read txt file
func (s *StartingPrompt) readPersonality() error {
	file, err := os.Open("system_prompt.txt")
	if err != nil {
		return errors.New("Error opening system_prompt.txt: " + err.Error())
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		s.Content += scanner.Text() + "\n"
	}

	if err := scanner.Err(); err != nil {
		return errors.New("Error reading system_prompt.txt: " + err.Error())
	}

	return nil
}

// Discord Bridge
func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	isMentioned := false
	for _, user := range m.Mentions {
		if user.ID == s.State.User.ID {
			isMentioned = true
			break
		}
	}

	isRepliedTo := false
	if m.MessageReference != nil {
		isRepliedTo = true
	}

	var reply string
	if isMentioned {
		fmt.Printf("Message received from %s: %s\n", m.Author.Username, m.Content)

		prompt := formatPrompt(m.Content)

		s.ChannelTyping(m.ChannelID)

		llamaReply, err := callOllama(prompt, m.ChannelID)
		if err != nil {
			llamaReply = fmt.Sprintf("Oops brain offline: %v", err)
		}

		_, err = s.ChannelMessageSendReply(m.ChannelID, llamaReply, m.Reference())
		if err != nil {
			fmt.Println("Error sending Discord message:", err)
		} else {
			fmt.Println("Message sent")
		}

		reply = llamaReply
	} else if isRepliedTo {
		fmt.Printf("Replying to %s", m.Author.Username)

		prompt := formatPrompt(m.Content)

		s.ChannelTyping(m.ChannelID)

		llamaReply, err := callOllama(prompt, m.ChannelID)
		if err != nil {
			llamaReply = fmt.Sprintf("Oops brain offline: %v", err)
		}

		_, err = s.ChannelMessageSendReply(m.ChannelID, llamaReply, m.Reference())
		if err != nil {
			fmt.Println("Error sending Discord message:", err)
		} else {
			fmt.Println("Message sent")
		}

		reply = llamaReply
	}

	if reply != "" {
		systemReply := Message{
			Role:    "assistant",
			Content: reply,
		}

		chatHistory[m.ChannelID] = append(chatHistory[m.ChannelID], systemReply)

		if len(chatHistory[m.ChannelID]) > MaxHistory {
			chatHistory[m.ChannelID] = chatHistory[m.ChannelID][len(chatHistory[m.ChannelID])-MaxHistory:]
		}
	}
}

func formatPrompt(p string) string {
	start := strings.Index(p, "<")
	end := strings.LastIndex(p, ">")

	if start != -1 && end != -1 && end > start {
		p = strings.TrimSpace(p[:start] + p[end+1:])
	}

	return p
}

func insertSystemPrompt(ch []Message) []Message {
	systemPrompt := Message{
		Role:    "system",
		Content: sp.Content,
	}

	ch = append([]Message{systemPrompt}, ch...)

	return ch
}

// Calling the LLM
func callOllama(prompt string, channelID string) (string, error) {
	fmt.Println("Calling model, with prompt:", prompt)

	chatHistory[channelID] = append(chatHistory[channelID], Message{
		Role:    "user",
		Content: prompt,
	})

	if len(chatHistory[channelID]) > MaxHistory {
		chatHistory[channelID] = chatHistory[channelID][len(chatHistory)-MaxHistory:]
	}

	fmt.Printf("History length: %d\n", len(chatHistory[channelID]))
	for _, msg := range chatHistory[channelID] {
		fmt.Printf("[%s]: %s\n", msg.Role, msg.Content[:min(50, len(msg.Content))])
	}

	reqBody := OllamaChatRequest{
		Model:     AppConfig.ModelVersion,
		Messages:  insertSystemPrompt(chatHistory[channelID]),
		Stream:    false,
		KeepAlive: "30m",
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(AppConfig.OllamaURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned status: %s", resp.Status)
	}

	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var ollamaResponse OllamaChatResponse
	err = json.Unmarshal(body, &ollamaResponse)
	if err != nil {
		return "", err
	}

	return ollamaResponse.Message.Content, nil
}

func main() {
	dg, err := discordgo.New("Bot " + AppConfig.DiscordBotToken)
	if err != nil {
		log.Fatal("Error creating Discord Session: ", err)
	}

	dg.AddHandler(messageCreate)
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentDirectMessages | discordgo.IntentMessageContent

	err = dg.Open()
	if err != nil {
		log.Fatal("Error opening connection: ", err)
	}

	e := sp.readPersonality()
	if e != nil {
		log.Fatal(e)
	}

	if chatHistory == nil {
		chatHistory = make(map[string][]Message)
	}

	fmt.Println("Bidoof is ready on Discord. Press Ctrl+C to exit.")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	dg.Close()
}
