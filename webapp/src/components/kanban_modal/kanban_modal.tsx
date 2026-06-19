// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// KanbanModal is the near-full-screen Kanban board (registered as a root
// component in #27). The drag-and-drop board itself is a later phase; this
// shell is render-safe so registration compiles and the root component slot is
// wired, matching the acceptance criterion in #27.

import React from 'react';

export default function KanbanModal(): JSX.Element {
    return (
        <div className='task-kanban-modal'>
            {'Kanban board'}
        </div>
    );
}
