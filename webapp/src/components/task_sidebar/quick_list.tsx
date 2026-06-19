// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// QuickList is the flat task list shown in the RHS (issue #28). It has two
// scopes — "My Tasks" (assigned to me) and "Channel Tasks" (this channel) —
// plus status/due filters and cursor pagination ("Load more"). Tasks and
// subtasks appear as independent flat rows (MVP decision: no grouping by
// parent). Clicking a row selects it (TaskSidebar swaps to TaskDetailPanel);
// the "+ New Task" button opens NewTaskDialog.

import * as client from 'client';
import {ClientError} from 'client';
import {useActiveLocale, useFormatMessage} from 'i18n_utils';
import React, {useCallback, useEffect, useState} from 'react';
import {useDispatch} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import type {ListScope, ListTasksParams, Task} from 'types/tasks';

// QuickListTab enumerates the two scopes the user can switch between.
export type QuickListTab = 'mine' | 'channel';

export interface QuickListProps {

    // channelID is the context channel; required to list channel tasks.
    channelID?: string;

    // currentUserID is informational (the server derives scope=mine from the
    // auth context); kept on the prop for documentation and future use.
    currentUserID?: string;

    // onSelectTask is called when a row is clicked; TaskSidebar uses it to swap
    // to the Task Detail panel.
    onSelectTask?: (taskID: string) => void;

    // onNewTask opens the New Task dialog.
    onNewTask?: () => void;
}

// pageLimit is the cursor page size for "Load more".
const pageLimit = 25;

export default function QuickList({channelID, onSelectTask, onNewTask}: QuickListProps): JSX.Element {
    const dispatch = useDispatch();
    const t = useFormatMessage();
    const locale = useActiveLocale();

    const [tab, setTab] = useState<QuickListTab>('mine');
    const [statusFilter, setStatusFilter] = useState('');
    const [dueFilter, setDueFilter] = useState('');
    const [tasks, setTasks] = useState<Task[]>([]);
    const [afterOrderKey, setAfterOrderKey] = useState('');
    const [hasMore, setHasMore] = useState(false);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    // loadFirst resets the list and fetches the first page. Memoized so the
    // effect below depends on a stable reference across renders.
    const loadFirst = useCallback(async (scope: QuickListTab, status: string, due: string) => {
        setLoading(true);
        setError('');
        try {
            const page = await client.listTasks(buildParams(scope, status, due, channelID, ''));
            const list = page ?? [];
            setTasks(list);
            setAfterOrderKey(list.length > 0 ? list[list.length - 1].order_key : '');
            setHasMore(list.length >= pageLimit);
        } catch (err) {
            setError(messageFor(err));
        } finally {
            setLoading(false);
        }
    }, [channelID]);

    // Fetch the first page whenever the tab or filters change.
    useEffect(() => {
        loadFirst(tab, statusFilter, dueFilter);
    }, [tab, statusFilter, dueFilter, loadFirst]);

    const loadMore = async () => {
        if (!afterOrderKey || loading) {
            return;
        }
        setLoading(true);
        try {
            const page = await client.listTasks(buildParams(tab, statusFilter, dueFilter, channelID, afterOrderKey));
            const list = page ?? [];
            const merged = [...tasks, ...list];
            setTasks(merged);
            setAfterOrderKey(list.length > 0 ? list[list.length - 1].order_key : '');
            setHasMore(list.length >= pageLimit);
        } catch (err) {
            setError(messageFor(err));
        } finally {
            setLoading(false);
        }
    };

    const select = (taskID: string) => {
        dispatch({type: ACTION_TYPES.SELECT_TASK, taskID});
        onSelectTask?.(taskID);
    };

    return (
        <div className='quick-list'>
            <div className='quick-list__tabs'>
                <button
                    className={`quick-list__tab ${tab === 'mine' ? 'quick-list__tab--active' : ''}`}
                    onClick={() => setTab('mine')}
                    type='button'
                >
                    {t('webapp.task.tab.mine')}
                </button>
                <button
                    className={`quick-list__tab ${tab === 'channel' ? 'quick-list__tab--active' : ''}`}
                    onClick={() => setTab('channel')}
                    type='button'
                    disabled={!channelID}
                >
                    {t('webapp.task.tab.channel')}
                </button>
            </div>

            <div className='quick-list__filters'>
                <select
                    className='quick-list__filter'
                    value={statusFilter}
                    onChange={(e) => setStatusFilter(e.target.value)}
                    aria-label={t('webapp.task.filter.status')}
                >
                    <option value=''>{t('webapp.task.filter.status')}</option>
                    <option value='todo'>{t('webapp.task.status.todo')}</option>
                    <option value='in_progress'>{t('webapp.task.status.in_progress')}</option>
                    <option value='done'>{t('webapp.task.status.done')}</option>
                    <option value='cancelled'>{t('webapp.task.status.cancelled')}</option>
                </select>
                <select
                    className='quick-list__filter'
                    value={dueFilter}
                    onChange={(e) => setDueFilter(e.target.value)}
                    aria-label={t('webapp.task.filter.due')}
                >
                    <option value=''>{t('webapp.task.filter.due')}</option>
                    <option value='overdue'>{'⏰'}</option>
                    <option value='today'>{'Today'}</option>
                    <option value='week'>{'This week'}</option>
                </select>
            </div>

            {error && <div className='quick-list__error'>{error}</div>}

            <ul className='quick-list__items'>
                {tasks.length === 0 && !loading && (
                    <li className='quick-list__empty'>{t('webapp.task.empty')}</li>
                )}
                {tasks.map((task) => (
                    <li
                        key={task.id}
                        className={`quick-list__item quick-list__item--${task.status}`}
                        onClick={() => select(task.id)}
                    >
                        <div className='quick-list__item-summary'>{task.summary}</div>
                        <div className='quick-list__item-meta'>
                            <span className={`quick-list__item-status quick-list__item-status--${task.status}`}>
                                {statusLabel(task.status, t)}
                            </span>
                            {task.due && (
                                <span className={isOverdue(task) ? 'quick-list__item-due quick-list__item-due--overdue' : 'quick-list__item-due'}>
                                    {formatDueShort(task.due, locale)}
                                </span>
                            )}
                            {task.assignee_id && (
                                <span className='quick-list__item-assignee'>{task.assignee_id}</span>
                            )}
                        </div>
                    </li>
                ))}
            </ul>

            {hasMore && (
                <button
                    className='quick-list__load-more'
                    onClick={loadMore}
                    type='button'
                    disabled={loading}
                >
                    {t('webapp.task.load_more')}
                </button>
            )}

            <button
                className='quick-list__new-btn'
                onClick={onNewTask}
                type='button'
            >
                {'+ ' + t('webapp.task.new')}
            </button>
        </div>
    );
}

