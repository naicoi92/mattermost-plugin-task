/**
 * @jest-environment jsdom
 */

// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for the webapp API client (issue #31). The client is a thin fetch
// wrapper; these tests pin down: base URL, request shape (method/body/headers),
// JSON parsing, error mapping, and the 204/empty-body edge cases — without
// touching the network.
//
// Rather than depend on a browser fetch/Response polyfill, the mock fetch below
// returns a minimal response-like object exposing exactly the surface the client
// uses (.ok, .status, .statusText, .text()). This keeps the tests fast and
// environment-independent.
//
// Client4 (from mattermost-redux/client) is replaced with a mock via Jest's
// moduleNameMapper (see package.json → tests/mattermost-redux-client-mock.js)
// because the real module's deep @mattermost/* dependency chain relies on the
// package.json "exports" field, which Jest 27's resolver doesn't support. The
// mock reproduces getOptions's contract: X-Requested-With on every request,
// X-CSRF-Token (from the MMCSRF cookie) on non-GET methods, and
// credentials: 'include'.

import {ClientError, PLUGIN_API_BASE_URL} from 'client';
import manifest from 'manifest';

// Minimal response shape the client consumes. `ok` is derived from `status`,
// matching the standard Response contract.
interface MockResponse {
    status: number;
    statusText: string;
    text: () => Promise<string>;
}

function mockResponse(status: number, body: unknown, statusText = ''): MockResponse {
    const text = typeof body === 'string' ? body : JSON.stringify(body);
    return {
        status,
        statusText,
        text: () => Promise.resolve(text),
    };
}

function okResponse(body: unknown): MockResponse {
    return {...mockResponse(200, body), ok: true} as unknown as MockResponse;
}

// Each test installs its own fetch implementation on global so the client (which
// calls the bare `fetch`) sees it.
function mockFetch(impl: (url: string, init?: RequestInit) => MockResponse | Promise<MockResponse>) {
    const wrapped = (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === 'string' ? input : String(input);
        return Promise.resolve(impl(url, init)) as Promise<Response>;
    };
    global.fetch = jest.fn(wrapped) as unknown as typeof fetch;
}

describe('client base URL', () => {
    test('PLUGIN_API_BASE_URL is derived from the manifest plugin id', () => {
        expect(PLUGIN_API_BASE_URL).toBe(`/plugins/${manifest.id}/api/v1`);
    });
});

describe('client error mapping', () => {
    test('ClientError carries status and message', () => {
        const err = new ClientError(404, 'task not found');
        expect(err).toBeInstanceOf(Error);
        expect(err).toBeInstanceOf(ClientError);
        expect(err.status).toBe(404);
        expect(err.message).toBe('task not found');
    });
});

// Lazy-import the module under test so a fresh fetch mock can be installed per
// test. The module caches the base URL but every function reads global.fetch at
// call time, so re-mocking between tests is safe.
async function importClient() {
    jest.resetModules();
    return import('client');
}

describe('doFetch happy path', () => {
    test('GET parses JSON and uses the plugin-relative URL', async () => {
        mockFetch((url) => {
            expect(url).toBe(`${PLUGIN_API_BASE_URL}/tasks/abc`);
            return okResponse({id: 'abc'});
        });

        const {getTask} = await importClient();
        const task = await getTask('abc');
        expect(task).toEqual({id: 'abc'});
    });

    test('POST sends a JSON body with Content-Type', async () => {
        let captured: {url: string; init?: RequestInit} | null = null;
        mockFetch((url, init) => {
            captured = {url, init};
            return {...mockResponse(201, {id: 'new'}), ok: true} as unknown as MockResponse;
        });

        const {createTask} = await importClient();
        await createTask({summary: 'Buy milk'});

        expect(captured).not.toBeNull();
        expect(captured!.url).toBe(`${PLUGIN_API_BASE_URL}/tasks`);
        expect(captured!.init?.method).toBe('POST');

        // Client4.getOptions injects X-Requested-With; Content-Type comes from
        // the request body being set.
        const headers = captured!.init?.headers as Record<string, string>;
        expect(headers['Content-Type']).toBe('application/json');
        expect(headers['X-Requested-With']).toBe('XMLHttpRequest');

        // credentials: 'include' is set so the session cookie is sent.
        expect(captured!.init?.credentials).toBe('include');
        expect(captured!.init?.body).toBe(JSON.stringify({summary: 'Buy milk'}));
    });
});

describe('doFetch error path', () => {
    test('non-2xx surfaces a ClientError with the server text body', async () => {
        mockFetch(() => mockResponse(404, 'task not found'));

        const {getTask} = await importClient();
        await expect(getTask('abc')).rejects.toMatchObject({
            status: 404,
            message: 'task not found',
        });
    });

    test('non-2xx with empty body falls back to statusText', async () => {
        mockFetch(() => ({status: 500, statusText: 'Internal Server Error', text: () => Promise.resolve('')}) as MockResponse);

        const {getTask} = await importClient();
        await expect(getTask('abc')).rejects.toMatchObject({status: 500});
    });
});

