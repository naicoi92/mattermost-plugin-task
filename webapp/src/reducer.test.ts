// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for the plugin reducer (issue #27). Pins down the RHS open/close
// state, task selection, the normalized task cache, and the delete cascade that
// also clears a selected task — the behaviors the WebSocket handler (#32) and
// components depend on.

import reducer, {ACTION_TYPES} from 'reducer';
import type {TaskPartial} from 'reducer';

// summaryOf reads the summary field off a TaskPartial (or null) for assertions,
// avoiding a direct cast that the index signature makes type-unsafe. The cast
// through `unknown` is intentional: tests only ever read fields they set.
function summaryOf(t: TaskPartial | null): unknown {
    return (t as unknown as {summary?: unknown})?.summary;
}

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
        expect(summaryOf(s2.tasks['1'])).toBe('b');
    });

    test('UPSERT_TASK refreshes the selected task when it is the one updated', () => {
        const selected = reducer(undefined, {type: ACTION_TYPES.SELECT_TASK, taskID: '1', task: task('1', {summary: 'old'})});
        const state = reducer(selected, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'new'})});
        expect(summaryOf(state.selectedTask)).toBe('new');
    });

    test('UPSERT_TASK does not touch the selected task when updating another', () => {
        const selected = reducer(undefined, {type: ACTION_TYPES.SELECT_TASK, taskID: '1', task: task('1', {summary: 'mine'})});
        const state = reducer(selected, {type: ACTION_TYPES.UPSERT_TASK, task: task('2', {summary: 'other'})});
        expect(summaryOf(state.selectedTask)).toBe('mine');
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

describe('stale-event drop (seq/updated_at, #32)', () => {
    test('UPSERT_TASK with an older seq is dropped', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'new'}), seq: 100});
        const state = reducer(s1, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'stale'}), seq: 50});

        // The older seq must not overwrite the newer state.
        expect(summaryOf(state.tasks['1'])).toBe('new');
        expect(state.lastSeq['1']).toBe(100);
    });

    test('UPSERT_TASK with an equal seq is dropped (only strictly newer applies)', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'a'}), seq: 10});
        const state = reducer(s1, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'b'}), seq: 10});
        expect(summaryOf(state.tasks['1'])).toBe('a');
    });

    test('UPSERT_TASK with a newer seq is applied', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'old'}), seq: 10});
        const state = reducer(s1, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'fresh'}), seq: 20});
        expect(summaryOf(state.tasks['1'])).toBe('fresh');
        expect(state.lastSeq['1']).toBe(20);
    });

    test('UPSERT_TASK without seq always applies (local optimistic mutation)', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'a'}), seq: 10});
        const state = reducer(s1, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'local'})});
        expect(summaryOf(state.tasks['1'])).toBe('local');
    });

    test('DELETE_TASK with an older seq does not evict a newer task', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'alive'}), seq: 100});
        const state = reducer(s1, {type: ACTION_TYPES.DELETE_TASK, taskID: '1', seq: 50});
        expect(state.tasks['1']).toBeDefined();
        expect(summaryOf(state.tasks['1'])).toBe('alive');
    });

    test('DELETE_TASK with a newer-or-equal seq evicts', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1'), seq: 10});
        const state = reducer(s1, {type: ACTION_TYPES.DELETE_TASK, taskID: '1', seq: 10});
        expect(state.tasks['1']).toBeUndefined();
        expect(state.lastSeq['1']).toBeUndefined();
    });

    test('seq is tracked per task independently', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.UPSERT_TASK, task: task('1'), seq: 5});
        const s2 = reducer(s1, {type: ACTION_TYPES.UPSERT_TASK, task: task('2'), seq: 100});

        // task 2's high seq must not gate task 1.
        const state = reducer(s2, {type: ACTION_TYPES.UPSERT_TASK, task: task('1', {summary: 'upd'}), seq: 6});
        expect(summaryOf(state.tasks['1'])).toBe('upd');
    });
});

describe('COMMENT_REV_BUMP (Task 6 — comment refetch signal)', () => {
    test('bumps commentRev for the task on a fresh seq', () => {
        const state = reducer(undefined, {type: ACTION_TYPES.COMMENT_REV_BUMP, taskID: 'T', seq: 5});
        expect(state.commentRev.T).toBe(5);
    });

    test('a stale seq (<= current) is ignored — commentRev unchanged', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.COMMENT_REV_BUMP, taskID: 'T', seq: 5});
        const stale = reducer(s1, {type: ACTION_TYPES.COMMENT_REV_BUMP, taskID: 'T', seq: 5});
        expect(stale.commentRev.T).toBe(5);
        const older = reducer(s1, {type: ACTION_TYPES.COMMENT_REV_BUMP, taskID: 'T', seq: 4});
        expect(older.commentRev.T).toBe(5);
    });

    test('a newer seq bumps commentRev', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.COMMENT_REV_BUMP, taskID: 'T', seq: 5});
        const state = reducer(s1, {type: ACTION_TYPES.COMMENT_REV_BUMP, taskID: 'T', seq: 7});
        expect(state.commentRev.T).toBe(7);
    });

    test('commentRev is tracked per task independently', () => {
        const s1 = reducer(undefined, {type: ACTION_TYPES.COMMENT_REV_BUMP, taskID: 'A', seq: 5});
        const state = reducer(s1, {type: ACTION_TYPES.COMMENT_REV_BUMP, taskID: 'B', seq: 100});
        expect(state.commentRev.A).toBe(5);
        expect(state.commentRev.B).toBe(100);
    });

    test('a seq-less (local optimistic) bump advances commentRev from a real default, not -Infinity', () => {
        // Regression: the seed was -Infinity, so seq-less bumps computed
        // -Infinity + 1 = -Infinity and never advanced.
        const s1 = reducer(undefined, {type: ACTION_TYPES.COMMENT_REV_BUMP, taskID: 'T'});
        expect(s1.commentRev.T).toBe(1);
        const s2 = reducer(s1, {type: ACTION_TYPES.COMMENT_REV_BUMP, taskID: 'T'});
        expect(s2.commentRev.T).toBe(2);
    });
});
