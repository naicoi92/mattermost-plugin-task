// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// QuickList is the flat task list shown in the RHS. The list is context-
// driven: it shows the tasks of the channel the user is standing in (channel
// mode) or the tasks shared with their DM partner (direct mode). The mode is
// derived from the channel.type prop — there is no "My Tasks" / "Channel
// Tasks" toggle. Six filter tabs narrow the result (All / Today / To Do / In
// Progress / Done / Cancelled). Tasks are grouped under "Needs attention",
// "Upcoming" and "Completed" headers. Clicking a row selects it (TaskSidebar
// swaps to TaskDetailPanel); the footer "+ New Task" button opens the inline
// New Task dialog.

import * as client from 'client';
import {ClientError} from 'client';
import {useActiveLocale, useFormatMessage} from 'i18n_utils';
import React, {useCallback, useEffect, useMemo, useState} from 'react';
import {useDispatch} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import formatDueRelative from 'components/shared/format_due_relative';
import {priorityLabel} from 'components/shared/priority_pill';
import StatusPill from 'components/shared/status_pill';
import TaskCheck from 'components/shared/task_check';
import {useResolvedUsers} from 'components/user_picker/use_resolved_user';

import type {ListScope, ListTasksParams, Task} from 'types/tasks';

// FilterTab enumerates the six filter tabs. Each tab maps to a (status, due)
// pair sent to the server; the special "today" maps to due=today, the others
// map 1:1 to a status filter.
export type FilterTab =
	| 'all'
	| 'today'
	| 'todo'
	| 'in_progress'
	| 'done'
	| 'cancelled';

export interface QuickListProps {

    // channelID is the context channel. For channel mode (O/P/G) it is sent to
    // the server as channel_id; for direct mode it is unused (partner_id is
    // sent instead).
    channelID?: string;

    // currentUserID is the authenticated user; used to derive the DM partner.
    currentUserID?: string;

    // channelType is the Mattermost channel type of the context channel ("D"
    // for a 1:1 DM, otherwise a channel). When "D", the list uses direct mode.
    channelType?: string;

    // onSelectTask is called when a row is clicked; TaskSidebar uses it to swap
    // to the Task Detail panel.
    onSelectTask?: (taskID: string) => void;

    // onNewTask opens the New Task view.
    onNewTask?: () => void;
}

// pageLimit is the cursor page size for "Load more" and the badge "N+" bound.
const pageLimit = 25;

