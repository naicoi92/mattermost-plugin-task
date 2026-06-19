package command

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// taskCommandTrigger is the root slash command for the plugin.
const taskCommandTrigger = "task"

// TaskService is the subset of task.Service the command handler needs. Kept as
// an interface so the handler is testable with a fake, and so future command
// scopes (assignee, edit, ...) can be added without touching this file's shape.
type TaskService interface {
	SetStatus(id, status string) (*taskmodel.Task, error)
	SetReminder(id string, offsetMS int64) (*taskmodel.Task, error)
	ClearReminder(id string) (*taskmodel.Task, error)
}

// Handler dispatches slash commands. Today it owns the /task command; the
// legacy /hello command from the starter template is retained for reference.
type Handler struct {
	client      *pluginapi.Client
	taskService TaskService
}

// Command is the dispatch contract implemented by Handler.
type Command interface {
	Handle(args *model.CommandArgs) (*model.CommandResponse, error)
	executeHelloCommand(args *model.CommandArgs) *model.CommandResponse
}

const helloCommandTrigger = "hello"

// NewCommandHandler registers the plugin's slash commands and returns a Handler
// wired to the given task service.
func NewCommandHandler(client *pluginapi.Client, taskService TaskService) Command {
	if err := client.SlashCommand.Register(&model.Command{
		Trigger:          helloCommandTrigger,
		AutoComplete:     true,
		AutoCompleteDesc: "Say hello to someone",
		AutoCompleteHint: "[@username]",
		AutocompleteData: model.NewAutocompleteData(helloCommandTrigger, "[@username]", "Username to say hello to"),
	}); err != nil {
		client.Log.Error("Failed to register command", "error", err)
	}

	taskCmd := model.NewAutocompleteData(taskCommandTrigger, "[subcommand]", "Manage tasks")

	// /task status <id> <status>
	statusCmd := model.NewAutocompleteData("status", "<id> <status>", "Change a task's status")
	statusCmd.AddStaticListArgument("task id", true, []model.AutocompleteListItem{})
	statusCmd.AddStaticListArgument("status", true, []model.AutocompleteListItem{
		{Item: taskmodel.StatusTodo, HelpText: "To do"},
		{Item: taskmodel.StatusInProgress, HelpText: "In progress"},
		{Item: taskmodel.StatusDone, HelpText: "Done"},
		{Item: taskmodel.StatusCancelled, HelpText: "Cancelled"},
	})
	taskCmd.AddCommand(statusCmd)

	// /task done <id>
	doneCmd := model.NewAutocompleteData("done", "<id>", "Mark a task done")
	doneCmd.AddStaticListArgument("task id", true, []model.AutocompleteListItem{})
	taskCmd.AddCommand(doneCmd)

	// /task cancel <id>
	cancelCmd := model.NewAutocompleteData("cancel", "<id>", "Cancel a task")
	cancelCmd.AddStaticListArgument("task id", true, []model.AutocompleteListItem{})
	taskCmd.AddCommand(cancelCmd)

	// /task remind <id> <15m|1h|1d|off>
	remindCmd := model.NewAutocompleteData("remind", "<id> <15m|1h|1d|off>", "Set or turn off a reminder")
	remindCmd.AddStaticListArgument("task id", true, []model.AutocompleteListItem{})
	remindCmd.AddStaticListArgument("offset", true, []model.AutocompleteListItem{
		{Item: "15m", HelpText: "15 minutes before due"},
		{Item: "1h", HelpText: "1 hour before due"},
		{Item: "1d", HelpText: "1 day before due"},
		{Item: "off", HelpText: "Turn the reminder off"},
	})
	taskCmd.AddCommand(remindCmd)

	// /task help
	helpCmd := model.NewAutocompleteData("help", "", "Show task command help")
	taskCmd.AddCommand(helpCmd)

	if err := client.SlashCommand.Register(&model.Command{
		Trigger:          taskCommandTrigger,
		AutoComplete:     true,
		AutoCompleteDesc: "Create, update, and track tasks",
		AutoCompleteHint: "[subcommand]",
		AutocompleteData: taskCmd,
	}); err != nil {
		client.Log.Error("Failed to register /task command", "error", err)
	}

	return &Handler{
		client:      client,
		taskService: taskService,
	}
}

// Handle dispatches a slash command to its handler.
func (c *Handler) Handle(args *model.CommandArgs) (*model.CommandResponse, error) {
	fields := strings.Fields(args.Command)
	if len(fields) == 0 {
		return ephemeral("Empty command"), nil
	}
	trigger := strings.TrimPrefix(fields[0], "/")
	switch trigger {
	case helloCommandTrigger:
		return c.executeHelloCommand(args), nil
	case taskCommandTrigger:
		return c.handleTask(args, fields[1:])
	default:
		return ephemeral(fmt.Sprintf("Unknown command: %s", args.Command)), nil
	}
}

// handleTask dispatches /task subcommands. Only the status workflow subcommands
// (status/done/cancel) are implemented here; other subcommands are added by
// downstream issues.
func (c *Handler) handleTask(args *model.CommandArgs, subFields []string) (*model.CommandResponse, error) {
	if len(subFields) == 0 {
		return ephemeral(taskHelp()), nil
	}
	switch subFields[0] {
	case "status":
		return c.handleStatus(args, subFields[1:])
	case "done":
		return c.handleShortcut(args, subFields[1:], taskmodel.StatusDone, "done")
	case "cancel":
		return c.handleShortcut(args, subFields[1:], taskmodel.StatusCancelled, "cancelled")
	case "remind":
		return c.handleRemind(args, subFields[1:])
	case "help":
		return ephemeral(taskHelp()), nil
	default:
		return ephemeral(fmt.Sprintf("Unknown /task subcommand: %s\n\n%s", subFields[0], taskHelp())), nil
	}
}

