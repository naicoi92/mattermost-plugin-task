// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// NewTaskDialog is the desktop popup for creating a new task (issue #30,
// building on the #27 shell). Registered as a root component, it is mounted
// once and toggled via the `visible` prop. Fields: summary (required), assignee
// (text input in MVP), due datetime, description, and a scope toggle (Personal
// / Channel). Submit goes through POST /tasks (#31); on success the dialog
// closes, the new task is dispatched into the store (#27), and the Quick List
// refreshes. The same dialog is reachable from the RHS "+ New Task" button, the
// channel header, and the post dropdown action (#16).

import * as client from 'client';
import {ClientError} from 'client';
import {useFormatMessage} from 'i18n_utils';
import React, {useEffect, useState} from 'react';
import {useDispatch} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import type {CreateTaskInput, Task} from 'types/tasks';

// Scope values for the toggle. 'personal' omits channel_id; 'channel' requires
// one (provided by the opener via the channelID prop).
export type NewTaskScope = 'personal' | 'channel';

export interface NewTaskDialogProps {

    // visible gates rendering; defaults to hidden.
    visible?: boolean;

    // channelID is the context channel (when opened from a channel). Required
    // when scope === 'channel'; ignored for personal tasks.
    channelID?: string;

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
    scope: 'personal' as NewTaskScope,
};

export default function NewTaskDialog({visible, channelID, onClose, onCreated}: NewTaskDialogProps): JSX.Element | null {
    const dispatch = useDispatch();
    const t = useFormatMessage();

    const [form, setForm] = useState(emptyForm);
    const [error, setError] = useState('');
    const [submitting, setSubmitting] = useState(false);

    // Reset the form whenever the dialog is opened so a previous draft doesn't
    // linger. Default the scope to 'channel' when a channel context is present.
    useEffect(() => {
        if (visible) {
            setForm({...emptyForm, scope: channelID ? 'channel' : 'personal'});
            setError('');
        }
    }, [visible, channelID]);

    if (!visible) {
        return null;
    }

    const update = (patch: Partial<typeof form>) => {
        setForm((prev) => ({...prev, ...patch}));
    };

    const submit = async () => {
        const summary = form.summary.trim();
        if (!summary) {
            setError(t('webapp.error.required'));
            return;
        }
        if (form.scope === 'channel' && !channelID) {
            // Defensive: the opener should always pass a channel for channel
            // scope; if not, surface a clear error rather than posting blindly.
            setError(t('webapp.error.required'));
            return;
        }

        const input: CreateTaskInput = {
            summary,
            description: form.description,
            channel_id: form.scope === 'channel' ? channelID : undefined,
        };
        if (form.assigneeID.trim()) {
            input.assignee_id = form.assigneeID.trim();
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
        <div
            className='task-new-dialog__overlay'
            onClick={cancel}
            role='presentation'
        >
            <div
                className='task-new-dialog'
                onClick={(e) => e.stopPropagation()}
                role='dialog'
                aria-label={t('webapp.task.new')}
            >
                <div className='task-new-dialog__title'>{t('webapp.task.new')}</div>

                {error && <div className='task-new-dialog__error'>{error}</div>}

                <label className='task-new-dialog__field'>
                    <span className='task-new-dialog__label'>{t('webapp.task.summary')}</span>
                    <input
                        className='task-new-dialog__input'
                        value={form.summary}
                        onChange={(e) => update({summary: e.target.value})}
                        placeholder={t('webapp.task.summary.placeholder')}
                        autoFocus={true}
                    />
                </label>

                <label className='task-new-dialog__field'>
                    <span className='task-new-dialog__label'>{t('webapp.task.assignee')}</span>
                    <input
                        className='task-new-dialog__input'
                        value={form.assigneeID}
                        onChange={(e) => update({assigneeID: e.target.value})}
                        placeholder={'@user_id'}
                    />
                </label>

                <label className='task-new-dialog__field'>
                    <span className='task-new-dialog__label'>{t('webapp.task.due')}</span>
                    <input
                        className='task-new-dialog__input'
                        type='datetime-local'
                        value={form.dueLocal}
                        onChange={(e) => update({dueLocal: e.target.value})}
                    />
                </label>

                <label className='task-new-dialog__field'>
                    <span className='task-new-dialog__label'>{t('webapp.task.description')}</span>
                    <textarea
                        className='task-new-dialog__textarea'
                        value={form.description}
                        onChange={(e) => update({description: e.target.value})}
                    />
                </label>

                <fieldset className='task-new-dialog__field task-new-dialog__scope'>
                    <legend className='task-new-dialog__label'>{t('webapp.task.scope.personal')}</legend>
                    <label>
                        <input
                            type='radio'
                            name='task-scope'
                            value='personal'
                            checked={form.scope === 'personal'}
                            onChange={() => update({scope: 'personal'})}
                        />
                        {t('webapp.task.scope.personal')}
                    </label>
                    <label>
                        <input
                            type='radio'
                            name='task-scope'
                            value='channel'
                            checked={form.scope === 'channel'}
                            onChange={() => update({scope: 'channel'})}
                            disabled={!channelID}
                        />
                        {t('webapp.task.scope.channel')}
                    </label>
                </fieldset>

                <div className='task-new-dialog__actions'>
                    <button
                        className='task-new-dialog__btn task-new-dialog__btn--secondary'
                        onClick={cancel}
                        type='button'
                        disabled={submitting}
                    >
                        {t('webapp.task.cancel')}
                    </button>
                    <button
                        className='task-new-dialog__btn task-new-dialog__btn--primary'
                        onClick={submit}
                        type='button'
                        disabled={submitting}
                    >
                        {t('webapp.task.create')}
                    </button>
                </div>
            </div>
        </div>
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

// messageFor extracts a user-facing message from a thrown error, preferring the
// server's text body (ClientError) and falling back to a generic string.
function messageFor(err: unknown): string {
    if (err instanceof ClientError) {
        return err.message || tFallback();
    }
    return err instanceof Error ? err.message : tFallback();
}

// tFallback is a static generic message for when no better text is available.
function tFallback(): string {
    return 'request failed';
}
