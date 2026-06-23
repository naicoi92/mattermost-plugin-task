// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// TaskPostCard is the custom React body rendered in place of the native
// SlackAttachment for "custom_task" posts. It is registered via
// registry.registerPostTypeComponent('custom_task', ...) in index.tsx. The
// server still builds the SlackAttachment (kept as a mobile / fallback), but on
// desktop this component renders instead, matching the design's compact inline
// card: a real checkbox toggle, the summary, and a single inline meta row
// (status pill · priority · due · assignee mention).
//
// The host passes the post as a prop. We read task_id from post.props, then
// hydrate the task via the plugin cache (real-time via WebSocket) or a REST
// fetch. Above the card we render a small "creator started a task [for
// assignee]" caption so the post reads like a normal message. Clicking the card
// (outside the checkbox) opens the task in the RHS; the caption is left
// non-interactive so clicking it behaves like a normal post (opens the thread).

import * as client from 'client';
import {useActiveLocale, useFormatMessage} from 'i18n_utils';
import React, {useEffect, useState} from 'react';
import {useDispatch, useSelector} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import type {Post} from '@mattermost/types/posts';
import type {GlobalState} from '@mattermost/types/store';

import formatDueRelative from 'components/shared/format_due_relative';
import {priorityLabel} from 'components/shared/priority_pill';
import StatusPill from 'components/shared/status_pill';
import TaskCheck from 'components/shared/task_check';
import {isOverdue} from 'components/task_sidebar/quick_list';
import {useResolvedUser} from 'components/user_picker/use_resolved_user';

import type {Task} from 'types/tasks';

// PluginState is the minimal slice we read from the plugin cache. Kept local so
// this file has no import cycle with the reducer.
interface PluginState {
    tasks: Record<string, Task>;
}
type GlobalStateWithPlugin = GlobalState & {
    'plugins-com.mattermost.plugin-task'?: PluginState;
};
const PLUGIN_STATE_KEY = 'plugins-com.mattermost.plugin-task';

// rhsOpener is captured during initialize so the card can open the RHS without
// re-deriving the registry action. Set from index.tsx.
let rhsOpener: () => void = () => {};
export function setTaskPostCardRhsOpener(opener: () => void) {
    rhsOpener = opener;
}

export interface TaskPostCardProps {
    post: Post;
}

