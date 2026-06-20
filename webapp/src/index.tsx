// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Webapp plugin entry point (issue #27). Wires the plugin into the Mattermost
// desktop UI through the official registry methods:
//   - channel header button → opens the RHS
//   - channel header icon ("New Task") → opens the New Task dialog (#107)
//   - Right-Hand Sidebar → TaskSidebar (Quick List + Task Detail)
//   - root components → KanbanModal (board) and NewTaskDialog (create popup)
//   - WebSocket event handler → real-time task updates (#32 fills the body)
//   - reducer → plugin view state
//   - translations → en/vi (#33 swaps the bundle via getTranslationsForLocale)
//   - post dropdown action → "Tạo task" from a message (#16 handler)
//
// React/Redux/ReactRouter are webpack externals (webpack.config.js), supplied
// by the host app at runtime; they are never bundled.

import {useFormatMessage} from 'i18n_utils';
import manifest from 'manifest';
import React from 'react';
import {useDispatch} from 'react-redux';
import reducer, {ACTION_TYPES} from 'reducer';
import type {Store} from 'redux';

import type {WebSocketMessage} from '@mattermost/client';
import type {Channel} from '@mattermost/types/channels';
import type {GlobalState} from '@mattermost/types/store';

import KanbanModal from 'components/kanban_modal/kanban_modal';
import NewTaskDialog from 'components/new_task_dialog/connected_new_task_dialog';
import TaskSidebar from 'components/task_sidebar/task_sidebar';

import type {PluginRegistry} from 'types/mattermost-webapp';

import en from '../i18n/en.json';
import vi from '../i18n/vi.json';

// Plugin-wide stylesheet (#93). Imported once so webpack bundles it into
// main.js alongside the JS. Without it the RHS, Quick List, New Task dialog
// and Task Detail panel render with no styling at all.
import './styles/index.scss';

// getTranslationsForLocale returns the JSON bundle for the requested locale, or
// the English bundle as a safe fallback. Used by registerTranslations (#33);
// the same files are the single source of truth copied from assets/i18n/ by the
// Makefile (i18n-copy target).
function getTranslationsForLocale(locale: string): Record<string, string> {
    switch (locale) {
    case 'vi':
        return vi as Record<string, string>;
    default:
        // English is the fallback for any unrecognized locale (including the
        // default server locale), so an unknown locale never shows raw keys.
        return en as Record<string, string>;
    }
}

// NewTaskHeaderIconProps is the prop shape the host's ChannelHeaderIcon
// Pluggable passes to the registered component: the current channel and
// membership, plus theme/webSocketClient injected by Pluggable. We declare
// only what we read.
interface NewTaskHeaderIconProps {
    channel?: Channel;
}

// NewTaskHeaderIcon is the "New Task" icon rendered in the channel header icon
// group (issue #107). Registered via registerChannelHeaderIcon, so it renders
// INDEPENDENTLY in the header icon group (alongside mute/members/pinned),
// never collapsed into the "Call" dropdown or grouped with the "Tasks" button
// (both are different host slots — see channel_header.tsx). Desktop only: the
// host does not render ChannelHeaderIcon on mobile (mobile uses the "..."
// menu, where New Task is also listed via MobileChannelHeaderButton).
//
// On click it dispatches OPEN_NEW_TASK_DIALOG with the current channel id, so
// NewTaskDialog opens pre-scoped (scope "Channel" radio enabled). The label
// and tooltip are locale-aware via useFormatMessage.
export function NewTaskHeaderIcon({channel}: NewTaskHeaderIconProps): JSX.Element {
    const dispatch = useDispatch();
    const t = useFormatMessage();
    const onClick = () => {
        dispatch({
            type: ACTION_TYPES.OPEN_NEW_TASK_DIALOG,
            channelID: channel?.id,
        });
    };
    return (
        <button
            type='button'
            className='style--none task-header-new-task-btn'
            onClick={onClick}
            aria-label={t('webapp.task.tooltip.new_task')}
            title={t('webapp.task.tooltip.new_task')}
        >
            <i className='icon fa fa-plus'/>
            <span className='task-header-new-task-btn__label'>{t('webapp.task.new')}</span>
        </button>
    );
}

export default class Plugin {
    // registry is captured in initialize so the post-dropdown action and other
    // imperative callbacks can dispatch without re-deriving it.
    private store?: Store<GlobalState>;

