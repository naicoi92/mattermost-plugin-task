// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// StatusPill renders the uppercase pill-with-leading-dot used by both the
// Quick List row and the Task Detail meta-table. The modifier class drives the
// color via .task-status-pill--<status> in styles/index.scss.

import {useFormatMessage} from 'i18n_utils';
import React from 'react';

import type {TaskStatus} from 'types/tasks';

export interface StatusPillProps {
    status: TaskStatus;
}

export default function StatusPill({status}: StatusPillProps): JSX.Element {
    const t = useFormatMessage();
    return (
        <span className={`task-status-pill task-status-pill--${status}`}>
            {statusLabel(status, t)}
        </span>
    );
}

// statusLabel maps a status to its localized label. Exported so the Quick List
// row and Task Detail can reuse it without duplicating the switch.
export function statusLabel(status: TaskStatus, t: (id: string) => string): string {
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
