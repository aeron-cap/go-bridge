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
	"os/exec"
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
	AllowedPaths	string
	AllowedCommands	string
}

func init() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("Error loading .env files")
	}

	AppConfig.OllamaURL = os.Getenv("OLLAMA_URL")
	AppConfig.DiscordBotToken = os.Getenv("DISCORD_BOT_TOKEN")
	AppConfig.ModelVersion = os.Getenv("MODEL_VERSION")
	AppConfig.AllowedPaths = os.Getenv("ALLOWED_PATHS")
	AppConfig.AllowedCommands = os.Getenv("ALLOWED_COMMANDS")
}

type ToolProperties map[string]interface{}

type ToolParameters struct {
	Type       string         `json:"type"`
	Properties ToolProperties `json:"properties"`
	Required   []string       `json:"required"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  ToolParameters `json:"parameters"`
}

type Tools struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type Message struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	ToolCalls []struct {
		Function struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		} `json:"function,omitempty"`
	} `json:"tool_calls,omitempty"`
}

type OllamaChatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream"`
	KeepAlive string    `json:"keep_alive"`
	Tools     []Tools   `json:"tools,omitempty"`
}

type OllamaChatResponse struct {
	Message Message `json:"message"`
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

func defineTools() []Tools {
	var tools []Tools

	tools = append(tools, Tools{
		Type: "function",
		Function: ToolFunction{
			Name:        "read_file",
			Description: "Reads the content of a file from the server's filesystem. Use this function to access files that the bot has permission to read. The argument should be the file path as a string.",
			Parameters: ToolParameters{
				Type: "object",
				Properties: ToolProperties{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "The path to the file to be read. Ensure that the file is within the allowed directories and that the bot has permission to access it.",
					},
				},
				Required: []string{"file_path"},
			},
		},
	})

	tools = append(tools, Tools{
		Type: "function",
		Function: ToolFunction{
			Name:        "run_shell",
			Description: "Executes a shell command on the server. Use this function to run commands that the bot has permission to execute. The argument should be the command as a string.",
			Parameters: ToolParameters{
				Type: "object",
				Properties: ToolProperties{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "The shell command to execute. Ensure that the command is in the following, git, ls, df, free, cat, cd, and more. And that the bot has permission to execute it.",
					},
				},
				Required: []string{"command"},
			},
		},
	})

	tools = append(tools, Tools{
		Type: "function",
		Function: ToolFunction{
			Name:        "remember",
			Description: "Stores a piece of information in the bot's long-term memory. Use this function to save important details that the bot should remember across sessions. The argument should be a key-value pair, where the key is a string identifier and the value is the information to be stored.",
			Parameters: ToolParameters{
				Type: "object",
				Properties: ToolProperties{
					"key": map[string]interface{}{
						"type":        "string",
						"description": "The identifier for the information being stored. This should be a unique string that describes the information.",
					},
					"value": map[string]interface{}{
						"type":        "string",
						"description": "The information to be stored in memory. This can be any string data that the bot should remember.",
					},
				},
				Required: []string{"key", "value"},
			},
		},
	})

	tools = append(tools, Tools{
		Type: "function",
		Function: ToolFunction{
			Name:        "recall",
			Description: "Retrieves a piece of information from the bot's long-term memory. Use this function to access details that the bot has previously stored. The argument should be the key as a string, and the function will return the corresponding value.",
			Parameters: ToolParameters{
				Type: "object",
				Properties: ToolProperties{
					"key": map[string]interface{}{
						"type":        "string",
						"description": "The identifier for the information being retrieved. This should match the key used when the information was stored.",
					},
				},
				Required: []string{"key"},
			},
		},
	})

	return tools
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

