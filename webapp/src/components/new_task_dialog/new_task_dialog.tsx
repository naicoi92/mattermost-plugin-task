// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// NewTaskDialog is the inline "New task" view rendered inside the RHS (the
// former desktop popup was converted to an RHS view so the whole task UI lives
// in one Lark-style sidebar). Fields: summary (required), assignee (user
// picker), due datetime, description. The task's scope (personal vs channel) is
// NO LONGER chosen by the user — it is derived from the channel context the
// form was opened in (see deriveNewTaskContext). Submit goes through
// POST /tasks; on success the RHS returns to the Quick List.
//
// The connected root component (connected_new_task_dialog) is still registered
// so index.test's "two root components" assertion holds, but it renders null.

import * as client from 'client';
import {ClientError} from 'client';
import {useFormatMessage} from 'i18n_utils';
import React, {useEffect, useState} from 'react';
import {useDispatch} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import type {Channel} from '@mattermost/types/channels';

import {useResolvedUser} from 'components/user_picker/use_resolved_user';
import UserPicker from 'components/user_picker/user_picker';

import type {CreateTaskInput, Task} from 'types/tasks';

// ChannelLike is the minimal slice of Channel deriveNewTaskContext reads.
// Channel.type values: 'D' (direct), 'G' (group), 'O' (public), 'P' (private).
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

    // Any non-DM channel → a channel task belonging to that channel.
    if (channel.type !== 'D') {
        return {channelId: channel.id, suggestedAssigneeID: ''};
    }

    // DM: parse the two user ids from the channel name.
    const parts = (channel.name || '').split('__').filter((s) => s.length > 0);
    const partner = parts.find((id) => id !== currentUserId);
    if (!partner) {
        // No partner distinct from me → DM with myself (nota).
        return {channelId: '', suggestedAssigneeID: currentUserId};
    }
    return {channelId: '', suggestedAssigneeID: partner};
}

// channelToContext normalizes a host Channel (or a bare channelID) into the
// ChannelLike shape deriveNewTaskContext reads. A bare channelID is treated as
// a public channel (best effort when the host didn't supply the full object).
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

    // channel supplies the channel type/name for scope derivation. When omitted
    // the dialog falls back to treating channelID as a channel task (best
    // effort). The host RHS passes the full channel object.
    channel?: Channel | null;

    // currentUserID is the authenticated user; used as the default assignee for
    // personal/DM-with-self tasks.
    currentUserID?: string;

    // initialSummary / initialDescription pre-fill the form when the dialog opens
    // (e.g. from the post-dropdown "Tạo task" action, #16).
    initialSummary?: string;
    initialDescription?: string;

    // onClose is called when the dialog is dismissed or after a successful
    // create, so the host can flip `visible` back to false.
    onClose?: () => void;

    // onCreated is called with the newly created task, letting the host refresh
    // the Quick List / post a card.
    onCreated?: (task: Task) => void;
}

// emptyForm is the reset state used when the dialog opens and after a submit.
const emptyForm = {
    summary: '',
    assigneeID: '',
    dueLocal: '',
    description: '',
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
    const [error, setError] = useState('');
    const [submitting, setSubmitting] = useState(false);

    // Resolve the currently-selected assignee id → "@username" for the picker
    // chip. Store-first, fetch fallback. Recomputed whenever the selection
    // changes (open dialog, user picks from the picker, DM suggest).
    const resolvedAssigneeLabel = useResolvedUser(form.assigneeID).label;

    // Reset the form whenever the dialog is opened. Derive the task scope from
    // the channel context and pre-select the suggested assignee (DM partner or
    // self), applying any prefilled summary/description (e.g. from the
    // post-dropdown "Tạo task" action, #16).
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

    const submit = async () => {
        const summary = form.summary.trim();
        if (!summary) {
            setError(t('webapp.error.required'));
            return;
        }

        const input: CreateTaskInput = {
            summary,
            description: form.description,
        };

        // Scope: a derived channel id → channel task; empty → personal.
        if (ctx.channelId) {
            input.channel_id = ctx.channelId;
        }

        // Assignee: the picker resolves to a user id. Keep the legacy
        // @username → user lookup as a fallback when an opaque id is present but
        // no picker selection was made (e.g. prefilled by an older caller).
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
                <button
                    className='task-detail__back'
                    onClick={cancel}
                    type='button'
                    aria-label={t('webapp.task.cancel')}
                >
                    <BackIcon/>
                </button>
                <span style={{fontWeight: 600, fontSize: 16}}>{t('webapp.task.new')}</span>
            </div>

            {error && <div className='task-detail__error-block'>{error}</div>}

            <label className='task-field'>
                <span className='task-field__label'>{t('webapp.task.summary')}</span>
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

            <div className='task-fields-row'>
                <label className='task-field'>
                    <span className='task-field__label'>{t('webapp.task.assignee')}</span>
                    <UserPicker
                        value={form.assigneeID}
                        valueLabel={resolvedAssigneeLabel}
                        channelID={ctx.channelId || channelID}
                        onSelect={(u) => update({assigneeID: u ? u.id : ''})}
                    />
                </label>
                <label className='task-field'>
                    <span className='task-field__label'>{t('webapp.task.due')}</span>
                    <input
                        className='task-input'
                        type='datetime-local'
                        value={form.dueLocal}
                        onChange={(e) => update({dueLocal: e.target.value})}
                    />
                </label>
            </div>

            <label className='task-field'>
                <span className='task-field__label'>{t('webapp.task.description')}</span>
                <textarea
                    className='task-textarea'
                    value={form.description}
                    onChange={(e) => update({description: e.target.value})}
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
                    {t('webapp.task.create')}
                </button>
            </div>
        </div>
    );
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

// parseDueLocal converts a datetime-local string (e.g. "2026-06-19T12:00") into
// epoch milliseconds, or null when empty/invalid. datetime-local is interpreted
// as the user's local time, which matches the picker's UX.
export function parseDueLocal(value: string): number | null {
    if (!value.trim()) {
        return null;
    }
    const ms = Date.parse(value);
    return Number.isNaN(ms) ? null : ms;
}

// normalizeAssigneeUsername strips a single leading "@" from the assignee field
// value so the lookup hits the host /api/v4/users/username/<name> endpoint
// correctly. Trims surrounding whitespace. Exported for unit testing (#96).
export function normalizeAssigneeUsername(value: string): string {
    return value.trim().replace(/^@/, '');
}

// assigneeLookupError maps a thrown assignee-lookup error to the user-facing
// message. A 404 (unknown username) returns the localized not-found text via
// the notFoundText callback so the message is actionable and translated; any
// other error surfaces its raw message via messageFor. Extracted so the UX
// contract is unit-testable without an i18n/Redux harness (#96).
//
// notFoundText is a callback (not a string) so it's only evaluated on the 404
// path, keeping the non-404 path free of any i18n dependency.
export function assigneeLookupError(err: unknown, notFoundText: () => string): string {
    if (err instanceof ClientError && err.status === 404) {
        return notFoundText();
    }
    return messageFor(err);
}

// messageFor extracts a user-facing message from a thrown error, preferring the
// server's text body (ClientError) and falling back to a generic string.
// Exported so tests verify the production logic rather than a hand-copied twin.
export function messageFor(err: unknown): string {
    if (err instanceof ClientError) {
        return err.message || tFallback();
    }
    return err instanceof Error ? err.message : tFallback();
}

// tFallback is a static generic message for when no better text is available.
function tFallback(): string {
    return 'request failed';
}
