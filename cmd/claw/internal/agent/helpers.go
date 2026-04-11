package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ergochat/readline"

	"github.com/PivotLLM/ClawEh/cmd/claw/internal"
	"github.com/PivotLLM/ClawEh/pkg/agent"
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

func agentCmd(message, sessionKey, model string, debug bool) error {
	if sessionKey == "" {
		sessionKey = "cli:default"
	}

	if debug {
		logger.SetLevel(logger.DEBUG)
		logger.DebugC("agent", "Debug mode enabled")
	}

	cfg, err := internal.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	if model != "" {
		cfg.Agents.Defaults.SetDefaultModel(model)
	}

	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		return fmt.Errorf("error creating provider: %w", err)
	}

	// Use the resolved model ID from provider creation
	if modelID != "" {
		cfg.Agents.Defaults.SetDefaultModel(modelID)
	}

	dispatcher := providers.NewProviderDispatcher(cfg)
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider, dispatcher)
	defer agentLoop.Close()

	// Print agent startup info (only for interactive mode)
	startupInfo := agentLoop.GetStartupInfo()
	logger.InfoCF("agent", "Agent initialized",
		map[string]any{
			"tools_count":      startupInfo["tools"].(map[string]any)["count"],
			"skills_total":     startupInfo["skills"].(map[string]any)["total"],
			"skills_available": startupInfo["skills"].(map[string]any)["available"],
		})

	if message != "" {
		ctx := context.Background()
		response, err := agentLoop.ProcessDirect(ctx, message, sessionKey)
		if err != nil {
			return fmt.Errorf("error processing message: %w", err)
		}
		fmt.Printf("\n%s\n", response)
		return nil
	}

	fmt.Print("Interactive mode (Ctrl+C to exit)\n\n")
	interactiveMode(agentLoop, sessionKey)

	return nil
}

func interactiveMode(agentLoop *agent.AgentLoop, sessionKey string) {
	prompt := "You: "

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          prompt,
		HistoryFile:     filepath.Join(os.TempDir(), ".claw_history"),
		HistoryLimit:    100,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		logger.ErrorCF("agent", "Error initializing readline, falling back to simple input mode", map[string]any{"error": err.Error()})
		simpleInteractiveMode(agentLoop, sessionKey)
		return
	}
	defer rl.Close()

	for {
		line, err := rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt || err == io.EOF {
				fmt.Println("\nGoodbye!")
				return
			}
			logger.ErrorCF("agent", "Error reading input", map[string]any{"error": err.Error()})
			continue
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if input == "exit" || input == "quit" {
			fmt.Println("Goodbye!")
			return
		}

		ctx := context.Background()
		response, err := agentLoop.ProcessDirect(ctx, input, sessionKey)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		fmt.Printf("\n%s\n\n", response)
	}
}

func simpleInteractiveMode(agentLoop *agent.AgentLoop, sessionKey string) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("You: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Println("\nGoodbye!")
				return
			}
			logger.ErrorCF("agent", "Error reading input", map[string]any{"error": err.Error()})
			continue
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if input == "exit" || input == "quit" {
			fmt.Println("Goodbye!")
			return
		}

		ctx := context.Background()
		response, err := agentLoop.ProcessDirect(ctx, input, sessionKey)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		fmt.Printf("\n%s\n\n", response)
	}
}
