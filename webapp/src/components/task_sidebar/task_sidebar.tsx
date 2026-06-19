// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// TaskSidebar is the Right-Hand Sidebar container for the desktop plugin
// (issue #27 registration; issue #28 builds out the Quick List). It is the
// component passed to registerRightHandSidebarComponent.
//
// This is a minimal, render-safe shell: it dispatches OPEN_RHS on mount and
// renders a translated title. The full Quick List (tabs, filters, flat task
// list, "+ New Task" button) lands in #28 by extending this component.

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

    return (
        <div className='task-rhs'>
            <div className='task-rhs__title'>{'Tasks'}</div>
            <div className='task-rhs__body'>{'Quick List (issue #28)'}</div>
        </div>
    );
}
