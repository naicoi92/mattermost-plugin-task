// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// ConnectedNewTaskDialog is the Redux-connected wrapper registered as the root
// component for the New Task popup (#27 registration, #16 wiring). It reads the
// dialog open/prefill state from the plugin store (driven by the post-dropdown
// action and the RHS "+ New Task" button) and renders NewTaskDialog accordingly,
// closing it via a CLOSE_NEW_TASK_DIALOG dispatch on dismiss/submit.

import React from 'react';
import {useDispatch, useSelector} from 'react-redux';
import {ACTION_TYPES} from 'reducer';

import NewTaskDialog from 'components/new_task_dialog/new_task_dialog';

const PLUGIN_KEY = 'plugins-com.mattermost.plugin-task';

interface DialogState {
    open: boolean;
    prefillSummary?: string;
    prefillDescription?: string;
    channelID?: string;
}

type GlobalStateWithPlugin = Record<string, unknown> & {
    [PLUGIN_KEY]?: {newTaskDialog: DialogState};
};

function selectDialog(state: GlobalStateWithPlugin): DialogState {
    return state[PLUGIN_KEY]?.newTaskDialog ?? {open: false};
}

export default function ConnectedNewTaskDialog(): JSX.Element {
    const dispatch = useDispatch();
    const dialog = useSelector(selectDialog);

    return (
        <NewTaskDialog
            visible={dialog.open}
            channelID={dialog.channelID}
            initialSummary={dialog.prefillSummary}
            initialDescription={dialog.prefillDescription}
            onClose={() => dispatch({type: ACTION_TYPES.CLOSE_NEW_TASK_DIALOG})}
        />
    );
}
