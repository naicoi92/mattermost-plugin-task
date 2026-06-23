// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Webapp plugin entry point (issue #27). Wires the plugin into the Mattermost
// desktop UI through the official registry methods:
//   - channel header button → opens the RHS
//   - composer button ("New Task") → opens the New Task dialog (#107)
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
// eslint-disable-next-line no-restricted-imports -- plugin has no access to the host's components/overlay_trigger wrapper, so we use react-bootstrap directly (a webpack external supplied by the host at runtime), the same pattern mattermost-plugin-calls uses.
import {OverlayTrigger, Tooltip} from 'react-bootstrap';
import {useDispatch} from 'react-redux';
import reducer, {ACTION_TYPES} from 'reducer';
import type {Store} from 'redux';

import type {WebSocketMessage} from '@mattermost/client';
import type {GlobalState} from '@mattermost/types/store';

import KanbanModal from 'components/kanban_modal/kanban_modal';
import NewTaskDialog from 'components/new_task_dialog/connected_new_task_dialog';
import TaskPostCard, {setTaskPostCardRhsOpener} from 'components/task_post_card/task_post_card';
import TaskSidebar from 'components/task_sidebar/task_sidebar';

import type {PluginRegistry} from 'types/mattermost-webapp';

import en from '../i18n/en.json';
import vi from '../i18n/vi.json';

// Plugin-wide stylesheet (#93). Imported once so webpack bundles it into
// main.js alongside the JS. Without it the RHS, Quick List, New Task dialog
// and Task Detail panel render with no styling at all.
import './styles/index.scss';

// rhsOpener opens the plugin's Right-Hand Sidebar. It is captured during
// initialize (registry is only safely usable there) so the composer "New Task"
// button and the post-dropdown "Tạo task" action can open the RHS and route the
// New Task form into it. Default no-op until initialize runs.
let rhsOpener: () => void = () => {};

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

// PostEditorActionProps is the prop shape the host's PostEditorAction Pluggable
// passes to composer toolbar buttons (see advanced_text_editor/use_plugin_items).
// We read `draft.channelId` to open the New Task dialog scoped to the current
// channel; getSelectedText/updateText are unused but part of the contract.
interface PostEditorActionProps {
    draft?: {channelId?: string; rootId?: string};
    getSelectedText?: () => {start?: number; end?: number};
    updateText?: (message: string) => void;
}

// NewTaskComposerButton is the "New Task" button in the message composer
// toolbar (issue #107). Registered via registerPostEditorActionComponent, it
// renders in the composer's additionalControls area, next to the attachment
// and emoji buttons. It reuses the host's own `AdvancedTextEditor__action-button`
// class so its styling — color, size, hover/active states — matches the other
// composer buttons and adapts to the active theme automatically (no custom CSS).
//
// On click it dispatches OPEN_NEW_TASK_DIALOG with the draft's channelId, so
// NewTaskDialog opens pre-scoped (scope "Channel" radio enabled). Desktop only:
// the host does not render the composer plugin slot on the mobile app; mobile
// uses the /task new slash command instead.
export function NewTaskComposerButton({draft}: PostEditorActionProps): JSX.Element {
    const dispatch = useDispatch();
    const t = useFormatMessage();
    const onClick = () => {
        // Open the RHS first so the inline New Task form has somewhere to
        // render, then dispatch the open-new-task action with the draft's
        // channel id so the form derives the right scope.
        rhsOpener();
        dispatch({
            type: ACTION_TYPES.OPEN_NEW_TASK_DIALOG,
            channelID: draft?.channelId,
        });
    };
    return (
        <OverlayTrigger
            delay={{show: 300, hide: 0}}
            placement='top'
            overlay={
                <Tooltip id='task-new-composer-tooltip'>
                    {t('webapp.task.tooltip.new_task')}
                </Tooltip>
            }
        >
            <button
                type='button'
                className='style--none AdvancedTextEditor__action-button'
                onClick={onClick}
                aria-label={t('webapp.task.tooltip.new_task')}
            >
                <i className='icon fa fa-check-square'/>
            </button>
        </OverlayTrigger>
    );
}

export default class Plugin {
    // registry is captured in initialize so the post-dropdown action and other
    // imperative callbacks can dispatch without re-deriving it.
    private store?: Store<GlobalState>;

    // clickListener is captured so uninitialize can detach it. It opens the
    // Task Details panel when the user clicks anywhere on a custom_task post.
    private clickListener?: (e: MouseEvent) => void;

