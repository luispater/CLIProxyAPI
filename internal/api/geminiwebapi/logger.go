package geminiwebapi

import (
    "fmt"
    "log"
    "os"
    "strings"
)

type LogLevel int

const (
    LevelTrace LogLevel = iota
    LevelDebug
    LevelInfo
    LevelWarn
    LevelError
    LevelCritical
)

var currentLevel = LevelInfo

func SetLogLevel(level string) {
    switch strings.ToUpper(level) {
    case "TRACE":
        currentLevel = LevelTrace
    case "DEBUG":
        currentLevel = LevelDebug
    case "INFO":
        currentLevel = LevelInfo
    case "WARNING", "WARN":
        currentLevel = LevelWarn
    case "ERROR":
        currentLevel = LevelError
    case "CRITICAL":
        currentLevel = LevelCritical
    default:
        currentLevel = LevelInfo
    }
}

func logf(level LogLevel, prefix, format string, v ...any) {
    if level < currentLevel {
        return
    }
    log.Printf("[gemini_webapi] %s %s", prefix, fmt.Sprintf(format, v...))
}

func init() {
    // honor G_LOG_LEVEL env if present
    if lvl := os.Getenv("GEMINI_WEBAPI_LOG"); lvl != "" {
        SetLogLevel(lvl)
    }
}

func Debug(format string, v ...any)   { logf(LevelDebug, "DEBUG", format, v...) }
func Info(format string, v ...any)    { logf(LevelInfo, "INFO", format, v...) }
func Warning(format string, v ...any) { logf(LevelWarn, "WARN", format, v...) }
func Error(format string, v ...any)   { logf(LevelError, "ERROR", format, v...) }
func Success(format string, v ...any) { logf(LevelInfo, "SUCCESS", format, v...) }

