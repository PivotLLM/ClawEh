package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/rs/zerolog"
)

type LogLevel = zerolog.Level

const (
	DEBUG = zerolog.DebugLevel
	INFO  = zerolog.InfoLevel
	WARN  = zerolog.WarnLevel
	ERROR = zerolog.ErrorLevel
	FATAL = zerolog.FatalLevel
)

var (
	logLevelNames = map[LogLevel]string{
		DEBUG: "DEBUG",
		INFO:  "INFO",
		WARN:  "WARN",
		ERROR: "ERROR",
		FATAL: "FATAL",
	}

	currentLevel      = INFO
	logMessageContent = false
	logger            zerolog.Logger
	fileLogger        zerolog.Logger
	logFile           *os.File
	once              sync.Once
	mu                sync.RWMutex

	// error.log is a high-signal companion to claw.log: it receives only events
	// at errorLogLevel and above, so it can be scanned quickly for problems.
	errorLogger   zerolog.Logger
	errorFile     *os.File
	errorLogLevel = WARN

	// Tracked so RollLogFile can reopen fresh files with the same path/format.
	logFilePath  string
	errorLogPath string
	logJSON      bool
)

// errorLogName is the high-signal problem log written beside claw.log.
const errorLogName = "error.log"

// osExit is indirected so tests can assert the FATAL path writes to every sink
// before the process exits. Production code always uses os.Exit.
var osExit = os.Exit

// SetErrorLogLevel sets the minimum level (this level and above) that is also
// written to error.log.
func SetErrorLogLevel(level LogLevel) {
	mu.Lock()
	defer mu.Unlock()
	errorLogLevel = level
}

func init() {
	once.Do(func() {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)

		if os.Getenv("LOG_FORMAT") == "json" {
			logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
		} else {
			consoleWriter := zerolog.ConsoleWriter{
				Out:        os.Stdout,
				TimeFormat: "2006-01-02 15:04:05",
				NoColor:    true,

				// Custom formatter to handle multiline strings and JSON objects
				FormatFieldValue: formatFieldValue,
			}
			logger = zerolog.New(consoleWriter).With().Timestamp().Logger()
		}

		fileLogger = zerolog.Logger{}
	})
}

func formatFieldValue(i any) string {
	var s string

	switch val := i.(type) {
	case string:
		s = val
	case []byte:
		s = string(val)
	default:
		return fmt.Sprintf("%v", i)
	}

	if unquoted, err := strconv.Unquote(s); err == nil {
		s = unquoted
	}

	if strings.Contains(s, "\n") {
		return fmt.Sprintf("\n%s", s)
	}

	if strings.Contains(s, " ") {
		if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
			return s
		}
		return fmt.Sprintf("%q", s)
	}

	return s
}

func SetLevel(level LogLevel) {
	mu.Lock()
	defer mu.Unlock()
	currentLevel = level
	zerolog.SetGlobalLevel(level)
}

func GetLevel() LogLevel {
	mu.RLock()
	defer mu.RUnlock()
	return currentLevel
}

// GetLogFilePath returns the active claw.log path, or "" when file logging is
// disabled. Used by the WebUI logs endpoint to tail the unified log.
func GetLogFilePath() string {
	mu.RLock()
	defer mu.RUnlock()
	return logFilePath
}

// SetLogMessageContent controls whether message body content is included in log entries.
// When false (default), message text and API request/response bodies are omitted from logs.
func SetLogMessageContent(enabled bool) {
	mu.Lock()
	defer mu.Unlock()
	logMessageContent = enabled
}

// GetLogMessageContent returns whether message body content should be included in log entries.
func GetLogMessageContent() bool {
	mu.RLock()
	defer mu.RUnlock()
	return logMessageContent
}

func EnableFileLogging(filePath string, jsonFormat bool) error {
	mu.Lock()
	defer mu.Unlock()
	return enableFileLoggingLocked(filePath, jsonFormat)
}

// enableFileLoggingLocked opens (or reopens) claw.log and its companion
// error.log. Callers must hold mu.
func enableFileLoggingLocked(filePath string, jsonFormat bool) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	newFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	if logFile != nil {
		logFile.Close()
	}
	logFile = newFile
	logFilePath = filePath
	logJSON = jsonFormat
	fileLogger = buildFileLogger(logFile, jsonFormat)

	// error.log lives beside claw.log and captures errorLogLevel and above.
	errorPath := filepath.Join(filepath.Dir(filePath), errorLogName)
	ef, err := os.OpenFile(errorPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open error log file: %w", err)
	}
	if errorFile != nil {
		errorFile.Close()
	}
	errorFile = ef
	errorLogPath = errorPath
	errorLogger = buildFileLogger(errorFile, jsonFormat)
	return nil
}

