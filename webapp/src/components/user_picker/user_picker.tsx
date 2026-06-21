// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// UserPicker is the searchable assignee selector used in the New Task form and
// the Task Detail panel. It loads candidate users from the host REST API
// (client.searchUsers) — scoped to a channel when one is supplied, otherwise
// globally — and lets the user pick one by typing a query. The selected user is
// surfaced as `value` (user id) + `valueLabel` (display name) so the parent can
// render a chip.
//
// Auth is handled by Client4.getOptions inside client.searchUsers (CSRF +
// credentials), matching the rest of the plugin's API calls.

import * as client from 'client';
import type {UserSearchResult} from 'client';
import {useFormatMessage} from 'i18n_utils';
import React, {useEffect, useRef, useState} from 'react';

export interface UserPickerProps {

    // value is the selected user id ("" = none).
    value: string;

    // valueLabel is the display name for the currently selected user; when the
    // selected user is not in the loaded list the parent should still pass a
    // label so the chip renders.
    valueLabel?: string;

    // channelID scopes the candidate list to channel members when provided.
    channelID?: string;

    // onSelect fires with the chosen user (id + label), or null when cleared.
    onSelect: (user: {id: string; label: string} | null) => void;

    // placeholder overrides the default localized placeholder.
    placeholder?: string;
}

export default function UserPicker({value, valueLabel, channelID, onSelect, placeholder}: UserPickerProps): JSX.Element {
    const t = useFormatMessage();

    const [open, setOpen] = useState(false);
    const [query, setQuery] = useState('');
    const [users, setUsers] = useState<UserSearchResult[]>([]);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');

    const rootRef = useRef<HTMLDivElement>(null);
    const reqIdRef = useRef(0);

    // Close the panel on outside click.
    useEffect(() => {
        if (!open) {
            return undefined;
        }
        const onDocClick = (e: MouseEvent) => {
            if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
                setOpen(false);
            }
        };
        document.addEventListener('mousedown', onDocClick);
        return () => document.removeEventListener('mousedown', onDocClick);
    }, [open]);

    // Load candidates whenever the panel opens or the query changes. Debounced
    // by a short timeout so typing doesn't fire a request per keystroke.
    useEffect(() => {
        if (!open) {
            return undefined;
        }
        const myReq = ++reqIdRef.current;
        const handle = window.setTimeout(async () => {
            setLoading(true);
            setError('');
            try {
                const list = await client.searchUsers(query, channelID, 50);

                // Drop stale responses: only the most recent request wins.
                if (myReq === reqIdRef.current) {
                    setUsers(list);
                }
            } catch (err) {
                if (myReq === reqIdRef.current) {
                    setError(messageFor(err));
                    setUsers([]);
                }
            } finally {
                if (myReq === reqIdRef.current) {
                    setLoading(false);
                }
            }
        }, 200);
        return () => window.clearTimeout(handle);
    }, [open, query, channelID]);

    const selectedLabel = valueLabel || '';
    const ph = placeholder || t('webapp.task.assignee.placeholder');

    // resolved reports whether the currently-selected user has a real display
    // label yet. Until the store/fetch resolves the id to a name we show a
    // muted loading hint instead of the raw id, so the field never flashes
    // an opaque value.
    const resolved = Boolean(value) && selectedLabel.length > 0 && selectedLabel !== value;

    const choose = (u: UserSearchResult) => {
        onSelect({id: u.id, label: userLabel(u)});
        setOpen(false);
        setQuery('');
    };

    return (
        <div
            className='user-picker'
            ref={rootRef}
        >
            <button
                type='button'
                className={`task-input user-picker__trigger ${resolved ? '' : 'user-picker__trigger--placeholder'}`}
                onClick={() => setOpen((v) => !v)}
                aria-haspopup='listbox'
                aria-expanded={open}
            >
                {value && resolved ? (
                    <span className='user-picker__selected'>
                        {selectedLabel}
                        <span
                            className='user-picker__clear'
                            role='button'
                            tabIndex={0}
                            aria-label='clear'
                            onClick={(e) => {
                                e.stopPropagation();
                                onSelect(null);
                            }}
                            onKeyDown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    e.stopPropagation();
                                    onSelect(null);
                                }
                            }}
                        >
                            {'×'}
                        </span>
                    </span>
                ) : (

                    // Either nothing is selected, or a selection exists but its
                    // name hasn't resolved yet. Show a muted hint (never the
                    // raw id) so the field doesn't flash an opaque value.
                    <span className='user-picker__trigger--placeholder'>
                        {value ? '…' : ph}
                    </span>
                )}
            </button>

            {open && (
                <div
                    className='user-picker__panel'
                    role='listbox'
                >
                    <input
                        className='task-input'
                        type='text'
                        value={query}
                        onChange={(e) => setQuery(e.target.value)}
                        placeholder={t('webapp.task.search')}
                        autoFocus={true}
                        style={{border: 0, borderBottom: '1px solid var(--task-border)', borderRadius: 0}}
                    />
                    {loading && <div className='user-picker__loading'>{'…'}</div>}
                    {!loading && error && <div className='user-picker__empty'>{error}</div>}
                    {!loading && !error && users.length === 0 && (
                        <div className='user-picker__empty'>{t('webapp.task.empty')}</div>
                    )}
                    {!loading && !error && users.map((u) => (
                        <button
                            key={u.id}
                            type='button'
                            className={`user-picker__option ${value === u.id ? 'user-picker__option--active' : ''}`}
                            onClick={() => choose(u)}
                            role='option'
                            aria-selected={value === u.id}
                        >
                            <span className='quick-list__avatar'>{initialsOf(u)}</span>
                            <span className='user-picker__option-name'>
                                <span>{userLabel(u)}</span>
                                <span>{'@' + u.username}</span>
                            </span>
                        </button>
                    ))}
                </div>
            )}
        </div>
    );
}

// userLabel returns a display label for a user: nickname if set, else
// "First Last" if either name is set, else the username.
export function userLabel(u: UserSearchResult): string {
    if (u.nickname && u.nickname.trim()) {
        return u.nickname.trim();
    }
    const full = [u.first_name, u.last_name].filter((s) => s && s.trim()).join(' ').trim();
    return full || u.username;
}

// initialsOf derives up to two uppercase initials for the avatar bubble.
export function initialsOf(u: UserSearchResult): string {
    const a = (u.first_name || '').trim();
    const b = (u.last_name || '').trim();
    if (a && b) {
        return (a[0] + b[0]).toUpperCase();
    }
    if (u.nickname && u.nickname.trim()) {
        return u.nickname.trim()[0].toUpperCase();
    }
    return (u.username[0] || '?').toUpperCase();
}

// messageFor extracts a user-facing message from a thrown error, preferring the
// server's text body (ClientError) and falling back to a generic string.
export function messageFor(err: unknown): string {
    // Local copy so this file has no import cycle with the other messageFor
    // twins; behavior is identical (verified by the shared contract tests).
    const anyErr = err as {status?: number; message?: string};
    if (anyErr && typeof anyErr.status === 'number') {
        return anyErr.message || 'request failed';
    }
    return err instanceof Error ? err.message : 'request failed';
}
