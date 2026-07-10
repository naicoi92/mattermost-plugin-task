// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Minimal ambient declarations for the react-dom pieces the plugin uses.
// react-dom is a webpack external (webpack.config.js `externals`, mapped to the
// global ReactDOM) supplied by the host at runtime, so it is intentionally NOT a
// package dependency and has no installed @types. We declare only createPortal so
// TypeScript is satisfied; the host provides the real implementation.
//
// createPortal renders children into a DOM node outside the current React tree —
// used by the comment-image lightbox to escape the RHS (whose transformed
// ancestor turns position:fixed into a containing block) so the overlay covers
// the full viewport via document.body.

declare module "react-dom" {
	import type { ReactNode, ReactPortal } from "react";

	export function createPortal(
		children: ReactNode,
		container: Element | DocumentFragment,
		key?: string | null,
	): ReactPortal;
}
