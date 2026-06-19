// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for the plugin reducer (issue #27). Pins down the RHS open/close
// state, task selection, the normalized task cache, and the delete cascade that
// also clears a selected task — the behaviors the WebSocket handler (#32) and
// components depend on.

import reducer, {ACTION_TYPES} from 'reducer';
import type {TaskPartial} from 'reducer';

function task(id: string, extra: Partial<TaskPartial> = {}): TaskPartial {
    return {id, summary: 't', ...extra};
}

describe('reducer initial state', () => {
    test('starts with RHS closed, no selection, empty cache', () => {
        const state = reducer(undefined, {type: 'noop'});
        expect(state.rhsOpen).toBe(false);
        expect(state.selectedTaskID).toBe('');
        expect(state.selectedTask).toBeNull();
        expect(state.tasks).toEqual({});
    });
});

describe('RHS open/close', () => {
    test('OPEN_RHS opens the sidebar', () => {
        const state = reducer(undefined, {type: ACTION_TYPES.OPEN_RHS});
        expect(state.rhsOpen).toBe(true);
    });

    test('CLOSE_RHS closes an open sidebar', () => {
        const open = reducer(undefined, {type: ACTION_TYPES.OPEN_RHS});
        const closed = reducer(open, {type: ACTION_TYPES.CLOSE_RHS});
        expect(closed.rhsOpen).toBe(false);
    });
});

describe('task selection', () => {
    test('SELECT_TASK records the id and optional task', () => {
        const t = task('1');
        const state = reducer(undefined, {type: ACTION_TYPES.SELECT_TASK, taskID: '1', task: t});
        expect(state.selectedTaskID).toBe('1');
        expect(state.selectedTask).toBe(t);
    });

    test('SET_SELECTED_TASK hydrates the detail view', () => {
        const t = task('1');
        const state = reducer(undefined, {type: ACTION_TYPES.SET_SELECTED_TASK, task: t});
        expect(state.selectedTask).toBe(t);
    });

    test('SET_SELECTED_TASK with no task clears the detail', () => {
        const before = reducer(undefined, {type: ACTION_TYPES.SET_SELECTED_TASK, task: task('1')});
        const state = reducer(before, {type: ACTION_TYPES.SET_SELECTED_TASK});
        expect(state.selectedTask).toBeNull();
    });
});

describe('task cache upsert', () => {
    test('UPSERT_TASK adds a new task to the cache', () => {
        const state = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1')});
        expect(state.tasks['1']).toBeDefined();
    });

    test('UPSERT_TASK replaces an existing task', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'a'})});
        const s2 = reducer(s1, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'b'})});
        expect((s2.tasks['1'] as {summary: string}).summary).toBe('b');
    });

    test('UPSERT_TASK refreshes the selected task when it is the one updated', () => {
        const selected = reducer(undefined, {type: ACTION_TYPES.SELECT_TASK, taskID: '1', task: task('1', {summary: 'old'})});
        const state = reducer(selected, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'new'})});
        expect((state.selectedTask as {summary: string}).summary).toBe('new');
    });

    test('UPSERT_TASK does not touch the selected task when updating another', () => {
        const selected = reducer(undefined, {type: ACTION_TYPES.SELECT_TASK, taskID: '1', task: task('1', {summary: 'mine'})});
        const state = reducer(selected, {type: ACTION_TYPES.UPSERT_TASK, task: task('2', {summary: 'other'})});
        expect((state.selectedTask as {summary: string}).summary).toBe('mine');
    });

    test('UPSERT_TASK without a task is a no-op', () => {
        const before = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1')});
        const state = reducer(before, {type: ACTION_TYPES.UPSERT_TASK});
        expect(state).toBe(before);
    });
});

describe('task delete cascade', () => {
    test('DELETE_TASK removes the task from the cache', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1')});
        const state = reducer(s1, {type: ACTION_TYPES.DELETE_TASK, taskID: '1'});
        expect(state.tasks['1']).toBeUndefined();
    });

    test('DELETE_TASK clears the selection when deleting the selected task', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.SELECT_TASK, taskID: '1', task: task('1')});
        const state = reducer(s1, {type: ACTION_TYPES.DELETE_TASK, taskID: '1'});
        expect(state.selectedTaskID).toBe('');
        expect(state.selectedTask).toBeNull();
    });

    test('DELETE_TASK leaves an unrelated selection intact', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.SELECT_TASK, taskID: '1', task: task('1')});
        const state = reducer(s1, {type: ACTION_TYPES.DELETE_TASK, taskID: '2'});
        expect(state.selectedTaskID).toBe('1');
    });

    test('DELETE_TASK without a taskID is a no-op', () => {
        const before = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1')});
        const state = reducer(before, {type: ACTION_TYPES.DELETE_TASK});
        expect(state).toBe(before);
    });

    test('DELETE_TASK of a missing id is a no-op', () => {
        const before = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1')});
        const state = reducer(before, {type: ACTION_TYPES.DELETE_TASK, taskID: 'nope'});
        expect(state).toBe(before);
    });
});

describe('reducer returns prior state for unknown actions', () => {
    test('unknown action is a no-op', () => {
        const before = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1')});
        const state = reducer(before, {type: 'something_else'});
        expect(state).toBe(before);
    });
});