export default function TaskPostCard({
    post,
}: TaskPostCardProps): JSX.Element | null {
    const dispatch = useDispatch();
    const t = useFormatMessage();
    const locale = useActiveLocale();

    const taskID = readTaskID(post);
    const cached = useSelector((s: GlobalStateWithPlugin) =>
        (taskID ? s[PLUGIN_STATE_KEY]?.tasks?.[taskID] : undefined),
    );

    const [task, setTask] = useState<Task | null>(cached ?? null);
    const [loading, setLoading] = useState(!task);

    // Hydrate the task: prefer the cache (real-time), else REST-fetch once.
    useEffect(() => {
        if (!taskID) {
            return undefined;
        }
        if (cached) {
            setTask(cached);
            setLoading(false);
            return undefined;
        }
        let cancelled = false;
        const load = async () => {
            try {
                const detail = await client.getTask(taskID);
                if (!cancelled) {
                    setTask(detail);
                    dispatch({type: ACTION_TYPES.UPSERT_TASK, task: detail});
                }
            } catch {
                // Best-effort: a deleted/inaccessible task leaves the card in
                // its loading state rather than throwing.
            } finally {
                if (!cancelled) {
                    setLoading(false);
                }
            }
        };
        load();
        return () => {
            cancelled = true;
        };
    }, [taskID, cached, dispatch]);

    // Keep local state in sync when the cache updates (e.g. a WebSocket event
    // toggled the status from elsewhere).
    useEffect(() => {
        if (cached) {
            setTask(cached);
        }
    }, [cached]);

    const creatorLabel = useResolvedUser(task?.creator_id ?? '').label;
    const assigneeLabel = useResolvedUser(task?.assignee_id ?? '').label;

    if (!taskID) {
        return null;
    }
    if (loading || !task) {
        return <div className='task-post-card task-post-card--loading'>{'…'}</div>;
    }

    const done = task.status === 'done' || task.status === 'cancelled';

    // caption is the small "creator started a task [for assignee]" line shown
    // above the card so the post reads like a normal message. It is intentionally
    // NOT clickable — clicks on it fall through to the host's default post
    // behaviour (open the thread), mirroring how a plain text post behaves. Only
    // the card below opens Task Details.
    const caption = task.assignee_id && task.assignee_id !== task.creator_id ? t('webapp.task.post.caption.assigned', creatorLabel || task.creator_id, assigneeLabel || task.assignee_id) : t('webapp.task.post.caption.created', creatorLabel || task.creator_id);

    // toggleDone flips the checkbox: open → done, terminal → in_progress.
    const toggleDone = async (e: React.MouseEvent) => {
        e.stopPropagation();
        const next = done ? 'in_progress' : 'done';
        const prev = task.status;
        setTask({...task, status: next});
        try {
            const updated = await client.setTaskStatus(task.id, next);
            dispatch({type: ACTION_TYPES.UPSERT_TASK, task: updated});
        } catch {
            setTask({...task, status: prev});
        }
    };

    // openRHS opens the task detail in the Right-Hand Sidebar.
    const openRHS = () => {
        rhsOpener();
        dispatch({type: ACTION_TYPES.SELECT_TASK, taskID: task.id});
    };

    const onCardKey = (e: React.KeyboardEvent) => {
        if (e.key === 'Enter' && e.target === e.currentTarget) {
            e.preventDefault();
            openRHS();
        }
    };

    return (
        <div className='task-post-card-post'>
            <span className='task-post-card-post__caption'>{caption}</span>
            <div
                className={`task-post-card task-post-card--${task.status} ${done ? 'task-post-card--done' : ''}`}
                onClick={openRHS}
                onKeyDown={onCardKey}
                role='button'
                tabIndex={0}
                data-task-id={task.id}
            >
                <span
                    className={`task-post-card__check ${done ? 'task-post-card__check--done' : ''}`}
                    role='checkbox'
                    aria-checked={done}
                    tabIndex={0}
                    onClick={toggleDone}
                    onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault();
                            toggleDone(e as unknown as React.MouseEvent);
                        }
                    }}
                >
                    <TaskCheck done={done}/>
                </span>
                <span className='task-post-card__body'>
                    <span className='task-post-card__title'>{task.summary}</span>
                    <span className='task-post-card__meta'>
                        <StatusPill status={task.status}/>
                        <span className='task-post-card__priority'>
                            <span
                                className={`task-priority-dot task-priority-dot--${(task.priority || 'standard') === 'standard' ? 'standard-dot' : task.priority}`}
                            />
                            {priorityLabel(task.priority || 'standard', t)}
                        </span>
                        {task.due ? (
                            <span
                                className={`task-post-card__due ${isOverdue(task) ? 'task-post-card__due--overdue' : ''}`}
                            >
                                <CalendarIcon/>
                                {formatDueRelative({
                                    dueMs: task.due,
                                    locale,
                                    isOverdue: isOverdue(task),
                                })}
                            </span>
                        ) : null}
                        {task.assignee_id && (
                            <span className='task-post-card__assignee'>
                                <span className='task-post-card__assignee-avatar'>
                                    {(assigneeLabel || '?').
                                        replace(/^@/, '').
                                        slice(0, 2).
                                        toUpperCase()}
                                </span>
                                {assigneeLabel || '…'}
                            </span>
                        )}
                    </span>
                </span>
            </div>
        </div>
    );
}

// readTaskID extracts the task id from a post's props. The server sets it under
// post.props.task_id (see server/message_attachment.go taskCardProps).
function readTaskID(post: Post): string {
    const props = (post.props ?? {}) as Record<string, unknown> & {
        task_id?: string;
    };
    const id = props.task_id;
    return typeof id === 'string' ? id : '';
}

function CalendarIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
            style={{
                width: 12,
                height: 12,
                fill: 'none',
                stroke: 'currentColor',
                strokeWidth: 1.7,
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
