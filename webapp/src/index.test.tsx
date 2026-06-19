// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for the plugin entry point (issue #27). Verifies that initialize
// registers every desktop integration the issue requires: channel header button,
// RHS component, root components (Kanban + New Task dialog), WebSocket handler,
// reducer, translations, and the post dropdown action. Uses a fake registry +
// store so no host app is needed.

import Plugin, {openNewTaskFromMessage} from 'index';
import {ACTION_TYPES} from 'reducer';
import type {Store} from 'redux';

import type {GlobalState} from '@mattermost/types/store';

// A minimal store whose dispatch records every action, used to assert the
// channel header button and post dropdown action dispatch the right thing.
function fakeStore() {
    const actions: Array<{type: string}> = [];
    const store = {
        dispatch: jest.fn((a: {type: string}) => {
            actions.push(a);
        }),
        getState: jest.fn(),
        subscribe: jest.fn(),
        replaceReducer: jest.fn(),
        [Symbol.observable]: jest.fn(),
    } as unknown as Store<GlobalState>;
    return {store, actions};
}

// A recording registry: each register* method is a jest.fn so the test can assert
// it was called with the expected component/event/etc.
function fakeRegistry() {
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

describe('Plugin.initialize registrations (#27)', () => {
    test('registers every required desktop integration', async () => {
        const {store} = fakeStore();
        const registry = fakeRegistry();
        const plugin = new Plugin();
        await plugin.initialize(registry as never, store);

        // RHS component with a title.
        expect(registry.registerRightHandSidebarComponent).toHaveBeenCalledTimes(1);
        const rhsArgs = registry.registerRightHandSidebarComponent.mock.calls[0] as unknown[];
        expect(typeof rhsArgs[0]).toBe('function'); // TaskSidebar component
        expect(rhsArgs[1]).toBe('Tasks');

        // Channel header button: icon, action, dropdown text, tooltip.
        expect(registry.registerChannelHeaderButtonAction).toHaveBeenCalledTimes(1);
        const headerArgs = registry.registerChannelHeaderButtonAction.mock.calls[0] as unknown[];
        expect(headerArgs[2]).toBe('Tasks');
        expect(headerArgs[3]).toBe('Mở danh sách task');

        // Two root components: Kanban modal + New Task dialog.
        expect(registry.registerRootComponent).toHaveBeenCalledTimes(2);

        // WebSocket handler for "task_updated".
        expect(registry.registerWebSocketEventHandler).toHaveBeenCalledTimes(1);
        const wsArgs = registry.registerWebSocketEventHandler.mock.calls[0];
        expect(wsArgs[0]).toBe('task_updated');
        expect(typeof wsArgs[1]).toBe('function');

        // Reducer.
        expect(registry.registerReducer).toHaveBeenCalledTimes(1);
        expect(typeof registry.registerReducer.mock.calls[0][0]).toBe('function');

        // Translations.
        expect(registry.registerTranslations).toHaveBeenCalledTimes(1);
        expect(typeof registry.registerTranslations.mock.calls[0][0]).toBe('function');

        // Post dropdown action "Tạo task".
        expect(registry.registerPostDropdownMenuAction).toHaveBeenCalledTimes(1);
        const dropArgs = registry.registerPostDropdownMenuAction.mock.calls[0] as unknown[];
        expect(dropArgs[0]).toBe('Tạo task');
        expect(typeof dropArgs[1]).toBe('function'); // action
        expect(typeof dropArgs[2]).toBe('function'); // filter
    });

    test('channel header button action dispatches showRHSPlugin', async () => {
        const {store, actions} = fakeStore();
        const registry = fakeRegistry();
        const plugin = new Plugin();
        await plugin.initialize(registry as never, store);

        const headerArgs = registry.registerChannelHeaderButtonAction.mock.calls[0] as unknown[];
        const buttonAction = headerArgs[1] as () => void;
        buttonAction();

        // showRHSPlugin is the object the RHS registration returned.
        expect(actions).toContainEqual({type: 'SHOW_RHS'});
    });
});

describe('openNewTaskFromMessage', () => {
    test('opens the RHS', () => {
        const {store, actions} = fakeStore();
        openNewTaskFromMessage(store);
        expect(actions).toContainEqual({type: ACTION_TYPES.OPEN_RHS});
    });
});

describe('translations locale lookup', () => {
    test('registerTranslations returns a bundle for vi and en, fallback for unknown', async () => {
        const {store} = fakeStore();
        const registry = fakeRegistry();
        const plugin = new Plugin();
        await plugin.initialize(registry as never, store);

        const getTranslations = registry.registerTranslations.mock.calls[0][0] as (
            locale: string,
        ) => Record<string, string>;

        // Jest's i18n moduleNameMapper returns the same mock object for every
        // en/vi import, so both 'vi' and 'en' resolve to that mock. The behavior
        // we assert here is the lookup contract: known locales return a bundle
        // object, and an unknown locale returns the (English-fallback) bundle
        // rather than throwing or returning undefined.
        const vi = getTranslations('vi');
        const en = getTranslations('en');
        const fallback = getTranslations('fr');

        expect(vi).toEqual(expect.any(Object));
        expect(en).toEqual(expect.any(Object));

        // Unknown locale must fall back to the English bundle (same reference),
        // never undefined or a raw-key leak.
        expect(fallback).toBe(en);
    });
});

describe('WebSocket task_updated handler', () => {
    test('upserts a task into the cache', async () => {
        const {store, actions} = fakeStore();
        const registry = fakeRegistry();
        const plugin = new Plugin();
        await plugin.initialize(registry as never, store);

        const handler = registry.registerWebSocketEventHandler.mock.calls[0][1] as (
            msg: {data: {task_id?: string; task?: Record<string, unknown>}},
        ) => void;
        handler({data: {task_id: '1', task: {id: '1', summary: 'hi'}}});

        expect(actions).toContainEqual({
            type: ACTION_TYPES.UPSERT_TASK,
            task: {id: '1', summary: 'hi'},
        });
    });

    test('deletes a task when the payload carries only an id', async () => {
        const {store, actions} = fakeStore();
        const registry = fakeRegistry();
        const plugin = new Plugin();
        await plugin.initialize(registry as never, store);

        const handler = registry.registerWebSocketEventHandler.mock.calls[0][1] as (
            msg: {data: {task_id?: string; task?: Record<string, unknown>}},
        ) => void;
        handler({data: {task_id: '1'}});

        expect(actions).toContainEqual({type: ACTION_TYPES.DELETE_TASK, taskID: '1'});
    });

    test('ignores an empty payload', async () => {
        const {store, actions} = fakeStore();
        const registry = fakeRegistry();
        const plugin = new Plugin();
        await plugin.initialize(registry as never, store);

        const handler = registry.registerWebSocketEventHandler.mock.calls[0][1] as (
            msg: {data: {task_id?: string; task?: Record<string, unknown>}},
        ) => void;
        handler({data: {}});

        // No task mutation actions should have been dispatched.
        expect(actions.find((a) => a.type === ACTION_TYPES.UPSERT_TASK || a.type === ACTION_TYPES.DELETE_TASK)).toBeUndefined();
    });
});
