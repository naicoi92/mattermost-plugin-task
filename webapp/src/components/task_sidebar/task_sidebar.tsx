// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// TaskSidebar is the Right-Hand Sidebar container for the desktop plugin
// (issue #27 registration; issue #28 builds out the Quick List). It is the
// component passed to registerRightHandSidebarComponent.
//
// This is a minimal, render-safe shell: it dispatches OPEN_RHS on mount and
// renders a title. The full Quick List (tabs, filters, flat task list, "+ New
// Task" button) lands in #28 by extending this component, and all user-visible
// strings become i18n-backed in #33 (which adds the gettext helper this shell
// will adopt). The title passed to registerRightHandSidebarComponent already
// localizes the RHS tab; the body placeholder here is development-only text.

import React from 'react';
import {useDispatch} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

export default function TaskSidebar(): JSX.Element {
    const dispatch = useDispatch();

    // The RHS is considered open while it is mounted; record that so the
    // channel header button reflects state and the detail panel knows it can
    // dispatch selection updates.
    React.useEffect(() => {
        dispatch({type: ACTION_TYPES.OPEN_RHS});
        return () => {
            dispatch({type: ACTION_TYPES.CLOSE_RHS});
        };
    }, [dispatch]);

    // TODO(#28, #33): replace this development-only placeholder with the Quick
    // List and translate every string via the i18n helper wired in #33.
    return (
        <div className='task-rhs'>
            <div className='task-rhs__title'>{'Tasks'}</div>
            <div className='task-rhs__body'>{'Quick List'}</div>
        </div>
    );
}
