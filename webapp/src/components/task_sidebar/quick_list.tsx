// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// QuickList is the flat task list shown in the RHS. The user picks one of two
// scopes: "My Tasks" (default) — every task assigned to them across all
// channels (scope=mine) — or "Channel Tasks" — tasks of the current channel
// (scope=channel). Tasks are bucketed into four priority/time groups (URGENT,
// IMPORTANT, NORMAL, DONE) and sorted by deadline within each group. Clicking a
// row selects it (TaskSidebar swaps to TaskDetailPanel); the footer "+ New
// Task" button opens the inline New Task dialog.

import * as client from 'client';
import {ClientError} from 'client';
import {useActiveLocale, useFormatMessage} from 'i18n_utils';
import React, {useCallback, useEffect, useMemo, useState} from 'react';
import {useDispatch} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import {dueBand} from 'components/shared/due_band';
import formatDueRelative from 'components/shared/format_due_relative';
import {priorityLabel} from 'components/shared/priority_pill';
import StatusPill from 'components/shared/status_pill';
import TaskCheck from 'components/shared/task_check';
import {useResolvedUsers} from 'components/user_picker/use_resolved_user';

import type {ListScope, ListTasksParams, Task} from 'types/tasks';

export interface QuickListProps {

    // channelID is the context channel. Used only for the Channel Tasks scope
    // (sent to the server as channel_id). My Tasks ignores it.
    channelID?: string;

    // currentUserID is the authenticated user. The server derives the assignee
    // from the session header, so this is not sent as a param; it is kept on
    // the props for future host-driven overrides.
    // onSelectTask is called when a row is clicked; TaskSidebar uses it to swap
    // to the Task Detail panel.
    onSelectTask?: (taskID: string) => void;

    // onNewTask opens the New Task view.
    onNewTask?: () => void;
}

// pageLimit is the cursor page size for "Load more".
const pageLimit = 25;

// GroupKey enumerates the four priority/time groups. The display order is
// canonical: urgent → important → normal → done.
export type GroupKey = 'urgent' | 'important' | 'normal' | 'done';