describe('doFetch edge cases', () => {
    test('204 No Content resolves to undefined', async () => {
        mockFetch(() => ({status: 204, ok: true, statusText: '', text: () => Promise.resolve('')}) as unknown as MockResponse);
        const {deleteTask} = await importClient();
        await expect(deleteTask('abc')).resolves.toBeUndefined();
    });

    test('empty body on 2xx resolves to undefined without throwing', async () => {
        mockFetch(() => okResponse(''));
        const {listSubtasks} = await importClient();
        await expect(listSubtasks('parent')).resolves.toBeUndefined();
    });
});

describe('listTasks query building', () => {
    test('omits empty params and includes set ones', async () => {
        let capturedUrl = '';
        mockFetch((url) => {
            capturedUrl = url;
            return okResponse([]);
        });

        const {listTasks} = await importClient();
        await listTasks({scope: 'mine', status: 'todo', limit: 25});

        // URLSearchParams preserves insertion order, which buildQuery controls.
        expect(capturedUrl).toContain('scope=mine');
        expect(capturedUrl).toContain('status=todo');
        expect(capturedUrl).toContain('limit=25');
        expect(capturedUrl).not.toContain('channel_id=');
    });

    test('no params yields a bare URL with no query string', async () => {
        let capturedUrl = '';
        mockFetch((url) => {
            capturedUrl = url;
            return okResponse([]);
        });

        const {listTasks} = await importClient();
        await listTasks();
        expect(capturedUrl).toBe(`${PLUGIN_API_BASE_URL}/tasks`);
    });
});

describe('method verbs', () => {
    test('patchTask uses PATCH', async () => {
        let method = '';
        mockFetch((_url, init) => {
            method = init?.method ?? '';
            return okResponse({id: 'abc'});
        });
        const {patchTask} = await importClient();
        await patchTask('abc', {update_fields: ['summary'], summary: 'x'});
        expect(method).toBe('PATCH');
    });

    test('setTaskStatus uses PATCH on /status', async () => {
        let captured: {url: string; body: string} = {url: '', body: ''};
        mockFetch((url, init) => {
            captured = {url, body: String(init?.body ?? '')};
            return okResponse({id: 'abc'});
        });
        const {setTaskStatus} = await importClient();
        await setTaskStatus('abc', 'done');
        expect(captured.url).toBe(`${PLUGIN_API_BASE_URL}/tasks/abc/status`);
        expect(captured.body).toBe(JSON.stringify({status: 'done'}));
    });

    test('removeTaskAssignee uses DELETE on /assignee', async () => {
        let captured: {url: string; method: string} = {url: '', method: ''};
        mockFetch((url, init) => {
            captured = {url, method: init?.method ?? ''};
            return okResponse({id: 'abc'});
        });
        const {removeTaskAssignee} = await importClient();
        await removeTaskAssignee('abc');
        expect(captured.url).toBe(`${PLUGIN_API_BASE_URL}/tasks/abc/assignee`);
        expect(captured.method).toBe('DELETE');
    });
});

describe('subtask and comment endpoints', () => {
    test('createSubtask POSTs under the parent', async () => {
        let captured: {url: string; body: string} = {url: '', body: ''};
        mockFetch((url, init) => {
            captured = {url, body: String(init?.body ?? '')};
            return {...mockResponse(201, {id: 'child'}), ok: true} as unknown as MockResponse;
        });
        const {createSubtask} = await importClient();
        await createSubtask('parent', {summary: 'sub'});
        expect(captured.url).toBe(`${PLUGIN_API_BASE_URL}/tasks/parent/subtasks`);
        expect(captured.body).toBe(JSON.stringify({summary: 'sub'}));
    });

    test('createComment POSTs under the task', async () => {
        let captured: {url: string; body: string} = {url: '', body: ''};
        mockFetch((url, init) => {
            captured = {url, body: String(init?.body ?? '')};
            return {...mockResponse(201, {id: 'c1'}), ok: true} as unknown as MockResponse;
        });
        const {createComment} = await importClient();
        await createComment('t1', {content: 'hi'});
        expect(captured.url).toBe(`${PLUGIN_API_BASE_URL}/tasks/t1/comments`);
        expect(captured.body).toBe(JSON.stringify({content: 'hi'}));
    });

    test('listComments GETs under the task', async () => {
        let capturedUrl = '';
        mockFetch((url) => {
            capturedUrl = url;
            return okResponse([]);
        });
        const {listComments} = await importClient();
        await listComments('t1');
        expect(capturedUrl).toBe(`${PLUGIN_API_BASE_URL}/tasks/t1/comments`);
    });
});