    // eslint-disable-next-line @typescript-eslint/no-unused-vars, @typescript-eslint/no-empty-function
    public async initialize(registry: PluginRegistry, store: Store<GlobalState>) {
        this.store = store;

        // Register the RHS. The returned toggle action opens/closes the sidebar;
        // the channel header button below dispatches it.
        const {showRHSPlugin} = registry.registerRightHandSidebarComponent(TaskSidebar, 'Tasks');

        // Channel header button: opens the RHS. The icon is a simple checkmark
        // glyph; a richer SVG can replace it without changing the contract.
        registry.registerChannelHeaderButtonAction(
            <i className='icon fa fa-check-square'/>,
            () => store.dispatch(showRHSPlugin),
            'Tasks',
            'Mở danh sách task',
        );

        // "New Task" channel header icon (issue #107). Registered via
        // registerChannelHeaderIcon, which renders the component in the header
        // icon group (channel_header.tsx ChannelHeaderIcon Pluggable) —
        // INDEPENDENTLY, never grouped with the "Call" dropdown (slot
        // CallButton) nor with the "Tasks" button (slot ChannelHeaderPlug).
        // Each registered icon renders side-by-side (the host maps over them).
        // Desktop only: the host does not render ChannelHeaderIcon on mobile;
        // on mobile New Task is reachable via the "..." menu instead.
        registry.registerChannelHeaderIcon(NewTaskHeaderIcon);

        // Root components: the Kanban board and the New Task popup. Mounted once
        // and toggled via Redux/props by their consumers.
        registry.registerRootComponent(KanbanModal);
        registry.registerRootComponent(NewTaskDialog);

        // Real-time updates: the server publishes "task_updated" on every
        // create/update/delete/status/assignee/due/comment/reminder change (#32).
        // The handler forwards the server's seq so the reducer can drop stale
        // out-of-order events; a delete carries task_id without a task body.
        registry.registerWebSocketEventHandler('task_updated', (msg: WebSocketMessage) => {
            const data = (msg.data ?? {}) as {
                task_id?: string;
                task?: Record<string, unknown>;
                seq?: number;
            };
            const seq = typeof data.seq === 'number' ? data.seq : undefined;
            if (data.task_id && !data.task) {
                store.dispatch({type: ACTION_TYPES.DELETE_TASK, taskID: data.task_id, seq});
                return;
            }
            if (data.task) {
                store.dispatch({type: ACTION_TYPES.UPSERT_TASK, task: data.task, seq});
            }
        });

        // Plugin view state: RHS open/close, selected task, normalized cache.
        registry.registerReducer(reducer);

        // i18n: en/vi bundles. Changing the user's Mattermost locale re-renders
        // plugin strings via this lookup (#33).
        registry.registerTranslations(getTranslationsForLocale);

        // Post dropdown action: "Tạo task" creates a task from a message (#16).
        // At runtime the registry passes the source post id to the action; we
        // resolve the post's message from the host Redux store, split it into
        // summary (first line) and description (rest), then open the New Task
        // dialog pre-filled. The dialog submits through POST /tasks like the
        // normal New Task flow.
        registry.registerPostDropdownMenuAction(
            'Tạo task',
            (postId?: string) => {
                openNewTaskFromMessage(store, postId);
            },
            () => true,
        );
    }
}

// openNewTaskFromMessage is the post-dropdown handler (#16). It resolves the
// source post's message and channel from the host Redux store, splits the
// message into a summary (first non-empty line, truncated) and a description
// (the remaining lines), then dispatches OPEN_NEW_TASK_DIALOG so the
// NewTaskDialog root component opens pre-filled. Declared module-level so it can
// be referenced by name in the registration and unit-tested independently.
export function openNewTaskFromMessage(store: Store<GlobalState>, postId?: string): void {
    const post = postId ? resolvePost(store.getState(), postId) : undefined;
    const message = post?.message ?? '';
    const {summary, description} = splitMessageForTask(message);
    store.dispatch({
        type: ACTION_TYPES.OPEN_NEW_TASK_DIALOG,
        prefillSummary: summary,
        prefillDescription: description,
        channelID: post?.channel_id,
    });
}

// PostLike is the minimal shape we read from the host post entities.
interface PostLike {
    message?: string;
    channel_id?: string;
}

// resolvePost reads a post by id from the host Redux store. Mattermost stores
// posts at state.entities.posts.posts[postId]; we access it defensively so a
// missing or differently-shaped store yields undefined rather than throwing.
export function resolvePost(state: unknown, postId: string): PostLike | undefined {
    const entities = (state as {entities?: {posts?: {posts?: Record<string, PostLike>}}})?.entities;
    return entities?.posts?.posts?.[postId];
}

// splitMessageForTask derives a task summary and description from a message body.
// The first non-empty line becomes the summary (truncated to summaryLimit chars
// with an ellipsis); any remaining lines form the description. An empty message
// yields empty fields so the dialog opens blank.
export function splitMessageForTask(message: string): {summary: string; description: string} {
    const trimmed = message.trim();
    if (trimmed === '') {
        return {summary: '', description: ''};
    }
    const lines = trimmed.split('\n');
    const firstLine = lines[0].trim();
    const summary = firstLine.length > summaryLimit ?
        firstLine.slice(0, summaryLimit - 1) + '…' :
        firstLine;
    const description = lines.slice(1).join('\n').trim();
    return {summary, description};
}

// summaryLimit keeps the prefilled summary a reasonable single-line length.
const summaryLimit = 120;

declare global {
    interface Window {
        registerPlugin(pluginId: string, plugin: Plugin): void;
    }
}

// Register the plugin with the host. Guarded so importing the module in a non-
// browser context (e.g. Jest's default node environment) doesn't throw on the
// missing global; in the browser the host always provides window.registerPlugin.
if (typeof window !== 'undefined') {
    window.registerPlugin(manifest.id, new Plugin());
}
