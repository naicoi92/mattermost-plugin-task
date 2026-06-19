// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// KanbanModal is the near-full-screen Kanban board (registered as a root
// component in #27). The drag-and-drop board itself is a later phase; this
// shell is render-safe so registration compiles and the root component slot is
// wired, matching the acceptance criterion in #27.
//
// Registered as a root component, KanbanModal is mounted once at the channel
// root; it stays hidden until its consumer flips `visible` (via Redux/props),
// so the placeholder never shows unprompted.

import React from 'react';

export interface KanbanModalProps {

    // visible gates rendering; the board stays hidden until a consumer opens it.
    // Defaults to hidden so registering the root component doesn't paint a board.
    visible?: boolean;
}

export default function KanbanModal({visible}: KanbanModalProps): JSX.Element | null {
    if (!visible) {
        return null;
    }
    return (
        <div className='task-kanban-modal'>
            {'Kanban board'}
        </div>
    );
}