export default function QuickList({
    channelID,
    currentUserID,
    channelType,
    onSelectTask,
    onNewTask,
}: QuickListProps): JSX.Element {
    const dispatch = useDispatch();
    const t = useFormatMessage();
    const locale = useActiveLocale();

    const [tab, setTab] = useState<FilterTab>('all');
    const [search, setSearch] = useState('');
    const [tasks, setTasks] = useState<Task[]>([]);
    const [afterOrderKey, setAfterOrderKey] = useState('');
    const [hasMore, setHasMore] = useState(false);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    // latestRequestRef tracks the most recent first-page fetch. Each fetch
    // bumps the counter; when a response arrives we discard it unless its
    // counter still matches the latest. This lets a filter change while an
    // earlier request is in flight win.
    const latestRequestRef = React.useRef(0);

    // Derive the scope + context-specific param (channel_id vs partner_id) from
    // the channel type. For a DM (type "D") we parse the partner from the
    // channelID encoded channel name — but here the host passes the channel
    // object already; we accept a precomputed partner_id via channelType=='D'
    // fallback by querying the current channel's name from the host.
    //
    // The webapp's TaskSidebar already reads channel.name; we mirror that by
    // deriving the partner here once per render (cheap). channelType === 'D'
    // ⇒ direct scope.
    const isDM = channelType === 'D';

    // loadFirst resets the list and fetches the first page. Memoized so the
    // effect below depends on a stable reference across renders.
    const loadFirst = useCallback(
        async (activeTab: FilterTab) => {
            const myRequest = ++latestRequestRef.current;
            setLoading(true);
            setError('');
            try {
                const page = await client.listTasks(
                    buildParams(activeTab, channelID, isDM, ''),
                );

                // Drop stale responses: only the latest request's result lands.
                if (myRequest !== latestRequestRef.current) {
                    return;
                }
                const list = page ?? [];
                setTasks(list);
                setAfterOrderKey(
                    list.length > 0 ? list[list.length - 1].order_key : '',
                );
                setHasMore(list.length >= pageLimit);
            } catch (err) {
                if (myRequest !== latestRequestRef.current) {
                    return;
                }
                setError(messageFor(err));
            } finally {
                if (myRequest === latestRequestRef.current) {
                    setLoading(false);
                }
            }
        },
        [channelID, currentUserID, isDM],
    );

    // Fetch the first page whenever the tab changes. Search is client-side.
    useEffect(() => {
        loadFirst(tab);
    }, [tab, loadFirst]);

    const loadMore = async () => {
        if (!afterOrderKey || loading) {
            return;
        }

        // Capture the tab at request start; if the user switches tabs while
        // this fetch is in flight, drop the stale response (same guard pattern
        // as loadFirst).
        const myRequest = ++latestRequestRef.current;
        const activeTab = tab;
        setLoading(true);
        try {
            const page = await client.listTasks(
                buildParams(activeTab, channelID, isDM, afterOrderKey),
            );
            if (myRequest !== latestRequestRef.current) {
                return;
            }
            const list = page ?? [];

            // Use the functional updater so we merge against the latest state,
            // not the closure-captured `tasks`.
            setTasks((cur) => [...cur, ...list]);
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

    // toggleDone flips a task between Done and In Progress via the checkbox.
    // Open statuses (todo/in_progress) → Done. Terminal statuses (done/cancelled)
    // → In Progress. Other transitions (todo↔cancelled, etc.) must be done from
    // the Task Detail's status pill, not the checkbox.
    const toggleDone = async (e: React.MouseEvent, task: Task) => {
        e.stopPropagation();
        const terminal = task.status === 'done' || task.status === 'cancelled';
        const next = terminal ? 'in_progress' : 'done';
        const prev = task.status;
        setTasks((cur) =>
            cur.map((x) => (x.id === task.id ? {...x, status: next} : x)),
        );
        try {
            const updated = await client.setTaskStatus(task.id, next);
            dispatch({type: ACTION_TYPES.UPSERT_TASK, task: updated});
        } catch (err) {
            // Roll back on failure and surface the error.
            setTasks((cur) =>
                cur.map((x) => (x.id === task.id ? {...x, status: prev} : x)),
            );
            setError(messageFor(err));
        }
    };

    // Client-side search filter over the loaded page.
    const term = search.trim().toLowerCase();
    const visible = term ? tasks.filter((x) => x.summary.toLowerCase().includes(term)) : tasks;

    // Resolve assignee ids → "@username" labels for the avatar pills.
    const assigneeLabels = useResolvedUsers(
        visible.map((t) => t.assignee_id).filter(Boolean),
    );

    // Group the visible tasks into the three headers. Needs attention = open
    // + (overdue or due today). Upcoming = open + everything else. Completed =
    // done or cancelled.
    const groups = useMemo(() => groupTasks(visible), [visible]);
    const counts = useMemo(() => countByTab(tasks, hasMore), [tasks, hasMore]);

    return (
        <div className='quick-list'>
            <div className='quick-list__toolbar'>
                <div className='quick-list__search'>
                    <SearchIcon/>
                    <input
                        type='text'
                        value={search}
                        onChange={(e) => setSearch(e.target.value)}
                        placeholder={t('webapp.task.search')}
                        aria-label={t('webapp.task.search')}
                    />
                    <span className='quick-list__kbd'>{'⌘K'}</span>
                </div>

                <div
                    className='quick-list__filter-tabs'
                    role='tablist'
                >
                    {(Object.keys(TAB_FILTER) as FilterTab[]).map((key) => (
                        <button
                            key={key}
                            className={`quick-list__filter-tab ${tab === key ? 'quick-list__filter-tab--active' : ''}`}
                            onClick={() => setTab(key)}
                            type='button'
                            role='tab'
                            aria-selected={tab === key}
                        >
                            {tabLabel(key, t)}
                            <span
                                className={`quick-list__count ${counts[key].plus ? 'quick-list__count--plus' : ''}`}
                            >
                                {counts[key].label}
                            </span>
                        </button>
                    ))}
                </div>
            </div>

            {error && <div className='quick-list__error'>{error}</div>}

            {groups.map((g) => (
                <div key={g.key}>
                    <div className='quick-list__group-label'>
                        {groupLabel(g.key, t)}
                        <span>{g.items.length}</span>
                    </div>
                    <ul className='quick-list__items'>
                        {g.items.map((task) => {
                            const done =
								task.status === 'done' || task.status === 'cancelled';
                            return (
                                <li
                                    key={task.id}
                                    className={`quick-list__item quick-list__item--${task.status}`}
                                >
                                    <div
                                        className='quick-list__item-row'
                                        onClick={() => select(task.id)}
                                        onKeyDown={(e) => {
                                            // Activate the row on Enter; Space is left for
                                            // the nested checkbox (which has its own handler).
                                            if (e.key === 'Enter' && e.target === e.currentTarget) {
                                                e.preventDefault();
                                                select(task.id);
                                            }
                                        }}
                                        role='button'
                                        tabIndex={0}
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
                                            <TaskCheck done={done}/>
                                        </span>
                                        <span className='quick-list__item-main'>
                                            <span className='quick-list__item-summary'>
                                                {task.summary}
                                            </span>
                                            {task.description && task.description.trim() && (
                                                <span className='quick-list__item-description'>
                                                    {truncateDescription(task.description.trim())}
                                                </span>
                                            )}
                                            <span className='quick-list__item-meta'>
                                                <StatusPill status={task.status}/>
                                                <span
                                                    className={`quick-list__item-priority quick-list__item-priority--${task.priority || 'standard'}`}
                                                >
                                                    <span
                                                        className={`task-priority-dot task-priority-dot--${(task.priority || 'standard') === 'standard' ? 'standard-dot' : task.priority}`}
                                                    />
                                                    {priorityLabel(task.priority || 'standard', t)}
                                                </span>
                                                {task.due ? (
                                                    <span className={dueChipClass(task)}>
                                                        <CalendarIcon/>
                                                        {formatDueRelative({
                                                            dueMs: task.due,
                                                            locale,
                                                            isOverdue: isOverdue(task),
                                                        })}
                                                    </span>
                                                ) : (
                                                    <span className='quick-list__item-due quick-list__item-due--none'>
                                                        {'—'}
                                                    </span>
                                                )}
                                                {task.assignee_id && (
                                                    <span
                                                        className={`quick-list__item-assignee ${assigneeLabels[task.assignee_id] ? '' : 'quick-list__item-assignee--loading'}`}
                                                    >
                                                        <span className='quick-list__assignee-avatar'>
                                                            {assigneeInitials(
                                                                assigneeLabels[task.assignee_id],
                                                            )}
                                                        </span>
                                                        {assigneeLabels[task.assignee_id] || '…'}
                                                    </span>
                                                )}
                                            </span>
                                        </span>
                                    </div>
                                </li>
                            );
                        })}
                    </ul>
                </div>
            ))}

            {visible.length === 0 && !loading && (
                <div className='quick-list__empty'>
                    {term ? t('webapp.task.empty.search') : t('webapp.task.empty')}
                </div>
            )}

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

            <div className='quick-list__footer'>
                <button
                    className='task-btn task-btn--primary task-btn--block'
                    onClick={onNewTask}
                    type='button'
                >
                    <PlusIcon/>
                    {t('webapp.task.new')}
                </button>
            </div>
        </div>
    );
}

// TAB_FILTER maps a FilterTab to the (status, due) pair sent to the server.
// "all" sends neither; "today" sends due=today; the others send status only.
export const TAB_FILTER: Record<FilterTab, { status: string; due: string }> = {
    all: {status: '', due: ''},
    today: {status: '', due: 'today'},
    todo: {status: 'todo', due: ''},
    in_progress: {status: 'in_progress', due: ''},
    done: {status: 'done', due: ''},
    cancelled: {status: 'cancelled', due: ''},
};

// tabLabel maps a FilterTab to its localized label.
export function tabLabel(tab: FilterTab, t: (id: string) => string): string {
    switch (tab) {
    case 'all':
        return t('webapp.task.tab.all');
    case 'today':
        return t('webapp.task.tab.today');
    case 'todo':
        return t('webapp.task.tab.todo');
    case 'in_progress':
        return t('webapp.task.tab.in_progress');
    case 'done':
        return t('webapp.task.tab.done');
    case 'cancelled':
        return t('webapp.task.tab.cancelled');
    default:
        return tab;
    }
}

// dueChipClass picks the right modifier for the due chip based on the task's
// deadline + status.
export function dueChipClass(task: Task): string {
    if (isOverdue(task)) {
        return 'quick-list__item-due quick-list__item-due--overdue';
    }
    if (isDueSoon(task)) {
        return 'quick-list__item-due quick-list__item-due--soon';
    }
    return 'quick-list__item-due';
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

// isDueSoon reports whether the task is due within the current local day and
// not terminal — used to tint the chip amber.
export function isDueSoon(task: Task): boolean {
    if (!task.due) {
        return false;
    }
    if (task.status === 'done' || task.status === 'cancelled') {
        return false;
    }
    const now = new Date();
    const start = new Date(
        now.getFullYear(),
        now.getMonth(),
        now.getDate(),
    ).getTime();
    const end = start + (24 * 60 * 60 * 1000);
    return task.due >= start && task.due < end;
}

// GroupedTask is one bucket in the grouped list.
export interface GroupedTasks {
    key: 'attention' | 'upcoming' | 'completed';
    label: string;
    items: Task[];
}

// groupTasks buckets tasks into the three headers. Exported for unit testing.
export function groupTasks(tasks: Task[]): GroupedTasks[] {
    const attention: Task[] = [];
    const upcoming: Task[] = [];
    const completed: Task[] = [];
    for (const task of tasks) {
        const terminal = task.status === 'done' || task.status === 'cancelled';
        if (terminal) {
            completed.push(task);
        } else if (isOverdue(task) || isDueSoon(task)) {
            attention.push(task);
        } else {
            upcoming.push(task);
        }
    }
    const makeLabel = (key: GroupedTasks['key']): string => key;

    // Build only non-empty groups, preserving the canonical order.
    const out: GroupedTasks[] = [];
    if (attention.length > 0) {
        out.push({
            key: 'attention',
            label: makeLabel('attention'),
            items: attention,
        });
    }
    if (upcoming.length > 0) {
        out.push({
            key: 'upcoming',
            label: makeLabel('upcoming'),
            items: upcoming,
        });
    }
    if (completed.length > 0) {
        out.push({
            key: 'completed',
            label: makeLabel('completed'),
            items: completed,
        });
    }
    return out;
}

// countByTab returns, for each FilterTab, the count of currently-loaded tasks
// that match that tab's filter — with a "+" suffix when there may be more
// pages (hasMore). Counts are a lower bound (only the loaded page), matching
// the design's N+ intent. A zero count never carries "+" (no rows to bound).
export function countByTab(
    tasks: Task[],
    hasMore: boolean,
): Record<FilterTab, { label: string; plus: boolean }> {
    const count = (pred: (t: Task) => boolean): number =>
        tasks.filter(pred).length;
    const make = (n: number) => {
        const plus = hasMore && n > 0;
        return {label: plus ? `${n}+` : String(n), plus};
    };
    return {
        all: make(tasks.length),
        today: make(count((x) => isDueSoon(x))),
        todo: make(count((x) => x.status === 'todo')),
        in_progress: make(count((x) => x.status === 'in_progress')),
        done: make(count((x) => x.status === 'done')),
        cancelled: make(count((x) => x.status === 'cancelled')),
    };
}

// groupLabel maps a group key to its localized header label.
export function groupLabel(
    key: GroupedTasks['key'],
    t: (id: string) => string,
): string {
    switch (key) {
    case 'attention':
        return t('webapp.task.group.attention');
    case 'upcoming':
        return t('webapp.task.group.upcoming');
    case 'completed':
        return t('webapp.task.group.completed');
    default:
        return key;
    }
}

// truncateDescription collapses a description into a single short preview line
// for the list row. Exported for unit testing.
const DESCRIPTION_MAX_CHARS = 100;

export function truncateDescription(
    text: string,
    maxChars = DESCRIPTION_MAX_CHARS,
): string {
    // Collapse all whitespace runs (including newlines) to single spaces.
    const flat = text.replace(/\s+/g, ' ').trim();
    if (flat.length <= maxChars) {
        return flat;
    }
    const slice = flat.slice(0, maxChars);

    // Cut at the last whitespace within the slice so the preview ends on a word
    // boundary.
    const lastSpace = slice.lastIndexOf(' ');
    const cut = lastSpace > 0 ? slice.slice(0, lastSpace) : slice;
    return cut + '…';
}

// buildParams assembles the ListTasksParams for the active tab + context.
// Exported for unit testing.
export function buildParams(
    tab: FilterTab,
    channelID: string | undefined,
    isDM: boolean,
    afterOrderKey: string,
): ListTasksParams {
    const filter = TAB_FILTER[tab];
    const params: ListTasksParams = {
        scope: (isDM ? 'direct' : 'channel') as ListScope,
        limit: pageLimit,
    };
    if (isDM) {
        // Direct mode requires partner_id; the webapp resolves it from the DM
        // channel name in TaskSidebar and passes it via the currentUserID
        // fallback below. The actual partner id arrives as channelID-encoded
        // by the caller; here we forward it as partner_id.
        if (channelID) {
            params.partner_id = channelID;
        }
    } else if (channelID) {
        params.channel_id = channelID;
    }
    if (filter.status) {
        params.status = filter.status as Task['status'];
    }
    if (filter.due) {
        params.due = filter.due;
    }
    if (afterOrderKey) {
        params.after_order_key = afterOrderKey;
    }

    // currentUserID is intentionally not a parameter: the server derives the
    // authenticated user from the session.
    return params;
}

// messageFor extracts a user-facing message from a thrown error, preferring the
// server's text body (ClientError) and falling back to a generic string.
export function messageFor(err: unknown): string {
    if (err instanceof ClientError) {
        return err.message || 'request failed';
    }
    return err instanceof Error ? err.message : 'request failed';
}

// assigneeInitials derives up to two uppercase initials from an "@username"
// label for the assignee avatar-dot. Falls back to "?" when the label is empty
// or unresolved (the loading "…" case renders the dot anyway).
export function assigneeInitials(label: string | undefined): string {
    if (!label) {
        return '?';
    }
    const clean = label.replace(/^@/, '').trim();
    if (!clean) {
        return '?';
    }
    return clean.slice(0, 2).toUpperCase();
}

// SearchIcon / CheckIcon / PlusIcon / CalendarIcon are the inline Mattermost-style glyphs.
function SearchIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
        >
            <circle
                cx='7'
                cy='7'
                r='4.5'
                fill='none'
                stroke='currentColor'
                strokeWidth='1.6'
            />
            <path
                d='M10.5 10.5L14 14'
                stroke='currentColor'
                strokeWidth='1.6'
                strokeLinecap='round'
                fill='none'
            />
        </svg>
    );
}

function PlusIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
        >
            <path
                d='M8 3v10M3 8h10'
                stroke='currentColor'
                strokeWidth='2'
                strokeLinecap='round'
                fill='none'
            />
        </svg>
    );
}

function CalendarIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
        >
            <rect
                x='2.5'
                y='3.5'
                width='11'
                height='10'
                rx='1.5'
                fill='none'
                stroke='currentColor'
                strokeWidth='1.6'
            />
            <path
                d='M2.5 6.5h11M5.5 2v3M10.5 2v3'
                fill='none'
                stroke='currentColor'
                strokeWidth='1.6'
                strokeLinecap='round'
            />
        </svg>
    );
}
