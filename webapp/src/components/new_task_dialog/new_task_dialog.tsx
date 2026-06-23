// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// NewTaskDialog is the inline "New task" view rendered inside the RHS. Fields:
// summary (required), assignee (user picker), due datetime (hybrid select +
// datetime-local), description, status, and priority. The task's scope
// (personal vs channel) is derived from the channel context the form was
// opened in (see deriveNewTaskContext). Submit goes through POST /tasks; on
// success the RHS returns to the Quick List.

import * as client from 'client';
import {ClientError} from 'client';
import {useFormatMessage} from 'i18n_utils';
import React, {useEffect, useState} from 'react';
import {useDispatch, useSelector} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import type {Channel} from '@mattermost/types/channels';
import type {GlobalState} from '@mattermost/types/store';

import {getChannel} from 'mattermost-redux/selectors/entities/channels';

import MetaDropdown from 'components/shared/meta_dropdown';
import {priorityLabel} from 'components/shared/priority_pill';
import {useResolvedUser} from 'components/user_picker/use_resolved_user';
import UserPicker from 'components/user_picker/user_picker';

import type {CreateTaskInput, Task, TaskPriority} from 'types/tasks';

// ChannelType values: 'D' (direct), 'G' (group), 'O' (public), 'P' (private).
// A DM's partner is encoded in the channel name as "<uid1>__<uid2>".
type ChannelType = 'D' | 'G' | 'O' | 'P';

export interface ChannelLike {
    id: string;
    type: ChannelType | string;
    name?: string;
}

// NewTaskContext is the derived scope decision. channelId "" means a personal
// task (only creator + assignee can see it); a non-empty channelId makes a
// channel task visible to the whole channel. suggestedAssigneeID pre-selects an
// assignee in the picker (the DM partner).
export interface NewTaskContext {
    channelId: string;
    suggestedAssigneeID: string;
}

// deriveNewTaskContext decides a new task's scope from where it's created:
//   - Group/public/private channel (type O/P/G) → channel task, no assignee hint.
//   - Direct message (type D) with a partner → personal task, assignee = partner.
//   - Direct message with yourself (nota) → personal task, assignee = you.
//   - No channel context → personal task, assignee = you.
//
// The DM partner is encoded in channel.name as "<uid1>__<uid2>"; we pick the id
// that is not the current user. Exported (pure) for unit testing.
export function deriveNewTaskContext(
    channel: ChannelLike | null | undefined,
    currentUserId: string,
): NewTaskContext {
    if (!channel || !channel.id) {
        return {channelId: '', suggestedAssigneeID: currentUserId};
    }
    if (channel.type !== 'D') {
        return {channelId: channel.id, suggestedAssigneeID: ''};
    }
    const parts = (channel.name || '').split('__').filter((s) => s.length > 0);
    const partner = parts.find((id) => id !== currentUserId);
    if (!partner) {
        return {channelId: '', suggestedAssigneeID: currentUserId};
    }
    return {channelId: '', suggestedAssigneeID: partner};
}

// channelToContext normalizes a host Channel (or a bare channelID) into the
// ChannelLike shape deriveNewTaskContext reads.
export function channelToContext(
    channel: Channel | null | undefined,
    channelID: string | undefined,
): ChannelLike | null {
    if (channel) {
        return {id: channel.id, type: channel.type, name: channel.name};
    }
    if (channelID) {
        return {id: channelID, type: 'O', name: ''};
    }
    return null;
}

// CreateInputForm is the subset of the dialog form that buildCreateInput reads.
// Structural so tests can pass a plain object without the component's full
// form/state harness.
export interface CreateInputForm {
    summary: string;
    description: string;
    priority: TaskPriority;
    assigneeID: string;
    dueLocal: string;
}

// buildCreateInput assembles the POST /tasks body from the dialog form, the
// derived scope context, and the originating channel id. Extracted (pure) so
// the announce-card contract is unit-testable without a Redux/Intl harness,
// mirroring the quick_list buildParams pattern.
//
// post_channel_id is always sent when an originating channel exists so the
// server posts an announce card even for a personal task created in a DM (the
// task's own channel_id stays empty; the card destination is decided
// server-side). For channel tasks it equals channel_id — redundant but
// harmless, and the server prefers channel_id.
export function buildCreateInput(
    form: CreateInputForm,
    ctx: NewTaskContext,
    channelID: string | undefined,
): CreateTaskInput {
    const input: CreateTaskInput = {
        summary: form.summary.trim(),
        description: form.description,
        priority: form.priority,
    };
    if (ctx.channelId) {
        input.channel_id = ctx.channelId;
    }
    if (channelID) {
        input.post_channel_id = channelID;
    }
    const assigneeID = (form.assigneeID || ctx.suggestedAssigneeID).trim();
    if (assigneeID) {
        input.assignee_id = assigneeID;
    }
    const dueMs = parseDueLocal(form.dueLocal);
    if (dueMs !== null) {
        input.due = dueMs;
    }
    return input;
}

