// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Webapp plugin entry point (issue #27). Wires the plugin into the Mattermost
// desktop UI through the official registry methods:
//   - channel header button → opens the RHS
//   - Right-Hand Sidebar → TaskSidebar (Quick List + Task Detail)
//   - root components → KanbanModal (board) and NewTaskDialog (create popup)
//   - WebSocket event handler → real-time task updates (#32 fills the body)
//   - reducer → plugin view state
//   - translations → en/vi (#33 swaps the bundle via getTranslationsForLocale)
//   - post dropdown action → "Tạo task" from a message (#16 handler)
//
// React/Redux/ReactRouter are webpack externals (webpack.config.js), supplied
// by the host app at runtime; they are never bundled.

import manifest from 'manifest';
import React from 'react';
import reducer, {ACTION_TYPES} from 'reducer';
import type {Store} from 'redux';

import type {WebSocketMessage} from '@mattermost/client';
import type {GlobalState} from '@mattermost/types/store';

import KanbanModal from 'components/kanban_modal/kanban_modal';
import NewTaskDialog from 'components/new_task_dialog/new_task_dialog';
import TaskSidebar from 'components/task_sidebar/task_sidebar';

import type {PluginRegistry} from 'types/mattermost-webapp';

import en from '../i18n/en.json';
import vi from '../i18n/vi.json';

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
        // The full handler (prefilling summary from the message, opening the
        // dialog) lands with #16; this wires the menu item so the integration is
        // registered and the desktop dropdown shows it. The registry passes the
        // source post to the action at runtime; the filter always shows the item.
        registry.registerPostDropdownMenuAction(
            'Tạo task',
            () => openNewTaskFromMessage(store),
            () => true,
        );
    }
}

// openNewTaskFromMessage is the post-dropdown handler shared with the server-side
// post action from #16. For now it opens the RHS so the user can use the New Task
// button; #16 will replace this with a proper dialog open + summary prefill from
// the source message. Declared module-level so it can be referenced by name in
// the registration and unit-tested independently.
export function openNewTaskFromMessage(store: Store<GlobalState>): void {
    // Placeholder until #16 lands the real dialog flow: open the RHS so the task
    // list is visible while the create-from-message flow is built out.
    store.dispatch({type: ACTION_TYPES.OPEN_RHS});
}

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
