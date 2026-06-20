// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for the plugin entry point (issue #27). Verifies that initialize
// registers every desktop integration the issue requires: channel header button,
// channel header icon (New Task #107), RHS component, root components
// (Kanban + New Task dialog), WebSocket handler, reducer, translations, and the
// post dropdown action. Uses a fake registry + store so no host app is needed.

// Mock react-redux so the NewTaskHeaderIcon component (which calls useDispatch
// and, via useFormatMessage, useSelector) can be exercised without a real
// <Provider>. The dispatch and the locale selector are captured per-test.
jest.mock('react-redux', () => ({
    useDispatch: () => mockDispatch,
    useSelector: (selector: (s: unknown) => unknown) => selector(mockState),
}));

// Mock i18n_utils so useFormatMessage returns a plain identity function and
// activeLocaleSelector returns a stable locale without touching the store shape.
jest.mock('i18n_utils', () => ({
    useFormatMessage: () => (id: string) => id,
    formatMessage: (id: string) => id,
    activeLocaleSelector: () => 'en',
}));

import Plugin, {NewTaskHeaderIcon, openNewTaskFromMessage, resolvePost, splitMessageForTask} from 'index';
import {ACTION_TYPES} from 'reducer';
import type {Store} from 'redux';

import type {Channel} from '@mattermost/types/channels';
import type {GlobalState} from '@mattermost/types/store';

// Per-test dispatch + state shared with the react-redux / i18n_utils mocks
// above. Reset at the top of each New Task icon test.
let mockDispatch: (a: {type: string}) => void;
let mockState: unknown;

// A minimal store whose dispatch records every action, used to assert the
// channel header button and post dropdown action dispatch the right thing.
// `state` is returned by getState (defaults to empty) so post-dropdown tests can
// seed the host post entities.
function fakeStore(state: unknown = {}) {
    const actions: Array<{type: string}> = [];
    const store = {
        dispatch: jest.fn((a: {type: string}) => {
            actions.push(a);
        }),
        getState: jest.fn(() => state),
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
        registerChannelHeaderIcon: jest.fn(),
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

        // Channel header button: ONE registration — "Tasks" (opens RHS).
        // registerChannelHeaderButtonAction renders in the ChannelHeaderPlug
        // slot, which the host also dispatches to MobileChannelHeaderButton.
        expect(registry.registerChannelHeaderButtonAction).toHaveBeenCalledTimes(1);
        const tasksArgs = registry.registerChannelHeaderButtonAction.mock.calls[0] as unknown[];
        expect(tasksArgs[2]).toBe('Tasks');
        expect(tasksArgs[3]).toBe('Mở danh sách task');

        // Channel header icon: ONE registration — "New Task" (#107).
        // registerChannelHeaderIcon renders in the ChannelHeaderIcon Pluggable
        // (header icon group), independently — never grouped into the "Call"
        // dropdown or with the "Tasks" button. Desktop only.
        expect(registry.registerChannelHeaderIcon).toHaveBeenCalledTimes(1);
        const iconArgs = registry.registerChannelHeaderIcon.mock.calls[0] as unknown[];
        expect(typeof iconArgs[0]).toBe('function'); // NewTaskHeaderIcon component

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

    test('NewTaskHeaderIcon dispatches OPEN_NEW_TASK_DIALOG with the current channel id on click (#107)', () => {
        const actions: Array<{type: string}> = [];
        mockDispatch = (a: {type: string}) => {
            actions.push(a);
        };
        mockState = {};

        // Invoke the component as a function (it is a function component) with
        // the channel prop the host's ChannelHeaderIcon Pluggable would pass.
        // We then pull the onClick handler off the rendered <button> and fire it.
        const element = NewTaskHeaderIcon({channel: {id: 'ch123'} as Channel}) as {
            props: {onClick: () => void};
        };
        element.props.onClick();

        expect(actions).toContainEqual({
            type: ACTION_TYPES.OPEN_NEW_TASK_DIALOG,
            channelID: 'ch123',
        });
    });

    test('NewTaskHeaderIcon is nil-safe when no channel is passed', () => {
        const actions: Array<{type: string}> = [];
        mockDispatch = (a: {type: string}) => {
            actions.push(a);
        };
        mockState = {};

        const element = NewTaskHeaderIcon({}) as {
            props: {onClick: () => void};
        };
        element.props.onClick();

        expect(actions).toContainEqual({
            type: ACTION_TYPES.OPEN_NEW_TASK_DIALOG,
            channelID: undefined,
        });
    });
});

