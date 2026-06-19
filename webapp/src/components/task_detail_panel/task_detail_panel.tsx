// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// TaskDetailPanel is the detail view shown inside the RHS when a task is
// selected (issue #29). It renders the task's summary, description, due date
// (in the user's timezone), assignee, the subtask list with an "x/y done"
// progress summary, and the comment list with timestamps. Actions mutate via
// the API client (#31) and dispatch into the Redux store (#27) so the change
// is reflected immediately and broadcast over WebSocket (#32).

import * as client from 'client';
import {ClientError} from 'client';
import {useActiveLocale, useFormatMessage} from 'i18n_utils';
import React, {useEffect, useState} from 'react';
import {useDispatch, useSelector} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import type {Task, Comment} from 'types/tasks';

// The plugin reducer is mounted by registerReducer at
// state['plugins-<pluginId>'] (Mattermost convention), so the slice lives at a
// top-level key named with the plugin id — not under state.plugins. This type
// models the relevant slice of GlobalState the panel reads.
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

export interface TaskDetailPanelProps {

    // taskID overrides the store selection (e.g. when opened with a fixed id).
    taskID?: string;

    // onBack returns to the Quick List view.
    onBack?: () => void;

    // currentUserID gates the delete control (creator/assignee may delete).
    currentUserID?: string;
}

export default function TaskDetailPanel({taskID: taskIDProp, onBack, currentUserID}: TaskDetailPanelProps): JSX.Element | null {
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

    if (!taskID) {
        return null;
    }
    if (!full) {
        return (
            <div className='task-detail task-detail--loading'>
                <div className='task-detail__error'>{error || t('webapp.task.empty')}</div>
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

            // Clear local state so no stale task remains rendered even if the
            // host doesn't supply onBack (e.g. the panel is the only view).
            setFull(null);
            setSubtasks([]);
            setComments([]);
            onBack?.();
        } catch (err) {
            setError(messageFor(err));
        }
    };

    // Delete is permitted for the creator or current assignee; hide otherwise.
    const canDelete = currentUserID !== undefined &&
        (full.creator_id === currentUserID || full.assignee_id === currentUserID);

    return (
        <div className='task-detail'>
            <div className='task-detail__header'>
                {onBack && (
                    <button
                        className='task-detail__back'
                        onClick={onBack}
                        type='button'
                        aria-label={t('webapp.task.filter.status')}
                    >
                        {'‹'}
                    </button>
                )}
                <div className='task-detail__summary'>{full.summary}</div>
            </div>

            {error && <div className='task-detail__error'>{error}</div>}

            <dl className='task-detail__meta'>
                <dt>{t('webapp.task.filter.status')}</dt>
                <dd>
                    <select
                        className='task-detail__status'
                        value={full.status}
                        onChange={(e) => changeStatus(e.target.value as Task['status'])}
                    >
                        <option value='todo'>{t('webapp.task.status.todo')}</option>
                        <option value='in_progress'>{t('webapp.task.status.in_progress')}</option>
                        <option value='done'>{t('webapp.task.status.done')}</option>
                        <option value='cancelled'>{t('webapp.task.status.cancelled')}</option>
                    </select>
                </dd>
                <dt>{t('webapp.task.due')}</dt>
                <dd className={isOverdue(full) ? 'task-detail__due--overdue' : ''}>
                    {full.due ? formatDue(full.due, locale) : '—'}
                </dd>
                <dt>{t('webapp.task.assignee')}</dt>
                <dd>{full.assignee_id || '—'}</dd>
            </dl>

            {full.description && (
                <div className='task-detail__description'>{full.description}</div>
            )}

            <section className='task-detail__subtasks'>
                <div className='task-detail__section-title'>
                    {t('webapp.task.subtasks')}
                    <span className='task-detail__progress'>
                        {t('webapp.task.subtasks.progress', doneCount, subtasks.length)}
                    </span>
                </div>
                <ul className='task-detail__subtask-list'>
                    {subtasks.map((s) => (
                        <li
                            key={s.id}
                            className={`task-detail__subtask task-detail__subtask--${s.status}`}
                        >
                            {s.summary}
                        </li>
                    ))}
                </ul>
                <div className='task-detail__add-row'>
                    <input
                        className='task-detail__input'
                        value={newSubtask}
                        onChange={(e) => setNewSubtask(e.target.value)}
                        placeholder={t('webapp.task.add_subtask')}
                    />
                    <button
                        className='task-detail__add-btn'
                        onClick={addSubtask}
                        type='button'
                        aria-label={t('webapp.task.add_subtask')}
                    >
                        {'+'}
                    </button>
                </div>
            </section>

            <section className='task-detail__comments'>
                <div className='task-detail__section-title'>{t('webapp.task.comments')}</div>
                <ul className='task-detail__comment-list'>
                    {comments.map((c) => (
                        <li
                            key={c.id}
                            className='task-detail__comment'
                        >
                            <div className='task-detail__comment-body'>{c.content}</div>
                            <div className='task-detail__comment-meta'>
                                {formatTimestamp(c.created_at, locale)}
                            </div>
                        </li>
                    ))}
                </ul>
                <div className='task-detail__add-row'>
                    <input
                        className='task-detail__input'
                        value={newComment}
                        onChange={(e) => setNewComment(e.target.value)}
                        placeholder={t('webapp.task.add_comment')}
                    />
                    <button
                        className='task-detail__add-btn'
                        onClick={addComment}
                        type='button'
                        aria-label={t('webapp.task.add_comment')}
                    >
                        {'+'}
                    </button>
                </div>
            </section>

            {canDelete && (
                <div className='task-detail__actions'>
                    <button
                        className='task-detail__delete'
                        onClick={remove}
                        type='button'
                    >
                        {t('webapp.task.delete')}
                    </button>
                </div>
            )}
        </div>
    );
}

// messageFor extracts a user-facing message from a thrown error, preferring the
// server's text body (ClientError) and falling back to a generic string.
// Exported so tests verify the production logic rather than a hand-copied twin.
export function messageFor(err: unknown): string {
    if (err instanceof ClientError) {
        return err.message || 'request failed';
    }
    return err instanceof Error ? err.message : 'request failed';
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

// formatDue renders a due timestamp in the given locale, defensively returning
// a fallback if the Intl API is unavailable (older runtimes / SSR).
export function formatDue(dueMs: number, locale: string): string {
    try {
        return new Intl.DateTimeFormat(locale, {
            dateStyle: 'medium',
            timeStyle: 'short',
        }).format(new Date(dueMs));
    } catch {
        return new Date(dueMs).toISOString();
    }
}

// formatDue is exported for testing; formatTimestamp mirrors it but trims the
// time when only a date is meaningful (comments always show time).
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
