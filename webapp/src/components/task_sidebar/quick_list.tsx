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

import {useResolvedUsers} from 'components/user_picker/use_resolved_user';

import type {ListScope, ListTasksParams, Task} from 'types/tasks';

// QuickListTab enumerates the two scopes the user can switch between.
export type QuickListTab = 'mine' | 'channel';

// ChipFilter is the client-side status filter set. The "all" chip sends no
// status param; the others map 1:1 to a TaskStatus.
export type ChipFilter = 'all' | 'todo' | 'done' | 'cancelled';

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

// CHIP_TO_STATUS maps a chip to the status sent to the server. "all" maps to an
// empty string (no status filter).
const CHIP_TO_STATUS: Record<ChipFilter, string> = {
    all: '',
    todo: 'todo',
    done: 'done',
    cancelled: 'cancelled',
};

export default function QuickList({channelID, onSelectTask, onNewTask}: QuickListProps): JSX.Element {
    const dispatch = useDispatch();
    const t = useFormatMessage();
    const locale = useActiveLocale();

    const [tab, setTab] = useState<QuickListTab>('mine');
    const [chip, setChip] = useState<ChipFilter>('all');
    const [dueFilter, setDueFilter] = useState('');
    const [search, setSearch] = useState('');
    const [tasks, setTasks] = useState<Task[]>([]);
    const [afterOrderKey, setAfterOrderKey] = useState('');
    const [hasMore, setHasMore] = useState(false);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    // fetchingRef tracks whether a first-page fetch is in flight. It's a ref
    // (not state) so mutating it never changes loadFirst's identity or re-fires
    // the effect below — adding `loading` to useCallback deps caused a loop
    // (every fetch-completion flipped loading, recreating loadFirst, re-firing
    // the effect, starting another fetch). The ref gates concurrent calls
    // without that side effect.
    const fetchingRef = React.useRef(false);

    // loadFirst resets the list and fetches the first page. Memoized so the
    // effect below depends on a stable reference across renders. It bails out
    // when a fetch is already in flight so rapid tab/filter changes can't stack
    // concurrent requests (the last response would otherwise win out of order).
    const loadFirst = useCallback(async (scope: QuickListTab, status: string, due: string) => {
        if (fetchingRef.current) {
            return;
        }
        fetchingRef.current = true;
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
            fetchingRef.current = false;
            setLoading(false);
        }
    }, [channelID]);

    // Fetch the first page whenever the tab or filters change. Search is
    // client-side (filters the loaded list), so it does NOT trigger a refetch.
    useEffect(() => {
        loadFirst(tab, CHIP_TO_STATUS[chip], dueFilter);
    }, [tab, chip, dueFilter, loadFirst]);

    const loadMore = async () => {
        if (!afterOrderKey || loading) {
            return;
        }
        setLoading(true);
        try {
            const page = await client.listTasks(buildParams(tab, CHIP_TO_STATUS[chip], dueFilter, channelID, afterOrderKey));
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

    // toggleDone flips a task between done and its previous open status. Done
    // tasks revert to todo; any open status becomes done. The update is
    // optimistic (state first) and the store is kept in sync via dispatch.
    const toggleDone = async (e: React.MouseEvent, task: Task) => {
        e.stopPropagation();
        const next = task.status === 'done' ? 'todo' : 'done';
        const prev = task.status;
        setTasks((cur) => cur.map((x) => (x.id === task.id ? {...x, status: next} : x)));
        try {
            const updated = await client.setTaskStatus(task.id, next);
            dispatch({type: ACTION_TYPES.UPSERT_TASK, task: updated});
        } catch (err) {
            // Roll back on failure and surface the error.
            setTasks((cur) => cur.map((x) => (x.id === task.id ? {...x, status: prev} : x)));
            setError(messageFor(err));
        }
    };

    // Client-side search filter over the loaded page.
    const term = search.trim().toLowerCase();
    const visible = term ? tasks.filter((x) => x.summary.toLowerCase().includes(term)) : tasks;

    // Resolve assignee ids → "@username" labels for the avatars. Store-first,
    // fetch fallback (see useResolvedUsers).
    const assigneeLabels = useResolvedUsers(visible.map((t) => t.assignee_id).filter(Boolean));

    return (
        <div className='quick-list'>
            <div className='quick-list__toolbar'>
                <div
                    className='quick-list__scope-tabs'
                    role='tablist'
                >
                    <button
                        className={`quick-list__scope-tab ${tab === 'mine' ? 'quick-list__scope-tab--active' : ''}`}
                        onClick={() => setTab('mine')}
                        type='button'
                        role='tab'
                        aria-selected={tab === 'mine'}
                    >
                        {t('webapp.task.tab.mine')}
                    </button>
                    <button
                        className={`quick-list__scope-tab ${tab === 'channel' ? 'quick-list__scope-tab--active' : ''}`}
                        onClick={() => setTab('channel')}
                        type='button'
                        role='tab'
                        aria-selected={tab === 'channel'}
                        disabled={!channelID}
                    >
                        {t('webapp.task.tab.channel')}
                    </button>
                </div>

                <div className='quick-list__search'>
                    <SearchIcon/>
                    <input
                        type='text'
                        value={search}
                        onChange={(e) => setSearch(e.target.value)}
                        placeholder={t('webapp.task.search')}
                        aria-label={t('webapp.task.search')}
                    />
                </div>

                <div className='quick-list__filters'>
                    <div className='quick-list__chips'>
                        {(['all', 'todo', 'done', 'cancelled'] as ChipFilter[]).map((c) => (
                            <button
                                key={c}
                                className={`quick-list__chip ${chip === c ? 'quick-list__chip--active' : ''}`}
                                onClick={() => setChip(c)}
                                type='button'
                            >
                                {chipLabel(c, t)}
                            </button>
                        ))}
                    </div>
                    <select
                        className='quick-list__due-select'
                        value={dueFilter}
                        onChange={(e) => setDueFilter(e.target.value)}
                        aria-label={t('webapp.task.filter.due')}
                    >
                        <option value=''>{t('webapp.task.filter.due')}</option>
                        <option value='overdue'>{t('webapp.task.filter.overdue')}</option>
                        <option value='today'>{t('webapp.task.filter.today')}</option>
                        <option value='week'>{t('webapp.task.filter.week')}</option>
                    </select>
                </div>
            </div>

            {error && <div className='quick-list__error'>{error}</div>}

            <ul className='quick-list__items'>
                {visible.length === 0 && !loading && (
                    <li className='quick-list__empty'>
                        {term ? t('webapp.task.empty.search') : t('webapp.task.empty')}
                    </li>
                )}
                {visible.map((task) => {
                    const done = task.status === 'done' || task.status === 'cancelled';
                    return (
                        <li
                            key={task.id}
                            className={`quick-list__item quick-list__item--${task.status}`}
                        >
                            <button
                                type='button'
                                className='quick-list__item-row'
                                onClick={() => select(task.id)}
                                aria-label={task.summary}
                            >
                                <span
                                    className={`quick-list__check ${done ? 'quick-list__check--done' : ''}`}
                                    role='checkbox'
                                    aria-checked={done}
                                    tabIndex={0}
                                    onClick={(e) => toggleDone(e, task)}
                                    onKeyDown={(e) => {
                                        if (e.key === 'Enter' || e.key === ' ') {
                                            e.preventDefault();
                                            toggleDone(e as unknown as React.MouseEvent, task);
                                        }
                                    }}
                                >
                                    <CheckIcon/>
                                </span>
                                <span className='quick-list__item-main'>
                                    <span className='quick-list__item-summary'>{task.summary}</span>
                                    {task.description && task.description.trim() && (
                                        <span className='quick-list__item-description'>
                                            {truncateDescription(task.description.trim())}
                                        </span>
                                    )}
                                    <span className='quick-list__item-meta'>
                                        <span className={`quick-list__item-status quick-list__item-status--${task.status}`}>
                                            {statusLabel(task.status, t)}
                                        </span>
                                        <span className='quick-list__item-sep'>{'·'}</span>
                                        {task.due ? (
                                            <span className={isOverdue(task) ? 'quick-list__item-due quick-list__item-due--overdue' : 'quick-list__item-due'}>
                                                {formatDueShort(task.due, locale)}
                                            </span>
                                        ) : (
                                            <span className='quick-list__item-due quick-list__item-due--none'>{'—'}</span>
                                        )}
                                        {task.assignee_id && (
                                            <>
                                                <span className='quick-list__item-sep'>{'·'}</span>
                                                <span className='quick-list__item-assignee'>
                                                    {assigneeLabels[task.assignee_id] || task.assignee_id}
                                                </span>
                                            </>
                                        )}
                                    </span>
                                </span>
                            </button>
                        </li>
                    );
                })}
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
                className='quick-list__fab'
                onClick={onNewTask}
                type='button'
            >
                <PlusIcon/>
                {t('webapp.task.new')}
            </button>
        </div>
    );
}

// chipLabel maps a chip to its localized label.
function chipLabel(chip: ChipFilter, t: (id: string) => string): string {
    switch (chip) {
    case 'all':
        return t('webapp.task.filter.all');
    case 'todo':
        return t('webapp.task.status.todo');
    case 'done':
        return t('webapp.task.filter.done');
    case 'cancelled':
        return t('webapp.task.status.cancelled');
    default:
        return chip;
    }
}

// truncateDescription collapses a description into a single short preview line
// for the list row. It squashes newlines to spaces, trims to roughly maxChars,
// cuts at the nearest word boundary (so it never breaks mid-word), and appends
// an ellipsis when truncation occurred. Exported for unit testing.
const DESCRIPTION_MAX_CHARS = 100;

export function truncateDescription(text: string, maxChars = DESCRIPTION_MAX_CHARS): string {
    // Collapse all whitespace runs (including newlines) to single spaces.
    const flat = text.replace(/\s+/g, ' ').trim();
    if (flat.length <= maxChars) {
        return flat;
    }
    const slice = flat.slice(0, maxChars);

    // Cut at the last whitespace within the slice so the preview ends on a word
    // boundary. If there is no whitespace (one long token), fall back to the
    // hard slice.
    const lastSpace = slice.lastIndexOf(' ');
    const cut = lastSpace > 0 ? slice.slice(0, lastSpace) : slice;
    return cut + '…';
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

// SearchIcon / CheckIcon / PlusIcon are the inline Lark-style glyphs.
function SearchIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 24 24'
            aria-hidden='true'
        >
            <path d='M15.5 14h-.79l-.28-.27A6.471 6.471 0 0 0 16 9.5 6.5 6.5 0 1 0 9.5 16c1.61 0 3.09-.59 4.23-1.57l.27.28v.79l5 4.99L20.49 19l-4.99-5zm-6 0C7.01 14 5 11.99 5 9.5S7.01 5 9.5 5 14 7.01 14 9.5 11.99 14 9.5 14z'/>
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

function PlusIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 24 24'
            aria-hidden='true'
        >
            <path d='M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z'/>
        </svg>
    );
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
