package command

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// taskCommandTrigger is the root slash command for the plugin.
const taskCommandTrigger = "task"

// TaskService is the subset of task.Service the command handler needs. Kept as
// an interface so the handler is testable with a fake and each command scope
// (status, edit, assignee, ...) can be added without changing this file's shape.
type TaskService interface {
	SetStatus(actorID, id, status string) (*taskmodel.Task, error)
	Patch(actorID, id string, in task.PatchInput) (*taskmodel.Task, error)
	SetReminder(actorID, id string, offsetMS int64) (*taskmodel.Task, error)
	ClearReminder(actorID, id string) (*taskmodel.Task, error)
	// Assign changes a task's single assignee; newAssigneeID == "" clears it.
	// It returns the updated task plus an AssignEvent describing the change.
	Assign(actorID, id, newAssigneeID string) (*taskmodel.Task, task.AssignEvent, error)
	// Get returns the task with the given id, or nil if it does not exist.
	Get(id string) (*taskmodel.Task, error)
	// CreateSubtask creates a new task under parentID. The subtask inherits the
	// parent's ChannelID and (as default) the parent's assignee; an explicit
	// assigneeID overrides the default. Returns the created subtask.
	CreateSubtask(parentID, creatorID, summary, assigneeID string, due *int64) (*taskmodel.Task, error)
	// LinkComment records that postID is a thread reply on taskID (the
	// comment-as-thread design: content lives in the Mattermost post). Returns
	// the comment mapping plus an event describing the task's participants for
	// notification.
	LinkComment(taskID, postID, userID string) (taskmodel.TaskComment, task.CommentEvent, error)
	// List returns the tasks matching the given query (scope/status/due/cursor).
	// Used by /task list (#9).
	List(q task.ListQuery) ([]*taskmodel.Task, error)
	// Search returns up to limit tasks whose summary or description contains
	// keyword (case-insensitive). Used by /task search (#9).
	Search(keyword string, limit int) ([]*taskmodel.Task, error)
	// Create persists a new top-level task and returns it. Used by /task add (#8)
	// and the New Task dialog / post-dropdown action (#16).
	Create(in task.CreateInput) (*taskmodel.Task, error)
}

// AssignNotifier fires the assignee-change DM. It is the subset of the
// notification package the command handler invokes after an assign; may be nil.
type AssignNotifier interface {
	NotifyAssigned(assigneeID, creatorID string, task AssignRef)
}

// AssignRef is the minimal task view the assign notifier needs.
type AssignRef struct {
	ID      string
	Summary string
}

// TaskNotifier fires task-event DMs. It is the subset of the notification
// package the command handler invokes after a state change; passing it in keeps
// this package free of notification imports. May be nil (notifications skipped).
type TaskNotifier interface {
	NotifyCompleted(task TaskRef, actorID, creatorID, assigneeID string)
	NotifyCancelled(task TaskRef, actorID, creatorID, assigneeID string)
}

// CommentNotifier fires the comment DM to task participants. May be nil
// (notification skipped).
type CommentNotifier interface {
	NotifyCommented(task TaskRef, actorID, creatorID, assigneeID string)
}

// CommentAuthorizer reports whether userID may comment on the task. It backs
// the view/comment permission rule including channel membership for
// channel-scoped tasks; supplied by the plugin layer (which has the channel
// membership API). May be nil, in which case the handler falls back to the
// creator/assignee co-owner check (matching the personal-task rule).
type CommentAuthorizer interface {
	CanComment(userID string, task *taskmodel.Task) bool
}

// UserResolver resolves an @username mention to a Mattermost user id. Supplied
// by the plugin layer (which has API access); nil disables assign parsing.
type UserResolver interface {
	// UserIDByUsername returns the user id for username (without the leading @),
	// or "" when not found.
	UserIDByUsername(username string) string
}

// NewTaskDialogOpener opens the New Task Interactive Dialog prefilled with a
// summary (#95). Supplied by the plugin layer (which owns
// OpenInteractiveDialog); nil keeps /task add on the immediate-create fallback.
// The opener returns true when the dialog was opened, so handleAdd can decide
// whether to also create the task immediately.
type NewTaskDialogOpener interface {
	// OpenNewTask opens the New Task dialog for the user identified by triggerID,
	// prefilled with prefillSummary. channelID is the originating channel (empty
	// for a DM with the bot → personal scope). Returns true when the dialog was
	// opened successfully.
	OpenNewTask(triggerID, prefillSummary, channelID string) bool
}

