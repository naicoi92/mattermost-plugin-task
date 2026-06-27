// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// TaskDetailPanel is the detail view shown inside the RHS when a task is
// selected. It renders the task's summary, status, priority, due date,
// assignee, the subtask list with an "x/y done" progress summary, the
// description as a click-to-edit block, and the activity feed (comments) with
// @mention-style avatars. Actions mutate via the API client and dispatch into
// the Redux store so the change is reflected immediately and broadcast over
// WebSocket.

import * as client from 'client';
import {ClientError} from 'client';
import type {UserSearchResult} from 'client';
import {useActiveLocale, useFormatMessage} from 'i18n_utils';
import React, {useEffect, useRef, useState} from 'react';
import {useDispatch, useSelector} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import type {GlobalState} from '@mattermost/types/store';

import {
    getChannel,
    getCurrentChannelId,
} from 'mattermost-redux/selectors/entities/channels';
import {
    getCurrentUserId,
    getUser,
} from 'mattermost-redux/selectors/entities/users';

import formatDueRelative from 'components/shared/format_due_relative';
import MetaDropdown from 'components/shared/meta_dropdown';
import {PriorityDot, priorityLabel} from 'components/shared/priority_pill';
import StatusPill, {statusLabel} from 'components/shared/status_pill';
import TaskCheck from 'components/shared/task_check';
import {
    applyMention,
    detectMention,
} from 'components/task_detail_panel/mention';
import {isDueSoon} from 'components/task_sidebar/quick_list';
import {
    useResolvedUser,
    useResolvedUsers,
    useResolvedStatuses,
} from 'components/user_picker/use_resolved_user';
import UserPicker from 'components/user_picker/user_picker';

import type {
    Task,
    Comment,
    PatchTaskInput,
    TaskEvent,
    TaskPriority,
} from 'types/tasks';
import {TaskEventType} from 'types/tasks';

// The plugin reducer is mounted by registerReducer at
// state['plugins-<pluginId>'] (Mattermost convention), so the slice lives at a
// top-level key named with the plugin id — not under state.plugins.
interface PluginState {
    selectedTaskID: string;
    selectedTask: Task | null;
    commentRev: Record<string, number>;
}

type GlobalStateWithPlugin = Record<string, unknown> & {
    'plugins-com.mattermost.plugin-task'?: PluginState;
};

const PLUGIN_STATE_KEY = 'plugins-com.mattermost.plugin-task';

function selectSlice(state: GlobalStateWithPlugin): PluginState {
    return (
        state[PLUGIN_STATE_KEY] ?? {
            selectedTaskID: '',
            selectedTask: null,
            commentRev: {},
        }
    );
}

export interface TaskDetailPanelProps {

    // taskID overrides the store selection (e.g. when opened with a fixed id).
    taskID?: string;

    // onBack returns to the previous view (parent task when viewing a subtask,
    // Quick List otherwise).
    onBack?: () => void;

    // currentUserID gates the delete control (creator/assignee may delete).
    currentUserID?: string;

    // onOpenSubtask opens a subtask's detail view (the subtask is itself a
    // Task). When omitted, subtask rows are not navigable.
    onOpenSubtask?: (taskID: string) => void;
}