export interface NewTaskDialogProps {

    // visible gates rendering; defaults to hidden.
    visible?: boolean;

    // channelID is the context channel (when opened from a channel). Used to
    // derive the task scope via deriveNewTaskContext.
    channelID?: string;

    // channel supplies the channel type/name for scope derivation.
    channel?: Channel | null;

    // currentUserID is the authenticated user; used as the default assignee for
    // personal/DM-with-self tasks.
    currentUserID?: string;

    // initialSummary / initialDescription pre-fill the form when the dialog opens.
    initialSummary?: string;
    initialDescription?: string;

    // onClose is called when the dialog is dismissed or after a successful
    // create, so the host can flip `visible` back to false.
    onClose?: () => void;

    // onCreated is called with the newly created task, letting the host refresh
    // the Quick List / post a card.
    onCreated?: (task: Task) => void;
}

// QuickDue enumerates the quick-options for the due select. The value is a
// sentinel consumed by applyQuickDue; "" means "use the datetime-local value".
type QuickDue = '' | 'today' | 'tomorrow' | 'weekend' | 'next_week';

// emptyForm is the reset state used when the dialog opens and after a submit.
// Note: status is not included — the server always creates tasks in "todo" and
// does not accept a status on POST /tasks. The form exposes priority + due +
// assignee + description only; if the user wants a different status they set
// it from the Task Detail after creation.
const emptyForm = {
    summary: '',
    assigneeID: '',
    dueLocal: '',
    description: '',
    priority: 'standard' as TaskPriority,
};

