// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Plugin Redux reducer (issue #27). Mounted under
// `state['plugins-<pluginId>']` by registerReducer, this holds the desktop
// plugin's view state: the RHS open/close flag, the currently selected task
// (for Task Detail), and a per-task cache that the WebSocket handler (#32)
// updates so Quick List / Task Detail / Kanban reflect server changes.
//
// The reducer is intentionally small and additive; component-local state (form
// drafts, transient filters) is not stored here — only what must be shared or
// survive across component unmounts.

// TaskPartial is the minimal shape the reducer needs to cache tasks. It is a
// structural subset of the full Task type (issue #31's types/tasks.ts). Kept
// local here so #27 stays independent of #31; once #31 merges the components can
// import the canonical Task type instead. Indexing by id requires `id`.
export interface TaskPartial {
    id: string;
    [field: string]: unknown;
}

// Action type constants. Prefixed with the plugin id namespace is unnecessary
// because the reducer is already namespaced under plugins-<pluginId>; these are
// dispatched through the host Redux store.
export const ACTION_TYPES = {
    OPEN_RHS: 'task/open_rhs',
    CLOSE_RHS: 'task/close_rhs',
    SELECT_TASK: 'task/select_task',
    SET_SELECTED_TASK: 'task/set_selected_task',
    UPSERT_TASK: 'task/upsert_task',
    DELETE_TASK: 'task/delete_task',
} as const;

// TaskState is the slice mounted at state['plugins-com.mattermost.plugin-task'].
export interface TaskState {

    // rhsOpen reflects whether the Right-Hand Sidebar is shown. Toggled by the
    // channel header button action returned from registerRightHandSidebarComponent.
    rhsOpen: boolean;

    // selectedTaskID is the task currently shown in Task Detail; empty means the
    // Quick List is the active view.
    selectedTaskID: string;

    // selectedTask caches the full object of the task shown in detail, so the
    // detail panel renders without an extra fetch after selection.
    selectedTask: TaskPartial | null;

    // tasks is a normalized by-id cache of tasks the webapp has seen. The
    // WebSocket handler (#32) upserts here so every view updates in lockstep.
    tasks: Record<string, TaskPartial>;
}

const initialState: TaskState = {
    rhsOpen: false,
    selectedTaskID: '',
    selectedTask: null,
    tasks: {},
};

// A discriminated-union action. Keeping this narrow (rather than `AnyAction`)
// gives compile-time safety to dispatchers and the WebSocket handler.
export interface PluginAction {
    type: string;
    rhsOpen?: boolean;
    taskID?: string;
    task?: TaskPartial;
}

// reducer is the entry point registered via registerReducer in index.tsx.
export default function reducer(state: TaskState = initialState, action: PluginAction): TaskState {
    switch (action.type) {
    case ACTION_TYPES.OPEN_RHS:
        return {...state, rhsOpen: true};
    case ACTION_TYPES.CLOSE_RHS:
        return {...state, rhsOpen: false};
    case ACTION_TYPES.SELECT_TASK:
        // Selecting a task keeps the previous cache as the detail panel fills it.
        return {
            ...state,
            selectedTaskID: action.taskID ?? '',
            selectedTask: action.task ?? null,
        };
    case ACTION_TYPES.SET_SELECTED_TASK:
        // Hydrate the detail panel after a fetch following selection.
        return {...state, selectedTask: action.task ?? null};
    case ACTION_TYPES.UPSERT_TASK:
        // WebSocket (#32) and successful mutations land here. If the updated task
        // is the one in detail, refresh the detail view too.
        // Guard against a malformed payload (e.g. a WebSocket event missing the
        // id): a task without an id can't be cached under a key, so drop it.
        if (!action.task || typeof action.task.id !== 'string' || !action.task.id.trim()) {
            return state;
        }
        return {
            ...state,
            tasks: {...state.tasks, [action.task.id]: action.task},
            selectedTask: state.selectedTaskID === action.task.id ? action.task : state.selectedTask,
        };
    case ACTION_TYPES.DELETE_TASK: {
        const id = action.taskID ?? '';
        if (!id) {
            return state;
        }

        // Two independent effects: remove from the cache (only if present), and
        // clear the selection when the deleted task is the one shown in detail.
        // The selection may reference a task that was never cached (e.g. selected
        // via SELECT_TASK before its body was fetched), so the selection clear is
        // decoupled from the cache removal.
        const inCache = id in state.tasks;
        const isSelected = state.selectedTaskID === id;
        if (!inCache && !isSelected) {
            return state;
        }
        return {
            ...state,
            tasks: inCache ? omit(state.tasks, id) : state.tasks,
            selectedTask: isSelected ? null : state.selectedTask,
            selectedTaskID: isSelected ? '' : state.selectedTaskID,
        };
    }
    default:
        return state;
    }
}

// omit returns a copy of obj without the given key, without mutating obj.
function omit<T extends Record<string, unknown>>(obj: T, key: string): T {
    if (!(key in obj)) {
        return obj;
    }
    const next = {...obj};
    delete next[key];
    return next;
}