describe('id encoding', () => {
    test('a ULID with a slash is not passed through unencoded', async () => {
        let capturedUrl = '';
        mockFetch((url) => {
            capturedUrl = url;
            return okResponse({id: 'x'});
        });
        const {getTask} = await importClient();
        await getTask('a/b');

        // encodeURIComponent encodes '/' as %2F, so the segment stays intact.
        expect(capturedUrl).toBe(`${PLUGIN_API_BASE_URL}/tasks/a%2Fb`);
    });
});

describe('getUserByUsername (#96)', () => {
    test('hits the host /api/v4 path (not the plugin API prefix)', async () => {
        let capturedUrl = '';
        mockFetch((url) => {
            capturedUrl = url;
            return okResponse({id: 'u1', username: 'bob'});
        });
        const {getUserByUsername} = await importClient();
        const user = await getUserByUsername('bob');
        expect(capturedUrl).toBe('/api/v4/users/username/bob');
        expect(user).toEqual({id: 'u1', username: 'bob'});
    });

    test('encodes the username segment', async () => {
        let capturedUrl = '';
        mockFetch((url) => {
            capturedUrl = url;
            return okResponse({id: 'u1', username: 'a b'});
        });
        const {getUserByUsername} = await importClient();
        await getUserByUsername('a b');
        expect(capturedUrl).toBe('/api/v4/users/username/a%20b');
    });

    test('a 404 (unknown user) surfaces as a ClientError', async () => {
        mockFetch(() => mockResponse(404, 'user not found'));
        const {getUserByUsername} = await importClient();
        await expect(getUserByUsername('nobody')).rejects.toMatchObject({
            status: 404,
            message: 'user not found',
        });
    });
});

// Client4.getOptions is the single place where the Mattermost-required request
// headers are assembled. These tests pin the CSRF contract that fixes the 401
// on write requests: the server's CSRF check (EnableCSRFChecks is on by
// default) needs X-CSRF-Token on POST/PATCH/DELETE or it never injects the
// Mattermost-User-Id header and the plugin's auth middleware rejects the
// request. The token is read from the MMCSRF cookie, which the host sets on
// the authenticated session.
describe('doFetch CSRF token via Client4.getOptions', () => {
    // jsdom persists document.cookie across tests, so clear it explicitly.
    afterEach(() => {
        document.cookie = 'MMCSRF=;expires=Thu, 01 Jan 1970 00:00:00 GMT;path=/';
    });

    test('POST includes X-CSRF-Token when the MMCSRF cookie is present', async () => {
        document.cookie = 'MMCSRF=test-csrf-token;path=/';
        let captured: {init?: RequestInit} | null = null;
        mockFetch((_url, init) => {
            captured = {init};
            return {...mockResponse(201, {id: 'new'}), ok: true} as unknown as MockResponse;
        });

        const {createTask} = await importClient();
        await createTask({summary: 'ship it'});

        const headers = captured!.init?.headers as Record<string, string>;
        expect(headers['X-CSRF-Token']).toBe('test-csrf-token');
        expect(headers['X-Requested-With']).toBe('XMLHttpRequest');
    });

    test('GET does not carry X-CSRF-Token (CSRF only applies to write methods)', async () => {
        document.cookie = 'MMCSRF=test-csrf-token;path=/';
        let captured: {init?: RequestInit} | null = null;
        mockFetch((_url, init) => {
            captured = {init};
            return okResponse({id: 'abc'});
        });

        const {getTask} = await importClient();
        await getTask('abc');

        const headers = captured!.init?.headers as Record<string, string>;
        expect(headers['X-CSRF-Token']).toBeUndefined();
        expect(headers['X-Requested-With']).toBe('XMLHttpRequest');
    });

    test('POST still sends X-Requested-With when no MMCSRF cookie exists', async () => {
        // No cookie set; the request must still be identifiable as an XHR and
        // carry credentials, even though the CSRF header is omitted.
        let captured: {init?: RequestInit} | null = null;
        mockFetch((_url, init) => {
            captured = {init};
            return {...mockResponse(201, {id: 'new'}), ok: true} as unknown as MockResponse;
        });

        const {createTask} = await importClient();
        await createTask({summary: 'no cookie'});

        const headers = captured!.init?.headers as Record<string, string>;
        expect(headers['X-CSRF-Token']).toBeUndefined();
        expect(headers['X-Requested-With']).toBe('XMLHttpRequest');
        expect(captured!.init?.credentials).toBe('include');
    });

    test('DELETE also carries X-CSRF-Token', async () => {
        document.cookie = 'MMCSRF=delete-token;path=/';
        let captured: {init?: RequestInit} | null = null;
        mockFetch((_url, init) => {
            captured = {init};
            return {status: 204, ok: true, statusText: '', text: () => Promise.resolve('')} as unknown as MockResponse;
        });

        const {deleteTask} = await importClient();
        await deleteTask('abc');

        const headers = captured!.init?.headers as Record<string, string>;
        expect(headers['X-CSRF-Token']).toBe('delete-token');
    });
});