export default function NewTaskDialog({
    visible,
    channelID,
    channel,
    currentUserID,
    initialSummary,
    initialDescription,
    onClose,
    onCreated,
}: NewTaskDialogProps): JSX.Element | null {
    const dispatch = useDispatch();
    const t = useFormatMessage();

    const [form, setForm] = useState(emptyForm);
    const [quickDue, setQuickDue] = useState<QuickDue>('');
    const [error, setError] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [showError, setShowError] = useState(false);

    // Derived scope (recomputed on render so it reflects the latest channel).
    // Computed unconditionally so all hooks below stay in a stable order.
    const ctx = deriveNewTaskContext(
        channelToContext(channel, channelID),
        currentUserID || '',
    );

    // Resolve the currently-selected assignee id → "@username" for the picker.
    const resolvedAssigneeLabel = useResolvedUser(form.assigneeID).label;

    // Resolve the context channel's display name (not the raw id) so the
    // meta-table shows a readable channel reference.
    const channelName = useSelector((s: GlobalState) => {
        const id = ctx?.channelId;
        if (!id) {
            return '';
        }
        const ch = getChannel(s as never, id);
        return ch?.display_name || ch?.name || '';
    });

    // Reset the form whenever the dialog is opened. Derive the task scope from
    // the channel context and pre-select the suggested assignee (DM partner or
    // self), applying any prefilled summary/description.
    useEffect(() => {
        if (!visible) {
            return;
        }
        const resetCtx = deriveNewTaskContext(
            channelToContext(channel, channelID),
            currentUserID || '',
        );
        setForm({
            ...emptyForm,
            summary: initialSummary ?? '',
            description: initialDescription ?? '',
            assigneeID: resetCtx.suggestedAssigneeID,
        });
        setQuickDue('');
        setError('');
        setShowError(false);
    }, [
        visible,
        channel,
        channelID,
        currentUserID,
        initialSummary,
        initialDescription,
    ]);

    if (!visible) {
        return null;
    }

    const update = (patch: Partial<typeof form>) => {
        setForm((prev) => ({...prev, ...patch}));
    };

    // applyQuickDue sets form.dueLocal from a quick-option sentinel. Computed
    // in the browser's local time (end of the chosen day), then sent as ms
    // epoch to the server — the server stores UTC neutral.
    const applyQuickDue = (option: QuickDue) => {
        setQuickDue(option);
        if (option === '') {
            return; // manual datetime-local mode — leave form.dueLocal as-is.
        }
        const value = computeQuickDue(option);
        setForm((prev) => ({...prev, dueLocal: value}));
    };

    const submit = async () => {
        if (submitting) {
            return;
        }
        const summary = form.summary.trim();
        if (!summary) {
            setShowError(true);
            setError(t('webapp.error.required'));
            return;
        }

        const input = buildCreateInput(form, ctx, channelID);

        setSubmitting(true);
        try {
            const created = await client.createTask(input);
            dispatch({type: ACTION_TYPES.UPSERT_TASK, task: created});
            onCreated?.(created);
            onClose?.();
        } catch (err) {
            setError(messageFor(err));
        } finally {
            setSubmitting(false);
        }
    };

    const cancel = () => {
        onClose?.();
    };

    return (
        <div
            className='task-detail'
            onKeyDown={(e) => {
                if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
                    e.preventDefault();
                    submit();
                }
            }}
        >
            <div className='task-detail__header'>
                <div className='task-detail__header-left'>
                    <button
                        className='task-detail__back'
                        onClick={cancel}
                        type='button'
                        aria-label={t('webapp.task.cancel')}
                    >
                        <BackIcon/>
                    </button>
                    <span className='task-detail__title-inline'>
                        {t('webapp.task.title.new')}
                    </span>
                </div>
                <button
                    className='task-detail__header-close'
                    onClick={cancel}
                    type='button'
                    aria-label={t('webapp.task.cancel')}
                >
                    <CloseIcon/>
                </button>
            </div>

            <div className='task-detail__scroll'>
                {error && <div className='task-detail__error-block'>{error}</div>}

                <div className='task-detail__title-row'>
                    <input
                        className={`task-detail__title-input ${!form.summary.trim() && showError ? 'task-detail__title-input--error' : ''}`}
                        type='text'
                        value={form.summary}
                        onChange={(e) => {
                            update({summary: e.target.value});
                            if (showError && e.target.value.trim()) {
                                setShowError(false);
                            }
                        }}
                        placeholder={t('webapp.task.summary.placeholder')}
                        autoFocus={true}
                        aria-label={t('webapp.task.summary')}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter') {
                                e.preventDefault();
                                submit();
                            }
                        }}
                    />
                </div>
                {showError && !form.summary.trim() && (
                    <div className='task-detail__field-error'>
                        {t('webapp.error.required')}
                    </div>
                )}

                <div className='task-detail__meta-table'>
                    <div className='task-detail__meta-label'>
                        {t('webapp.task.priority')}
                    </div>
                    <div
                        className={`task-detail__meta-value task-detail__meta-value--priority-${form.priority}`}
                    >
                        <MetaDropdown
                            ariaLabel={t('webapp.task.priority')}
                            value={form.priority}
                            onChange={(v) => update({priority: v as TaskPriority})}
                            options={['standard', 'important', 'urgent'].map((p) => ({
                                value: p,
                                label: priorityLabel(p as TaskPriority, t),
                            }))}
                            triggerNode={
                                <span className='task-detail__priority-trigger'>
                                    <span
                                        className={`task-priority-dot task-priority-dot--${form.priority === 'standard' ? 'important' : form.priority}`}
                                    />
                                    {priorityLabel(form.priority, t)}
                                </span>
                            }
                        />
                    </div>

                    <div className='task-detail__meta-label'>{t('webapp.task.due')}</div>
                    <div className='task-detail__meta-value task-detail__meta-value--picker'>
                        <span className='task-detail__due-field'>
                            <CalendarIcon/>
                            <select
                                className='task-detail__due-select'
                                value={quickDue}
                                aria-label={t('webapp.task.due')}
                                onChange={(e) => applyQuickDue(e.target.value as QuickDue)}
                            >
                                <option value=''>{t('webapp.task.due.pick')}</option>
                                <option value='today'>{t('webapp.task.due.today')}</option>
                                <option value='tomorrow'>
                                    {t('webapp.task.due.tomorrow')}
                                </option>
                                <option value='weekend'>{t('webapp.task.due.weekend')}</option>
                                <option value='next_week'>
                                    {t('webapp.task.due.next_week')}
                                </option>
                            </select>
                            <input
                                className='task-detail__due-input'
                                type='datetime-local'
                                value={form.dueLocal}
                                aria-label={t('webapp.task.due')}
                                onChange={(e) => {
                                    update({dueLocal: e.target.value});
                                    setQuickDue('');
                                }}
                            />
                        </span>
                    </div>

                    <div className='task-detail__meta-label'>
                        {t('webapp.task.assignee')}
                    </div>
                    <div className='task-detail__meta-value task-detail__meta-value--picker'>
                        <UserPicker
                            value={form.assigneeID}
                            valueLabel={resolvedAssigneeLabel}
                            channelID={ctx.channelId || channelID}
                            onSelect={(u) => update({assigneeID: u ? u.id : ''})}
                            placeholder={t('webapp.task.assignee.placeholder')}
                        />
                    </div>

                    {ctx.channelId && (
                        <>
                            <div className='task-detail__meta-label'>
                                {t('webapp.task.scope.channel')}
                            </div>
                            <div className='task-detail__meta-value'>
                                <span className='task-detail__ch-ref'>
                                    <HashIcon/>
                                    {channelName || '#' + ctx.channelId}
                                </span>
                            </div>
                        </>
                    )}
                </div>

                <div className='task-detail__section-label'>
                    {t('webapp.task.description')}
                </div>
                <textarea
                    className='task-detail__description-input'
                    value={form.description}
                    onChange={(e) => update({description: e.target.value})}
                    placeholder={t('webapp.task.description')}
                    aria-label={t('webapp.task.description')}
                />

                <div className='task-detail__form-actions'>
                    <span className='task-detail__form-hint'>
                        <span className='task-detail__kbd'>{'⌘'}</span>
                        <span className='task-detail__kbd'>{'↵'}</span>
                        {t('webapp.task.create.quick_hint')}
                    </span>
                    <button
                        className='task-btn task-btn--secondary'
                        onClick={cancel}
                        type='button'
                        disabled={submitting}
                    >
                        {t('webapp.task.cancel')}
                    </button>
                    <button
                        className='task-btn task-btn--primary'
                        onClick={submit}
                        type='button'
                        disabled={submitting}
                    >
                        <i className='icon fa fa-check'/>
                        {t('webapp.task.create')}
                    </button>
                </div>
            </div>
        </div>
    );
}