export default function QuickList({
    channelID,
    onSelectTask,
    onNewTask,
}: QuickListProps): JSX.Element {
    const dispatch = useDispatch();
    const t = useFormatMessage();
    const locale = useActiveLocale();

    // scope defaults to "My Tasks" (mine) per the product spec. The user can
    // toggle to "Channel Tasks" (channel), which then follows the context
    // channel via the channelID effect dependency.
    const [scope, setScope] = useState<ListScope>('mine');
    const [search, setSearch] = useState('');
    const [tasks, setTasks] = useState<Task[]>([]);
    const [afterOrderKey, setAfterOrderKey] = useState('');
    const [hasMore, setHasMore] = useState(false);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    // latestRequestRef tracks the most recent first-page fetch. Each fetch
    // bumps the counter; when a response arrives we discard it unless its
    // counter still matches the latest. This lets a scope/channel change while
    // an earlier request is in flight win.
    const latestRequestRef = React.useRef(0);

    // loadFirst resets the list and fetches the first page. Memoized so the
    // effect below depends on a stable reference across renders.
    const loadFirst = useCallback(
        async (activeScope: ListScope) => {
            const myRequest = ++latestRequestRef.current;
            setLoading(true);
            setError('');
            try {
                const page = await client.listTasks(
                    buildParams(activeScope, channelID, ''),
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
        [channelID],
    );

    // Fetch the first page whenever the scope or (for Channel Tasks) the
    // context channel changes. Search is client-side.
    useEffect(() => {
        loadFirst(scope);
    }, [scope, channelID, loadFirst]);

    const loadMore = async () => {
        if (!afterOrderKey || loading) {
            return;
        }

        // Capture the scope at request start; if the user switches scope while
        // this fetch is in flight, drop the stale response.
        const myRequest = ++latestRequestRef.current;
        const activeScope = scope;
        setLoading(true);
        try {
            const page = await client.listTasks(
                buildParams(activeScope, channelID, afterOrderKey),
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

    // Bucket visible tasks into the four priority/time groups and sort each by
    // deadline. Recomputed each render from the loaded page + current clock.
    const now = Date.now();
    const groups = useMemo(() => buildGroups(visible, now), [visible, now]);

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
                    className='quick-list__scope-toggle'
                    role='tablist'
                    aria-label={t('webapp.task.scope.label')}
                >
                    <button
                        key='mine'
                        className={`quick-list__scope-tab ${scope === 'mine' ? 'quick-list__scope-tab--active' : ''}`}
                        onClick={() => setScope('mine')}
                        type='button'
                        role='tab'
                        aria-selected={scope === 'mine'}
                    >
                        {t('webapp.task.scope.mine')}
                    </button>
                    <button
                        key='channel'
                        className={`quick-list__scope-tab ${scope === 'channel' ? 'quick-list__scope-tab--active' : ''}`}
                        onClick={() => setScope('channel')}
                        type='button'
                        role='tab'
                        aria-selected={scope === 'channel'}
                    >
                        {t('webapp.task.scope.channel_tasks')}
                    </button>
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
                                            aria-label={task.summary}
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
                                            <span
                                                className='quick-list__item-meta'
                                            >
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

// buildParams assembles the ListTasksParams for the active scope + context.
// scope=mine spans all channels (no channel_id); scope=channel narrows to the
// context channel. Exported for unit testing.
export function buildParams(
    scope: ListScope,
    channelID: string | undefined,
    afterOrderKey: string,
): ListTasksParams {
    const params: ListTasksParams = {
        scope,
        limit: pageLimit,
    };
    if (scope === 'channel' && channelID) {
        params.channel_id = channelID;
    }
    if (afterOrderKey) {
        params.after_order_key = afterOrderKey;
    }
    return params;
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
    return dueBand(task.due, Date.now(), task.status) === 'danger';
}

// isDueSoon reports whether the task is due within the current local day and
// not terminal — used to tint the chip amber.
export function isDueSoon(task: Task): boolean {
    if (!task.due) {
        return false;
    }
    return dueBand(task.due, Date.now(), task.status) === 'warning';
}

// isDueToday reports whether the task falls within the current local calendar
// day (open or terminal).
export function isDueToday(task: Task): boolean {
    if (!task.due) {
        return false;
    }
    const now = new Date();
    const due = new Date(task.due);
    return (
        due.getFullYear() === now.getFullYear() &&
		due.getMonth() === now.getMonth() &&
		due.getDate() === now.getDate()
    );
}

// startOfLocalDay returns the local-midnight timestamp for the calendar day of
// the given ms epoch.
function startOfLocalDay(ms: number): number {
    const d = new Date(ms);
    return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

const DAY_MS = 24 * 60 * 60 * 1000;

// isDueWithinDays reports whether the task's due date falls on a local calendar
// day between fromDays (inclusive) and toDays (inclusive) ahead of `now`'s
// calendar day. Day 0 is today. No due → false. Used by the IMPORTANT bucket
// (1..3 days). Exported for unit testing.
export function isDueWithinDays(
    task: Task,
    now: number,
    fromDays: number,
    toDays: number,
): boolean {
    if (!task.due) {
        return false;
    }
    const today0 = startOfLocalDay(now);
    const due0 = startOfLocalDay(task.due);
    const dayDiff = Math.round((due0 - today0) / DAY_MS);
    return dayDiff >= fromDays && dayDiff <= toDays;
}

// classifyGroup assigns a task to exactly one priority/time group at `now`.
// Evaluation order matters: terminal → urgent → important → normal (the first
// matching branch wins). Exported for unit testing.
export function classifyGroup(task: Task, now: number): GroupKey {
    const terminal = task.status === 'done' || task.status === 'cancelled';
    if (terminal) {
        return 'done';
    }

    // URGENT: urgent priority, OR due today, OR overdue (past).
    if (
        task.priority === 'urgent' ||
		isDueTodayAt(task, now) ||
		(task.due !== undefined && task.due < now)
    ) {
        return 'urgent';
    }

    // IMPORTANT: important priority, OR due in 1..3 days.
    if (task.priority === 'important' || isDueWithinDays(task, now, 1, 3)) {
        return 'important';
    }
    return 'normal';
}

// isDueTodayAt is the clock-parameterised twin of isDueToday, so classifyGroup
// is pure over its `now` argument (no hidden Date.now()).
function isDueTodayAt(task: Task, now: number): boolean {
    if (!task.due) {
        return false;
    }
    const n = new Date(now);
    const due = new Date(task.due);
    return (
        due.getFullYear() === n.getFullYear() &&
		due.getMonth() === n.getMonth() &&
		due.getDate() === n.getDate()
    );
}

// GroupedTasks is one bucket in the grouped list.
export interface GroupedTasks {
    key: GroupKey;
    label: string;
    items: Task[];
}

// GROUP_ORDER is the canonical display order of the four groups.
const GROUP_ORDER: GroupKey[] = ['urgent', 'important', 'normal', 'done'];

// sortTasksByDue sorts tasks ascending by due date, with tasks that have no due
// date placed last. The sort is stable so equal/absent due values keep input
// order. Exported for unit testing.
export function sortTasksByDue(tasks: Task[]): Task[] {
    // Array.prototype.sort is stable in the engines this plugin targets
    // (modern Chromium / Node). Infinity pushes no-due items to the tail.
    return [...tasks].sort((a, b) => {
        const av = a.due === undefined ? Number.POSITIVE_INFINITY : a.due;
        const bv = b.due === undefined ? Number.POSITIVE_INFINITY : b.due;
        if (av === bv) {
            return 0;
        }
        return av < bv ? -1 : 1;
    });
}

// buildGroups buckets tasks into the four priority/time groups (canonical
// order), sorts each bucket by due date, and drops empty buckets. Pure over
// `now`. Exported for unit testing.
export function buildGroups(tasks: Task[], now: number): GroupedTasks[] {
    const buckets: Record<GroupKey, Task[]> = {
        urgent: [],
        important: [],
        normal: [],
        done: [],
    };
    for (const task of tasks) {
        buckets[classifyGroup(task, now)].push(task);
    }
    const out: GroupedTasks[] = [];
    for (const key of GROUP_ORDER) {
        const items = buckets[key];
        if (items.length > 0) {
            out.push({key, label: key, items: sortTasksByDue(items)});
        }
    }
    return out;
}

// groupLabel maps a group key to its localized header label.
export function groupLabel(key: GroupKey, t: (id: string) => string): string {
    switch (key) {
    case 'urgent':
        return t('webapp.task.group.urgent');
    case 'important':
        return t('webapp.task.group.important');
    case 'normal':
        return t('webapp.task.group.normal');
    case 'done':
        return t('webapp.task.group.done');
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