// QuickListDialogOpener opens the Quick List Interactive Dialog scoped/filtered
// for the user (#97). Supplied by the plugin layer; nil keeps /task list on the
// ephemeral text fallback. Returns true when the dialog was opened.
type QuickListDialogOpener interface {
	// OpenQuickList opens the Quick List dialog for the user identified by
	// triggerID. scope is mine/channel/all; channelID is the context channel
	// (required when scope == channel); status/due are optional filters.
	OpenQuickList(triggerID, userID, scope, channelID, status, due string) bool
}

// TaskDetailDialogOpener opens the Task Detail Interactive Dialog for a task
// (#97). Supplied by the plugin layer; nil keeps /task show on the ephemeral
// text card fallback. Returns true when the dialog was opened.
type TaskDetailDialogOpener interface {
	// OpenTaskDetail opens the Task Detail dialog for the user identified by
	// triggerID, showing task taskID. Returns true when the dialog was opened.
	OpenTaskDetail(triggerID, taskID string) bool
}

// TaskRef is the minimal task view the notifier needs (mirrors
// notification.TaskSummary without importing that package).
type TaskRef struct {
	ID      string
	Summary string
}

// Handler dispatches slash commands. Today it owns the /task command; the
// legacy /hello command from the starter template is retained for reference.
type Handler struct {
	client            *pluginapi.Client
	taskService       TaskService
	notifier          TaskNotifier
	assignNotifier    AssignNotifier
	commentNotifier   CommentNotifier
	commentAuthorizer CommentAuthorizer
	newTaskOpener     NewTaskDialogOpener
	quickListOpener   QuickListDialogOpener
	taskDetailOpener  TaskDetailDialogOpener
	users             UserResolver
	botUserID         string
}

// Command is the dispatch contract implemented by Handler.
type Command interface {
	Handle(args *model.CommandArgs) (*model.CommandResponse, error)
	executeHelloCommand(args *model.CommandArgs) *model.CommandResponse
}

const helloCommandTrigger = "hello"

// Options configure optional collaborators on the command handler. Fields may
// be nil to disable the corresponding behavior (notifications / @user lookup).
type Options struct {
	Notifier          TaskNotifier           // done/cancelled DMs
	AssignNotifier    AssignNotifier         // assignee DM
	CommentNotifier   CommentNotifier        // comment DM
	CommentAuthorizer CommentAuthorizer      // view/comment permission (channel-aware)
	NewTaskOpener     NewTaskDialogOpener    // open New Task dialog from /task add (#95)
	QuickListOpener   QuickListDialogOpener  // open Quick List dialog from /task list (#97)
	TaskDetailOpener  TaskDetailDialogOpener // open Task Detail dialog from /task show (#97)
	Users             UserResolver           // @username -> user id for /task assign
	BotUserID         string                 // plugin bot id, used to detect bot DMs for /task add scope
}