    // eslint-disable-next-line @typescript-eslint/no-unused-vars, @typescript-eslint/no-empty-function
    public async initialize(registry: PluginRegistry, store: Store<GlobalState>) {
        this.store = store;

        // Click a task card in the channel → open its Task Details in the RHS.
        // The custom_task post body is rendered by TaskPostCard (registered
        // below), which opens the RHS itself; this listener is a fallback for
        // clicks on the surrounding post chrome / the mobile native attachment.
        // Desktop-only: the RHS doesn't exist on mobile, so the click is a
        // harmless no-op there.
        //
        // Scope: only a click landing inside the card itself (an element marked
        // data-task-id) opens the RHS. Clicks on the post's caption / the rest
        // of the post chrome fall through to the host's default behaviour
        // (opening the thread), matching how a plain text post behaves.
        //
        // Guarded for non-DOM environments (the Jest node test env) so importing
        // the module never throws.
        if (typeof document !== 'undefined') {
            this.clickListener = (e: MouseEvent) => {
                const target = e.target as HTMLElement | null;
                if (!target) {
                    return;
                }

                // Only clicks that land inside the card itself open the RHS.
                // The card is the element carrying data-task-id; the caption
                // above it is intentionally excluded so clicking it opens the
                // thread like any other post.
                const cardEl = target.closest('[data-task-id]') as HTMLElement | null;
                if (!cardEl) {
                    return;
                }

                // Don't hijack clicks on interactive bits inside the card (links,
                // buttons, the reaction picker) — let those do their own thing.
                if (target.closest('a, button, input, textarea, select, [role="button"]')) {
                    return;
                }

                const taskID = cardEl.getAttribute('data-task-id');
                if (!taskID) {
                    return;
                }
                e.preventDefault();
                rhsOpener();
                store.dispatch({type: ACTION_TYPES.SELECT_TASK, taskID});
            };
            document.addEventListener('click', this.clickListener);
        }

        // Register the RHS. The returned action creators open/close/toggle the
        // sidebar; showRHSPlugin is captured at module scope so the composer
        // "New Task" button and the post-dropdown "Tạo task" action can open the
        // RHS (and route the New Task form into it) without re-deriving it —
        // registry is only safely usable inside initialize.
        const {showRHSPlugin} = registry.registerRightHandSidebarComponent(TaskSidebar, 'Tasks');
        rhsOpener = () => store.dispatch(showRHSPlugin);
        setTaskPostCardRhsOpener(rhsOpener);

        // Custom React body for "custom_task" posts (the task card). Renders in
        // place of the native SlackAttachment on desktop, with a real checkbox
        // and an inline meta row matching the design. The server still builds
        // the SlackAttachment as a mobile / fallback.
        registry.registerPostTypeComponent('custom_task', TaskPostCard);

        // Channel header button: opens the RHS. The icon is a simple checkmark
        // glyph; a richer SVG can replace it without changing the contract.
        registry.registerChannelHeaderButtonAction(
            <i className='icon fa fa-check-square'/>,
            () => store.dispatch(showRHSPlugin),
            'Tasks',
            'Mở danh sách task',
        );

        // "New Task" composer button (issue #107). Registered via
        // registerPostEditorActionComponent, so it renders in the message
        // composer's additionalControls toolbar — next to the attachment and
        // emoji buttons. It reuses the host's AdvancedTextEditor__action-button
        // class (same as the attachment button), so styling matches the other
        // composer controls and follows the active theme with no custom CSS.
        // Desktop only: mobile app uses the /task new slash command instead.
        registry.registerPostEditorActionComponent(NewTaskComposerButton);

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

    // eslint-disable-next-line @typescript-eslint/no-empty-function
    public uninitialize() {
        // Detach the task-card click listener so a plugin reload doesn't stack
        // duplicate handlers. The registry's other registrations are cleaned up
        // by the host automatically.
        if (this.clickListener && typeof document !== 'undefined') {
            document.removeEventListener('click', this.clickListener);
            this.clickListener = undefined;
        }
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

    // Open the RHS so the inline New Task form (now an RHS view) has somewhere
    // to render, then dispatch the open-new-task action with the prefilled
    // summary/description and the source channel id so the form derives the
    // right scope.
    rhsOpener();
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
    type?: string;
    props?: Record<string, unknown> & {task_id?: string};
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
