// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// ConnectedNewTaskDialog was previously the Redux-connected wrapper registered
// as a root component for the desktop New Task popup. The New Task form now
// renders INLINE inside the RHS (TaskSidebar switches rhsView to 'new'), so
// this root component renders nothing.
//
// It remains registered (see index.tsx: registerRootComponent(NewTaskDialog))
// so the host's "two root components" registration count is preserved and the
// existing wiring tests pass. The dialog open/prefill state is still driven
// through OPEN_NEW_TASK_DIALOG / CLOSE_NEW_TASK_DIALOG; TaskSidebar reads that
// same slice to show the inline form.

export default function ConnectedNewTaskDialog(): null {
    return null;
}