export default function TaskDetailPanel({
    taskID: taskIDProp,
    onBack,
    currentUserID,
    onOpenSubtask,
}: TaskDetailPanelProps): JSX.Element | null {
    const dispatch = useDispatch();
    const t = useFormatMessage();
    const locale = useActiveLocale();

    const slice = useSelector(selectSlice);
    const taskID = taskIDProp ?? slice.selectedTaskID;
    const task = taskIDProp ? null : slice.selectedTask;

    // Comment-change signal: the reducer bumps commentRev[taskID] on a WS
    // task_updated with changedFields=["comment"] (Decision 2). Keying the
    // comment-refetch effect on this value lets a second viewer's open panel
    // refetch comments WITHOUT reselecting the task (AC3).
    const commentRevForTask = slice.commentRev[taskID] ?? 0;

    const [full, setFull] = useState<Task | null>(task);
    const [subtasks, setSubtasks] = useState<Task[]>([]);
    const [comments, setComments] = useState<Comment[]>([]);

    // events is the task's audit trail for the merged Activity feed (Decision 3).
    const [events, setEvents] = useState<TaskEvent[]>([]);
    const [error, setError] = useState<string>('');
    const [newComment, setNewComment] = useState('');
    const [newSubtask, setNewSubtask] = useState('');
    const [subtaskAdding, setSubtaskAdding] = useState(false);
    const [editingDescription, setEditingDescription] = useState(false);
    const [editingTitle, setEditingTitle] = useState(false);
    const [editingDue, setEditingDue] = useState(false);

    // Inline-edit buffers for summary / due / description. They mirror `full`
    // so the field renders the server value until the user starts editing,
    // then commit on blur/Enter via patchTask.
    const [editSummary, setEditSummary] = useState('');
    const [editDueLocal, setEditDueLocal] = useState('');
    const [editDescription, setEditDescription] = useState('');
    const [saving, setSaving] = useState(false);
    const subtaskInputRef = useRef<HTMLInputElement>(null);

    // composerRef backs the auto-growing textarea (AC7) so onInput can read
    // scrollHeight and cap it at 120px.
    const composerRef = useRef<HTMLTextAreaElement>(null);

    // @mention autocomplete state (FEAT-C). `mention` drives the dropdown;
    // mentionReqId drops stale searchUsers responses, mentionCache avoids
    // refetching the same query@channel, and mentionTimer debounces the fetch.
    const [mention, setMention] = useState<{
        open: boolean;
        query: string;
        candidates: UserSearchResult[];
        highlight: number;
    }>({open: false, query: '', candidates: [], highlight: 0});
    const mentionReqId = useRef(0);
    const mentionCache = useRef<Map<string, UserSearchResult[]>>(new Map());
    const mentionTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

    // Load the task + subtasks + comments + activity events whenever the
    // selected id changes.
    // Reset the panel's data state immediately on taskID change so the UI never
    // briefly renders task A's content while task B loads (CR #4). The fetch
    // effect below repopulates these once the new task's data arrives.
    useEffect(() => {
        setFull(null);
        setSubtasks([]);
        setComments([]);
        setEvents([]);
        setError('');

        // Reset draft composer/subtask state so a previous task's draft can't
        // be submitted to the next one (CR re-review). Also cancel any pending
        // mention debounce so a stale searchUsers result can't repopulate the
        // dropdown for the new task.
        setNewComment('');
        setNewSubtask('');
        setSubtaskAdding(false);
        setMention({open: false, query: '', candidates: [], highlight: 0});
        if (mentionTimer.current !== null) {
            clearTimeout(mentionTimer.current);
            mentionTimer.current = null;
        }
        mentionReqId.current++;
    }, [taskID]);

    useEffect(() => {
        let cancelled = false;
        const load = async () => {
            if (!taskID) {
                setFull(null);
                return;
            }
            try {
                // allSettled: a single sub-request failure (e.g. a transient
                // 403 on one slice) must not blank the whole panel. We render
                // whatever succeeded and surface the first rejection as the
                // error. This keeps the task visible even when comments/events
                // briefly fail.
                const results = await Promise.allSettled([
                    client.getTask(taskID),
                    client.listSubtasks(taskID),
                    client.listComments(taskID),
                    client.listTaskEvents(taskID),
                ]);
                if (cancelled) {
                    return;
                }
                const detail =
					results[0].status === 'fulfilled' ? results[0].value : undefined;
                const subs =
					results[1].status === 'fulfilled' ? results[1].value : undefined;
                const coms =
					results[2].status === 'fulfilled' ? results[2].value : undefined;
                const evs =
					results[3].status === 'fulfilled' ? results[3].value : undefined;
                if (!detail) {
                    // The task itself failed to load — surface the reason.
                    const r = results[0] as PromiseRejectedResult;
                    setError(messageFor(r.reason));
                    return;
                }
                setFull(detail);

                // On partial failure, keep whatever was already loaded rather than
                // blanking the panel: a failing slice (e.g. a transient comment/event
                // 403) must not discard the successful ones or previously-loaded data.
                if (subs) {
                    setSubtasks(subs);
                }
                if (coms) {
                    setComments(coms);
                }
                if (evs) {
                    setEvents(evs);
                }
                setEditingTitle(false);
                setEditingDue(false);
                setEditingDescription(false);
                setEditSummary(detail.summary);
                setEditDueLocal(detail.due ? dueToLocalInput(detail.due) : '');
                setEditDescription(detail.description);
                dispatch({type: ACTION_TYPES.SET_SELECTED_TASK, task: detail});
                setError('');
            } catch (err) {
                if (cancelled) {
                    return;
                }
                setError(messageFor(err));
            }
        };
        load();
        return () => {
            cancelled = true;
        };
    }, [taskID, dispatch]);

    // Comment-only refetch on a WS changedFields=["comment"] event (AC3):
    // a separate effect keyed on commentRevForTask refetches ONLY comments so a
    // second viewer's open panel updates without reselecting the task and
    // without clobbering in-flight edits. The full-load effect above handles
    // selection changes; this effect handles realtime comment changes only.
    const inflightRef = useRef<Promise<void> | null>(null);
    const pendingRef = useRef<boolean>(false);
    useEffect(() => {
        if (!taskID) {
            return undefined;
        }

        // If a refetch is already in flight, coalesce: remember to re-run once it
        // settles instead of starting a parallel duplicate fetch (AC3 dedupe).
        if (inflightRef.current) {
            pendingRef.current = true;
            return undefined;
        }
        const controller = new AbortController();
        const run = async () => {
            try {
                // allSettled: a comment/events refetch failure is non-fatal
                // (handled below). Use allSettled so a failing slice does not
                // discard a succeeding one.
                const results = await Promise.allSettled([
                    client.listComments(taskID),
                    client.listTaskEvents(taskID),
                ]);
                if (controller.signal.aborted) {
                    return;
                }
                const coms =
					results[0].status === 'fulfilled' ? results[0].value : undefined;
                const evs =
					results[1].status === 'fulfilled' ? results[1].value : undefined;

                // Only overwrite state for slices that succeeded; a failing slice
                // keeps the previously-loaded data (no blanking on transient error).
                if (coms) {
                    setComments(coms);
                }
                if (evs) {
                    setEvents(evs);
                }
            } catch (err) {
                if (controller.signal.aborted) {
                    return;
                }

                // A refetch failure is non-fatal here; the next bump retries.
                setError(messageFor(err));
            } finally {
                inflightRef.current = null;
                if (pendingRef.current) {
                    pendingRef.current = false;

                    // Re-run once after settling. Reassign inflightRef so the
                    // dedupe guard keeps covering the rerun (AC3).
                    inflightRef.current = run();
                }
            }
        };
        inflightRef.current = run();
        return () => {
            controller.abort();
        };
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [taskID, commentRevForTask]);

    // Resolve the assignee id → "@username" for display, and comment author
    // ids → labels. Hooks run before the early returns so the order is stable.
    // assigneeUser carries the profile (with .username) so the mention dropdown
    // can seed candidates with the known assignee participant (FEAT-C).
    const assigneeResolved = useResolvedUser(full?.assignee_id ?? '');
    const assigneeLabel = assigneeResolved.label;
    const assigneeUser = assigneeResolved.user;

    // The channel whose members seed the @mention candidates: the task's home
    // channel (where its card lives), falling back to the channel the viewer
    // is acting from. '' scopes searchUsers to globally visible users.
    // Declared after currentChannelID below; see mentionChannelID assignment.

    // Resolve labels for BOTH comment authors and event actors (the merged
    // Activity feed interleaves them, Decision 3).
    const actorIDs = Array.from(
        new Set(
            [
                ...comments.map((c) => c.author_id),
                ...events.map((e) => e.actor_id),
            ].filter(Boolean),
        ),
    );
    const actorLabels = useResolvedUsers(actorIDs);

    // Resolve each actor's presence status (online/away/dnd/offline) to drive
    // the avatar status-dot modifier class (AC5/AC6 — data-driven, not a
    // hardcoded offline default). Missing keys fall back to the offline
    // modifier via actorStatusClass.
    const actorStatuses = useResolvedStatuses(actorIDs);

    // The merged newest-first Activity feed (events + comments interleaved,
    // Decision 3 / AC5). Computed from the fetched comments + events.
    const activity = mergeActivity(comments, events);

    // Resolve the channel display name (not the raw id) for the meta-table.
    // Called unconditionally (rules-of-hooks) before the early return; the
    // selector returns '' when there is no channel.
    const channelIDForSelector = full?.channel_id ?? '';

    // The channel the user is currently viewing (center pane). Share Task posts
    // the card here. '' when there is no current channel (Share is disabled).
    const currentChannelID = useSelector(getCurrentChannelId) || '';

    // @mention candidate channel (FEAT-C): the task's home channel, else the
    // viewer's current channel. '' => globally visible users (personal task).
    const mentionChannelID = full?.channel_id || currentChannelID || '';

    // Resolve a human-friendly channel name. For a team/public channel this is
    // the channel display_name. For a DM (name like "<uid1>__<uid2>") the
    // Mattermost store leaves display_name empty and name as the raw id pair;
    // resolve the partner's username, or fall back to a generic label.
    const channelName = useSelector((s: GlobalState) => {
        if (!channelIDForSelector) {
            return '';
        }
        const ch = getChannel(s as never, channelIDForSelector);
        if (ch?.display_name) {
            return ch.display_name;
        }

        // DM fallback: name is "<uid1>__<uid2>". Resolve the non-current user's
        // username from the users store.
        if (ch?.name && ch.name.includes('__')) {
            const me = getCurrentUserId(s as never);
            const partner = ch.name.split('__').find((id) => id && id !== me);
            if (partner) {
                const u = getUser(s as never, partner);
                if (u?.username) {
                    return '@' + u.username;
                }
            }

            // User not loaded in store — show generic label instead of raw ids.
            return 'Direct Message';
        }

        // Channel not loaded or team channel without display_name.
        return ch?.display_name || '';
    });

    // Resolve the parent task's summary so the meta-table can show a readable
    // label instead of the raw ULID. Read from the plugin cache (best-effort).
    const parentTaskIDForSelector = full?.parent_task_id ?? '';
    const parentSummary = useSelector((s: GlobalStateWithPlugin) => {
        if (!parentTaskIDForSelector) {
            return '';
        }
        const pluginSlice = s[PLUGIN_STATE_KEY];
        const tasks = (
            pluginSlice as { tasks?: Record<string, { summary?: string }> }
        ).tasks;
        return tasks?.[parentTaskIDForSelector]?.summary || '';
    });

    if (!taskID) {
        return null;
    }
    if (!full) {
        return (
            <div className='task-detail task-detail--loading'>
                <div className='task-detail__error-block'>
                    {error || t('webapp.task.empty')}
                </div>
            </div>
        );
    }

    const doneCount = subtasks.filter(
        (s) => s.status === 'done' || s.status === 'cancelled',
    ).length;

    const changeStatus = async (status: Task['status']) => {
        try {
            const updated = await client.setTaskStatus(full.id, status);
            setFull(updated);
            dispatch({type: ACTION_TYPES.UPSERT_TASK, task: updated});
        } catch (err) {
            setError(messageFor(err));
        }
    };

    // toggleCheckboxStatus is the checkbox behavior: Done ↔ In Progress only.
    // Open (todo/in_progress) → Done; terminal (done/cancelled) → In Progress.
    // Other transitions are done via the Status dropdown in the meta-table.
    const toggleCheckboxStatus = () => {
        const terminal = full.status === 'done' || full.status === 'cancelled';
        changeStatus(terminal ? 'in_progress' : 'done');
    };

    // changePriority sets an explicit priority via PATCH (selected from the
    // Priority dropdown in the meta-table).
    const changePriority = async (priority: TaskPriority) => {
        if (priority === (full.priority || 'standard')) {
            return;
        }
        try {
            const input: PatchTaskInput = {
                update_fields: ['priority'],
                priority,
            };
            const updated = await client.patchTask(full.id, input);
            setFull(updated);
            dispatch({type: ACTION_TYPES.UPSERT_TASK, task: updated});
        } catch (err) {
            setError(messageFor(err));
        }
    };

    // patchField commits a single editable field (summary/description/due) via
    // PATCH /tasks/:id. Only fields whose value actually changed are sent, so
    // blur-without-edit is a no-op.
    const patchField = async (
        field: 'summary' | 'description' | 'due',
        nextValue: string,
    ): Promise<void> => {
        const currentByField: Record<typeof field, string> = {
            summary: full.summary,
            description: full.description,
            due: full.due ? dueToLocalInput(full.due) : '',
        };
        if (nextValue === currentByField[field]) {
            return; // unchanged — no PATCH.
        }
        setSaving(true);
        try {
            const input: PatchTaskInput = {update_fields: [field]};
            if (field === 'summary') {
                input.summary = nextValue;
            } else if (field === 'description') {
                input.description = nextValue || null;
            } else {
                const ms = localInputToDue(nextValue);
                input.due = ms === null ? null : ms;
            }
            const updated = await client.patchTask(full.id, input);
            setFull(updated);
            setEditSummary(updated.summary);
            setEditDueLocal(updated.due ? dueToLocalInput(updated.due) : '');
            setEditDescription(updated.description);
            dispatch({type: ACTION_TYPES.UPSERT_TASK, task: updated});
        } catch (err) {
            setError(messageFor(err));

            // Revert the buffer to the server value on failure.
            if (field === 'summary') {
                setEditSummary(full.summary);
            } else if (field === 'description') {
                setEditDescription(full.description);
            } else {
                setEditDueLocal(full.due ? dueToLocalInput(full.due) : '');
            }
        } finally {
            setSaving(false);
        }
    };

    const setAssignee = async (userID: string) => {
        try {
            if (userID) {
                const updated = await client.setTaskAssignee(full.id, userID);
                setFull(updated);
                dispatch({type: ACTION_TYPES.UPSERT_TASK, task: updated});
            } else {
                const updated = await client.removeTaskAssignee(full.id);
                setFull(updated);
                dispatch({type: ACTION_TYPES.UPSERT_TASK, task: updated});
            }
        } catch (err) {
            setError(messageFor(err));
        }
    };

    const addSubtask = async () => {
        const summary = newSubtask.trim();
        if (!summary) {
            return;
        }
        try {
            const created = await client.createSubtask(full.id, {summary});
            setSubtasks((prev) => [...prev, created]);
            setNewSubtask('');
        } catch (err) {
            setError(messageFor(err));
        }

        // Stay in adding mode + refocus so the user can keep adding subtasks.
        requestAnimationFrame(() => subtaskInputRef.current?.focus());
    };

    // commitSubtask commits the typed subtask when non-empty; an empty value
    // collapses the inline trigger back to its resting state.
    const commitSubtask = () => {
        if (newSubtask.trim()) {
            addSubtask();
        } else {
            cancelSubtaskAdd();
        }
    };

    const cancelSubtaskAdd = () => {
        setSubtaskAdding(false);
        setNewSubtask('');
    };

    const openSubtaskAdd = () => {
        setSubtaskAdding(true);
        requestAnimationFrame(() => subtaskInputRef.current?.focus());
    };

    const toggleSubtaskDone = async (sub: Task) => {
        // "cancelled" renders as checked in the UI, so it is a terminal state
        // alongside "done" — toggling either returns the subtask to "todo".
        const terminal = sub.status === 'done' || sub.status === 'cancelled';
        const next = terminal ? 'todo' : 'done';
        const prev = sub.status;
        setSubtasks((cur) =>
            cur.map((x) => (x.id === sub.id ? {...x, status: next} : x)),
        );
        try {
            const updated = await client.setTaskStatus(sub.id, next);
            setSubtasks((cur) => cur.map((x) => (x.id === sub.id ? updated : x)));
        } catch (err) {
            setSubtasks((cur) =>
                cur.map((x) => (x.id === sub.id ? {...x, status: prev} : x)),
            );
            setError(messageFor(err));
        }
    };

    const addComment = async () => {
        const content = newComment.trim();
        if (!content) {
            return;
        }
        try {
            // Change B: pass the active channel so the server threads the comment
            // under the task's card IN this channel (e.g. a shared card), not the
            // home channel card.
            const created = await client.createComment(full.id, {
                content,
                channel_id: currentChannelID || undefined,
            });
            setComments((prev) => [created, ...prev]);
            setNewComment('');
        } catch (err) {
            setError(messageFor(err));
        }
    };

    // closeMention resets the @mention dropdown state and cancels any pending
    // debounced fetch (FEAT-C).
    const closeMention = () => {
        if (mentionTimer.current !== null) {
            clearTimeout(mentionTimer.current);
            mentionTimer.current = null;
        }
        setMention((s) => ({...s, open: false}));
    };

    // fetchMentionCandidates runs the (debounced) searchUsers call for the
    // current query@channel, deduped against the known assignee participant,
    // and drops stale responses via mentionReqId. Results are cached per
    // query@channel for the life of the panel.
    const fetchMentionCandidates = (query: string) => {
        const cacheKey = `${query}@${mentionChannelID}`;

        // Invalidate any prior scheduled/in-flight lookup FIRST, before the cache
        // fast-path. Otherwise a cached-query sequence like cached "@a" -> type
        // "@ab" -> backspace to cached "@a" would leave the in-flight "ab"
        // request alive to overwrite the restored "a" candidates (CR re-review).
        if (mentionTimer.current !== null) {
            clearTimeout(mentionTimer.current);
            mentionTimer.current = null;
        }
        const myReq = ++mentionReqId.current;

        const mergeParticipants = (
            list: UserSearchResult[],
            query: string,
        ): UserSearchResult[] => {
            // Seed with the known assignee (if resolved and not already present)
            // so the dropdown is never empty while the fetch is in flight — but
            // ONLY when the current query is empty or matches the assignee's
            // username. Otherwise the dropdown would preselect an unrelated
            // assignee for a query they don't match (CR #5).
            const seed: UserSearchResult[] = [];
            if (assigneeUser?.username && assigneeUser.id) {
                const q = query.trim().toLowerCase();
                const matches =
					q === '' || assigneeUser.username.toLowerCase().includes(q);
                if (matches) {
                    seed.push({
                        id: assigneeUser.id,
                        username: assigneeUser.username,
                        first_name: assigneeUser.first_name,
                        last_name: assigneeUser.last_name,
                        nickname: assigneeUser.nickname,
                        is_bot: assigneeUser.is_bot,
                        delete_at: assigneeUser.delete_at,
                    });
                }
            }
            const seen = new Set<string>();
            const merged: UserSearchResult[] = [];
            for (const u of [...seed, ...list]) {
                if (!u.id || seen.has(u.id)) {
                    continue;
                }

                // Client-side filter mirrors searchUsers (drop bots/deleted) so
                // the seed participant is consistent with fetched results.
                if (u.is_bot || u.delete_at) {
                    continue;
                }
                seen.add(u.id);
                merged.push(u);
            }
            merged.sort((a, b) => a.username.localeCompare(b.username));
            return merged;
        };

        const apply = (list: UserSearchResult[]) => {
            setMention((s) => ({
                ...s,
                candidates: mergeParticipants(list, query),
                highlight: 0,
            }));
        };

        if (mentionCache.current.has(cacheKey)) {
            apply(mentionCache.current.get(cacheKey)!);
            return;
        }

        // Show the seed (participants) immediately while the fetch runs.
        apply([]);

        mentionTimer.current = setTimeout(async () => {
            try {
                const list = await client.searchUsers(
                    query,
                    mentionChannelID || undefined,
                    50,
                );
                if (myReq !== mentionReqId.current) {
                    return; // a newer keystroke superseded this request
                }
                mentionCache.current.set(cacheKey, list);
                apply(list);
            } catch {
                // Non-fatal: leave the seed candidates in place.
            } finally {
                if (myReq === mentionReqId.current) {
                    mentionTimer.current = null;
                }
            }
        }, 150);
    };

    // insertMention replaces the active @query token with @<username> + space,
    // updates the composer text, repositions the caret, and closes the dropdown.
    const insertMention = (username: string) => {
        const el = composerRef.current;
        const caret = el?.selectionStart ?? newComment.length;
        const detection = detectMention(newComment, caret);
        const ins = applyMention(newComment, caret, detection, username);
        setNewComment(ins.text);
        closeMention();

        // Restore the caret after React re-renders with the new text.
        // requestAnimationFrame may be absent in non-browser test envs; fall
        // back to setTimeout(0) there so the caret still lands correctly.
        const restoreCaret = () => {
            const node = composerRef.current;
            if (node) {
                node.setSelectionRange(ins.caret, ins.caret);
                node.focus();
            }
        };
        if (typeof requestAnimationFrame === 'function') {
            requestAnimationFrame(restoreCaret);
        } else {
            setTimeout(restoreCaret, 0);
        }
    };

    // handleComposerChange updates the comment text and re-evaluates the
    // @mention trigger at the caret position.
    const handleComposerChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
        const val = e.target.value;
        setNewComment(val);
        const caret = e.target.selectionStart ?? val.length;
        const detection = detectMention(val, caret);
        if (detection.open) {
            setMention((s) => ({...s, open: true, query: detection.query}));
            fetchMentionCandidates(detection.query);
        } else {
            closeMention();
        }
    };

    const remove = async () => {
        try {
            await client.deleteTask(full.id);
            dispatch({type: ACTION_TYPES.DELETE_TASK, taskID: full.id});
            setFull(null);
            setSubtasks([]);
            setComments([]);
            onBack?.();
        } catch (err) {
            setError(messageFor(err));
        }
    };

    // share posts this task's card into the channel the user is currently
    // viewing (the center pane). The server authorizes the caller (must view
    // the task + be a member of the channel) and is idempotent per channel; the
    // card refreshes on later task changes via the existing updateTaskCards
    // loop. Silent on success (the card appears in the channel), like the other
    // header actions; surfaces errors via the shared error block.
    const share = async () => {
        if (!currentChannelID) {
            return;
        }
        setError('');
        try {
            await client.shareTask(full.id, currentChannelID);
        } catch (err) {
            setError(messageFor(err));
        }
    };

    const canDelete =
		currentUserID !== undefined &&
		(full.creator_id === currentUserID || full.assignee_id === currentUserID);

    return (
        <div className='task-detail'>
            <div className='task-detail__header'>
                <div className='task-detail__header-left'>
                    {onBack && (
                        <button
                            className='task-detail__back'
                            onClick={onBack}
                            type='button'
                            aria-label={t('webapp.task.cancel')}
                        >
                            <BackIcon/>
                        </button>
                    )}
                    <span className='task-detail__title-inline'>
                        {t('webapp.task.title.detail')}
                    </span>
                </div>
                <button
                    className='task-detail__header-share'
                    onClick={share}
                    type='button'
                    disabled={!currentChannelID}
                    aria-label={t('webapp.task.share.button')}
                    title={
                        currentChannelID ?
                            t('webapp.task.share.button') :
                            t('webapp.task.share.no_channel')
                    }
                >
                    <ShareIcon/>
                </button>
                {canDelete && (
                    <button
                        className='task-detail__header-delete'
                        onClick={remove}
                        type='button'
                        aria-label={t('webapp.task.delete')}
                        title={t('webapp.task.delete')}
                    >
                        <TrashIcon/>
                    </button>
                )}
            </div>

            <div className='task-detail__scroll'>
                {error && <div className='task-detail__error-block'>{error}</div>}

                <div className='task-detail__title-row'>
                    <span
                        className={`quick-list__check task-detail__title-check ${full.status === 'done' || full.status === 'cancelled' ? 'quick-list__check--done' : ''}`}
                        role='checkbox'
                        aria-checked={full.status === 'done' || full.status === 'cancelled'}
                        aria-label={full.summary}
                        tabIndex={0}
                        onClick={toggleCheckboxStatus}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') {
                                e.preventDefault();
                                toggleCheckboxStatus();
                            }
                        }}
                    >
                        <TaskCheck
                            done={full.status === 'done' || full.status === 'cancelled'}
                        />
                    </span>
                    {editingTitle ? (
                        <input
                            className='task-detail__title-input'
                            value={editSummary}
                            onChange={(e) => setEditSummary(e.target.value)}
                            onBlur={() => {
                                patchField('summary', editSummary.trim());
                                setEditingTitle(false);
                            }}
                            onKeyDown={(e) => {
                                if (e.key === 'Enter') {
                                    e.preventDefault();
                                    (e.target as HTMLInputElement).blur();
                                }
                                if (e.key === 'Escape') {
                                    setEditSummary(full.summary);
                                    setEditingTitle(false);
                                }
                            }}
                            autoFocus={true}
                            disabled={saving}
                            aria-label={t('webapp.task.summary')}
                        />
                    ) : (
                        <h2
                            className={`task-detail__title ${full.status === 'done' || full.status === 'cancelled' ? 'task-detail__title--terminal' : ''}`}
                            onClick={() => setEditingTitle(true)}
                            onKeyDown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    setEditingTitle(true);
                                }
                            }}
                            role='button'
                            tabIndex={0}
                            title={t('webapp.task.summary')}
                        >
                            {full.summary}
                        </h2>
                    )}
                </div>

                <div className='task-detail__meta-table'>
                    <div className='task-detail__meta-label'>
                        {t('webapp.task.filter.status')}
                    </div>
                    <div className='task-detail__meta-value'>
                        <MetaDropdown
                            ariaLabel={t('webapp.task.filter.status')}
                            value={full.status}
                            onChange={(v) => changeStatus(v as Task['status'])}
                            options={(
                                ['todo', 'in_progress', 'done', 'cancelled'] as Array<
                                Task['status']
                                >
                            ).map((s) => ({
                                value: s,
                                label: statusLabel(s, t),
                            }))}
                            triggerNode={<StatusPill status={full.status}/>}
                        />
                    </div>

                    <div className='task-detail__meta-label'>
                        {t('webapp.task.priority')}
                    </div>
                    <div
                        className={`task-detail__meta-value task-detail__meta-value--priority-${full.priority || 'standard'}`}
                    >
                        <MetaDropdown
                            ariaLabel={t('webapp.task.priority')}
                            value={full.priority || 'standard'}
                            onChange={(v) => changePriority(v as TaskPriority)}
                            options={(
                                ['standard', 'important', 'urgent'] as TaskPriority[]
                            ).map((p) => ({
                                value: p,
                                label: priorityLabel(p, t),
                            }))}
                            triggerNode={
                                <span className='task-detail__priority-trigger'>
                                    <PriorityDot priority={full.priority || 'standard'}/>
                                    {priorityLabel(full.priority || 'standard', t)}
                                </span>
                            }
                        />
                    </div>

                    <div className='task-detail__meta-label'>{t('webapp.task.due')}</div>
                    <div className='task-detail__meta-value'>
                        {editingDue ? (
                            <input
                                className='task-input task-input--inline task-input--meta'
                                type='datetime-local'
                                value={editDueLocal}
                                onChange={(e) => setEditDueLocal(e.target.value)}
                                onBlur={() => {
                                    patchField('due', editDueLocal);
                                    setEditingDue(false);
                                }}
                                onKeyDown={(e) => {
                                    if (e.key === 'Enter') {
                                        e.preventDefault();
                                        (e.target as HTMLInputElement).blur();
                                    }
                                    if (e.key === 'Escape') {
                                        setEditDueLocal(full.due ? dueToLocalInput(full.due) : '');
                                        setEditingDue(false);
                                    }
                                }}
                                autoFocus={true}
                                disabled={saving}
                                aria-label={t('webapp.task.due')}
                            />
                        ) : (
                            <span
                                className={`task-detail__due-chip ${isOverdue(full) ? 'task-detail__due-chip--overdue' : ''} ${full.due && isDueSoon(full) ? 'task-detail__due-chip--soon' : ''}`}
                                onClick={() => setEditingDue(true)}
                                onKeyDown={(e) => {
                                    if (e.key === 'Enter' || e.key === ' ') {
                                        e.preventDefault();
                                        setEditingDue(true);
                                    }
                                }}
                                role='button'
                                tabIndex={0}
                            >
                                <CalendarIcon/>
                                {full.due ?
                                    formatDueRelative({
                                        dueMs: full.due,
                                        locale,
                                        isOverdue: isOverdue(full),
                                    }) :
                                    t('webapp.task.due.pick')}
                            </span>
                        )}
                    </div>

                    <div className='task-detail__meta-label'>
                        {t('webapp.task.assignee')}
                    </div>
                    <div className='task-detail__meta-value task-detail__meta-value--picker'>
                        <UserPicker
                            value={full.assignee_id}
                            valueLabel={assigneeLabel}
                            onSelect={(u) => setAssignee(u ? u.id : '')}
                            placeholder={t('webapp.task.assignee.placeholder')}
                        />
                    </div>

                    {full.channel_id && (
                        <>
                            <div className='task-detail__meta-label'>
                                {t('webapp.task.scope.channel')}
                            </div>
                            <div className='task-detail__meta-value'>
                                <span className='task-detail__ch-ref'>
                                    <HashIcon/>
                                    {channelName || '#' + full.channel_id}
                                </span>
                            </div>
                        </>
                    )}

                    {full.parent_task_id && (
                        <>
                            <div className='task-detail__meta-label'>
                                {t('webapp.task.subtasks')}
                            </div>
                            <div className='task-detail__meta-value'>
                                <button
                                    type='button'
                                    onClick={() => onOpenSubtask?.(full.parent_task_id)}
                                    disabled={!onOpenSubtask}
                                    style={{
                                        border: 0,
                                        background: 'transparent',
                                        padding: 0,
                                        color: 'var(--task-accent)',
                                        fontWeight: 500,
                                        cursor: onOpenSubtask ? 'pointer' : 'default',
                                        textDecoration: onOpenSubtask ? 'none' : 'none',
                                    }}
                                >
                                    {parentSummary || full.parent_task_id}
                                </button>
                            </div>
                        </>
                    )}
                </div>

                <div className='task-detail__section-label'>
                    {t('webapp.task.description')}
                </div>
                {editingDescription ? (
                    <textarea
                        className='task-textarea task-input--inline'
                        value={editDescription}
                        onChange={(e) => setEditDescription(e.target.value)}
                        onBlur={() => {
                            patchField('description', editDescription);
                            setEditingDescription(false);
                        }}
                        autoFocus={true}
                        disabled={saving}
                        placeholder={t('webapp.task.description')}
                        aria-label={t('webapp.task.description')}
                    />
                ) : (
                    <div
                        className={`task-detail__description ${full.description ? '' : 'task-detail__description--empty'}`}
                        onClick={() => setEditingDescription(true)}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') {
                                e.preventDefault();
                                setEditingDescription(true);
                            }
                        }}
                        role='button'
                        tabIndex={0}
                    >
                        {full.description || t('webapp.task.description')}
                    </div>
                )}

                <div className='task-detail__section-label'>
                    {t('webapp.task.subtasks')}
                    <span style={{marginLeft: 6, color: 'var(--task-meta)'}}>
                        {t('webapp.task.subtasks.progress', doneCount, subtasks.length)}
                    </span>
                </div>
                <ul className='task-detail__subtask-list'>
                    {subtasks.length === 0 && (
                        <li
                            style={{
                                padding: '8px 0',
                                color: 'var(--task-meta)',
                                fontSize: 13,
                            }}
                        >
                            {t('webapp.task.empty')}
                        </li>
                    )}
                    {subtasks.map((s) => {
                        const subDone = s.status === 'done' || s.status === 'cancelled';
                        return (
                            <li
                                key={s.id}
                                className={`task-detail__subtask task-detail__subtask--${s.status}`}
                            >
                                {(() => {
                                    const labelID = `task-subtask-${s.id}-label`;
                                    return (
                                        <>
                                            <span
                                                className={`task-detail__subtask-check ${subDone ? 'quick-list__check--done' : ''}`}
                                                role='checkbox'
                                                aria-checked={subDone}
                                                aria-labelledby={labelID}
                                                tabIndex={0}
                                                onClick={() => toggleSubtaskDone(s)}
                                                onKeyDown={(e) => {
                                                    if (e.key === 'Enter' || e.key === ' ') {
                                                        e.preventDefault();
                                                        toggleSubtaskDone(s);
                                                    }
                                                }}
                                            >
                                                <TaskCheck done={subDone}/>
                                            </span>
                                            {onOpenSubtask ? (
                                                <button
                                                    id={labelID}
                                                    type='button'
                                                    className='task-detail__subtask-link'
                                                    onClick={() => onOpenSubtask(s.id)}
                                                >
                                                    {s.summary}
                                                </button>
                                            ) : (
                                                <span id={labelID}>{s.summary}</span>
                                            )}
                                        </>
                                    );
                                })()}
                            </li>
                        );
                    })}
                </ul>
                <div
                    className={`task-detail__subtask-add ${subtaskAdding ? 'task-detail__subtask-add--editing' : ''}`}
                >
                    <button
                        type='button'
                        className='task-detail__subtask-add-trigger'
                        onClick={openSubtaskAdd}
                        aria-label={t('webapp.task.add_subtask')}
                    >
                        <span className='task-detail__subtask-add-plus'>{'+'}</span>
                        <span className='task-detail__subtask-add-label'>
                            {t('webapp.task.add_subtask')}
                        </span>
                    </button>
                    <input
                        ref={subtaskInputRef}
                        className='task-detail__subtask-add-input'
                        value={newSubtask}
                        onChange={(e) => setNewSubtask(e.target.value)}
                        placeholder={t('webapp.task.add_subtask.placeholder')}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter') {
                                e.preventDefault();
                                commitSubtask();
                            } else if (e.key === 'Escape') {
                                e.preventDefault();
                                cancelSubtaskAdd();
                            }
                        }}
                        onBlur={() => {
                            if (!newSubtask.trim()) {
                                cancelSubtaskAdd();
                            }
                        }}
                    />
                </div>

                <div className='task-detail__section-label'>
                    {t('webapp.task.activity')}
                </div>
                <ul className='task-detail__activity-list'>
                    {activity.length === 0 && (
                        <li
                            style={{
                                padding: '8px 0',
                                color: 'var(--task-meta)',
                                fontSize: 13,
                            }}
                        >
                            {t('webapp.task.empty')}
                        </li>
                    )}
                    {activity.map((item) => {
                        const isComment = item.kind === 'comment';
                        const c = item.comment;
                        const ev = item.event;
                        const actorID = isComment ?
                            (c?.author_id ?? '') :
                            (ev?.actor_id ?? '');
                        const label = isComment ?
                            commentAuthorLabel(c as Comment, actorLabels) :
                            actorLabels[actorID] || actorID || '?';
                        const initials =
							label.replace(/^@/, '').trim().slice(0, 2).toUpperCase() || '?';
                        const actionLabel = isComment ?
                            t(activityLabelKey('commented')) :
                            t(activityLabelKey(ev?.event_type ?? ''));
                        const statusClass = actorStatusClass(actorStatuses[actorID]);
                        return (
                            <li
                                key={item.id}
                                className='task-detail__activity-item'
                            >
                                <span
                                    className={`task-detail__activity-avatar ${statusClass}`}
                                    title={label}
                                >
                                    {initials}
                                </span>
                                <div className='task-detail__activity-body'>
                                    <strong>{label}</strong> {actionLabel}
                                    <span className='task-detail__activity-time'>
                                        {formatTimestamp(item.created_at, locale)}
                                    </span>
                                    {isComment && c && (
                                        <div className='task-detail__activity-comment'>
                                            {commentBodyText(
                                                c,
                                                t('webapp.task.comments.deleted_placeholder'),
                                            )}
                                        </div>
                                    )}
                                </div>
                            </li>
                        );
                    })}
                </ul>
            </div>
            <div className='task-detail__comment-box'>
                <div className='task-detail__comment-field'>
                    <textarea
                        ref={composerRef}
                        className='task-detail__comment-input'
                        value={newComment}
                        onChange={handleComposerChange}
                        onInput={(e) => {
                            // Auto-grow: reset then cap at 120px (AC7).
                            const el = e.currentTarget;
                            el.style.height = 'auto';
                            el.style.height = composerCappedHeight(el.scrollHeight) + 'px';
                        }}
                        placeholder={t('webapp.task.add_comment')}
                        onKeyDown={(e) => {
                            // @mention keyboard nav (FEAT-C): when the dropdown
                            // is open, ↑/↓ wrap the highlight, Enter inserts the
                            // highlighted mention, Esc closes without inserting.
                            if (mention.open && mention.candidates.length > 0) {
                                if (e.key === 'ArrowDown') {
                                    e.preventDefault();
                                    setMention((s) => ({
                                        ...s,
                                        highlight: (s.highlight + 1) % s.candidates.length,
                                    }));
                                    return;
                                }
                                if (e.key === 'ArrowUp') {
                                    e.preventDefault();
                                    setMention((s) => {
                                        const len = s.candidates.length;
                                        return {...s, highlight: ((s.highlight - 1) + len) % len};
                                    });
                                    return;
                                }
                                if (e.key === 'Enter') {
                                    e.preventDefault();
                                    const picked = mention.candidates[mention.highlight];
                                    if (picked) {
                                        insertMention(picked.username);
                                    }
                                    return;
                                }
                                if (e.key === 'Escape') {
                                    e.preventDefault();
                                    closeMention();
                                    return;
                                }
                            }

                            const decision = composerKeyDown({
                                key: e.key,
                                shiftKey: e.shiftKey,
                            });
                            if (decision === 'send') {
                                e.preventDefault();
                                addComment();
                            }

                            // 'newline' => let the default insert a newline; 'none' => default.
                        }}
                    />
                    <button
                        className='task-detail__comment-send'
                        type='button'
                        onClick={addComment}
                        disabled={!composerInputValid(newComment)}
                        aria-label={t('webapp.task.comments.post')}
                    >
                        <CommentSendIcon/>
                    </button>
                </div>
                {mention.open && mention.candidates.length > 0 && (
                    <ul
                        className='task-detail__mention-dropdown'
                        role='listbox'
                    >
                        {mention.candidates.map((u, i) => (
                            <li
                                key={u.id}
                                role='option'
                                aria-selected={i === mention.highlight}
                                className={`task-detail__mention-item${i === mention.highlight ? ' task-detail__mention-item--highlighted' : ''}`}
                                onMouseDown={(e) => {
                                    // mousedown (not click) so the textarea
                                    // never loses focus before we insert.
                                    e.preventDefault();
                                    insertMention(u.username);
                                }}
                                onMouseEnter={() => setMention((s) => ({...s, highlight: i}))}
                            >
                                <span className='task-detail__mention-avatar'>
                                    {u.username.slice(0, 2).toUpperCase()}
                                </span>
                                {'@'}
                                {u.username}
                            </li>
                        ))}
                    </ul>
                )}
            </div>
        </div>
    );
}