func buildFileLogger(w io.Writer, jsonFormat bool) zerolog.Logger {
	if jsonFormat {
		return zerolog.New(w).With().Timestamp().Caller().Logger()
	}
	fileWriter := zerolog.ConsoleWriter{
		Out:              w,
		TimeFormat:       "2006-01-02 15:04:05",
		NoColor:          true,
		FormatFieldValue: formatFieldValue,
	}
	return zerolog.New(fileWriter).With().Timestamp().Logger()
}

// RollLogFile archives the active log files (claw.log and error.log) into
// date-stamped copies named <YYYYMMDD>-<base>, using each file's last-modified
// date, then reopens fresh active files with the same format. Empty files are
// left in place. It is a no-op when file logging is disabled.
func RollLogFile() error {
	mu.Lock()
	defer mu.Unlock()

	if logFile == nil && errorFile == nil {
		return nil
	}

	paths := make([]string, 0, 2)
	if logFilePath != "" {
		paths = append(paths, logFilePath)
	}
	if errorLogPath != "" {
		paths = append(paths, errorLogPath)
	}

	// Close handles so the files can be renamed, then archive each by its mtime.
	if logFile != nil {
		logFile.Close()
		logFile = nil
		fileLogger = zerolog.Logger{}
	}
	if errorFile != nil {
		errorFile.Close()
		errorFile = nil
		errorLogger = zerolog.Logger{}
	}

	var firstErr error
	for _, p := range paths {
		if err := archiveLogByMtime(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Reopen fresh active files (this reopens both claw.log and error.log).
	if logFilePath != "" {
		if err := enableFileLoggingLocked(logFilePath, logJSON); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// archiveLogByMtime renames path to <dir>/<YYYYMMDD>-<base>, where the date is
// path's last-modified date. Missing or empty files are skipped. If the dated
// target already exists (e.g. a second roll on the same day), path's contents
// are appended to it instead.
func archiveLogByMtime(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Size() == 0 {
		return nil
	}
	dir := filepath.Dir(path)
	dated := filepath.Join(dir, fi.ModTime().Format("20060102")+"-"+filepath.Base(path))
	if _, err := os.Stat(dated); err == nil {
		return appendAndRemove(path, dated)
	}
	return os.Rename(path, dated)
}

// appendAndRemove appends src to dst then removes src.
func appendAndRemove(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Remove(src)
}

// DisableConsole replaces the console logger with a no-op logger.
func DisableConsole() {
	mu.Lock()
	defer mu.Unlock()
	logger = zerolog.Nop()
}

// RedirectForTest replaces the console logger with one that writes to w
// (JSON-encoded events) and returns a function that restores the prior
// logger. Intended for tests that need to assert on log output.
func RedirectForTest(w interface{ Write(p []byte) (int, error) }) func() {
	mu.Lock()
	prev := logger
	prevLevel := currentLevel
	logger = zerolog.New(w).With().Timestamp().Logger()
	currentLevel = DEBUG
	zerolog.SetGlobalLevel(DEBUG)
	mu.Unlock()
	return func() {
		mu.Lock()
		logger = prev
		currentLevel = prevLevel
		zerolog.SetGlobalLevel(prevLevel)
		mu.Unlock()
	}
}

// ParseLevel converts a level string ("debug","info","warn","error") to a LogLevel.
// Returns INFO for unrecognised values.
func ParseLevel(s string) LogLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return DEBUG
	case "warn", "warning":
		return WARN
	case "error":
		return ERROR
	case "fatal":
		return FATAL
	default:
		return INFO
	}
}

func DisableFileLogging() {
	mu.Lock()
	defer mu.Unlock()

	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
	fileLogger = zerolog.Logger{}
	if errorFile != nil {
		errorFile.Close()
		errorFile = nil
	}
	errorLogger = zerolog.Logger{}
}

func getCallerInfo() (string, int, string) {
	for i := 2; i < 15; i++ {
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			continue
		}

		fn := runtime.FuncForPC(pc)
		if fn == nil {
			continue
		}

		// bypass common loggers
		if strings.HasSuffix(file, "/logger.go") ||
			strings.HasSuffix(file, "/logger_3rd_party.go") ||
			strings.HasSuffix(file, "/log.go") {
			continue
		}

		funcName := fn.Name()
		if strings.HasPrefix(funcName, "runtime.") {
			continue
		}

		return filepath.Base(file), line, filepath.Base(funcName)
	}

	return "???", 0, "???"
}

//nolint:zerologlint
func getEvent(logger zerolog.Logger, level LogLevel) *zerolog.Event {
	switch level {
	case zerolog.DebugLevel:
		return logger.Debug()
	case zerolog.InfoLevel:
		return logger.Info()
	case zerolog.WarnLevel:
		return logger.Warn()
	case zerolog.ErrorLevel:
		return logger.Error()
	case zerolog.FatalLevel:
		// WithLevel(FatalLevel) writes at fatal level WITHOUT zerolog's built-in
		// os.Exit, so every sink (console, claw.log, error.log) receives the line.
		// logMessage performs the single os.Exit(1) after all sinks are written.
		return logger.WithLevel(zerolog.FatalLevel)
	default:
		return logger.Info()
	}
}

func logMessage(level LogLevel, component string, message string, fields map[string]any) {
	if level < currentLevel {
		return
	}

	callerFile, callerLine, callerFunc := getCallerInfo()

	event := getEvent(logger, level)

	// Build combined field with component and caller
	if component != "" {
		event.Str("caller", fmt.Sprintf("%-6s %s:%d (%s)", component, callerFile, callerLine, callerFunc))
	} else {
		event.Str("caller", fmt.Sprintf("<none> %s:%d (%s)", callerFile, callerLine, callerFunc))
	}

	appendFields(event, fields)
	event.Msg(message)

	// Also log to file if enabled
	if fileLogger.GetLevel() != zerolog.NoLevel {
		fileEvent := getEvent(fileLogger, level)

		if component != "" {
			fileEvent.Str("component", component)
		}

		appendFields(fileEvent, fields)
		fileEvent.Msg(message)
	}

	// Mirror problems (errorLogLevel and above) to error.log for quick scanning.
	if level >= errorLogLevel && errorLogger.GetLevel() != zerolog.NoLevel {
		errEvent := getEvent(errorLogger, level)

		if component != "" {
			errEvent.Str("component", component)
		}

		appendFields(errEvent, fields)
		errEvent.Msg(message)
	}

	if level == FATAL {
		osExit(1)
	}
}

func appendFields(event *zerolog.Event, fields map[string]any) {
	for k, v := range fields {
		// Type switch to avoid double JSON serialization of strings
		switch val := v.(type) {
		case string:
			event.Str(k, val)
		case int:
			event.Int(k, val)
		case int64:
			event.Int64(k, val)
		case float64:
			event.Float64(k, val)
		case bool:
			event.Bool(k, val)
		default:
			event.Interface(k, v) // Fallback for struct, slice and maps
		}
	}
}

func Debug(message string) {
	logMessage(DEBUG, "", message, nil)
}

func DebugC(component string, message string) {
	logMessage(DEBUG, component, message, nil)
}

func Debugf(message string, ss ...any) {
	logMessage(DEBUG, "", fmt.Sprintf(message, ss...), nil)
}

func DebugF(message string, fields map[string]any) {
	logMessage(DEBUG, "", message, fields)
}

func DebugCF(component string, message string, fields map[string]any) {
	logMessage(DEBUG, component, message, fields)
}

func Info(message string) {
	logMessage(INFO, "", message, nil)
}

func InfoC(component string, message string) {
	logMessage(INFO, component, message, nil)
}

func InfoF(message string, fields map[string]any) {
	logMessage(INFO, "", message, fields)
}

func Infof(message string, ss ...any) {
	logMessage(INFO, "", fmt.Sprintf(message, ss...), nil)
}

func InfoCF(component string, message string, fields map[string]any) {
	logMessage(INFO, component, message, fields)
}

func Warn(message string) {
	logMessage(WARN, "", message, nil)
}

func WarnC(component string, message string) {
	logMessage(WARN, component, message, nil)
}

func WarnF(message string, fields map[string]any) {
	logMessage(WARN, "", message, fields)
}

func WarnCF(component string, message string, fields map[string]any) {
	logMessage(WARN, component, message, fields)
}

func Error(message string) {
	logMessage(ERROR, "", message, nil)
}

func ErrorC(component string, message string) {
	logMessage(ERROR, component, message, nil)
}

func Errorf(message string, ss ...any) {
	logMessage(ERROR, "", fmt.Sprintf(message, ss...), nil)
}

func ErrorF(message string, fields map[string]any) {
	logMessage(ERROR, "", message, fields)
}

func ErrorCF(component string, message string, fields map[string]any) {
	logMessage(ERROR, component, message, fields)
}

func Fatal(message string) {
	logMessage(FATAL, "", message, nil)
}

func FatalC(component string, message string) {
	logMessage(FATAL, component, message, nil)
}

func Fatalf(message string, ss ...any) {
	logMessage(FATAL, "", fmt.Sprintf(message, ss...), nil)
}

func FatalF(message string, fields map[string]any) {
	logMessage(FATAL, "", message, fields)
}

func FatalCF(component string, message string, fields map[string]any) {
	logMessage(FATAL, component, message, fields)
}