// NewCommandHandler registers the plugin's slash commands and returns a Handler
// wired to the given task service and options.
func NewCommandHandler(client *pluginapi.Client, taskService TaskService, opts Options) Command {
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

	// /task edit <id> [summary=...] [due=...] [desc=...]
	editCmd := model.NewAutocompleteData("edit", "<id> [summary=...] [due=...] [desc=...]", "Edit task fields")
	editCmd.AddStaticListArgument("task id", true, []model.AutocompleteListItem{})
	editCmd.AddTextArgument("summary=..., due=<ms>, desc=...", "key=value pairs to update", "")
	taskCmd.AddCommand(editCmd)

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

	// /task assign <id> @user
	assignCmd := model.NewAutocompleteData("assign", "<id> @user", "Assign a task to a user")
	assignCmd.AddStaticListArgument("task id", true, []model.AutocompleteListItem{})
	assignCmd.AddTextArgument("@username of the new assignee", "@user", "")
	taskCmd.AddCommand(assignCmd)

	// /task unassign <id>
	unassignCmd := model.NewAutocompleteData("unassign", "<id>", "Remove the assignee")
	unassignCmd.AddStaticListArgument("task id", true, []model.AutocompleteListItem{})
	taskCmd.AddCommand(unassignCmd)

	// /task subtask <parentId> <summary>
	subtaskCmd := model.NewAutocompleteData("subtask", "<parentId> <summary>", "Add a subtask to a task")
	subtaskCmd.AddStaticListArgument("parent task id", true, []model.AutocompleteListItem{})
	subtaskCmd.AddTextArgument("subtask summary", "<summary>", "")
	taskCmd.AddCommand(subtaskCmd)

	// /task comment <id> <text>
	commentCmd := model.NewAutocompleteData("comment", "<id> <text>", "Add a comment to a task")
	commentCmd.AddStaticListArgument("task id", true, []model.AutocompleteListItem{})
	commentCmd.AddTextArgument("comment text", "<text>", "")
	taskCmd.AddCommand(commentCmd)

	// /task help
	helpCmd := model.NewAutocompleteData("help", "", "Show task command help")
	taskCmd.AddCommand(helpCmd)

	// /task add "<summary>"
	addCmd := model.NewAutocompleteData("add", "\"<summary>\"", "Create a new task")
	addCmd.AddTextArgument("task summary", "\"<summary>\"", "")
	taskCmd.AddCommand(addCmd)

	// /task new ["<summary>"] — opens the New Task dialog (blank, or pre-filled).
	// Mirrors the desktop channel-header "New Task" button; mobile uses this as
	// the primary create entry point. The summary is optional.
	newCmd := model.NewAutocompleteData("new", "[\"<summary>\"]", "Open the New Task dialog")
	newCmd.AddTextArgument("optional task summary", "[\"<summary>\"]", "")
	taskCmd.AddCommand(newCmd)

	// /task list [mine|channel|all] [status ...] [due ...]
	listCmd := model.NewAutocompleteData("list", "[mine|channel|all] [status] [due]", "List tasks")
	listCmd.AddStaticListArgument("scope", false, []model.AutocompleteListItem{
		{Item: "mine", HelpText: "Tasks assigned to me"},
		{Item: "channel", HelpText: "Tasks in this channel"},
		{Item: "all", HelpText: "All tasks I can see"},
	})
	taskCmd.AddCommand(listCmd)

	// /task show <id>
	showCmd := model.NewAutocompleteData("show", "<id>", "Show a task's details")
	showCmd.AddStaticListArgument("task id", true, []model.AutocompleteListItem{})
	taskCmd.AddCommand(showCmd)

	// /task search <keyword>
	searchCmd := model.NewAutocompleteData("search", "<keyword>", "Search tasks by keyword")
	searchCmd.AddTextArgument("keyword", "<keyword>", "")
	taskCmd.AddCommand(searchCmd)

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
		client:            client,
		taskService:       taskService,
		notifier:          opts.Notifier,
		assignNotifier:    opts.AssignNotifier,
		commentNotifier:   opts.CommentNotifier,
		commentAuthorizer: opts.CommentAuthorizer,
		newTaskOpener:     opts.NewTaskOpener,
		quickListOpener:   opts.QuickListOpener,
		taskDetailOpener:  opts.TaskDetailOpener,
		users:             opts.Users,
		botUserID:         opts.BotUserID,
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

// handleTask dispatches /task subcommands. The status workflow (status/done/
// cancel) and partial edit are implemented here; other subcommands are added by
// downstream issues.
func (c *Handler) handleTask(args *model.CommandArgs, subFields []string) (*model.CommandResponse, error) {
	if len(subFields) == 0 {
		return ephemeral(taskHelp()), nil
	}
	switch subFields[0] {
	case "add":
		return c.handleAdd(args, subFields[1:])
	case "new":
		return c.handleNew(args, subFields[1:])
	case "list":
		return c.handleList(args, subFields[1:])
	case "show":
		return c.handleShow(args, subFields[1:])
	case "search":
		return c.handleSearch(args, subFields[1:])
	case "status":
		return c.handleStatus(args, subFields[1:])
	case "done":
		return c.handleShortcut(args, subFields[1:], taskmodel.StatusDone, "done")
	case "cancel":
		return c.handleShortcut(args, subFields[1:], taskmodel.StatusCancelled, "cancelled")
	case "edit":
		return c.handleEdit(args, subFields[1:])
	case "remind":
		return c.handleRemind(args, subFields[1:])
	case "assign":
		return c.handleAssign(args, subFields[1:])
	case "unassign":
		return c.handleUnassign(args, subFields[1:])
	case "subtask":
		return c.handleSubtask(args, subFields[1:])
	case "comment":
		return c.handleComment(args, subFields[1:])
	case "help":
		return ephemeral(taskHelp()), nil
	default:
		return ephemeral(fmt.Sprintf("Unknown /task subcommand: %s\n\n%s", subFields[0], taskHelp())), nil
	}
}

// handleAdd implements /task add ["<summary>"] (issue #8, #95).
//
// Per PLAN.md §5.2 / review #9, `/task add "<summary>"` opens a New Task
// Interactive Dialog pre-filled with the summary so the user can fill
// assignee / due / description / scope before the task is created. When a
// trigger id is available (slash commands carry one) and the plugin wired a
// NewTaskDialogOpener, the dialog opens and the task is created on submit.
//
// Fallback: when there is no trigger id or no opener (e.g. a bare handler in
// unit tests), the task is created immediately with just the summary so the
// flow never dead-ends.
func (c *Handler) handleAdd(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	summary := strings.TrimSpace(strings.Join(rest, " "))
	// Strip surrounding quotes if the user quoted the summary.
	summary = strings.Trim(summary, "\"'")
	if summary == "" {
		return ephemeral("Usage: /task add \"<summary>\" — a summary is required."), nil
	}

	// A DM with the bot defaults to a Personal task (no channel scope). The
	// dialog builder uses an empty channelID to force the personal scope, so
	// pass "" here; otherwise the originating channel is the default scope.
	channelID := args.ChannelId
	if c.isBotDM(args.UserId, args.ChannelId) {
		channelID = ""
	}

	// Open the New Task dialog when possible; fall back to immediate create.
	if c.newTaskOpener != nil && args.TriggerId != "" {
		if c.newTaskOpener.OpenNewTask(args.TriggerId, summary, channelID) {
			// The dialog is now open; the task is created when the user submits.
			// An ephemeral hint isn't needed — the dialog itself is the feedback.
			return ephemeral(""), nil
		}
		// Opener reported failure (e.g. OpenInteractiveDialog error). Fall
		// through to immediate-create so the user isn't left with nothing.
	}

	created, err := c.taskService.Create(task.CreateInput{
		Summary:   summary,
		CreatorID: args.UserId,
		ChannelID: channelID,
	})
	if err != nil {
		return ephemeral(fmt.Sprintf("Failed to create task: %s", err.Error())), nil
	}
	return ephemeral(fmt.Sprintf("➕ Task created: **%s** (`%s`)", created.Summary, created.ID)), nil
}

// handleNew implements /task new ["<summary>"] (issue #107). It mirrors the
// desktop channel-header "New Task" button: it ALWAYS opens the New Task
// Interactive Dialog, even with no arguments (blank dialog), and optionally
// pre-fills the summary when one is supplied. This differs from /task add,
// which REQUIRES a summary and can fall back to immediate creation.
//
// Because a blank dialog is the intended UX (the user fills it in the form),
// there is no immediate-create fallback: if no trigger id or opener is
// available, we surface a clear message directing the user to /task add.
func (c *Handler) handleNew(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	summary := strings.TrimSpace(strings.Join(rest, " "))
	summary = strings.Trim(summary, "\"'")

	// A DM with the bot defaults to a Personal task (no channel scope). Same
	// rule as handleAdd: pass "" so the dialog forces the personal scope.
	channelID := args.ChannelId
	if c.isBotDM(args.UserId, args.ChannelId) {
		channelID = ""
	}

	if c.newTaskOpener != nil && args.TriggerId != "" {
		if c.newTaskOpener.OpenNewTask(args.TriggerId, summary, channelID) {
			return ephemeral(""), nil
		}
		return ephemeral("Could not open the New Task dialog. Use `/task add \"<summary>\"` instead."), nil
	}

	// No dialog path available (e.g. API-driven or bare-handler test). Give an
	// actionable hint rather than creating an empty task.
	return ephemeral("`/task new` opens a New Task dialog in the chat box. Use `/task add \"<summary>\"` to create a task immediately."), nil
}

// handleList implements /task list [mine|channel|all] [status ...] [due ...]
// (issue #9). Returns an ephemeral, paginated text list of tasks. The desktop
// RHS is the rich view; this is the chat/mobile fallback.
func (c *Handler) handleList(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	scope := task.ScopeMine
	status := ""
	due := ""
	for _, tok := range rest {
		switch tok {
		case "mine", "channel", "all":
			scope = task.Scope(tok)
		case "todo", "in_progress", "done", "cancelled":
			status = tok
		case "overdue", "today", "week":
			due = tok
		}
	}

	// channel scope requires the current channel; mine/all are user-scoped.
	channelID := ""
	if scope == task.ScopeChannel {
		channelID = args.ChannelId
	}

	// Open the Quick List Interactive Dialog when possible (PLAN §6.4 —
	// mobile/fallback path: /task list opens a dialog with filters + a task
	// picker). Fall back to the ephemeral text list when there is no trigger
	// id or no opener wired (e.g. unit tests) so the flow never dead-ends.
	if c.quickListOpener != nil && args.TriggerId != "" {
		if c.quickListOpener.OpenQuickList(args.TriggerId, args.UserId, string(scope), channelID, status, due) {
			return ephemeral(""), nil
		}
	}

	tasks, err := c.taskService.List(task.ListQuery{
		Scope:     scope,
		UserID:    args.UserId,
		ChannelID: channelID,
		Status:    status,
		Due:       due,
		Limit:     listResultLimit,
	})
	if err != nil {
		return ephemeral(fmt.Sprintf("Failed to list tasks: %s", err.Error())), nil
	}
	if len(tasks) == 0 {
		return ephemeral("No tasks found."), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Tasks (%s) — %d\n", scope, len(tasks))
	for _, t := range tasks {
		fmt.Fprintf(&b, "• `%s` %s — %s\n", t.ID, statusGlyph(t.Status), t.Summary)
	}
	return ephemeral(b.String()), nil
}

// handleShow implements /task show <id> (issue #9, #97). Opens the Task Detail
// Interactive Dialog when possible (PLAN §6.4 — mobile/fallback path); falls
// back to an ephemeral text card when there is no trigger id or no opener.
func (c *Handler) handleShow(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	if len(rest) < 1 {
		return ephemeral("Usage: /task show <id>"), nil
	}
	id := rest[0]

	// Open the Task Detail Interactive Dialog when possible (mobile-friendly,
	// editable fields on submit). The opener re-checks task existence and
	// surfaces "not found" via its own error handling.
	if c.taskDetailOpener != nil && args.TriggerId != "" {
		if c.taskDetailOpener.OpenTaskDetail(args.TriggerId, id) {
			return ephemeral(""), nil
		}
		// Opener failed (e.g. task not found or OpenInteractiveDialog error).
		// Fall through to the text card path so the user still gets feedback.
	}

	t, err := c.taskService.Get(id)
	if err != nil {
		return ephemeral(fmt.Sprintf("Failed to load task %s.", id)), nil
	}
	if t == nil {
		return ephemeral(fmt.Sprintf("Task %s not found.", id)), nil
	}
	return ephemeral(formatTaskDetail(t)), nil
}

// handleSearch implements /task search <keyword> (issue #9) as an escape hatch
// for finding tasks not in the top-N dialog list. Scans summary/description.
func (c *Handler) handleSearch(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	keyword := strings.TrimSpace(strings.Join(rest, " "))
	if keyword == "" {
		return ephemeral("Usage: /task search <keyword>"), nil
	}
	tasks, err := c.taskService.Search(keyword, searchResultLimit)
	if err != nil {
		return ephemeral(fmt.Sprintf("Search failed: %s", err.Error())), nil
	}
	if len(tasks) == 0 {
		return ephemeral(fmt.Sprintf("No tasks matching %q.", keyword)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Tasks matching %q — %d\n", keyword, len(tasks))
	for _, t := range tasks {
		fmt.Fprintf(&b, "• `%s` %s — %s\n", t.ID, statusGlyph(t.Status), t.Summary)
	}
	return ephemeral(b.String()), nil
}

// isBotDM reports whether channelID is the DM channel between the invoking user
// and the plugin bot. Used by handleAdd to default to a Personal task there.
// GetDirect takes two user ids and returns their DM channel (creating it if
// absent), so we resolve the bot↔user DM and check it matches channelID.
// Best-effort: on lookup failure it returns false so creation defaults to the
// channel scope rather than blocking.
func (c *Handler) isBotDM(userID, channelID string) bool {
	if c.botUserID == "" || userID == "" || channelID == "" {
		return false
	}
	dm, err := c.client.Channel.GetDirect(c.botUserID, userID)
	return err == nil && dm != nil && dm.Id == channelID
}

// statusGlyph returns a compact status indicator for list/search output.
func statusGlyph(status string) string {
	switch status {
	case taskmodel.StatusDone:
		return "✅"
	case taskmodel.StatusCancelled:
		return "🚫"
	case taskmodel.StatusInProgress:
		return "🔄"
	default:
		return "◻️"
	}
}

// formatTaskDetail renders a single task as an ephemeral text card.
func formatTaskDetail(t *taskmodel.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** %s `%s`\n", t.Summary, statusGlyph(t.Status), t.ID)
	if t.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", t.Description)
	}
	fmt.Fprintf(&b, "\nStatus: %s", t.Status)
	if t.AssigneeID != "" {
		fmt.Fprintf(&b, " · Assignee: %s", t.AssigneeID)
	}
	if t.Due != nil {
		fmt.Fprintf(&b, " · Due: %s", time.UnixMilli(*t.Due).Format("2006-01-02 15:04"))
	}
	return b.String()
}

// listResultLimit / searchResultLimit cap ephemeral output length.
const (
	listResultLimit   = 25
	searchResultLimit = 15
)

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
		t, err := c.taskService.ClearReminder(args.UserId, id)
		if err != nil {
			return formatReminderError(c, id, err, "clear reminder")
		}
		return ephemeral(fmt.Sprintf("🔔 Reminder turned off for **%s**.", t.Summary)), nil
	}

	offsetMS, ok := parseReminderOffset(token)
	if !ok {
		return ephemeral(fmt.Sprintf("Unknown reminder %q. Use 15m, 1h, 1d, or off.", token)), nil
	}

	t, err := c.taskService.SetReminder(args.UserId, id, offsetMS)
	if err != nil {
		return formatReminderError(c, id, err, "set reminder")
	}
	return ephemeral(fmt.Sprintf("🔔 Reminder set for **%s** (%s before due).", t.Summary, token)), nil
}

// formatReminderError maps reminder service errors to ephemeral text. Raw
// backend errors are logged server-side but never echoed to the user.
func formatReminderError(c *Handler, id string, err error, action string) (*model.CommandResponse, error) {
	switch {
	case errors.Is(err, task.ErrNotFound):
		return ephemeral(fmt.Sprintf("Task %s not found.", id)), nil
	case errors.Is(err, task.ErrReminderNeedsDue):
		return ephemeral("This task has no due date, so a reminder cannot be set."), nil
	default:
		c.client.Log.Error("Failed to "+action, "task_id", id, "error", err)
		return ephemeral(fmt.Sprintf("Failed to %s for task %s. Please try again.", action, id)), nil
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

// handleAssign implements /task assign <id> @user.
func (c *Handler) handleAssign(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	if len(rest) < 2 {
		return ephemeral("Usage: /task assign <id> @user"), nil
	}
	id, mention := rest[0], rest[1]

	username := strings.TrimPrefix(mention, "@")
	if username == "" || username == mention {
		// Not a mention.
		return ephemeral("Please specify the assignee as @username."), nil
	}

	if c.users == nil {
		return ephemeral("User lookup is not available; use the REST API to assign."), nil
	}
	userID := c.users.UserIDByUsername(username)
	if userID == "" {
		return ephemeral(fmt.Sprintf("User @%s not found.", username)), nil
	}

	t, ev, err := c.taskService.Assign(args.UserId, id, userID)
	if err != nil {
		return formatAssignError(c, id, err)
	}

	// DM the newly assigned user (skipped when assignee == creator).
	if c.assignNotifier != nil {
		c.assignNotifier.NotifyAssigned(ev.NewAssigneeID, ev.CreatorID, AssignRef{ID: t.ID, Summary: t.Summary})
	}
	return ephemeral(fmt.Sprintf("👤 Task **%s** assigned to @%s.", t.Summary, username)), nil
}

// handleUnassign implements /task unassign <id>. No DM is sent on unassign.
func (c *Handler) handleUnassign(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	if len(rest) < 1 {
		return ephemeral("Usage: /task unassign <id>"), nil
	}
	id := rest[0]

	t, _, err := c.taskService.Assign(args.UserId, id, "")
	if err != nil {
		return formatAssignError(c, id, err)
	}
	return ephemeral(fmt.Sprintf("👤 Assignee removed from **%s**.", t.Summary)), nil
}

// formatAssignError maps assignee service errors to ephemeral text.
func formatAssignError(c *Handler, id string, err error) (*model.CommandResponse, error) {
	if errors.Is(err, task.ErrNotFound) {
		return ephemeral(fmt.Sprintf("Task %s not found.", id)), nil
	}
	c.client.Log.Error("Failed to change assignee", "task_id", id, "error", err)
	return ephemeral(fmt.Sprintf("Failed to update task %s: %s", id, err.Error())), nil
}

// handleSubtask implements /task subtask <parentId> <summary>. Only the creator
// or current assignee of the parent may add subtasks. The summary runs to the
// end of the line (may contain spaces).
func (c *Handler) handleSubtask(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	if len(rest) < 2 {
		return ephemeral("Usage: /task subtask <parentId> <summary>"), nil
	}
	parentID := rest[0]
	summary := strings.TrimSpace(strings.Join(rest[1:], " "))
	if summary == "" {
		return ephemeral("Usage: /task subtask <parentId> <summary>"), nil
	}

	// Permission: only the creator or current assignee may add subtasks.
	parent, err := c.taskService.Get(parentID)
	if err != nil {
		c.client.Log.Error("Failed to load parent for subtask", "task_id", parentID, "error", err)
		return ephemeral(fmt.Sprintf("Failed to add subtask to %s. Please try again.", parentID)), nil
	}
	if parent == nil {
		return ephemeral(fmt.Sprintf("Task %s not found.", parentID)), nil
	}
	if args.UserId != parent.CreatorID && args.UserId != parent.AssigneeID {
		return ephemeral("You do not have permission to add subtasks to this task."), nil
	}

	created, err := c.taskService.CreateSubtask(parentID, args.UserId, summary, "", nil)
	if err != nil {
		if errors.Is(err, task.ErrParentNotFound) {
			return ephemeral(fmt.Sprintf("Task %s not found.", parentID)), nil
		}
		c.client.Log.Error("Failed to create subtask", "parent_id", parentID, "error", err)
		return ephemeral(fmt.Sprintf("Failed to add subtask to %s: %s", parentID, err.Error())), nil
	}
	return ephemeral(fmt.Sprintf("➕ Subtask **%s** added to **%s**.", created.Summary, parent.Summary)), nil
}

// handleComment implements /task comment <id> <text>. Anyone who can view the
// task may comment. The text runs to the end of the line (may contain spaces).
// A new comment DMs the task participants (creator + assignee), excluding the
// commenter.
func (c *Handler) handleComment(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	if len(rest) < 2 {
		return ephemeral("Usage: /task comment <id> <text>"), nil
	}
	id := rest[0]
	text := strings.TrimSpace(strings.Join(rest[1:], " "))
	if text == "" {
		return ephemeral("Usage: /task comment <id> <text>"), nil
	}

	// Resolve the task so the success message can name it and the notifier has
	// the participants. The service re-checks existence on LinkComment.
	t, err := c.taskService.Get(id)
	if err != nil {
		c.client.Log.Error("Failed to load task for comment", "task_id", id, "error", err)
		return ephemeral(fmt.Sprintf("Failed to comment on %s. Please try again.", id)), nil
	}
	if t == nil {
		return ephemeral(fmt.Sprintf("Task %s not found.", id)), nil
	}
	// Authorization: anyone who can view the task may comment. When a channel-aware
	// authorizer is wired, use it (covers channel members); otherwise fall back to
	// the personal-task co-owner rule (creator or assignee).
	if c.commentAuthorizer != nil {
		if !c.commentAuthorizer.CanComment(args.UserId, t) {
			return ephemeral("You do not have permission to comment on this task."), nil
		}
	} else if args.UserId != t.CreatorID && args.UserId != t.AssigneeID {
		return ephemeral("You do not have permission to comment on this task."), nil
	}

	// Comment-as-thread: create the reply post in the task's card thread, then
	// link the post to the task. The post root is the task's channel card so
	// the reply threads under the card; the bot authors the post so it renders
	// consistently.
	rootID := t.ChannelPostID
	if rootID == "" {
		rootID = t.DMPostID
	}
	commentPost := &model.Post{
		UserId:    c.botUserID,
		ChannelId: args.ChannelId,
		RootId:    rootID,
		Message:   text,
		Type:      model.PostTypeDefault,
	}
	if postErr := c.client.Post.CreatePost(commentPost); postErr != nil {
		c.client.Log.Error("Failed to create comment post", "task_id", id, "error", postErr)
		return ephemeral(fmt.Sprintf("Failed to comment on %s: %s", id, postErr.Error())), nil
	}

	_, ev, err := c.taskService.LinkComment(id, commentPost.Id, args.UserId)
	if err != nil {
		switch {
		case errors.Is(err, task.ErrNotFound):
			return ephemeral(fmt.Sprintf("Task %s not found.", id)), nil
		default:
			c.client.Log.Error("Failed to link comment", "task_id", id, "error", err)
			return ephemeral(fmt.Sprintf("Failed to comment on %s: %s", id, err.Error())), nil
		}
	}

	if c.commentNotifier != nil {
		c.commentNotifier.NotifyCommented(
			TaskRef{ID: t.ID, Summary: t.Summary},
			ev.UserID, ev.CreatorID, ev.AssigneeID,
		)
	}
	return ephemeral(fmt.Sprintf("💬 Comment added to **%s**.", t.Summary)), nil
}

// setStatus calls the service and formats the result for the user.
func (c *Handler) setStatus(args *model.CommandArgs, id, status string) (*model.CommandResponse, error) {
	t, err := c.taskService.SetStatus(args.UserId, id, status)
	if err != nil {
		switch {
		case errors.Is(err, task.ErrNotFound):
			return ephemeral(fmt.Sprintf("Task %s not found.", id)), nil
		case errors.Is(err, task.ErrInvalidStatus):
			return ephemeral(fmt.Sprintf("Invalid status %q.", status)), nil
		case errors.As(err, &task.ErrOpenSubtasks{}):
			// Parent-done guard: show the actionable message (lists open subtasks).
			return ephemeral("⚠️ " + err.Error()), nil
		default:
			c.client.Log.Error("Failed to set task status", "task_id", id, "status", status, "error", err)
			return ephemeral(fmt.Sprintf("Failed to update task %s. Please try again.", id)), nil
		}
	}

	// Fire done/cancelled DMs to participants (minus the actor).
	if c.notifier != nil && t != nil {
		ref := TaskRef{ID: t.ID, Summary: t.Summary}
		switch status {
		case taskmodel.StatusDone:
			c.notifier.NotifyCompleted(ref, args.UserId, t.CreatorID, t.AssigneeID)
		case taskmodel.StatusCancelled:
			c.notifier.NotifyCancelled(ref, args.UserId, t.CreatorID, t.AssigneeID)
		}
	}

	return ephemeral(fmt.Sprintf("✅ Task **%s** is now **%s**.", t.Summary, status)), nil
}

// handleEdit implements /task edit <id> [key=value ...].
//
// Recognized keys:
//
//	summary=<text>   new summary
//	desc=<text>      new description (description= is accepted too)
//	due=<ms>         new due date as a millisecond timestamp (use 0 to clear)
//
// Only the supplied keys are modified (partial update). Fields not listed are
// left untouched.
func (c *Handler) handleEdit(args *model.CommandArgs, rest []string) (*model.CommandResponse, error) {
	if len(rest) < 1 {
		return ephemeral("Usage: /task edit <id> [summary=...] [due=<ms>] [desc=...]"), nil
	}
	id := rest[0]
	if len(rest) < 2 {
		return ephemeral("Nothing to edit. Use key=value pairs, e.g. /task edit <id> summary=New due=1700000000000"), nil
	}

	in, bad := parseEditFields(rest[1:])
	if bad != "" {
		return ephemeral(fmt.Sprintf("Could not parse %q. Expected key=value (summary, due, desc).", bad)), nil
	}

	t, err := c.taskService.Patch(args.UserId, id, in)
	if err != nil {
		switch {
		case errors.Is(err, task.ErrNotFound):
			return ephemeral(fmt.Sprintf("Task %s not found.", id)), nil
		default:
			c.client.Log.Error("Failed to edit task", "task_id", id, "error", err)
			return ephemeral(fmt.Sprintf("Failed to update task %s: %s", id, err.Error())), nil
		}
	}
	return ephemeral(fmt.Sprintf("✏️ Task **%s** updated.", t.Summary)), nil
}

// parseEditFields parses the key=value tokens after /task edit <id> into a
// PatchInput. It returns the input plus the first offending token (empty when
// all tokens parsed successfully). due=<ms> must be a valid integer.
//
// Text fields (summary/description) may contain spaces: their value runs from
// the text after "key=" up to (but not including) the next recognized key
// token, so "summary=New title due=1700000000000" sets summary to "New title".
func parseEditFields(tokens []string) (task.PatchInput, string) {
	var in task.PatchInput
	i := 0
	for i < len(tokens) {
		tok := tokens[i]
		key, value, found := strings.Cut(tok, "=")
		if !found {
			return in, tok
		}

		switch strings.ToLower(key) {
		case "summary":
			// Consume following tokens until the next recognized key token.
			value, i = collectValue(value, tokens, i+1)
			in.UpdateFields = append(in.UpdateFields, "summary")
			in.Summary = &value
		case "desc", "description":
			value, i = collectValue(value, tokens, i+1)
			in.UpdateFields = append(in.UpdateFields, "description")
			in.Description = &value
		case "due":
			ms, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return in, tok
			}
			in.UpdateFields = append(in.UpdateFields, "due")
			if ms == 0 {
				in.Due = nil // clear
			} else {
				in.Due = &ms
			}
		default:
			return in, tok
		}
		i++
	}
	return in, ""
}

// knownEditKey reports whether tok begins with a recognized edit key, e.g.
// "summary=", "desc=", "description=", "due=".
func knownEditKey(tok string) bool {
	key, _, found := strings.Cut(tok, "=")
	if !found {
		return false
	}
	switch strings.ToLower(key) {
	case "summary", "desc", "description", "due":
		return true
	}
	return false
}

// collectValue appends to value every token from tokens[start:] that is not a
// recognized edit key, joining them with spaces. It returns the joined value
// and the index of the last consumed token (or start-1 when nothing consumed).
func collectValue(value string, tokens []string, start int) (string, int) {
	last := start - 1
	for j := start; j < len(tokens); j++ {
		if knownEditKey(tokens[j]) {
			break
		}
		value = strings.TrimSpace(value + " " + tokens[j])
		last = j
	}
	return strings.TrimSpace(value), last
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
		"• `/task new [\"<summary>\"]` — open the New Task dialog (blank or pre-filled)\n" +
		"• `/task add \"<summary>\"` — create a new task\n" +
		"• `/task list [mine|channel|all] [status] [due]` — list and filter tasks\n" +
		"• `/task show <id>` — view task details\n" +
		"• `/task search <keyword>` — search tasks by keyword\n" +
		"• `/task status <id> <todo|in_progress|done|cancelled>` — change status\n" +
		"• `/task done <id>` — mark a task done\n" +
		"• `/task cancel <id>` — cancel a task\n" +
		"• `/task edit <id> [summary=...] [due=<ms>] [desc=...]` — partial update\n" +
		"• `/task assign <id> @user` — assign a task to a user\n" +
		"• `/task unassign <id>` — remove the assignee\n" +
		"• `/task subtask <parentId> <summary>` — add a subtask\n" +
		"• `/task comment <id> <text>` — add a comment\n" +
		"• `/task remind <id> <15m|1h|1d|off>` — set or turn off a reminder\n" +
		"• `/task help` — show this help"
}
