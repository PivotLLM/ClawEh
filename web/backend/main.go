// ClawEh Web Console - Web-based chat and management interface
//
// Provides a web UI for chatting with ClawEh via the Pico Channel WebSocket,
// with configuration management and gateway process control.
//
// Usage:
//
//	go build -o claw-web ./web/backend/
//	./claw-web [config.json]
//	./claw-web -public config.json

package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/web/backend/api"
	"github.com/PivotLLM/ClawEh/web/backend/launcherconfig"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
	"github.com/PivotLLM/ClawEh/web/backend/utils"
)

func main() {
	port := flag.String("port", "18800", "Port to listen on")
	public := flag.Bool("public", false, "Listen on all interfaces (0.0.0.0) instead of localhost only")
	noBrowser := flag.Bool("no-browser", false, "Do not auto-open browser on startup")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "ClawEh Web - A web-based configuration editor\n\n")
		fmt.Fprintf(os.Stderr, "Usage: %s [options] [config.json]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  config.json    Path to the configuration file (default: ~/.claw/config.json)\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s                          Use default config path\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s ./config.json             Specify a config file\n", os.Args[0])
		fmt.Fprintf(
			os.Stderr,
			"  %s -public ./config.json     Allow access from other devices on the network\n",
			os.Args[0],
		)
	}
	flag.Parse()

	// Resolve config path
	configPath := utils.GetDefaultConfigPath()
	if flag.NArg() > 0 {
		configPath = flag.Arg(0)
	}

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		logger.FatalCF("web", "Failed to resolve config path", map[string]any{"error": err.Error()})
	}

	logPath := filepath.Join(utils.GetClawHome(), "logs", "claw-web.log")
	if err := logger.EnableFileLogging(logPath, false); err != nil {
		logger.WarnCF("web", "Failed to enable file logging", map[string]any{"path": logPath, "error": err.Error()})
	}
	logger.InfoCF("web", "Starting", map[string]any{"config": absPath})

	err = utils.EnsureOnboarded(absPath)
	if err != nil {
		logger.WarnCF("web", "Failed to initialize ClawEh config automatically", map[string]any{"error": err.Error()})
	}

	// Apply logging config from claw config if available
	if clawCfg, err := config.LoadConfig(absPath); err == nil {
		if clawCfg.Logging.File {
			logPath := filepath.Join(utils.GetClawHome(), "logs", "claw-web.log")
			if err := logger.EnableFileLogging(logPath, clawCfg.Logging.JSON); err != nil {
				logger.WarnCF("web", "Failed to enable file logging", map[string]any{"path": logPath, "error": err.Error()})
			}
		} else {
			logger.DisableFileLogging()
		}
		if !clawCfg.Logging.Console {
			logger.DisableConsole()
		}
		if clawCfg.Logging.Level != "" {
			logger.SetLevel(logger.ParseLevel(clawCfg.Logging.Level))
		}
	}

	var explicitPort bool
	var explicitPublic bool
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			explicitPort = true
		case "public":
			explicitPublic = true
		}
	})

	launcherPath := launcherconfig.PathForAppConfig(absPath)
	launcherCfg, err := launcherconfig.Load(launcherPath, launcherconfig.Default())
	if err != nil {
		logger.WarnCF("web", "Failed to load launcher config", map[string]any{"path": launcherPath, "error": err.Error()})
		launcherCfg = launcherconfig.Default()
	}

	effectivePort := *port
	effectivePublic := *public
	if !explicitPort {
		effectivePort = strconv.Itoa(launcherCfg.Port)
	}
	if !explicitPublic {
		effectivePublic = launcherCfg.Public
	}

	portNum, err := strconv.Atoi(effectivePort)
	if err != nil || portNum < 1 || portNum > 65535 {
		if err == nil {
			err = errors.New("must be in range 1-65535")
		}
		logger.FatalCF("web", "Invalid port", map[string]any{"port": effectivePort, "error": err.Error()})
	}

	// Determine listen address
	var addr string
	if effectivePublic {
		addr = "0.0.0.0:" + effectivePort
	} else {
		addr = "127.0.0.1:" + effectivePort
	}

	// Initialize Server components
	mux := http.NewServeMux()

	// API Routes (e.g. /api/status)
	apiHandler := api.NewHandler(absPath)
	apiHandler.SetServerOptions(portNum, effectivePublic, explicitPublic, launcherCfg.AllowedCIDRs)
	apiHandler.RegisterRoutes(mux)

	// Ensure WebUI channel is configured so the chat UI works regardless of how the gateway is started.
	if _, err := apiHandler.EnsureWebUIChannel(); err != nil {
		logger.WarnCF("web", "Failed to ensure webui channel", map[string]any{"error": err.Error()})
	}

	// Frontend Embedded Assets
	registerEmbedRoutes(mux)

	accessControlledMux, err := middleware.IPAllowlist(launcherCfg.AllowedCIDRs, mux)
	if err != nil {
		logger.FatalCF("web", "Invalid allowed CIDR configuration", map[string]any{"error": err.Error()})
	}

	// Apply middleware stack
	handler := middleware.Recoverer(
		middleware.Logger(
			middleware.JSONContentType(accessControlledMux),
		),
	)

	// Print startup banner
	fmt.Print(utils.Banner)
	fmt.Println()
	fmt.Println("  Open the following URL in your browser:")
	fmt.Println()
	fmt.Printf("    >> http://localhost:%s <<\n", effectivePort)
	if effectivePublic {
		if ip := utils.GetLocalIP(); ip != "" {
			fmt.Printf("    >> http://%s:%s <<\n", ip, effectivePort)
		}
	}
	fmt.Println()

	// Auto-open browser
	if !*noBrowser {
		go func() {
			time.Sleep(500 * time.Millisecond)
			url := "http://localhost:" + effectivePort
			if err := utils.OpenBrowser(url); err != nil {
				logger.WarnCF("web", "Failed to auto-open browser", map[string]any{"error": err.Error()})
			}
		}()
	}

	// Auto-start gateway after backend starts listening.
	go func() {
		time.Sleep(1 * time.Second)
		apiHandler.TryAutoStartGateway()
	}()

	// Start the Server
	if err := http.ListenAndServe(addr, handler); err != nil {
		logger.FatalCF("web", "Server failed to start", map[string]any{"error": err.Error()})
	}
}
