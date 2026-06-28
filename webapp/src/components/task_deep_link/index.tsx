// TaskDeepLink is the webapp custom route component mounted at
// /plug/<plugin-id>/task/:id. A DM notification renders the task name as a
// markdown link to this route; clicking it opens the task's Task Details in the
// RHS and sends the user back to where they came from (so they never land on a
// blank route page).
//
// On mount it: (1) dispatches SELECT_TASK with the id from the URL, (2) opens
// the RHS via the opener captured during initialize (same setter pattern as
// task_post_card), and (3) navigates back. When there is no previous history
// entry (the URL was pasted directly) it falls back to '/' so the user lands on
// a real page rather than a blank route.
//
// NOTE: clicking the markdown link causes a brief page reload/flash because the
// host navigates to the route URL before this component mounts. This is an
// accepted trade-off: the feature works (the task opens), the flash is cosmetic.
// See change notification-overdue-and-context, design D9.

import {useFormatMessage} from 'i18n_utils';
import React, {useEffect} from 'react';
import {useDispatch} from 'react-redux';
import {useHistory, useParams} from 'react-router-dom';
import {ACTION_TYPES} from 'reducer';

// rhsOpener opens the plugin's Right-Hand Sidebar. It is captured during
// initialize (index.tsx) and injected via setTaskDeepLinkRhsOpener, mirroring
// the task_post_card pattern. Keeping it module-scoped avoids a circular import
// back into index.tsx.
let rhsOpener: () => void = () => {};
export function setTaskDeepLinkRhsOpener(opener: () => void) {
    rhsOpener = opener;
}

interface RouteParams {
    id: string;
}

export default function TaskDeepLink() {
    const dispatch = useDispatch();
    const history = useHistory();
    const t = useFormatMessage();
    const {id} = useParams<RouteParams>();

    useEffect(() => {
        if (id) {
            dispatch({type: ACTION_TYPES.SELECT_TASK, taskID: id});
            rhsOpener();
        }

        // The click path (capture-phase listener in index.tsx) already handles
        // the common case without navigation. This route is only reached when
        // the URL is pasted/opened directly, so always replace it with the home
        // page rather than guessing via history.length (which could goBack to an
        // unrelated external page in an already-used tab).
        history.replace('/');
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [id]);

    // Render a centered loading placeholder instead of null so the brief
    // moment between host navigation and goBack shows feedback (spinner) rather
    // than a blank white page — reduces the flash's perceived severity (design
    // D9, option B).
    return (
        <div
            style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                height: '100vh',
                color: 'var(--center-channel-color, #3f4354)',
            }}
        >
            {t('task.deep_link.opening')}
        </div>
    );
}