// dueToLocalInput converts an epoch-ms due date into the value a datetime-local
// input expects ("YYYY-MM-DDTHH:mm"), in the user's local time.
function dueToLocalInput(dueMs: number | undefined): string {
    if (!dueMs) {
        return '';
    }
    const d = new Date(dueMs);
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// localInputToDue parses a datetime-local string into epoch ms (local time),
// or null when empty/invalid — null signals "clear the due date" to patchTask.
function localInputToDue(value: string): number | null {
    if (!value.trim()) {
        return null;
    }
    const ms = Date.parse(value);
    return Number.isNaN(ms) ? null : ms;
}

// isOverdue reports whether a task with a due date is past due and not terminal.
function isOverdue(task: Task): boolean {
    if (!task.due) {
        return false;
    }
    if (task.status === 'done' || task.status === 'cancelled') {
        return false;
    }
    return task.due < Date.now();
}

// formatDue is exported for testing; it formats a due timestamp using the
// shared formatDueRelative helper.
export function formatDue(dueMs: number, locale: string): string {
    return formatDueRelative({dueMs, locale});
}

// formatTimestamp formats a comment timestamp (date + time short).
export function formatTimestamp(ms: number, locale: string): string {
    try {
        return new Intl.DateTimeFormat(locale, {
            dateStyle: 'short',
            timeStyle: 'short',
        }).format(new Date(ms));
    } catch {
        return new Date(ms).toISOString();
    }
}

// messageFor extracts a user-facing message from a thrown error.
export function messageFor(err: unknown): string {
    if (err instanceof ClientError) {
        return err.message || 'request failed';
    }
    return err instanceof Error ? err.message : 'request failed';
}

// commentAuthorLabel resolves a comment's display label from the row author_id
// snapshot via the resolved-users map, falling back to the raw author_id (so
// the UI shows the opaque id — never '?' — while the name is resolving).
// Exported for unit testing (Task 5, AC1).
export function commentAuthorLabel(
    c: Comment,
    labels: Record<string, string>,
): string {
    return labels[c.author_id] || c.author_id || '?';
}

// commentBodyText returns the text to render for a comment: the placeholder
// when the backing post was deleted out-of-band, otherwise the post content.
// Exported for unit testing (Task 5, AC2).
export function commentBodyText(
    c: Comment,
    deletedPlaceholder: string,
): string {
    return c.deleted ? deletedPlaceholder : c.content;
}

// composerCappedHeight returns the textarea height for the auto-grow behavior:
// the content's scrollHeight, capped at max (120px by default, AC7). Beyond the
// cap the textarea scrolls internally instead of growing unbounded.
// Exported for unit testing (Task 8, AC7).
export function composerCappedHeight(scrollHeight: number, max = 120): number {
    return Math.min(scrollHeight, max);
}

// composerKeyDown decides the composer's response to a keydown event (AC7):
// 'send' on Enter without Shift (the comment is submitted, no newline);
// 'newline' on Shift+Enter (a newline is inserted, the comment is NOT sent);
// 'none' for every other key (default handling). Exported for unit testing.
export function composerKeyDown(e: {
    key: string;
    shiftKey: boolean;
}): 'send' | 'newline' | 'none' {
    if (e.key === 'Enter') {
        return e.shiftKey ? 'newline' : 'send';
    }
    return 'none';
}

// composerInputValid reports whether the composer input has non-whitespace
// content, driving the send button's disabled state (AC7). Exported for testing.
export function composerInputValid(text: string): boolean {
    return text.trim().length > 0;
}

// ActivityItem is the unified feed item: either a comment or a task event,
// normalized to a common shape for the merged newest-first Activity list.
export type ActivityItem = {
    kind: 'comment' | 'event';
    id: string;
    created_at: number;
    comment?: Comment;
    event?: TaskEvent;
};

// activityLabelKey maps a server event_type to its i18n key
// (webapp.task.activity.label.<event_type>). The map covers all 14 Event*
// constants; an unknown type falls back to the commented label key so the UI
// never renders an English/empty string (AC6).
export function activityLabelKey(eventType: string): string {
    return `webapp.task.activity.label.${eventType}`;
}

// actorStatusClass maps a presence status (online/away/dnd/offline) to the
// avatar status-dot modifier class (AC5/AC6, task-details-panel styling). The
// map is data-driven from useResolvedStatuses; a missing/unknown status falls
// back to the explicit offline modifier (NOT a dead default — the class is
// always derived from the resolved status). Exported for unit testing.
export function actorStatusClass(status: string | undefined): string {
    switch (status) {
    case 'online':
    case 'away':
    case 'dnd':
    case 'offline':
        return `task-detail__activity-avatar--${status}`;
    default:
        return 'task-detail__activity-avatar--offline';
    }
}

// mergeActivity merges comments and task events into one newest-first list
// (Decision 3, AC5). Sort: created_at descending; tie-breaker typeRank
// (comment=0 sorts above event=1 at equal timestamp, so the latest user input is
// visually prominent); final tie-breaker id descending (deterministic).
export function mergeActivity(
    comments: Comment[],
    events: TaskEvent[],
): ActivityItem[] {
    const typeRank = (item: ActivityItem) => (item.kind === 'comment' ? 0 : 1);

    // Dedup: a comment produces BOTH a task_comments row AND an EventCommented
    // audit event whose to_value points at the same comment id. Rendering both
    // would show two items for one comment (a card with the body + a bare
    // "@author đã bình luận" event). Drop EventCommented events whose to_value
    // matches a comment id that is present in the feed; the comment item
    // carries the body. An EventCommented whose to_value has no matching
    // comment (e.g. backing post deleted out-of-band) is kept so the action
    // still appears as a bare event.
    const commentIDs = new Set(comments.map((c) => c.id));
    const visibleEvents = events.filter((e) => {
        if (e.event_type !== TaskEventType.Commented) {
            return true;
        }
        return !(e.to_value && commentIDs.has(e.to_value));
    });

    const items: ActivityItem[] = [
        ...comments.map((c) => ({
            kind: 'comment' as const,
            id: c.id,
            created_at: c.created_at,
            comment: c,
        })),
        ...visibleEvents.map((e) => ({
            kind: 'event' as const,
            id: e.id,
            created_at: e.created_at,
            event: e,
        })),
    ];
    items.sort((a, b) => {
        if (a.created_at !== b.created_at) {
            return b.created_at - a.created_at;
        }
        const r = typeRank(a) - typeRank(b);
        if (r !== 0) {
            return r;
        }
        if (a.id < b.id) {
            return 1;
        }
        if (a.id > b.id) {
            return -1;
        }
        return 0;
    });
    return items;
}

// BackIcon / CheckIcon are the inline glyphs.
function BackIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 24 24'
            aria-hidden='true'
        >
            <path d='M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z'/>
        </svg>
    );
}

function CalendarIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
            style={{
                width: 14,
                height: 14,
                fill: 'none',
                stroke: 'currentColor',
                strokeWidth: 1.6,
                strokeLinecap: 'round',
            }}
        >
            <rect
                x='2.5'
                y='3.5'
                width='11'
                height='10'
                rx='1.5'
            />
            <path d='M2.5 6.5h11M5.5 2v3M10.5 2v3'/>
        </svg>
    );
}

// HashIcon is the # glyph used before the channel name in the meta-table.
function HashIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
            style={{
                width: 14,
                height: 14,
                fill: 'none',
                stroke: 'currentColor',
                strokeWidth: 1.6,
                strokeLinecap: 'round',
                strokeLinejoin: 'round',
            }}
        >
            <path d='M3 5h10M3 11h10M7 2l-2 12M11 2l-2 12'/>
        </svg>
    );
}

// TrashIcon is the delete glyph used in the header.
function TrashIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
            style={{
                width: 15,
                height: 15,
                fill: 'none',
                stroke: 'currentColor',
                strokeWidth: 1.6,
                strokeLinecap: 'round',
                strokeLinejoin: 'round',
            }}
        >
            <path d='M3 4h10M6.5 4V3a1 1 0 011-1h1a1 1 0 011 1v1M5 4l.5 9a1 1 0 001 1h3a1 1 0 001-1l.5-9'/>
        </svg>
    );
}

