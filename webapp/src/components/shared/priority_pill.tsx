// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// PriorityDot and PriorityPill render the priority indicator for the Quick
// List row (dot only, when priority != standard) and the Task Detail meta-
// table (dot + label, click-to-cycle). The modifier class drives the color via
// .task-priority-dot--<priority> / .task-priority-pill--<priority> in
// styles/index.scss.

import {useFormatMessage} from 'i18n_utils';
import React from 'react';

import type {TaskPriority} from 'types/tasks';

export interface PriorityDotProps {
    priority: TaskPriority;
}

// PriorityDot is a bare colored dot. Standard priority returns null so the
// list row doesn't clutter itself with the implicit default.
export function PriorityDot({priority}: PriorityDotProps): JSX.Element | null {
    if (priority === 'standard' || !priority) {
        return null;
    }
    return (
        <span
            className={`task-priority-dot task-priority-dot--${priority}`}
            aria-hidden='true'
        />
    );
}

export interface PriorityPillProps {
    priority: TaskPriority;
    onClick?: () => void;
}

// PriorityPill is the click-to-cycle control for the meta-table. The dot is
// always rendered (so standard shows a muted dot, matching the "neutral"
// intent); the label follows.
export default function PriorityPill({priority, onClick}: PriorityPillProps): JSX.Element {
    const t = useFormatMessage();
    const safePriority: TaskPriority = priority || 'standard';
    return (
        <button
            type='button'
            className={`task-priority-pill task-priority-pill--${safePriority}`}
            onClick={onClick}
            disabled={!onClick}
            aria-label={t('webapp.task.priority')}
        >
            <span className={`task-priority-dot task-priority-dot--${safePriority === 'standard' ? 'important' : safePriority}`}/>
            {priorityLabel(safePriority, t)}
        </button>
    );
}

// priorityLabel maps a priority to its localized label. Exported for reuse.
export function priorityLabel(priority: TaskPriority, t: (id: string) => string): string {
    switch (priority) {
    case 'standard':
        return t('webapp.task.priority.standard');
    case 'important':
        return t('webapp.task.priority.important');
    case 'urgent':
        return t('webapp.task.priority.urgent');
    default:
        return priority;
    }
}
