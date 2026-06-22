package notification

import (
	"fmt"
	"strings"
)

// MessageFormatter formats application check results for notifications
type MessageFormatter struct {
	MaxMessageLength int // Maximum length per message (default: 3900 for Telegram)
}

// NewMessageFormatter creates a new message formatter with default settings
func NewMessageFormatter() *MessageFormatter {
	return &MessageFormatter{
		MaxMessageLength: 3900, // Based on Telegram's 4096 character limit with safety margin
	}
}

// ApplicationUpdate represents an application with available updates for notification
type ApplicationUpdate struct {
	AppName                    string
	Project                    string
	ChartName                  string
	CurrentVersion             string
	LatestVersion              string
	RepoURL                    string
	ConstraintApplied          string
	HasUpdateOutsideConstraint bool
	LatestVersionAll           string
}

// FormatMessages formats application updates into notification messages
// Messages are split if they exceed the maximum length
func (f *MessageFormatter) FormatMessages(updates []ApplicationUpdate) []string {
	// Build individual app update strings
	var appMessages []string
	for _, update := range updates {
		appMessages = append(appMessages, f.formatSingleUpdate(update))
	}

	// Build header (empty for now, apps only)
	header := ""

	// Check if we can fit everything in one message
	totalLength := len(header)
	for _, msg := range appMessages {
		totalLength += len(msg)
	}

	if totalLength <= f.MaxMessageLength {
		// Everything fits in one message
		var message strings.Builder
		message.WriteString(header)
		for _, msg := range appMessages {
			message.WriteString(msg)
		}
		return []string{message.String()}
	}

	// Need to split into multiple messages
	return f.splitMessages(header, appMessages)
}

// formatSingleUpdate formats a single application update
func (f *MessageFormatter) formatSingleUpdate(update ApplicationUpdate) string {
	var sb strings.Builder

	// Compact format: app name as header with project
	fmt.Fprintf(&sb, "%s (%s)\n", update.AppName, update.Project)
	fmt.Fprintf(&sb, "  Chart: %s\n", update.ChartName)
	fmt.Fprintf(&sb, "  Version: %s -> %s\n", update.CurrentVersion, update.LatestVersion)

	// Show constraint if not "major" (default)
	if update.ConstraintApplied != "major" && update.ConstraintApplied != "" {
		fmt.Fprintf(&sb, "  Constraint: %s\n", update.ConstraintApplied)
	}

	// Show note if updates exist outside constraint
	if update.HasUpdateOutsideConstraint && update.LatestVersionAll != "" && update.LatestVersionAll != update.LatestVersion {
		fmt.Fprintf(&sb, "  Note: v%s available outside constraint\n", update.LatestVersionAll)
	}

	fmt.Fprintf(&sb, "  Repo: %s\n", update.RepoURL)
	sb.WriteString("\n")

	return sb.String()
}

// splitMessages splits app messages into multiple messages that fit within the max length
func (f *MessageFormatter) splitMessages(header string, appMessages []string) []string {
	var messages []string
	var currentMessage strings.Builder
	currentLength := 0

	// First message gets the header
	currentMessage.WriteString(header)
	currentLength = len(header)

	for _, appMsg := range appMessages {
		// Check if adding this app would exceed the limit
		if currentLength+len(appMsg) > f.MaxMessageLength {
			// Save current message and start a new one
			messages = append(messages, currentMessage.String())
			currentMessage.Reset()
			currentLength = 0
		}

		currentMessage.WriteString(appMsg)
		currentLength += len(appMsg)
	}

	// Add the last message if it has content
	if currentLength > 0 {
		messages = append(messages, currentMessage.String())
	}

	return messages
}