describe('openNewTaskFromMessage', () => {
    // A host state shape with a post under entities.posts.posts, matching where
    // Mattermost stores posts.
    function stateWithPost(postId: string, message: string, channelID: string) {
        return {
            entities: {posts: {posts: {[postId]: {message, channel_id: channelID}}}},
        };
    }

    test('opens the New Task dialog with prefilled summary/description from the resolved post', () => {
        const state = stateWithPost('p1', 'Fix the bug\nDetails here\nMore detail', 'ch1');
        const {store, actions} = fakeStore(state);
        openNewTaskFromMessage(store, 'p1');
        expect(actions).toContainEqual({
            type: ACTION_TYPES.OPEN_NEW_TASK_DIALOG,
            prefillSummary: 'Fix the bug',
            prefillDescription: 'Details here\nMore detail',
            channelID: 'ch1',
        });
    });

    test('an empty message opens the dialog with blank fields', () => {
        const state = stateWithPost('p1', '', 'ch1');
        const {store, actions} = fakeStore(state);
        openNewTaskFromMessage(store, 'p1');
        expect(actions).toContainEqual({
            type: ACTION_TYPES.OPEN_NEW_TASK_DIALOG,
            prefillSummary: '',
            prefillDescription: '',
            channelID: 'ch1',
        });
    });

    test('an unknown postId opens the dialog blank', () => {
        const {store, actions} = fakeStore(stateWithPost('p1', 'msg', 'ch1'));
        openNewTaskFromMessage(store, 'nope');
        expect(actions).toContainEqual({
            type: ACTION_TYPES.OPEN_NEW_TASK_DIALOG,
            prefillSummary: '',
            prefillDescription: '',
            channelID: undefined,
        });
    });

    test('a missing postId opens the dialog blank', () => {
        const {store, actions} = fakeStore();
        openNewTaskFromMessage(store);
        expect(actions).toContainEqual({
            type: ACTION_TYPES.OPEN_NEW_TASK_DIALOG,
            prefillSummary: '',
            prefillDescription: '',
            channelID: undefined,
        });
    });
});

describe('resolvePost', () => {
    test('reads a post from state.entities.posts.posts', () => {
        const state = {entities: {posts: {posts: {p1: {message: 'hi', channel_id: 'c1'}}}}};
        expect(resolvePost(state, 'p1')).toEqual({message: 'hi', channel_id: 'c1'});
    });

    test('returns undefined for a missing post', () => {
        expect(resolvePost({entities: {posts: {posts: {}}}}, 'p1')).toBeUndefined();
    });

    test('returns undefined when the entities shape is absent', () => {
        expect(resolvePost({}, 'p1')).toBeUndefined();
        expect(resolvePost(null, 'p1')).toBeUndefined();
    });
});

describe('splitMessageForTask', () => {
    test('first line is the summary, rest is the description', () => {
        const out = splitMessageForTask('Buy milk\n2% organic\nAt the store');
        expect(out.summary).toBe('Buy milk');
        expect(out.description).toBe('2% organic\nAt the store');
    });

    test('a single line yields an empty description', () => {
        const out = splitMessageForTask('Just a summary');
        expect(out.summary).toBe('Just a summary');
        expect(out.description).toBe('');
    });

    test('an empty message yields empty fields', () => {
        const out = splitMessageForTask('   ');
        expect(out.summary).toBe('');
        expect(out.description).toBe('');
    });

    test('a long first line is truncated with an ellipsis', () => {
        const long = 'x'.repeat(200);
        const out = splitMessageForTask(long);
        expect(out.summary.length).toBe(120);
        expect(out.summary.endsWith('…')).toBe(true);
    });

    test('leading/trailing whitespace is trimmed', () => {
        const out = splitMessageForTask('\n  Summary  \n  Desc  \n');
        expect(out.summary).toBe('Summary');
        expect(out.description).toBe('Desc');
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
            msg: {data: {task_id?: string; task?: Record<string, unknown>; seq?: number}},
        ) => void;
        handler({data: {task_id: '1', seq: 7}});

        expect(actions).toContainEqual({type: ACTION_TYPES.DELETE_TASK, taskID: '1', seq: 7});
    });

    test('forwards seq on an upsert so the reducer can drop stale events', async () => {
        const {store, actions} = fakeStore();
        const registry = fakeRegistry();
        const plugin = new Plugin();
        await plugin.initialize(registry as never, store);

        const handler = registry.registerWebSocketEventHandler.mock.calls[0][1] as (
            msg: {data: {task_id?: string; task?: Record<string, unknown>; seq?: number}},
        ) => void;
        handler({data: {task_id: '1', task: {id: '1', summary: 'hi'}, seq: 42}});

        expect(actions).toContainEqual({
            type: ACTION_TYPES.UPSERT_TASK,
            task: {id: '1', summary: 'hi'},
            seq: 42,
        });
    });

    test('ignores an empty payload', async () => {
        const {store, actions} = fakeStore();
        const registry = fakeRegistry();
        const plugin = new Plugin();
        await plugin.initialize(registry as never, store);

        const handler = registry.registerWebSocketEventHandler.mock.calls[0][1] as (
            msg: {data: {task_id?: string; task?: Record<string, unknown>; seq?: number}},
        ) => void;
        handler({data: {}});

        // No task mutation actions should have been dispatched.
        expect(actions.find((a) => a.type === ACTION_TYPES.UPSERT_TASK || a.type === ACTION_TYPES.DELETE_TASK)).toBeUndefined();
    });
});
