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
import {useDispatch} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import type {Channel} from '@mattermost/types/channels';

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
export function deriveNewTaskContext(channel: ChannelLike | null | undefined, currentUserId: string): NewTaskContext {
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
export function channelToContext(channel: Channel | null | undefined, channelID: string | undefined): ChannelLike | null {
    if (channel) {
        return {id: channel.id, type: channel.type, name: channel.name};
    }
    if (channelID) {
        return {id: channelID, type: 'O', name: ''};
    }
    return null;
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

    // Resolve the currently-selected assignee id → "@username" for the picker.
    const resolvedAssigneeLabel = useResolvedUser(form.assigneeID).label;

    // Reset the form whenever the dialog is opened. Derive the task scope from
    // the channel context and pre-select the suggested assignee (DM partner or
    // self), applying any prefilled summary/description.
    useEffect(() => {
        if (!visible) {
            return;
        }
        const ctx = deriveNewTaskContext(channelToContext(channel, channelID), currentUserID || '');
        setForm({
            ...emptyForm,
            summary: initialSummary ?? '',
            description: initialDescription ?? '',
            assigneeID: ctx.suggestedAssigneeID,
        });
        setQuickDue('');
        setError('');
    }, [visible, channel, channelID, currentUserID, initialSummary, initialDescription]);

    if (!visible) {
        return null;
    }

    const update = (patch: Partial<typeof form>) => {
        setForm((prev) => ({...prev, ...patch}));
    };

    // Derived scope (recomputed on render so it reflects the latest channel).
    const ctx = deriveNewTaskContext(channelToContext(channel, channelID), currentUserID || '');

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
            setError(t('webapp.error.required'));
            return;
        }

        const input: CreateTaskInput = {
            summary,
            description: form.description,
            priority: form.priority,
        };
        if (ctx.channelId) {
            input.channel_id = ctx.channelId;
        }
        const assigneeID = (form.assigneeID || ctx.suggestedAssigneeID).trim();
        if (assigneeID) {
            input.assignee_id = assigneeID;
        }
        const dueMs = parseDueLocal(form.dueLocal);
        if (dueMs !== null) {
            input.due = dueMs;
        }

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
        <div className='task-detail'>
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
                    <span className='task-detail__title-inline'>{t('webapp.task.title.new')}</span>
                </div>
            </div>

            <div className='task-detail__scroll'>
                <h2 style={{fontFamily: 'var(--task-font)', fontSize: 22, fontWeight: 700, margin: '8px 0 4px'}}>
                    {t('webapp.task.new')}
                </h2>

                {error && <div className='task-detail__error-block'>{error}</div>}

                <label className='task-field'>
                    <span className='task-field__label task-field__label--upper'>{t('webapp.task.summary')}</span>
                    <input
                        className='task-input task-input--title'
                        value={form.summary}
                        onChange={(e) => update({summary: e.target.value})}
                        placeholder={t('webapp.task.summary.placeholder')}
                        autoFocus={true}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter') {
                                e.preventDefault();
                                submit();
                            }
                        }}
                    />
                </label>

                <label className='task-field'>
                    <span className='task-field__label task-field__label--upper'>{t('webapp.task.description')}</span>
                    <textarea
                        className='task-textarea'
                        value={form.description}
                        onChange={(e) => update({description: e.target.value})}
                    />
                </label>

                <label className='task-field'>
                    <span className='task-field__label task-field__label--upper'>{t('webapp.task.priority')}</span>
                    <select
                        className='task-select'
                        value={form.priority}
                        onChange={(e) => update({priority: e.target.value as TaskPriority})}
                    >
                        <option value='standard'>{t('webapp.task.priority.standard')}</option>
                        <option value='important'>{t('webapp.task.priority.important')}</option>
                        <option value='urgent'>{t('webapp.task.priority.urgent')}</option>
                    </select>
                </label>

                <div className='task-fields-row task-fields-row--due'>
                    <label className='task-field'>
                        <span className='task-field__label task-field__label--upper'>{t('webapp.task.due')}</span>
                        <select
                            className='task-select'
                            value={quickDue}
                            aria-label={t('webapp.task.due')}
                            onChange={(e) => applyQuickDue(e.target.value as QuickDue)}
                        >
                            <option value=''>{t('webapp.task.due.pick')}</option>
                            <option value='today'>{t('webapp.task.due.today')}</option>
                            <option value='tomorrow'>{t('webapp.task.due.tomorrow')}</option>
                            <option value='weekend'>{t('webapp.task.due.weekend')}</option>
                            <option value='next_week'>{t('webapp.task.due.next_week')}</option>
                        </select>
                    </label>
                    <label className='task-field'>
                        <span className='task-field__label task-field__label--upper'>{' '}</span>
                        <input
                            className='task-input'
                            type='datetime-local'
                            value={form.dueLocal}
                            aria-label={t('webapp.task.due')}
                            onChange={(e) => {
                                update({dueLocal: e.target.value});
                                setQuickDue('');
                            }}
                        />
                    </label>
                </div>

                <label className='task-field'>
                    <span className='task-field__label task-field__label--upper'>{t('webapp.task.assignee')}</span>
                    <UserPicker
                        value={form.assigneeID}
                        valueLabel={resolvedAssigneeLabel}
                        channelID={ctx.channelId || channelID}
                        onSelect={(u) => update({assigneeID: u ? u.id : ''})}
                        placeholder={t('webapp.task.assignee.placeholder')}
                    />
                </label>

                <div className='task-actions-bar'>
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
                        <CheckIcon/>
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

function CheckIcon(): JSX.Element {
    return (
        <svg
            viewBox='0 0 16 16'
            aria-hidden='true'
            fill='none'
            stroke='currentColor'
            strokeWidth='2'
            strokeLinecap='round'
        >
            <path d='M3 8.5L6.5 12 13 4.5'/>
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
export function assigneeLookupError(err: unknown, notFoundText: () => string): string {
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
