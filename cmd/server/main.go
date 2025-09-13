// Package main provides the entry point for the CLI Proxy API server.
// This server acts as a proxy that provides OpenAI/Gemini/Claude compatible API interfaces
// for CLI models, allowing CLI models to be used with tools and libraries designed for standard AI APIs.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/luispater/CLIProxyAPI/internal/cmd"
	"github.com/luispater/CLIProxyAPI/internal/config"
	_ "github.com/luispater/CLIProxyAPI/internal/translator"
	"github.com/luispater/CLIProxyAPI/internal/util"
	log "github.com/sirupsen/logrus"
)

var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// LogFormatter defines a custom log format for logrus.
// This formatter adds timestamp, log level, and source location information
// to each log entry for better debugging and monitoring.
type LogFormatter struct {
}

// Format renders a single log entry with custom formatting.
// It includes timestamp, log level, source file and line number, and the log message.
func (m *LogFormatter) Format(entry *log.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}

	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	var newLog string
	// Customize the log format to include timestamp, level, caller file/line, and message.
	newLog = fmt.Sprintf("[%s] [%s] [%s:%d] %s\n", timestamp, entry.Level, filepath.Base(entry.Caller.File), entry.Caller.Line, entry.Message)

	b.WriteString(newLog)
	return b.Bytes(), nil
}

// init initializes the logger configuration.
// It sets up the custom log formatter, enables caller reporting,
// and configures the log output destination.
func init() {
	// Set logger output to standard output.
	log.SetOutput(os.Stdout)
	// Enable reporting the caller function's file and line number.
	log.SetReportCaller(true)
	// Set the custom log formatter.
	log.SetFormatter(&LogFormatter{})
}

// main is the entry point of the application.
// It parses command-line flags, loads configuration, and starts the appropriate
// service based on the provided flags (login, codex-login, or server mode).
func main() {
	log.Infof("CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s", Version, Commit, BuildDate)

	// Command-line flags to control the application's behavior.
	var login bool
	var codexLogin bool
	var claudeLogin bool
	var qwenLogin bool
    var geminiWebAuth bool
	var noBrowser bool
	var projectID string
	var configPath string

	// Define command-line flags for different operation modes.
	flag.BoolVar(&login, "login", false, "Login Google Account")
	flag.BoolVar(&codexLogin, "codex-login", false, "Login to Codex using OAuth")
	flag.BoolVar(&claudeLogin, "claude-login", false, "Login to Claude using OAuth")
	flag.BoolVar(&qwenLogin, "qwen-login", false, "Login to Qwen using OAuth")
    flag.BoolVar(&geminiWebAuth, "gemini-web-auth", false, "Auth Gemini Web using cookies")
	flag.BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically for OAuth")
	flag.StringVar(&projectID, "project_id", "", "Project ID (Gemini only, not required)")
	flag.StringVar(&configPath, "config", "", "Configure File Path")

	// Parse the command-line flags.
	flag.Parse()

	// Core application variables.
	var err error
	var cfg *config.Config
	var wd string

	// Determine and load the configuration file.
	// If a config path is provided via flags, it is used directly.
	// Otherwise, it defaults to "config.yaml" in the current working directory.
	var configFilePath string
	if configPath != "" {
		configFilePath = configPath
		cfg, err = config.LoadConfig(configPath)
	} else {
		wd, err = os.Getwd()
		if err != nil {
			log.Fatalf("failed to get working directory: %v", err)
		}
		configFilePath = filepath.Join(wd, "config.yaml")
		cfg, err = config.LoadConfig(configFilePath)
	}
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Set the log level based on the configuration.
	util.SetLogLevel(cfg)

	// Expand the tilde (~) in the auth directory path to the user's home directory.
	if strings.HasPrefix(cfg.AuthDir, "~") {
		home, errUserHomeDir := os.UserHomeDir()
		if errUserHomeDir != nil {
			log.Fatalf("failed to get home directory: %v", errUserHomeDir)
		}
		// Reconstruct the path by replacing the tilde with the user's home directory.
		parts := strings.Split(cfg.AuthDir, string(os.PathSeparator))
		if len(parts) > 1 {
			parts[0] = home
			cfg.AuthDir = filepath.Join(parts...)
		} else {
			// If the path is just "~", set it to the home directory.
			cfg.AuthDir = home
		}
	}

	// Create login options to be used in authentication flows.
	options := &cmd.LoginOptions{
		NoBrowser: noBrowser,
	}

	// Handle different command modes based on the provided flags.

	if login {
		// Handle Google/Gemini login
		cmd.DoLogin(cfg, projectID, options)
	} else if codexLogin {
		// Handle Codex login
		cmd.DoCodexLogin(cfg, options)
	} else if claudeLogin {
		// Handle Claude login
		cmd.DoClaudeLogin(cfg, options)
	} else if qwenLogin {
		cmd.DoQwenLogin(cfg, options)
    } else if geminiWebAuth {
        cmd.DoGeminiWebAuth(cfg)
    } else {
		// Start the main proxy service
		cmd.StartService(cfg, configFilePath)
	}
}