func processToolCall(name string, args map[string]interface{}) Message {
	switch name {
		case "read_file":
			filepath, ok := args["file_path"].(string)
			if !ok {
				fmt.Println("Invalid arguments for read_file:", args)
				return Message{
					Role:    "tool",
					Content: "Invalid arguments for read_file",
				}
			}

			allowedPaths := strings.Split(AppConfig.AllowedPaths, ",")
			allowed := false
			for _, p := range allowedPaths {
				if !strings.HasPrefix(filepath, "/home/aeron/projects") {
					filepath = "/home/aeron/projects/" + filepath
				}
				
				if strings.HasPrefix(filepath, p) {
					allowed = true
					break
				}
			}
			if !allowed {
				fmt.Println("Access denied for file path:", filepath)
				return Message{
					Role:    "tool",
					Content: "Access denied: file path is not allowed",
				}
			}

			content, err := os.ReadFile(filepath)
			if err != nil {
				return Message{
					Role:    "tool",
					Content: fmt.Sprintf("Error reading file: %v", err),
				}
			}

			result := string(content)
			if len(result) > 500 {
				result = result[:500] + "... (truncated)"
			}
			
			return Message{
				Role:    "tool",
				Content: string(result),
			}
		case "run_shell":
			command, ok := args["command"].(string)
			if !ok {
				fmt.Println("Invalid arguments for run_shell:", args)
				return Message{
					Role:    "tool",
					Content: "Invalid arguments for run_shell",
				}
			}

			allowedCommands := strings.Split(AppConfig.AllowedCommands, ",")
			allowed := false
			for _, cmd := range allowedCommands {
				if strings.HasPrefix(command, cmd) {
					allowed = true
					break
				}
			}
			if !allowed {
				fmt.Println("Access denied for command:", command)
				return Message{
					Role:    "tool",
					Content: "Access denied: command is not allowed",
				}
			}

			commandParts := strings.Fields(command)
			cmd := commandParts[0]
			cmdArgs := commandParts[1:]

			output, err := exec.Command(cmd, cmdArgs...).CombinedOutput()
			if err != nil {
				fmt.Println("Error executing command:", err)
				return Message{
					Role:    "tool",
					Content: fmt.Sprintf("Error executing command: %v", err),
				}
			}

			result := string(output)
			if len(result) > 500 {
				result = result[:500] + "... (truncated)"
			}
			
			return Message{
				Role:    "tool",
				Content: result,
			}
	}
	
	return Message{
		Role:    "tool",
		Content: fmt.Sprintf("Executed tool %s with args %v", name, args),
	}
}

// Calling the LLM
func callOllama(prompt string, channelID string) (string, error) {
	fmt.Println("Calling model, with prompt:", prompt)

	chatHistory[channelID] = append(chatHistory[channelID], Message{
		Role:    "user",
		Content: prompt,
	})

	if len(chatHistory[channelID]) > MaxHistory {
		chatHistory[channelID] = chatHistory[channelID][len(chatHistory[channelID])-MaxHistory:]
	}

	fmt.Printf("History length: %d\n", len(chatHistory[channelID]))
	for _, msg := range chatHistory[channelID] {
		fmt.Printf("[%s]: %s\n", msg.Role, msg.Content[:min(50, len(msg.Content))])
	}

	var response string
	for i := 0; i < 5; i++ {
		reqBody := OllamaChatRequest{
			Model:     AppConfig.ModelVersion,
			Messages:  insertSystemPrompt(chatHistory[channelID]),
			Stream:    false,
			KeepAlive: "30m",
			Tools:     defineTools(),
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

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var ollamaResponse OllamaChatResponse
		err = json.Unmarshal(body, &ollamaResponse)
		if err != nil {
			return "", err
		}

		if ollamaResponse.Message.Content != "" && len(ollamaResponse.Message.ToolCalls) == 0 {
			response = ollamaResponse.Message.Content
			break
		}

		if ollamaResponse.Message.Content == "" && len(ollamaResponse.Message.ToolCalls) > 0 &&
			ollamaResponse.Message.ToolCalls[0].Function.Name != "" {
			for _, toolCall := range ollamaResponse.Message.ToolCalls {
				fmt.Println("Processing tool call:", toolCall.Function.Name, "with args:", toolCall.Function.Arguments)
				result := processToolCall(toolCall.Function.Name, toolCall.Function.Arguments)

				chatHistory[channelID] = append(chatHistory[channelID], result)

				if len(chatHistory[channelID]) > MaxHistory {
					chatHistory[channelID] = chatHistory[channelID][len(chatHistory[channelID])-MaxHistory:]
				}
			}
		}
	}

	return response, nil
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
