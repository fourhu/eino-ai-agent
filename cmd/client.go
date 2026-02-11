// Package cmd provides CLI commands for the AI agent.
package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/fourhu/eino-ai-agent/internal/logger"
	"github.com/spf13/cobra"
)

var (
	clientServerURL string
	clientSession   string
	clientModel     string
)

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest represents a chat completion request
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Session  string    `json:"session"`
}

// ChatResponse represents a chat completion response
type ChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int     `json:"index"`
		Delta        Message `json:"delta"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
}

func init() {
	rootCmd.AddCommand(clientCmd)
	clientCmd.Flags().StringVarP(&clientServerURL, "server", "s", "http://localhost:8000", "Server URL")
	clientCmd.Flags().StringVarP(&clientSession, "session", "n", "", "Session ID (auto-generated if not provided)")
	clientCmd.Flags().StringVarP(&clientModel, "model", "m", "glm-4.7", "Model name")
}

var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Start an interactive chat client",
	Long:  `Start an interactive chat client that connects to the AI agent server.`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := runClient(); err != nil {
			logger.Errorf("Client error: %v", err)
			os.Exit(1)
		}
	},
}

func runClient() error {
	// Initialize logger for client
	if err := logger.Init("info"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize logger: %v\n", err)
	}

	fmt.Printf("Connecting to server: %s\n", clientServerURL)

	// Generate session ID if not provided
	if clientSession == "" {
		clientSession = generateSessionID()
	}
	fmt.Printf("Session ID: %s\n", clientSession)
	fmt.Printf("Streaming: true\n\n")

	// Check if server is healthy
	if err := checkHealth(); err != nil {
		return fmt.Errorf("server health check failed: %w", err)
	}

	fmt.Println("Enter your messages (type 'exit' or 'quit' to exit):")
	fmt.Println("Commands:")
	fmt.Println("  /new    - Start a new session")
	fmt.Println("  /clear  - Clear screen")
	fmt.Println("  /help   - Show help")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("You: ")
		if !scanner.Scan() {
			break
		}

		message := strings.TrimSpace(scanner.Text())
		if message == "" {
			continue
		}

		// Handle commands
		switch message {
		case "exit", "quit":
			fmt.Println("Goodbye!")
			return nil
		case "/new":
			clientSession = generateSessionID()
			fmt.Printf("Started new session: %s\n\n", clientSession)
			continue
		case "/clear":
			fmt.Print("\033[H\033[2J")
			continue
		case "/help":
			printHelp()
			continue
		}

		// Send message
		if err := sendStreamMessage(message); err != nil {
			logger.Errorf("Failed to send message: %v", err)
			fmt.Printf("Error: %v\n\n", err)
		}
	}

	return scanner.Err()
}

func checkHealth() error {
	resp, err := http.Get(clientServerURL + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned status %d", resp.StatusCode)
	}
	return nil
}

func printHelp() {
	fmt.Println("\nCommands:")
	fmt.Println("  /new    - Start a new session")
	fmt.Println("  /clear  - Clear screen")
	fmt.Println("  /help   - Show this help")
	fmt.Println("  exit    - Exit the client")
	fmt.Println()
}

func sendStreamMessage(message string) error {
	req := ChatRequest{
		Model:   clientModel,
		Stream:  true,
		Session: clientSession,
		Messages: []Message{
			{Role: "user", Content: message},
		},
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.Debugf("Sending streaming request: %s", string(reqBody))

	httpReq, err := http.NewRequest("POST", clientServerURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned error: %s - %s", resp.Status, string(body))
	}

	fmt.Print("\nAssistant: ")
	reader := bufio.NewReader(resp.Body)
	contentReceived := false
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read stream: %w", err)
		}

		line = strings.TrimSpace(line)
		logger.Debugf("Received line: %q", line)

		// Skip empty lines and non-data lines
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			logger.Debug("Received [DONE]")
			break
		}

		var streamResp ChatResponse
		if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
			logger.Debugf("Failed to unmarshal stream data: %v, data: %s", err, data)
			continue
		}

		logger.Debugf("Parsed response: %+v", streamResp)

		if len(streamResp.Choices) > 0 {
			content := streamResp.Choices[0].Delta.Content
			if content != "" {
				// Skip MCP tool result JSON format
				if isMCPToolResult(content) {
					logger.Debug("Skipping MCP tool result JSON")
					continue
				}
				fmt.Print(content)
				contentReceived = true
			}
			// Check for finish reason
			if streamResp.Choices[0].FinishReason == "stop" {
				logger.Debug("Received finish reason: stop")
				break
			}
		}
	}
	if !contentReceived {
		fmt.Print("(no content received)")
	}
	fmt.Println("\n")

	return nil
}

func generateSessionID() string {
	// Simple session ID generation
	return fmt.Sprintf("session-%d", os.Getpid())
}

// isMCPToolResult checks if content is an MCP tool result JSON format
func isMCPToolResult(content string) bool {
	// Check if it starts with {"content": which is MCP tool result format
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, `{"content"`) {
		return false
	}
	// Try to parse as JSON
	var mcpResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	return json.Unmarshal([]byte(content), &mcpResult) == nil
}
