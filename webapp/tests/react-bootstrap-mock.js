// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Jest mock for react-bootstrap. react-bootstrap is a webpack external
// (webpack.config.js) supplied by the host at runtime — it is intentionally NOT
// installed as a package dependency, so Jest cannot resolve the real module.
// This passthrough renders the wrapped children so tests can reach the inner
// <button> (e.g. NewTaskComposerButton) and fire its onClick. Mapped via Jest's
// moduleNameMapper in package.json.
module.exports = {
    OverlayTrigger: ({children}) => children,
    Tooltip: ({children}) => children,
};
