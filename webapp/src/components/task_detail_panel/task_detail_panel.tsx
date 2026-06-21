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
import {useActiveLocale, useFormatMessage} from 'i18n_utils';
import React, {useEffect, useState} from 'react';
import {useDispatch, useSelector} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import formatDueRelative from 'components/shared/format_due_relative';
import PriorityPill, {priorityLabel} from 'components/shared/priority_pill';
import StatusPill from 'components/shared/status_pill';
import {useResolvedUser, useResolvedUsers} from 'components/user_picker/use_resolved_user';
import UserPicker from 'components/user_picker/user_picker';

import type {Task, Comment, PatchTaskInput, PatchTaskInput as _PatchTaskInput, TaskPriority} from 'types/tasks';

// The plugin reducer is mounted by registerReducer at
// state['plugins-<pluginId>'] (Mattermost convention), so the slice lives at a
// top-level key named with the plugin id — not under state.plugins.
interface PluginState {
    selectedTaskID: string;
    selectedTask: Task | null;
}

type GlobalStateWithPlugin = Record<string, unknown> & {
    'plugins-com.mattermost.plugin-task'?: PluginState;
};

const PLUGIN_STATE_KEY = 'plugins-com.mattermost.plugin-task';

function selectSlice(state: GlobalStateWithPlugin): PluginState {
    return state[PLUGIN_STATE_KEY] ?? {selectedTaskID: '', selectedTask: null};
}

// STATUS_CYCLE is the order the status pill advances through on click.
const STATUS_CYCLE: Array<Task['status']> = ['todo', 'in_progress', 'done', 'cancelled'];

// PRIORITY_CYCLE is the order the priority pill advances through on click.
const PRIORITY_CYCLE: TaskPriority[] = ['standard', 'important', 'urgent'];

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

