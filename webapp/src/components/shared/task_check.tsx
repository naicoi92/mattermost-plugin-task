// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// TaskCheck renders the custom SVG checkbox box used by the Quick List row,
// the Task Detail title/subtask rows, and the Task Post Card. It replaces the
// previous FontAwesome `fa-square-o` / `fa-check-square` glyphs so the box
// matches the design: a rounded-rect (1.5px border, 4px radius) that fills
// `--task-success` (green) with a white check mark when done.
//
// The element is a pure glyph (aria-hidden): it is placed inside the existing
// wrapper spans (`.quick-list__check`, `.task-detail__title-check`,
// `.task-post-card__check`, `.task-detail__subtask-check`) which own the role,
// aria, click/keyboard handlers and the box size. TaskCheck fills the wrapper
// (width/height 100%) so each consumer's wrapper size still drives the visible
// checkbox size, and reads `currentColor` for its open border so each consumer's
// muted color applies.

import React from 'react';

export interface TaskCheckProps {

    // done selects the open vs filled state.
    done: boolean;
}

export default function TaskCheck({done}: TaskCheckProps): JSX.Element {
    return (
        <span
            className={`task-check ${done ? 'task-check--done' : ''}`}
            aria-hidden='true'
        >
            {done && (
                <svg
                    viewBox='0 0 16 16'
                    fill='none'
                    stroke='currentColor'
                    strokeWidth='2.4'
                    strokeLinecap='round'
                    strokeLinejoin='round'
                >
                    <path d='M3 8.5L6.5 12 13 4.5'/>
                </svg>
            )}
        </span>
    );
}
