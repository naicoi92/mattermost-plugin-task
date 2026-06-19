// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

/**
 * @jest-environment jsdom
 */

// Phase 3 integration test (issue #34). Unlike the per-module unit tests, this
// file wires the REAL reducer together with the WebSocket handler registered by
// Plugin.initialize, proving the end-to-end chain a manual E2E would exercise:
//   server task_updated event → registered WS handler dispatches → reducer cache
// reflects the change (and drops stale seq).
//
// It uses a real Redux store (so the reducer actually runs) and a fake registry
// (so we can invoke the registered handler with crafted payloads). No host app.

import Plugin from 'index';
import reducer, {ACTION_TYPES} from 'reducer';
import {combineReducers, createStore} from 'redux';
import type {Store} from 'redux';

import type {GlobalState} from '@mattermost/types/store';

// The plugin reducer is mounted under state['plugins-<pluginId>'] by the host;
// replicate that mount here so the real reducer runs against dispatched actions.
const PLUGIN_KEY = 'plugins-com.mattermost.plugin-task';

function buildStore(): Store<GlobalState> {
    const combined = combineReducers({

        // The host mounts plugin reducers under a 'plugins' namespace; we mount
        // ours directly under the plugin key the reducer expects.
        [PLUGIN_KEY]: reducer,
    });
    return createStore(combined) as Store<GlobalState>;
}

// A recording registry capturing only the methods the integration path touches.
function recordingRegistry() {
    return {
        registerRightHandSidebarComponent: jest.fn(() => ({
            id: 'rhs',
            showRHSPlugin: {type: 'SHOW_RHS'},
            hideRHSPlugin: {type: 'HIDE_RHS'},
            toggleRHSPlugin: {type: 'TOGGLE_RHS'},
        })),
        registerChannelHeaderButtonAction: jest.fn(),
        registerRootComponent: jest.fn(),
        registerWebSocketEventHandler: jest.fn(),
        registerReducer: jest.fn(),
        registerTranslations: jest.fn(),
        registerPostDropdownMenuAction: jest.fn(),
    };
}

describe('Phase 3 integration: WebSocket → reducer cache', () => {
    test('a task_updated upsert lands in the reducer cache', async () => {
        const store = buildStore();
        const registry = recordingRegistry();
        const plugin = new Plugin();
        await plugin.initialize(registry as never, store);

        // The handler the plugin registered for "task_updated".
        const handler = registry.registerWebSocketEventHandler.mock.calls[0][1] as (
            msg: {data: Record<string, unknown>},
        ) => void;

        // Simulate a server task_updated event with a fresh task.
        handler({data: {task_id: 't1', seq: 10, task: {id: 't1', summary: 'buy milk', status: 'todo'}}});

        const slice = (store.getState() as never as {[k: string]: {tasks: Record<string, unknown>}})[PLUGIN_KEY];
        expect(slice.tasks.t1).toBeDefined();
        expect((slice.tasks.t1 as {summary: string}).summary).toBe('buy milk');
    });

    test('a stale task_updated (older seq) does NOT overwrite the cache', async () => {
        const store = buildStore();
        const registry = recordingRegistry();
        await new Plugin().initialize(registry as never, store);
        const handler = registry.registerWebSocketEventHandler.mock.calls[0][1] as (
            msg: {data: Record<string, unknown>},
        ) => void;

        // Newer event first.
        handler({data: {task_id: 't1', seq: 50, task: {id: 't1', summary: 'fresh'}}});

        // Older event arrives late — must be dropped.
        handler({data: {task_id: 't1', seq: 20, task: {id: 't1', summary: 'stale'}}});

        const slice = (store.getState() as never as {[k: string]: {tasks: Record<string, unknown>}})[PLUGIN_KEY];
        expect((slice.tasks.t1 as {summary: string}).summary).toBe('fresh');
    });

    test('a delete event removes the task from the cache', async () => {
        const store = buildStore();
        const registry = recordingRegistry();
        await new Plugin().initialize(registry as never, store);
        const handler = registry.registerWebSocketEventHandler.mock.calls[0][1] as (
            msg: {data: Record<string, unknown>},
        ) => void;

        // Create then delete.
        handler({data: {task_id: 't1', seq: 1, task: {id: 't1', summary: 'x'}}});
        handler({data: {task_id: 't1', seq: 2}}); // delete (task_id, no task body)

        const slice = (store.getState() as never as {[k: string]: {tasks: Record<string, unknown>}})[PLUGIN_KEY];
        expect(slice.tasks.t1).toBeUndefined();
    });

    test('selecting a task then upserting it refreshes the selection', async () => {
        const store = buildStore();
        const registry = recordingRegistry();
        await new Plugin().initialize(registry as never, store);
        const handler = registry.registerWebSocketEventHandler.mock.calls[0][1] as (
            msg: {data: Record<string, unknown>},
        ) => void;

        // Select the task (as the Quick List does on click).
        store.dispatch({type: ACTION_TYPES.SELECT_TASK, taskID: 't1', task: {id: 't1', summary: 'old'}});

        // A WS upsert for the same task updates the selected detail.
        handler({data: {task_id: 't1', seq: 5, task: {id: 't1', summary: 'new', status: 'done'}}});

        const slice = (store.getState() as never as {
            [k: string]: {selectedTaskID: string; selectedTask: {summary: string} | null};
        })[PLUGIN_KEY];
        expect(slice.selectedTaskID).toBe('t1');
        expect(slice.selectedTask?.summary).toBe('new');
    });
});

// (No local Store alias — the redux Store type is imported directly above.)