// handleStatus implements /task status <id> <status>.
func (c *Handler) handleStatus(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	if len(rest) < 2 {
		return ephemeral("Usage: /task status <id> <todo|in_progress|done|cancelled>"), nil
	}
	id, status := rest[0], rest[1]
	if !taskmodel.IsValidStatus(status) {
		return ephemeral(fmt.Sprintf("Invalid status %q. Use one of: todo, in_progress, done, cancelled.", status)), nil
	}
	return c.setStatus(args, id, status)
}

// handleShortcut implements /task done <id> and /task cancel <id>.
func (c *Handler) handleShortcut(args *model.CommandArgs, rest []string, status string, label string) (*model.CommandResponse, error) {
	if len(rest) < 1 {
		return ephemeral(fmt.Sprintf("Usage: /task %s <id>", label)), nil
	}
	return c.setStatus(args, rest[0], status)
}

// handleRemind implements /task remind <id> <15m|1h|1d|off>.
func (c *Handler) handleRemind(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	if len(rest) < 2 {
		return ephemeral("Usage: /task remind <id> <15m|1h|1d|off>"), nil
	}
	id, token := rest[0], rest[1]

	if token == "off" {
		t, err := c.taskService.ClearReminder(id)
		if err != nil {
			return formatReminderError(c, id, err, "clear reminder")
		}
		return ephemeral(fmt.Sprintf("🔔 Reminder turned off for **%s**.", t.Summary)), nil
	}

	offsetMS, ok := parseReminderOffset(token)
	if !ok {
		return ephemeral(fmt.Sprintf("Unknown reminder %q. Use 15m, 1h, 1d, or off.", token)), nil
	}

	t, err := c.taskService.SetReminder(id, offsetMS)
	if err != nil {
		return formatReminderError(c, id, err, "set reminder")
	}
	return ephemeral(fmt.Sprintf("🔔 Reminder set for **%s** (%s before due).", t.Summary, token)), nil
}

// formatReminderError maps reminder service errors to ephemeral text.
func formatReminderError(c *Handler, id string, err error, action string) (*model.CommandResponse, error) {
	switch {
	case errors.Is(err, task.ErrNotFound):
		return ephemeral(fmt.Sprintf("Task %s not found.", id)), nil
	case errors.Is(err, task.ErrReminderNeedsDue):
		return ephemeral("This task has no due date, so a reminder cannot be set."), nil
	default:
		c.client.Log.Error("Failed to "+action, "task_id", id, "error", err)
		return ephemeral(fmt.Sprintf("Failed to %s for task %s: %s", action, id, err.Error())), nil
	}
}

// parseReminderOffset converts a friendly token (15m, 1h, 1d, 2h...) into a
// millisecond offset. ok is false when the token is not recognized.
func parseReminderOffset(token string) (int64, bool) {
	if len(token) < 2 {
		return 0, false
	}
	unit := token[len(token)-1]
	numStr := token[:len(token)-1]
	num, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil || num <= 0 {
		return 0, false
	}
	switch unit {
	case 'm':
		return num * 60 * 1000, true
	case 'h':
		return num * 60 * 60 * 1000, true
	case 'd':
		return num * 24 * 60 * 60 * 1000, true
	default:
		return 0, false
	}
}

// setStatus calls the service and formats the result for the user.
func (c *Handler) setStatus(args *model.CommandArgs, id, status string) (*model.CommandResponse, error) {
	t, err := c.taskService.SetStatus(id, status)
	if err != nil {
		switch {
		case errors.Is(err, task.ErrNotFound):
			return ephemeral(fmt.Sprintf("Task %s not found.", id)), nil
		case errors.Is(err, task.ErrInvalidStatus):
			return ephemeral(fmt.Sprintf("Invalid status %q.", status)), nil
		default:
			c.client.Log.Error("Failed to set task status", "task_id", id, "status", status, "error", err)
			return ephemeral(fmt.Sprintf("Failed to update task %s: %s", id, err.Error())), nil
		}
	}
	return ephemeral(fmt.Sprintf("✅ Task **%s** is now **%s**.", t.Summary, status)), nil
}

func (c *Handler) executeHelloCommand(args *model.CommandArgs) *model.CommandResponse {
	parts := strings.Fields(args.Command)
	if len(parts) < 2 {
		return ephemeral("Please specify a username")
	}
	return ephemeral("Hello, " + parts[1])
}

// ephemeral builds an ephemeral CommandResponse.
func ephemeral(text string) *model.CommandResponse {
	return &model.CommandResponse{
		ResponseType: model.CommandResponseTypeEphemeral,
		Text:         text,
	}
}

// taskHelp returns the help text for the /task command.
func taskHelp() string {
	return "`/task` commands:\n" +
		"• `/task status <id> <todo|in_progress|done|cancelled>` — change status\n" +
		"• `/task done <id>` — mark a task done\n" +
		"• `/task cancel <id>` — cancel a task\n" +
		"• `/task remind <id> <15m|1h|1d|off>` — set or turn off a reminder\n" +
		"• `/task help` — show this help"
}