// computeQuickDue maps a quick-option sentinel to a datetime-local string
// representing the end of the chosen day in the browser's local time. Exported
// for unit testing.
export function computeQuickDue(option: QuickDue): string {
    const now = new Date();
    const pad = (n: number) => String(n).padStart(2, '0');
    const formatEndOfDay = (d: Date): string => {
        return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T23:59`;
    };
    switch (option) {
    case 'today':
        return formatEndOfDay(now);
    case 'tomorrow': {
        const d = new Date(now);
        d.setDate(d.getDate() + 1);
        return formatEndOfDay(d);
    }
    case 'weekend': {
        // "This weekend" = upcoming Sunday 23:59. If today is Sunday, next week's
        // Sunday.
        const d = new Date(now);
        const day = d.getDay();
        const daysUntilSunday = day === 0 ? 7 : 7 - day;
        d.setDate(d.getDate() + daysUntilSunday);
        return formatEndOfDay(d);
    }
    case 'next_week': {
        // "Next week" = end of next Sunday.
        const d = new Date(now);
        const day = d.getDay();
        const daysUntilSunday = day === 0 ? 7 : 7 - day;
        d.setDate(d.getDate() + daysUntilSunday + 7);
        return formatEndOfDay(d);
    }
    default:
        return '';
    }
}

// BackIcon is the ‹ arrow used in the inline view header.
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

// CloseIcon is the × glyph used in the New Task header close button.
function CloseIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
            style={{
                width: 15,
                height: 15,
                fill: 'none',
                stroke: 'currentColor',
                strokeWidth: 1.8,
                strokeLinecap: 'round',
            }}
        >
            <path d='M4 4l8 8M12 4l-8 8'/>
        </svg>
    );
}

// CalendarIcon is the calendar glyph used before the due field in the meta-
// table. Stroke-based to match Mattermost's line-icon style.
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

// parseDueLocal converts a datetime-local string into epoch milliseconds, or
// null when empty/invalid. datetime-local is interpreted as the user's local
// time.
export function parseDueLocal(value: string): number | null {
    if (!value.trim()) {
        return null;
    }
    const ms = Date.parse(value);
    return Number.isNaN(ms) ? null : ms;
}

// normalizeAssigneeUsername strips a single leading "@" from the assignee field
// value. Exported for unit testing (#96).
export function normalizeAssigneeUsername(value: string): string {
    return value.trim().replace(/^@/, '');
}

// assigneeLookupError maps a thrown assignee-lookup error to the user-facing
// message. Exported for unit testing (#96).
export function assigneeLookupError(
    err: unknown,
    notFoundText: () => string,
): string {
    if (err instanceof ClientError && err.status === 404) {
        return notFoundText();
    }
    return messageFor(err);
}

// messageFor extracts a user-facing message from a thrown error.
export function messageFor(err: unknown): string {
    if (err instanceof ClientError) {
        return err.message || tFallback();
    }
    return err instanceof Error ? err.message : tFallback();
}

function tFallback(): string {
    return 'request failed';
}
