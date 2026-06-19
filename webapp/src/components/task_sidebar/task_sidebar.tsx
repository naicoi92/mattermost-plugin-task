// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// TaskSidebar is the Right-Hand Sidebar container (issue #27 registration;
// issue #28 builds out the Quick List + detail navigation). It is the component
// passed to registerRightHandSidebarComponent. It renders the Quick List by
// default; selecting a task swaps in the TaskDetailPanel, and the "+ New Task"
// button opens the NewTaskDialog (host-driven via the onNewTask prop or the
// NewTaskDialog root component toggled in Redux).

import {useFormatMessage} from 'i18n_utils';
import React, {useEffect, useState} from 'react';
import {useDispatch, useSelector} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import TaskDetailPanel from 'components/task_detail_panel/task_detail_panel';
import QuickList from 'components/task_sidebar/quick_list';

// The plugin reducer is mounted by registerReducer at
// state['plugins-<pluginId>'] (Mattermost convention), a top-level key.
const PLUGIN_STATE_KEY = 'plugins-com.mattermost.plugin-task';

interface PluginState {
    selectedTaskID: string;
}

type GlobalStateWithPlugin = Record<string, unknown> & {
    [PLUGIN_STATE_KEY]?: PluginState;
};

function selectSelectedTaskID(state: GlobalStateWithPlugin): string {
    return state[PLUGIN_STATE_KEY]?.selectedTaskID ?? '';
}

export interface TaskSidebarProps {

    // channelID is the context channel passed to QuickList / NewTaskDialog.
    channelID?: string;

    // currentUserID gates task detail delete and drives the "mine" scope.
    currentUserID?: string;

    // onNewTask opens the New Task dialog (host-driven).
    onNewTask?: () => void;
}

export default function TaskSidebar({channelID, currentUserID, onNewTask}: TaskSidebarProps): JSX.Element {
    const dispatch = useDispatch();
    const t = useFormatMessage();
    const selectedTaskID = useSelector(selectSelectedTaskID);

    // Local view state so clicking a task swaps to detail even before the store
    // selection lands; the store is the source of truth for cross-view sync.
    const [detailID, setDetailID] = useState('');

    // The RHS is considered open while it is mounted; record that so the
    // channel header button reflects state and the detail panel knows it can
    // dispatch selection updates.
    useEffect(() => {
        dispatch({type: ACTION_TYPES.OPEN_RHS});
        return () => {
            dispatch({type: ACTION_TYPES.CLOSE_RHS});
        };
    }, [dispatch]);

    const showDetailID = detailID || selectedTaskID;

    const backToList = () => {
        setDetailID('');
        dispatch({type: ACTION_TYPES.SELECT_TASK, taskID: ''});
    };

    return (
        <div className='task-rhs'>
            <div className='task-rhs__title'>{t('webapp.task.title')}</div>
            <div className='task-rhs__body'>
                {showDetailID ? (
                    <TaskDetailPanel
                        taskID={showDetailID}
                        onBack={backToList}
                        currentUserID={currentUserID}
                    />
                ) : (
                    <QuickList
                        channelID={channelID}
                        currentUserID={currentUserID}
                        onSelectTask={(id) => setDetailID(id)}
                        onNewTask={onNewTask}
                    />
                )}
            </div>
        </div>
    );
}
