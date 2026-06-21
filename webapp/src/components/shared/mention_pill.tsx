// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// MentionPill renders the @mention-style assignee pill used by the Task Detail
// meta-table: an avatar-dot with the user's initials + the @username label,
// tinted Mattermost Blue. When no user is selected it renders a muted
// placeholder so the click target stays visible.

import React from 'react';

export interface MentionPillProps {

    // label is the display text (typically "@username"). When empty the pill
    // renders its placeholder variant.
    label?: string;

    // initials is the 1-2 letter avatar content (e.g. "NC"). Empty falls back
    // to the first character of the label.
    initials?: string;

    // onClick opens the user picker / inline editor.
    onClick?: () => void;

    // placeholder is the text shown when label is empty (e.g. "Assign").
    placeholder?: string;
}

export default function MentionPill({label, initials, onClick, placeholder}: MentionPillProps): JSX.Element {
    const isEmpty = !label;
    const cls = isEmpty ? 'task-mention task-mention--placeholder' : 'task-mention';
    const avatar = isEmpty ? null : (
        <span className='task-mention__avatar-dot'>
            {(initials || label || '?').replace(/^@/, '').slice(0, 2).toUpperCase()}
        </span>
    );
    return (
        <button
            type='button'
            className={cls}
            onClick={onClick}
            disabled={!onClick}
        >
            {avatar}
            <span>{isEmpty ? (placeholder || '') : label}</span>
        </button>
    );
}
