// Package logger provides structured logging with zap.
package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	// Log is the global logger instance
	Log *zap.SugaredLogger
)

// Init initializes the global logger with the specified log level
func Init(level string) error {
	var logger *zap.Logger
	var err error

	// Always use JSON format for structured logging
	config := zap.NewProductionConfig()

	// Set log level
	switch level {
	case "debug":
		config.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	case "info":
		config.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	case "warn":
		config.Level = zap.NewAtomicLevelAt(zapcore.WarnLevel)
	case "error":
		config.Level = zap.NewAtomicLevelAt(zapcore.ErrorLevel)
	default:
		config.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	}

	logger, err = config.Build()
	if err != nil {
		return err
	}

	Log = logger.Sugar()
	return nil
}

// SetLevel dynamically changes the log level (not supported in simplified mode)
func SetLevel(level string) error {
	return fmt.Errorf("dynamic log level change not supported, restart with --debug flag")
}

// IsDebugEnabled returns true if debug level is enabled
func IsDebugEnabled() bool {
	if Log == nil {
		return false
	}
	return Log.Desugar().Core().Enabled(zapcore.DebugLevel)
}

// Debug logs a debug message
func Debug(args ...interface{}) {
	Log.Debug(args...)
}

// Debugf logs a formatted debug message
func Debugf(template string, args ...interface{}) {
	Log.Debugf(template, args...)
}

// Info logs an info message
func Info(args ...interface{}) {
	Log.Info(args...)
}

// Infof logs a formatted info message
func Infof(template string, args ...interface{}) {
	Log.Infof(template, args...)
}

// Warn logs a warning message
func Warn(args ...interface{}) {
	Log.Warn(args...)
}

// Warnf logs a formatted warning message
func Warnf(template string, args ...interface{}) {
	Log.Warnf(template, args...)
}

// Error logs an error message
func Error(args ...interface{}) {
	Log.Error(args...)
}

// Errorf logs a formatted error message
func Errorf(template string, args ...interface{}) {
	Log.Errorf(template, args...)
}

// Fatal logs a fatal message and exits
func Fatal(args ...interface{}) {
	Log.Fatal(args...)
}

// Fatalf logs a formatted fatal message and exits
func Fatalf(template string, args ...interface{}) {
	Log.Fatalf(template, args...)
}
