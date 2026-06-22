// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// TaskSidebar is the Right-Hand Sidebar container. It is the component passed
// to registerRightHandSidebarComponent. It renders one of three views based on
// the Redux slice's `rhsView`:
//   - 'list'   → QuickList (context-driven: channel tasks or DM-shared tasks)
//   - 'detail' → TaskDetailPanel (the currently selected task)
//   - 'new'    → NewTaskDialog inline (the former desktop popup, now an RHS view)
//
// The current channel + user are read from the host Redux store so the Quick
// List can derive its scope (channel vs direct) from the channel type, and the
// New Task form can derive its scope (personal vs channel) automatically.

import React, {useEffect} from 'react';
import {useDispatch, useSelector} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import type {Channel} from '@mattermost/types/channels';
import type {GlobalState} from '@mattermost/types/store';

import {getChannel, getCurrentChannelId} from 'mattermost-redux/selectors/entities/channels';
import {getCurrentUserId} from 'mattermost-redux/selectors/entities/users';

import NewTaskDialog from 'components/new_task_dialog/new_task_dialog';
import TaskDetailPanel from 'components/task_detail_panel/task_detail_panel';
import QuickList from 'components/task_sidebar/quick_list';

// The plugin reducer is mounted by registerReducer at
// state['plugins-<pluginId>'] (Mattermost convention), a top-level key.
const PLUGIN_STATE_KEY = 'plugins-com.mattermost.plugin-task';

interface PluginState {
    rhsView: 'list' | 'detail' | 'new';
    selectedTaskID: string;
    newTaskDialog: {open: boolean; prefillSummary?: string; prefillDescription?: string; channelID?: string};
}

type GlobalStateWithPlugin = GlobalState & {
    [PLUGIN_STATE_KEY]?: PluginState;
};

function selectSlice(state: GlobalStateWithPlugin): PluginState {
    return state[PLUGIN_STATE_KEY] ?? {
        rhsView: 'list',
        selectedTaskID: '',
        newTaskDialog: {open: false},
    };
}

export interface TaskSidebarProps {

    // channelID overrides the host-derived current channel (e.g. when the host
    // pins the RHS to a specific channel).
    channelID?: string;

    // currentUserID overrides the host-derived current user.
    currentUserID?: string;

    // onNewTask opens the New Task view (host-driven). When omitted the sidebar
    // dispatches OPEN_NEW_TASK_DIALOG itself.
    onNewTask?: () => void;
}

// dmPartnerID parses the partner user id from a DM channel's name
// ("<uid1>__<uid2>"), returning "" for non-DM channels / DM-with-self.
function dmPartnerID(channel: Channel | undefined, currentUserID: string): string {
    if (!channel || channel.type !== 'D') {
        return '';
    }
    const parts = (channel.name || '').split('__').filter((s) => s.length > 0);
    const partner = parts.find((id) => id !== currentUserID);
    return partner || '';
}

export default function TaskSidebar({channelID, currentUserID, onNewTask}: TaskSidebarProps): JSX.Element {
    const dispatch = useDispatch();
    const slice = useSelector(selectSlice);

    // Current channel + user from the host store. Falls back to the prop when
    // the host store isn't populated (tests / edge cases). Hooks are called
    // unconditionally (rules-of-hooks); the channel selector returns undefined
    // when the id is empty, which getChannel handles gracefully.
    const hostChannelID = useSelector(getCurrentChannelId) || channelID || '';
    const hostUserID = useSelector(getCurrentUserId) || currentUserID || '';
    const channel: Channel | undefined = useSelector((s: GlobalStateWithPlugin) =>
        (hostChannelID ? getChannel(s, hostChannelID) : undefined),
    );

    // The RHS is considered open while it is mounted; record that so the
    // channel header button reflects state and the detail panel knows it can
    // dispatch selection updates.
    useEffect(() => {
        dispatch({type: ACTION_TYPES.OPEN_RHS});
        return () => {
            dispatch({type: ACTION_TYPES.CLOSE_RHS});
        };
    }, [dispatch]);

    const openNewTask = onNewTask ?? (() => dispatch({
        type: ACTION_TYPES.OPEN_NEW_TASK_DIALOG,
        channelID: hostChannelID,
    }));

    const backToList = () => {
        if (slice.rhsView === 'new') {
            dispatch({type: ACTION_TYPES.CLOSE_NEW_TASK_DIALOG});
            return;
        }
        dispatch({type: ACTION_TYPES.SELECT_TASK, taskID: ''});
    };

    // Derive the Quick List context: for a DM, the "channel" passed to the
    // list is the partner id (scope=direct); for a regular channel it is the
    // channel id (scope=channel).
    const isDM = channel?.type === 'D';
    const quickListChannel = isDM ? dmPartnerID(channel, hostUserID) : hostChannelID;

    return (
        <div
            className='task-rhs'
            data-theme={undefined}
        >
            <div className='task-rhs__body'>
                <div
                    key={slice.rhsView}
                    className='task-rhs__view task-rhs__view--enter'
                >
                    {slice.rhsView === 'new' && (
                        <NewTaskDialog
                            visible={true}
                            channelID={slice.newTaskDialog.channelID ?? hostChannelID}
                            channel={channel ?? null}
                            currentUserID={hostUserID}
                            initialSummary={slice.newTaskDialog.prefillSummary}
                            initialDescription={slice.newTaskDialog.prefillDescription}
                            onClose={backToList}
                        />
                    )}
                    {slice.rhsView === 'detail' && (
                        <TaskDetailPanel
                            taskID={slice.selectedTaskID}
                            onBack={backToList}
                            currentUserID={hostUserID}
                            onOpenSubtask={(id) => dispatch({type: ACTION_TYPES.SELECT_TASK, taskID: id})}
                        />
                    )}
                    {slice.rhsView === 'list' && (
                        <QuickList
                            channelID={quickListChannel}
                            currentUserID={hostUserID}
                            channelType={channel?.type}
                            onSelectTask={(id) => dispatch({type: ACTION_TYPES.SELECT_TASK, taskID: id})}
                            onNewTask={openNewTask}
                        />
                    )}
                </div>
            </div>
        </div>
    );
}