// ShareIcon is the share glyph used in the header (posts the task card into the
// current channel). A simple arrow-out-of-box shape.
function ShareIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
            style={{
                width: 15,
                height: 15,
                fill: 'none',
                stroke: 'currentColor',
                strokeWidth: 1.6,
                strokeLinecap: 'round',
                strokeLinejoin: 'round',
            }}
        >
            <path d='M9 3h3a1 1 0 011 1v9a1 1 0 01-1 1H4a1 1 0 01-1-1V4a1 1 0 011-1h3'/>
            <path d='M8 1.5v8M5 4.5L8 1.5L11 4.5'/>
        </svg>
    );
}

// CommentSendIcon is the icon-only send control for the comment composer
// (open-design .comment-send). Matches the reference arrow glyph in
// mattermost-task-sidebar-3-2.html.
function CommentSendIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
            style={{
                width: 16,
                height: 16,
                fill: 'none',
                stroke: 'currentColor',
                strokeWidth: 1.8,
                strokeLinecap: 'round',
                strokeLinejoin: 'round',
            }}
        >
            <path d='M2.5 8H13.5'/>
            <path d='M9 3.5L13.5 8L9 12.5'/>
        </svg>
    );
}

// Re-export priorityLabel for tests that previously imported it from here.
export {priorityLabel};
