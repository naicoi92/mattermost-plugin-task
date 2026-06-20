// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Minimal ambient declarations for the react-bootstrap pieces the plugin uses.
// react-bootstrap is a webpack external (webpack.config.js `externals`) supplied
// by the host at runtime, so it is intentionally NOT a package dependency and
// has no installed @types. We declare only the props we read so TypeScript is
// satisfied; the host provides the real implementation.

declare module 'react-bootstrap' {
    import type {ReactNode} from 'react';

    export interface OverlayTriggerProps {
        // placement of the overlay relative to the wrapped element.
        placement?: 'top' | 'bottom' | 'left' | 'right' | 'auto';
        // delay (ms) before showing/hiding; accepts {show, hide} or a number.
        delay?: number | {show: number; hide: number};
        children: ReactNode;
        // overlay is the tooltip/popover element to render.
        overlay: ReactNode;
        trigger?: string[] | string;
        rootClose?: boolean;
    }

    // OverlayTrigger wraps a child and renders the overlay on hover/focus.
    export class OverlayTrigger extends React.Component<OverlayTriggerProps> {}

    export interface TooltipProps {
        id: string;
        children?: ReactNode;
    }

    export class Tooltip extends React.Component<TooltipProps> {}
}
