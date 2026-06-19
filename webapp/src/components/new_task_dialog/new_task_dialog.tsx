// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// NewTaskDialog is the desktop popup for creating a new task (issue #27
// registration; issue #30 builds out the form). It is registered as a root
// component so it can be mounted imperatively from the RHS button, the channel
// header, and the post dropdown action (#16).
//
// This shell renders nothing visible until opened; issue #30 will add the
// summary/assignee/due/description/scope fields and POST submission.

import React from 'react';

export interface NewTaskDialogProps {

    // visible gates rendering so the host can mount the component once and
    // toggle visibility. Issue #30 will drive this from Redux.
    visible?: boolean;
}

export default function NewTaskDialog({visible}: NewTaskDialogProps): JSX.Element | null {
    if (visible === false) {
        return null;
    }
    return (
        <div className='task-new-dialog'>
            {'New Task dialog (issue #30)'}
        </div>
    );
}
