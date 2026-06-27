// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Tests for the TaskDeepLink route component (change
// notification-overdue-and-context, design D9). On mount it must dispatch
// SELECT_TASK with the route's task id, open the RHS, and navigate the user back
// (or to '/' when there is no history).

// Mock react-redux so useDispatch returns a recording fn without a Provider.
const mockDispatch = jest.fn();
jest.mock('react-redux', () => ({
    useDispatch: () => mockDispatch,
}));

// Mock react-router-dom: useHistory/useParams return captured fns/values.
const mockGoBack = jest.fn();
const mockPush = jest.fn();
let mockParams: {id?: string} = {};
let mockHistoryLength = 2;
jest.mock('react-router-dom', () => ({
    useHistory: () => ({goBack: mockGoBack, push: mockPush, length: mockHistoryLength}),
    useParams: () => mockParams,
}));

import React from 'react';
import TestRenderer, {act} from 'react-test-renderer';
import {ACTION_TYPES} from 'reducer';

import TaskDeepLink, {setTaskDeepLinkRhsOpener} from 'components/task_deep_link';

describe('components/task_deep_link', () => {
    const rhsOpener = jest.fn();

    beforeEach(() => {
        jest.clearAllMocks();
        mockParams = {};
        mockHistoryLength = 2;
        setTaskDeepLinkRhsOpener(rhsOpener);
    });

    test('dispatches SELECT_TASK, opens RHS, and goes back on mount', async () => {
        mockParams = {id: '01HXYZTASK0001'};
        mockHistoryLength = 2;

        await act(async () => {
            TestRenderer.create(<TaskDeepLink/>);
        });

        expect(mockDispatch).toHaveBeenCalledWith({
            type: ACTION_TYPES.SELECT_TASK,
            taskID: '01HXYZTASK0001',
        });
        expect(rhsOpener).toHaveBeenCalledTimes(1);

        // Previous history exists → go back, not push.
        expect(mockGoBack).toHaveBeenCalledTimes(1);
        expect(mockPush).not.toHaveBeenCalled();
    });

    test('falls back to "/" when there is no previous history (pasted URL)', async () => {
        mockParams = {id: '01HXYZTASK0001'};
        mockHistoryLength = 1; // no entry before this one

        await act(async () => {
            TestRenderer.create(<TaskDeepLink/>);
        });

        expect(mockPush).toHaveBeenCalledWith('/');
        expect(mockGoBack).not.toHaveBeenCalled();
    });

    test('renders nothing (route exists only for side effects)', async () => {
        mockParams = {id: '01HXYZTASK0001'};
        let root: TestRenderer.ReactTestRenderer;
        await act(async () => {
            root = TestRenderer.create(<TaskDeepLink/>);
        });

        // The component returns null.
        expect(root!.toJSON()).toBeNull();
    });
});