// buildParams assembles the ListTasksParams for the active tab/filters.
// currentUserID is intentionally not a parameter: the server derives scope=mine
// from the authenticated user, so the client never sends it.
export function buildParams(
    tab: QuickListTab,
    status: string,
    due: string,
    channelID: string | undefined,
    afterOrderKey: string,
): ListTasksParams {
    const params: ListTasksParams = {
        scope: (tab === 'channel' ? 'channel' : 'mine') as ListScope,
        limit: pageLimit,
    };
    if (tab === 'channel' && channelID) {
        params.channel_id = channelID;
    }
    if (status) {
        // status is a TaskStatus union member selected from a fixed <option> set.
        params.status = status as Task['status'];
    }
    if (due) {
        params.due = due;
    }
    if (afterOrderKey) {
        params.after_order_key = afterOrderKey;
    }
    return params;
}

// statusLabel maps a status to its localized label.
function statusLabel(status: Task['status'], t: (id: string) => string): string {
    switch (status) {
    case 'todo':
        return t('webapp.task.status.todo');
    case 'in_progress':
        return t('webapp.task.status.in_progress');
    case 'done':
        return t('webapp.task.status.done');
    case 'cancelled':
        return t('webapp.task.status.cancelled');
    default:
        return status;
    }
}

// isOverdue reports whether a task with a due date is past due and not terminal.
export function isOverdue(task: Task): boolean {
    if (!task.due) {
        return false;
    }
    if (task.status === 'done' || task.status === 'cancelled') {
        return false;
    }
    return task.due < Date.now();
}

// formatDueShort renders a due timestamp compactly in the user's locale, with an
// ISO fallback when Intl is unavailable.
export function formatDueShort(dueMs: number, locale: string): string {
    try {
        return new Intl.DateTimeFormat(locale, {dateStyle: 'medium'}).format(new Date(dueMs));
    } catch {
        return new Date(dueMs).toISOString();
    }
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
