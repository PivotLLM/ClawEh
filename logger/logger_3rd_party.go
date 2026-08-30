// this file is for compatible with 3rd party loggers, should not be called in this project

package logger

import "fmt"

// Logger implements common Logger interface
type Logger struct {
	component        string
	levels           map[int]LogLevel
	// contentSensitive marks this logger as one whose Debug output may contain
	// message content (e.g. API request/response bodies). When true, Debug and
	// Debugf calls are suppressed unless log_message_content is enabled.
	contentSensitive bool
	// errorDowngrade, when non-nil, is consulted on every Error/Errorf call: if
	// it returns true for the formatted message, the line is logged at WARN
	// instead of ERROR. Used to demote a 3rd-party library's recoverable,
	// auto-retried transient errors (e.g. telego long-poll getUpdates 5xx) so
	// they don't read as fatal or trip alerts.
	errorDowngrade func(msg string) bool
}

// WithContentSensitive marks the logger so that its debug-level output is
// suppressed unless log_message_content is enabled in the logging config.
func (b *Logger) WithContentSensitive() *Logger {
	b.contentSensitive = true
	return b
}

// WithErrorDowngrade installs a predicate that demotes matching Error/Errorf
// messages to WARN. Pass nil to clear. The predicate receives the fully
// formatted message string.
func (b *Logger) WithErrorDowngrade(fn func(msg string) bool) *Logger {
	b.errorDowngrade = fn
	return b
}

// errorLevel returns WARN when the downgrade predicate matches msg, else ERROR.
func (b *Logger) errorLevel(msg string) LogLevel {
	if b.errorDowngrade != nil && b.errorDowngrade(msg) {
		return WARN
	}
	return ERROR
}

// Debug logs debug messages
func (b *Logger) Debug(v ...any) {
	if b.contentSensitive && !GetLogMessageContent() {
		return
	}
	logMessage(DEBUG, b.component, fmt.Sprint(v...), nil)
}

// Info logs info messages
func (b *Logger) Info(v ...any) {
	logMessage(INFO, b.component, fmt.Sprint(v...), nil)
}

// Warn logs warning messages
func (b *Logger) Warn(v ...any) {
	logMessage(WARN, b.component, fmt.Sprint(v...), nil)
}

// Error logs error messages
func (b *Logger) Error(v ...any) {
	msg := fmt.Sprint(v...)
	logMessage(b.errorLevel(msg), b.component, msg, nil)
}

// Debugf logs formatted debug messages
func (b *Logger) Debugf(format string, v ...any) {
	if b.contentSensitive && !GetLogMessageContent() {
		return
	}
	logMessage(DEBUG, b.component, fmt.Sprintf(format, v...), nil)
}

// Infof logs formatted info messages
func (b *Logger) Infof(format string, v ...any) {
	logMessage(INFO, b.component, fmt.Sprintf(format, v...), nil)
}

// Warnf logs formatted warning messages
func (b *Logger) Warnf(format string, v ...any) {
	logMessage(WARN, b.component, fmt.Sprintf(format, v...), nil)
}

// Warningf logs formatted warning messages
func (b *Logger) Warningf(format string, v ...any) {
	logMessage(WARN, b.component, fmt.Sprintf(format, v...), nil)
}

// Errorf logs formatted error messages
func (b *Logger) Errorf(format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	logMessage(b.errorLevel(msg), b.component, msg, nil)
}

// Fatalf logs formatted fatal messages and exits
func (b *Logger) Fatalf(format string, v ...any) {
	logMessage(FATAL, b.component, fmt.Sprintf(format, v...), nil)
}

// Log logs a message at a given level with caller information
// the func name must be this because 3rd party loggers expect this
// msgL: message level (DEBUG, INFO, WARN, ERROR, FATAL)
// caller: unused parameter reserved for compatibility
// format: format string
// a: format arguments
//
//nolint:goprintffuncname
func (b *Logger) Log(msgL, caller int, format string, a ...any) {
	level := LogLevel(msgL)
	if b.levels != nil {
		if lvl, ok := b.levels[msgL]; ok {
			level = lvl
		}
	}
	logMessage(level, b.component, fmt.Sprintf(format, a...), nil)
}

// Sync flushes log buffer (no-op for this implementation)
func (b *Logger) Sync() error {
	return nil
}

// WithLevels sets log levels mapping for this logger
func (b *Logger) WithLevels(levels map[int]LogLevel) *Logger {
	b.levels = levels
	return b
}

// NewLogger creates a new logger instance with optional component name
func NewLogger(component string) *Logger {
	return &Logger{component: component}
}