export default function TaskDetailPanel({taskID: taskIDProp, onBack, currentUserID, onOpenSubtask}: TaskDetailPanelProps): JSX.Element | null {
    const dispatch = useDispatch();
    const t = useFormatMessage();
    const locale = useActiveLocale();

    const slice = useSelector(selectSlice);
    const taskID = taskIDProp ?? slice.selectedTaskID;
    const task = taskIDProp ? null : slice.selectedTask;

    const [full, setFull] = useState<Task | null>(task);
    const [subtasks, setSubtasks] = useState<Task[]>([]);
    const [comments, setComments] = useState<Comment[]>([]);
    const [error, setError] = useState<string>('');
    const [newComment, setNewComment] = useState('');
    const [newSubtask, setNewSubtask] = useState('');
    const [editingDescription, setEditingDescription] = useState(false);

    // Inline-edit buffers for summary / due / description. They mirror `full`
    // so the field renders the server value until the user starts editing,
    // then commit on blur/Enter via patchTask.
    const [editSummary, setEditSummary] = useState('');
    const [editDueLocal, setEditDueLocal] = useState('');
    const [editDescription, setEditDescription] = useState('');
    const [saving, setSaving] = useState(false);

    // Load the task + subtasks + comments whenever the selected id changes.
    useEffect(() => {
        let cancelled = false;
        const load = async () => {
            if (!taskID) {
                setFull(null);
                return;
            }
            try {
                const [detail, subs, coms] = await Promise.all([
                    client.getTask(taskID),
                    client.listSubtasks(taskID),
                    client.listComments(taskID),
                ]);
                if (cancelled) {
                    return;
                }
                setFull(detail);
                setSubtasks(subs ?? []);
                setComments(coms ?? []);
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

    // Resolve the assignee id → "@username" for display, and comment author
    // ids → labels. Hooks run before the early returns so the order is stable.
    const assigneeLabel = useResolvedUser(full?.assignee_id ?? '').label;
    const commentAuthorLabels = useResolvedUsers(comments.map((c) => c.user_id).filter(Boolean));

    if (!taskID) {
        return null;
    }
    if (!full) {
        return (
            <div className='task-detail task-detail--loading'>
                <div className='task-detail__error-block'>{error || t('webapp.task.empty')}</div>
            </div>
        );
    }

    const doneCount = subtasks.filter((s) => s.status === 'done' || s.status === 'cancelled').length;

    const changeStatus = async (status: Task['status']) => {
        try {
            const updated = await client.setTaskStatus(full.id, status);
            setFull(updated);
            dispatch({type: ACTION_TYPES.UPSERT_TASK, task: updated});
        } catch (err) {
            setError(messageFor(err));
        }
    };

    const cycleStatus = () => {
        const idx = STATUS_CYCLE.indexOf(full.status);
        const next = STATUS_CYCLE[(idx + 1) % STATUS_CYCLE.length];
        changeStatus(next);
    };

    // cyclePriority advances the priority pill to the next value.
    const cyclePriority = async () => {
        const idx = PRIORITY_CYCLE.indexOf(full.priority || 'standard');
        const next = PRIORITY_CYCLE[(idx + 1) % PRIORITY_CYCLE.length];
        try {
            const input: PatchTaskInput = {
                update_fields: ['priority'],
                priority: next,
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
    };

    const toggleSubtaskDone = async (sub: Task) => {
        const next = sub.status === 'done' ? 'todo' : 'done';
        const prev = sub.status;
        setSubtasks((cur) => cur.map((x) => (x.id === sub.id ? {...x, status: next} : x)));
        try {
            const updated = await client.setTaskStatus(sub.id, next);
            setSubtasks((cur) => cur.map((x) => (x.id === sub.id ? updated : x)));
        } catch (err) {
            setSubtasks((cur) => cur.map((x) => (x.id === sub.id ? {...x, status: prev} : x)));
            setError(messageFor(err));
        }
    };

    const addComment = async () => {
        const content = newComment.trim();
        if (!content) {
            return;
        }
        try {
            const created = await client.createComment(full.id, {content});
            setComments((prev) => [...prev, created]);
            setNewComment('');
        } catch (err) {
            setError(messageFor(err));
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

    const canDelete = currentUserID !== undefined &&
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
                    <span className='task-detail__title-inline'>{t('webapp.task.title.detail')}</span>
                </div>
            </div>

            <div className='task-detail__scroll'>
                {error && <div className='task-detail__error-block'>{error}</div>}

                {full.parent_task_id && (
                    <button
                        className='task-detail__parent-link'
                        type='button'
                        onClick={() => onOpenSubtask?.(full.parent_task_id)}
                        disabled={!onOpenSubtask}
                    >
                        <BackIcon/>
                        {t('webapp.task.subtasks')}
                    </button>
                )}

                <div className='task-detail__title-row'>
                    <span
                        className={`quick-list__check task-detail__title-check ${full.status === 'done' || full.status === 'cancelled' ? 'quick-list__check--done' : ''}`}
                        role='checkbox'
                        aria-checked={full.status === 'done'}
                        tabIndex={0}
                        onClick={cycleStatus}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') {
                                e.preventDefault();
                                cycleStatus();
                            }
                        }}
                    >
                        <CheckIcon/>
                    </span>
                    <input
                        className='task-input task-input--inline task-input--title'
                        value={editSummary}
                        onChange={(e) => setEditSummary(e.target.value)}
                        onBlur={() => patchField('summary', editSummary.trim())}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter') {
                                e.preventDefault();
                                (e.target as HTMLInputElement).blur();
                            }
                        }}
                        disabled={saving}
                        aria-label={t('webapp.task.summary')}
                    />
                </div>

                <div className='task-detail__meta-table'>
                    <div className='task-detail__meta-label'>{t('webapp.task.filter.status')}</div>
                    <div className='task-detail__meta-value'>
                        <button
                            type='button'
                            className='task-detail__meta-value-button'
                            onClick={cycleStatus}
                            style={{border: 0, background: 'transparent', padding: 0, cursor: 'pointer'}}
                        >
                            <StatusPill status={full.status}/>
                        </button>
                    </div>

                    <div className='task-detail__meta-label'>{t('webapp.task.priority')}</div>
                    <div className='task-detail__meta-value'>
                        <PriorityPill
                            priority={full.priority || 'standard'}
                            onClick={cyclePriority}
                        />
                    </div>

                    <div className='task-detail__meta-label'>{t('webapp.task.due')}</div>
                    <div className={`task-detail__meta-value ${isOverdue(full) ? 'task-detail__meta-value--overdue' : ''}`}>
                        <input
                            className='task-input task-input--inline'
                            type='datetime-local'
                            value={editDueLocal}
                            onChange={(e) => setEditDueLocal(e.target.value)}
                            onBlur={() => patchField('due', editDueLocal)}
                            disabled={saving}
                            aria-label={t('webapp.task.due')}
                        />
                        {full.due && (
                            <span style={{color: 'var(--task-meta)', fontSize: 12}}>
                                {formatDueRelative({dueMs: full.due, locale, isOverdue: isOverdue(full)})}
                            </span>
                        )}
                    </div>

                    <div className='task-detail__meta-label'>{t('webapp.task.assignee')}</div>
                    <div className='task-detail__meta-value'>
                        <UserPicker
                            value={full.assignee_id}
                            valueLabel={assigneeLabel}
                            channelID={full.channel_id || undefined}
                            onSelect={(u) => setAssignee(u ? u.id : '')}
                            placeholder={t('webapp.task.assignee.placeholder')}
                        />
                    </div>

                    {full.channel_id && (
                        <>
                            <div className='task-detail__meta-label'>{t('webapp.task.scope.channel')}</div>
                            <div className='task-detail__meta-value'>
                                <span style={{color: 'var(--task-muted)'}}>{'#' + full.channel_id}</span>
                            </div>
                        </>
                    )}
                </div>

                <div className='task-detail__section-label'>{t('webapp.task.description')}</div>
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
                        <li style={{padding: '8px 0', color: 'var(--task-meta)', fontSize: 13}}>
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
                                                <CheckIcon/>
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
                <div className='task-detail__add-row'>
                    <input
                        className='task-input'
                        value={newSubtask}
                        onChange={(e) => setNewSubtask(e.target.value)}
                        placeholder={t('webapp.task.add_subtask')}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter') {
                                e.preventDefault();
                                addSubtask();
                            }
                        }}
                    />
                    <button
                        className='task-btn task-btn--secondary'
                        onClick={addSubtask}
                        type='button'
                        aria-label={t('webapp.task.add_subtask')}
                    >
                        {'+'}
                    </button>
                </div>

                <div className='task-detail__section-label'>{t('webapp.task.comments')}</div>
                <ul className='task-detail__comment-list'>
                    {comments.length === 0 && (
                        <li style={{padding: '8px 0', color: 'var(--task-meta)', fontSize: 13}}>
                            {t('webapp.task.empty')}
                        </li>
                    )}
                    {comments.map((c) => (
                        <li
                            key={c.id}
                            className='task-detail__activity-item'
                        >
                            <span
                                className='task-detail__activity-avatar'
                                title={commentAuthorLabels[c.user_id] || c.user_id}
                            >
                                {(commentAuthorLabels[c.user_id] || c.user_id || '?').
                                    replace(/^@/, '').trim()[0]?.toUpperCase() || '?'}
                            </span>
                            <div className='task-detail__activity-body'>
                                <strong>{commentAuthorLabels[c.user_id] || t('webapp.task.comments')}</strong>
                                {' '}
                                <span className='task-detail__activity-time'>
                                    {formatTimestamp(c.created_at, locale)}
                                </span>
                                <div className='task-detail__activity-comment'>{c.content}</div>
                            </div>
                        </li>
                    ))}
                </ul>
                <div className='task-detail__comment-input'>
                    <input
                        className='task-input'
                        value={newComment}
                        onChange={(e) => setNewComment(e.target.value)}
                        placeholder={t('webapp.task.add_comment')}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter') {
                                e.preventDefault();
                                addComment();
                            }
                        }}
                    />
                    <button
                        className='task-btn task-btn--primary'
                        onClick={addComment}
                        type='button'
                    >
                        {t('webapp.task.comments.post')}
                    </button>
                </div>

                {canDelete && (
                    <div className='task-detail__actions'>
                        <button
                            className='task-btn task-btn--danger'
                            onClick={remove}
                            type='button'
                        >
                            {t('webapp.task.delete')}
                        </button>
                    </div>
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

function CheckIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 24 24'
            aria-hidden='true'
        >
            <path d='M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41L9 16.17z'/>
        </svg>
    );
}

// Re-export priorityLabel for tests that previously imported it from here.
export {priorityLabel};

// _PatchTaskInput is imported only to keep the type referenced; the runtime
// uses PatchTaskInput above. Suppress the unused warning.
// eslint-disable-next-line @typescript-eslint/no-unused-vars
type KeepPatchTaskInput = _PatchTaskInput;
